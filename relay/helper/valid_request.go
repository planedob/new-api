package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
)

func GetAndValidateRequest(c *gin.Context, format types.RelayFormat) (request dto.Request, err error) {
	relayMode := relayconstant.Path2RelayMode(c.Request.URL.Path)

	switch format {
	case types.RelayFormatOpenAI:
		request, err = GetAndValidateTextRequest(c, relayMode)
	case types.RelayFormatGemini:
		if strings.Contains(c.Request.URL.Path, ":embedContent") {
			request, err = GetAndValidateGeminiEmbeddingRequest(c)
		} else if strings.Contains(c.Request.URL.Path, ":batchEmbedContents") {
			request, err = GetAndValidateGeminiBatchEmbeddingRequest(c)
		} else {
			request, err = GetAndValidateGeminiRequest(c)
		}
	case types.RelayFormatClaude:
		request, err = GetAndValidateClaudeRequest(c)
	case types.RelayFormatOpenAIResponses:
		request, err = GetAndValidateResponsesRequest(c)
	case types.RelayFormatOpenAIResponsesCompaction:
		request, err = GetAndValidateResponsesCompactionRequest(c)

	case types.RelayFormatOpenAIImage:
		request, err = GetAndValidOpenAIImageRequest(c, relayMode)
	case types.RelayFormatEmbedding:
		request, err = GetAndValidateEmbeddingRequest(c, relayMode)
	case types.RelayFormatRerank:
		request, err = GetAndValidateRerankRequest(c)
	case types.RelayFormatOpenAIAudio:
		request, err = GetAndValidAudioRequest(c, relayMode)
	case types.RelayFormatOpenAIRealtime:
		request = &dto.BaseRequest{}
	default:
		return nil, fmt.Errorf("unsupported relay format: %s", format)
	}
	return request, err
}

func GetAndValidAudioRequest(c *gin.Context, relayMode int) (*dto.AudioRequest, error) {
	audioRequest := &dto.AudioRequest{}
	err := common.UnmarshalBodyReusable(c, audioRequest)
	if err != nil {
		return nil, err
	}
	switch relayMode {
	case relayconstant.RelayModeAudioSpeech:
		if audioRequest.Model == "" {
			return nil, errors.New("model is required")
		}
	default:
		if audioRequest.Model == "" {
			return nil, errors.New("model is required")
		}
		if audioRequest.ResponseFormat == "" {
			audioRequest.ResponseFormat = "json"
		}
	}
	return audioRequest, nil
}

func GetAndValidateRerankRequest(c *gin.Context) (*dto.RerankRequest, error) {
	var rerankRequest *dto.RerankRequest
	err := common.UnmarshalBodyReusable(c, &rerankRequest)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("getAndValidateTextRequest failed: %s", err.Error()))
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if rerankRequest.Query == "" {
		return nil, types.NewError(fmt.Errorf("query is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if len(rerankRequest.Documents) == 0 {
		return nil, types.NewError(fmt.Errorf("documents is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	return rerankRequest, nil
}

func GetAndValidateEmbeddingRequest(c *gin.Context, relayMode int) (*dto.EmbeddingRequest, error) {
	var embeddingRequest *dto.EmbeddingRequest
	err := common.UnmarshalBodyReusable(c, &embeddingRequest)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("getAndValidateTextRequest failed: %s", err.Error()))
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	if embeddingRequest.Input == nil {
		return nil, fmt.Errorf("input is empty")
	}
	if relayMode == relayconstant.RelayModeModerations && embeddingRequest.Model == "" {
		embeddingRequest.Model = "omni-moderation-latest"
	}
	if relayMode == relayconstant.RelayModeEmbeddings && embeddingRequest.Model == "" {
		embeddingRequest.Model = c.Param("model")
	}
	return embeddingRequest, nil
}

func GetAndValidateResponsesRequest(c *gin.Context) (*dto.OpenAIResponsesRequest, error) {
	request := &dto.OpenAIResponsesRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	if request.Input == nil {
		return nil, errors.New("input is required")
	}
	return request, nil
}

func GetAndValidateResponsesCompactionRequest(c *gin.Context) (*dto.OpenAIResponsesCompactionRequest, error) {
	request := &dto.OpenAIResponsesCompactionRequest{}
	if err := common.UnmarshalBodyReusable(c, request); err != nil {
		return nil, err
	}
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	return request, nil
}

func GetAndValidOpenAIImageRequest(c *gin.Context, relayMode int) (*dto.ImageRequest, error) {
	imageRequest := &dto.ImageRequest{}

	switch relayMode {
	case relayconstant.RelayModeImagesEdits:
		if common.IsMultipartFormData(c.Request.Header.Get("Content-Type")) {
			formData, err := common.ParseMultipartFormReusable(c)
			if err != nil {
				return nil, fmt.Errorf("failed to parse image edit form request: %w", err)
			}
			c.Request.MultipartForm = formData
			if err := validateImage2MultipartForm(formData); err != nil {
				return nil, err
			}
			values := url.Values(formData.Value)
			imageRequest.Prompt = values.Get("prompt")
			imageRequest.Model = values.Get("model")
			imageRequest.N = common.GetPointer(uint(common.String2Int(values.Get("n"))))
			imageRequest.Quality = values.Get("quality")
			imageRequest.Size = values.Get("size")
			if imageValue := values.Get("image"); imageValue != "" {
				imageRequest.Image, _ = json.Marshal(imageValue)
			}

			if imageRequest.Model == "gpt-image-1" {
				if imageRequest.Quality == "" {
					imageRequest.Quality = "standard"
				}
			}
			if imageRequest.N == nil || *imageRequest.N == 0 {
				imageRequest.N = common.GetPointer(uint(1))
			}

			_, hasWatermark := values["watermark"]
			if hasWatermark {
				watermark := values.Get("watermark") == "true"
				imageRequest.Watermark = &watermark
			}
			break
		}
		fallthrough
	default:
		err := common.UnmarshalBodyReusable(c, imageRequest)
		if err != nil {
			return nil, err
		}

		if imageRequest.Model == "" {
			//imageRequest.Model = "dall-e-3"
			return nil, errors.New("model is required")
		}

		if strings.Contains(imageRequest.Size, "×") {
			return nil, errors.New("size an unexpected error occurred in the parameter, please use 'x' instead of the multiplication sign '×'")
		}

		// Not "256x256", "512x512", or "1024x1024"
		if imageRequest.Model == "dall-e-2" || imageRequest.Model == "dall-e" {
			if imageRequest.Size != "" && imageRequest.Size != "256x256" && imageRequest.Size != "512x512" && imageRequest.Size != "1024x1024" {
				return nil, errors.New("size must be one of 256x256, 512x512, or 1024x1024 for dall-e-2 or dall-e")
			}
			if imageRequest.Size == "" {
				imageRequest.Size = "1024x1024"
			}
		} else if imageRequest.Model == "dall-e-3" {
			if imageRequest.Size != "" && imageRequest.Size != "1024x1024" && imageRequest.Size != "1024x1792" && imageRequest.Size != "1792x1024" {
				return nil, errors.New("size must be one of 1024x1024, 1024x1792 or 1792x1024 for dall-e-3")
			}
			if imageRequest.Quality == "" {
				imageRequest.Quality = "standard"
			}
			if imageRequest.Size == "" {
				imageRequest.Size = "1024x1024"
			}
		} else if imageRequest.Model == "gpt-image-1" {
			if imageRequest.Quality == "" {
				imageRequest.Quality = "auto"
			}
		}

		//if imageRequest.Prompt == "" {
		//	return nil, errors.New("prompt is required")
		//}

		if imageRequest.N == nil || *imageRequest.N == 0 {
			imageRequest.N = common.GetPointer(uint(1))
		}
	}

	return imageRequest, nil
}

const defaultImage2MaxInputMB = 50

// validateImage2MultipartForm validates the bytes, not the client supplied
// filename or Content-Type header. This prevents empty files and extension
// spoofing from reaching an upstream and gives all Image2 providers the same
// fail-closed input contract.
func validateImage2MultipartForm(form *multipart.Form) error {
	if form == nil || form.File == nil {
		return common.ErrImageInputRequired
	}

	imageFiles := form.File["image"]
	if len(imageFiles) == 0 {
		imageFiles = form.File["image[]"]
	}
	if len(imageFiles) == 0 {
		for fieldName, files := range form.File {
			if strings.HasPrefix(fieldName, "image[") {
				imageFiles = append(imageFiles, files...)
			}
		}
	}
	if len(imageFiles) == 0 {
		return common.ErrImageInputRequired
	}
	for index, fileHeader := range imageFiles {
		if err := validateImage2FileHeader(fileHeader, "image", index); err != nil {
			return err
		}
	}

	for index, fileHeader := range form.File["mask"] {
		if err := validateImage2FileHeader(fileHeader, "mask", index); err != nil {
			return err
		}
	}
	return nil
}

func validateImage2FileHeader(fileHeader *multipart.FileHeader, fieldName string, index int) error {
	if fileHeader == nil || fileHeader.Size <= 0 {
		return fmt.Errorf("%w: %s file %d is empty", common.ErrImageInputEmpty, fieldName, index)
	}
	maxMB := constant.MaxImage2InputMB
	if maxMB <= 0 {
		maxMB = defaultImage2MaxInputMB
	}
	if fileHeader.Size > int64(maxMB)<<20 {
		return fmt.Errorf("%w: %s file %d exceeds %d MB", common.ErrRequestBodyTooLarge, fieldName, index, maxMB)
	}

	file, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("failed to open %s file %d: %w", fieldName, index, err)
	}
	defer file.Close()

	header, err := io.ReadAll(io.LimitReader(file, 512))
	if err != nil {
		return fmt.Errorf("failed to read %s file %d: %w", fieldName, index, err)
	}
	if len(header) == 0 {
		return fmt.Errorf("%s file %d is empty", fieldName, index)
	}
	mimeType := http.DetectContentType(header)
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp":
		return nil
	default:
		return fmt.Errorf("%w: %s file %d has unsupported image content", common.ErrImageInputUnsupported, fieldName, index)
	}
}

func GetAndValidateClaudeRequest(c *gin.Context) (textRequest *dto.ClaudeRequest, err error) {
	textRequest = &dto.ClaudeRequest{}
	err = common.UnmarshalBodyReusable(c, textRequest)
	if err != nil {
		return nil, err
	}
	if textRequest.Messages == nil || len(textRequest.Messages) == 0 {
		return nil, errors.New("field messages is required")
	}
	if textRequest.Model == "" {
		return nil, errors.New("field model is required")
	}

	//if textRequest.Stream {
	//	relayInfo.IsStream = true
	//}

	return textRequest, nil
}

func GetAndValidateTextRequest(c *gin.Context, relayMode int) (*dto.GeneralOpenAIRequest, error) {
	textRequest := &dto.GeneralOpenAIRequest{}
	err := common.UnmarshalBodyReusable(c, textRequest)
	if err != nil {
		return nil, err
	}

	if relayMode == relayconstant.RelayModeModerations && textRequest.Model == "" {
		textRequest.Model = "text-moderation-latest"
	}
	if relayMode == relayconstant.RelayModeEmbeddings && textRequest.Model == "" {
		textRequest.Model = c.Param("model")
	}

	if lo.FromPtrOr(textRequest.MaxTokens, uint(0)) > math.MaxInt32/2 {
		return nil, errors.New("max_tokens is invalid")
	}
	if textRequest.Model == "" {
		return nil, errors.New("model is required")
	}
	if textRequest.WebSearchOptions != nil {
		if textRequest.WebSearchOptions.SearchContextSize != "" {
			validSizes := map[string]bool{
				"high":   true,
				"medium": true,
				"low":    true,
			}
			if !validSizes[textRequest.WebSearchOptions.SearchContextSize] {
				return nil, errors.New("invalid search_context_size, must be one of: high, medium, low")
			}
		} else {
			textRequest.WebSearchOptions.SearchContextSize = "medium"
		}
	}
	switch relayMode {
	case relayconstant.RelayModeCompletions:
		if textRequest.Prompt == "" {
			return nil, errors.New("field prompt is required")
		}
	case relayconstant.RelayModeChatCompletions:
		// For FIM (Fill-in-the-middle) requests with prefix/suffix, messages is optional
		// It will be filled by provider-specific adaptors if needed (e.g., SiliconFlow)。Or it is allowed by model vendor(s) (e.g., DeepSeek)
		if len(textRequest.Messages) == 0 && textRequest.Prefix == nil && textRequest.Suffix == nil {
			return nil, errors.New("field messages is required")
		}
	case relayconstant.RelayModeEmbeddings:
	case relayconstant.RelayModeModerations:
		if textRequest.Input == nil || textRequest.Input == "" {
			return nil, errors.New("field input is required")
		}
	case relayconstant.RelayModeEdits:
		if textRequest.Instruction == "" {
			return nil, errors.New("field instruction is required")
		}
	}
	return textRequest, nil
}

func GetAndValidateGeminiRequest(c *gin.Context) (*dto.GeminiChatRequest, error) {
	request := &dto.GeminiChatRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	if len(request.Contents) == 0 && len(request.Requests) == 0 {
		return nil, errors.New("contents is required")
	}
	if err := validateGeminiThoughtSignatures(request); err != nil {
		return nil, err
	}

	//if c.Query("alt") == "sse" {
	//	relayInfo.IsStream = true
	//}

	return request, nil
}

// validateGeminiThoughtSignatures enforces the native Gemini continuation
// contract before channel selection, billing, or an upstream request. The
// signature remains a json.RawMessage in dto.GeminiPart so validation never
// rewrites or normalizes the bytes that a valid model turn supplied.
func validateGeminiThoughtSignatures(request *dto.GeminiChatRequest) error {
	return validateGeminiThoughtSignaturesAtPath(request, "")
}

func validateGeminiThoughtSignaturesAtPath(request *dto.GeminiChatRequest, pathPrefix string) error {
	if request == nil {
		return nil
	}

	for contentIndex, content := range request.Contents {
		contentPath := fmt.Sprintf("%scontents[%d]", pathPrefix, contentIndex)
		if err := validateGeminiContentThoughtSignatures(contentPath, content); err != nil {
			return err
		}
	}

	if request.SystemInstructions != nil {
		if err := validateGeminiContentThoughtSignatures(pathPrefix+"systemInstruction", *request.SystemInstructions); err != nil {
			return err
		}
	}

	for requestIndex := range request.Requests {
		requestPath := fmt.Sprintf("%srequests[%d].", pathPrefix, requestIndex)
		if err := validateGeminiThoughtSignaturesAtPath(&request.Requests[requestIndex], requestPath); err != nil {
			return err
		}
	}

	return nil
}

func validateGeminiContentThoughtSignatures(contentPath string, content dto.GeminiChatContent) error {
	role := content.Role
	if role == "" {
		role = "user"
	}

	for partIndex, part := range content.Parts {
		if len(part.ThoughtSignature) == 0 {
			continue
		}

		partPath := fmt.Sprintf("%s.parts[%d].thoughtSignature", contentPath, partIndex)
		if common.GetJsonType(part.ThoughtSignature) != "string" {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("%s must be a non-empty JSON string", partPath),
				types.ErrorCodeInvalidRequest,
				http.StatusBadRequest,
				types.ErrOptionWithSkipRetry(),
			)
		}

		var signature string
		if err := common.Unmarshal(part.ThoughtSignature, &signature); err != nil || strings.TrimSpace(signature) == "" {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("%s must be a non-empty JSON string", partPath),
				types.ErrorCodeInvalidRequest,
				http.StatusBadRequest,
				types.ErrOptionWithSkipRetry(),
			)
		}

		if role != "model" {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("%s is only allowed on role=model content", partPath),
				types.ErrorCodeInvalidRequest,
				http.StatusBadRequest,
				types.ErrOptionWithSkipRetry(),
			)
		}
	}

	return nil
}

func GetAndValidateGeminiEmbeddingRequest(c *gin.Context) (*dto.GeminiEmbeddingRequest, error) {
	request := &dto.GeminiEmbeddingRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	return request, nil
}

func GetAndValidateGeminiBatchEmbeddingRequest(c *gin.Context) (*dto.GeminiBatchEmbeddingRequest, error) {
	request := &dto.GeminiBatchEmbeddingRequest{}
	err := common.UnmarshalBodyReusable(c, request)
	if err != nil {
		return nil, err
	}
	return request, nil
}
