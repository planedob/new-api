// Package secureskill implements the isolated SecureSkill MiniMax H3 video
// task adapter.  H3 is deliberately kept separate from the legacy MiniMax
// (Hailuo) adapter: the two providers expose different request and polling
// contracts even though both use a MiniMax model name.
package secureskill

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

const (
	ModelName = "minimax-h3"

	minImages = 1
	maxImages = 5

	// ZZone MiniMax H3 contract: duration is 5–15 seconds.
	minDuration = 5
	maxDuration = 15

	maxImageFileSize = 20 * 1024 * 1024
)

var supportedTopLevelFields = map[string]struct{}{
	"prompt":          {},
	"model":           {},
	"mode":            {},
	"image":           {},
	"images":          {},
	"input_reference": {},
	"duration":        {},
	"seconds":         {},
	"metadata":        {},
	// size is recognized so we can return a deliberate capability error
	// instead of silently accepting an unverified resolution.
	"size": {},
}

var imageFieldNames = map[string]struct{}{
	"image":             {},
	"image[]":           {},
	"images":            {},
	"images[]":          {},
	"input_reference":   {},
	"input_reference[]": {},
}

var unsupportedFieldNames = map[string]string{
	"audio":                "audio input is not enabled for minimax-h3 candidate",
	"audio_url":            "audio input is not enabled for minimax-h3 candidate",
	"audio_urls":           "audio input is not enabled for minimax-h3 candidate",
	"input_video":          "reference video is not enabled for minimax-h3 candidate",
	"input_video_url":      "reference video is not enabled for minimax-h3 candidate",
	"reference_video":      "reference video is not enabled for minimax-h3 candidate",
	"reference_video_url":  "reference video is not enabled for minimax-h3 candidate",
	"reference_video_urls": "reference video is not enabled for minimax-h3 candidate",
	"video":                "reference video is not enabled for minimax-h3 candidate",
	"video_url":            "reference video is not enabled for minimax-h3 candidate",
	"video_urls":           "reference video is not enabled for minimax-h3 candidate",
}

type requestPayload struct {
	Model   string   `json:"model"`
	Prompt  string   `json:"prompt"`
	Images  []string `json:"images"`
	Seconds int      `json:"seconds"`
}

type secureSkillNativeRequestPayload struct {
	Model     string   `json:"model"`
	Prompt    string   `json:"prompt"`
	ImageURLs []string `json:"image_urls"`
	Duration  int      `json:"duration"`
}

// h3Duration accepts the provider's observed response encodings while keeping
// malformed or fractional values fail-closed. Duration is informational in a
// task response, so a provider null maps to zero.
type h3Duration int

var h3DurationNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func (d *h3Duration) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "null" {
		*d = 0
		return nil
	}
	if raw == "" {
		return fmt.Errorf("empty minimax-h3 duration")
	}
	if strings.HasPrefix(raw, "\"") {
		var value string
		if err := common.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("invalid minimax-h3 duration: %w", err)
		}
		raw = strings.TrimSpace(value)
		// SecureSkill documents completed task responses using values such as
		// "6s". This is response-only compatibility: incoming client request
		// validation remains numeric and therefore cannot silently change a
		// requested duration.
		if strings.HasSuffix(raw, "s") {
			raw = strings.TrimSuffix(raw, "s")
		}
	}
	if raw == "" {
		return fmt.Errorf("invalid minimax-h3 duration: empty string")
	}
	if !h3DurationNumberPattern.MatchString(raw) {
		return fmt.Errorf("invalid minimax-h3 duration %q", raw)
	}
	value, ok := new(big.Rat).SetString(raw)
	if !ok || !value.IsInt() {
		return fmt.Errorf("invalid minimax-h3 duration %q", raw)
	}
	limit := new(big.Int).Lsh(big.NewInt(1), uint(strconv.IntSize-1))
	minimum := new(big.Int).Neg(new(big.Int).Set(limit))
	maximum := new(big.Int).Sub(new(big.Int).Set(limit), big.NewInt(1))
	integer := value.Num()
	if integer.Cmp(minimum) < 0 || integer.Cmp(maximum) > 0 {
		return fmt.Errorf("minimax-h3 duration out of range")
	}
	*d = h3Duration(int(integer.Int64()))
	return nil
}

type responseTask struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id,omitempty"`
	Object      string     `json:"object,omitempty"`
	Model       string     `json:"model,omitempty"`
	Status      string     `json:"status"`
	Progress    int        `json:"progress,omitempty"`
	CreatedAt   int64      `json:"created_at,omitempty"`
	CompletedAt int64      `json:"completed_at,omitempty"`
	ExpiresAt   int64      `json:"expires_at,omitempty"`
	Duration    h3Duration `json:"duration,omitempty"`
	VideoURL    string     `json:"video_url,omitempty"`
	URL         string     `json:"url,omitempty"`
	Error       *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

type rawTaskRequest struct {
	JSON   map[string]interface{}
	Values map[string][]string
	Files  map[string][]*multipart.FileHeader
	HasRaw bool
}

type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
	apiKey      string
	baseURL     string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	if info == nil {
		return
	}
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
	a.apiKey = info.ApiKey
}

// ValidateRequestAndSetAction validates H3 before model pricing, pre-charge,
// channel distribution, or an upstream request can occur.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	// Gin's MultipartForm parser consumes the request body.  Seed and rewind
	// the shared body storage first so the generic validator and our strict H3
	// parser can both inspect the same request without an EOF.
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return localTaskError(err, "invalid_multipart_form")
		}
		if _, err := storage.Seek(0, io.SeekStart); err != nil {
			return localTaskError(err, "invalid_multipart_form")
		}
		c.Request.Body = io.NopCloser(storage)
	}
	if err := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate); err != nil {
		return err
	}

	req, err := a.validatedRequest(c)
	if err != nil {
		return localTaskError(err, "unsupported_minimax_h3_request")
	}
	c.Set("task_request", req)
	info.Action = constant.TaskActionGenerate
	return nil
}

func (a *TaskAdaptor) EstimateBilling(c *gin.Context, _ *relaycommon.RelayInfo) map[string]float64 {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil || req.Duration == 0 {
		return nil
	}
	// H3's candidate price is per request.  Duration validation is still
	// performed locally; no unverified duration multiplier is applied here.
	return nil
}

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	if strings.TrimSpace(a.baseURL) == "" {
		return "", errors.New("SecureSkill base URL is required")
	}
	return buildVideoURL(a.baseURL, ""), nil
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	// Native SecureSkill accepts Idempotency-Key on task creation. Bind it to
	// the pre-generated public task ID so a transport ambiguity cannot create
	// a second upstream task for the same Aibuff request. Keep the legacy ZZone
	// wire contract untouched.
	if a.ChannelType == constant.ChannelTypeSecureSkillNativeH3 && info != nil && strings.TrimSpace(info.PublicTaskID) != "" {
		req.Header.Set("Idempotency-Key", "aibuff-"+strings.TrimSpace(info.PublicTaskID))
	}
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, _ *relaycommon.RelayInfo) (io.Reader, error) {
	req, err := a.validatedRequest(c)
	if err != nil {
		return nil, err
	}
	var body interface{}
	if a.ChannelType == constant.ChannelTypeSecureSkillNativeH3 {
		body = secureSkillNativeRequestPayload{
			Model: ModelName, Prompt: req.Prompt,
			ImageURLs: req.Images, Duration: req.Duration,
		}
	} else {
		body = requestPayload{
			Model: ModelName, Prompt: req.Prompt,
			Images: req.Images, Seconds: req.Duration,
		}
	}
	data, err := common.Marshal(body)
	if err != nil {
		return nil, errors.Wrap(err, "marshal minimax-h3 request failed")
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	if resp == nil || resp.Body == nil {
		return "", nil, service.TaskErrorWrapper(fmt.Errorf("empty upstream response"), "invalid_response", http.StatusBadGateway)
	}
	responseBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusBadGateway)
	}

	var upstream responseTask
	if err := common.Unmarshal(responseBody, &upstream); err != nil {
		return "", nil, service.TaskErrorWrapper(err, "unmarshal_response_failed", http.StatusBadGateway)
	}
	if strings.EqualFold(upstream.Status, "failed") {
		message, code := "minimax-h3 task failed", "upstream_task_failed"
		if upstream.Error != nil {
			if strings.TrimSpace(upstream.Error.Message) != "" {
				message = upstream.Error.Message
			}
			if strings.TrimSpace(upstream.Error.Code) != "" {
				code = upstream.Error.Code
			}
		}
		return "", nil, service.TaskErrorWrapperLocal(errors.New(message), code, http.StatusBadGateway)
	}
	switch strings.ToLower(strings.TrimSpace(upstream.Status)) {
	case "queued", "pending", "in_progress", "processing":
		// Accepted async states.  The final content remains a separate GET.
	default:
		return "", nil, service.TaskErrorWrapper(fmt.Errorf("unsupported minimax-h3 initial status: %q", upstream.Status), "invalid_response", http.StatusBadGateway)
	}

	taskID = strings.TrimSpace(upstream.ID)
	if taskID == "" {
		taskID = strings.TrimSpace(upstream.TaskID)
	}
	if taskID == "" {
		return "", nil, service.TaskErrorWrapper(fmt.Errorf("task id is empty"), "invalid_response", http.StatusBadGateway)
	}

	public := dto.NewOpenAIVideo()
	public.ID = info.PublicTaskID
	public.TaskID = info.PublicTaskID
	public.CreatedAt = time.Now().Unix()
	public.Model = publicModelName(info)
	c.JSON(http.StatusOK, public)
	return taskID, responseBody, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]interface{}, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	uri := buildVideoURL(baseURL, taskID)
	if uri == "" {
		return nil, errors.New("SecureSkill base URL is required")
	}
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) GetModelList() []string {
	return []string{ModelName}
}

func (a *TaskAdaptor) GetChannelName() string {
	return "secureskill"
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var upstream responseTask
	if err := common.Unmarshal(respBody, &upstream); err != nil {
		return nil, errors.Wrap(err, "unmarshal minimax-h3 task result failed")
	}

	result := &relaycommon.TaskInfo{}
	switch strings.ToLower(strings.TrimSpace(upstream.Status)) {
	case "queued", "pending":
		result.Status = model.TaskStatusQueued
	case "in_progress", "processing":
		result.Status = model.TaskStatusInProgress
	case "completed", "succeeded", "success":
		result.Status = model.TaskStatusSuccess
		// Content is deliberately fetched only by the read-only /content proxy;
		// polling never downloads or creates a second task.
	case "failed", "cancelled", "canceled":
		result.Status = model.TaskStatusFailure
		result.Reason = "minimax-h3 task failed"
		if upstream.Error != nil && strings.TrimSpace(upstream.Error.Message) != "" {
			result.Reason = upstream.Error.Message
		}
	default:
		return nil, fmt.Errorf("unknown minimax-h3 task status: %q", upstream.Status)
	}
	if upstream.Progress > 0 && upstream.Progress < 100 {
		result.Progress = fmt.Sprintf("%d%%", upstream.Progress)
	}
	return result, nil
}

// ConvertToOpenAIVideo returns a public representation without exposing the
// provider task id or raw provider payload.  The content URL is the local
// read-only proxy, so polling and content delivery never recreate a task.
func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	video := task.ToOpenAIVideo()
	video.Model = task.Properties.OriginModelName
	if video.Model == "" {
		video.Model = ModelName
	}
	video.SetMetadata("url", taskcommon.BuildProxyURL(task.TaskID))
	return common.Marshal(video)
}

// BuildContentURL is used by the read-only content proxy.  No POST or billing
// operation is reachable through this helper.
func BuildContentURL(baseURL, taskID string) string {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(taskID) == "" {
		return ""
	}
	return buildVideoURL(baseURL, taskID) + "/content"
}

func buildVideoURL(baseURL, taskID string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	if strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/videos"
	} else if strings.HasSuffix(baseURL, "/openai") {
		baseURL += "/v1/videos"
	} else {
		baseURL += "/v1/videos"
	}
	if taskID == "" {
		return baseURL
	}
	return baseURL + "/" + url.PathEscape(taskID)
}

func publicModelName(info *relaycommon.RelayInfo) string {
	if info != nil && strings.TrimSpace(info.OriginModelName) != "" {
		return info.OriginModelName
	}
	return ModelName
}

func localTaskError(err error, code string) *dto.TaskError {
	return service.TaskErrorWrapperLocal(err, code, http.StatusBadRequest)
}

func (a *TaskAdaptor) validatedRequest(c *gin.Context) (relaycommon.TaskSubmitReq, error) {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return relaycommon.TaskSubmitReq{}, err
	}
	raw, err := readRawTaskRequest(c)
	if err != nil {
		return relaycommon.TaskSubmitReq{}, err
	}

	if raw.HasRaw {
		if err := validateRawFields(raw); err != nil {
			return relaycommon.TaskSubmitReq{}, err
		}
		// TaskSubmitReq's compatibility unmarshaller intentionally accepts a
		// few legacy duration encodings.  Reapply the validated raw value here
		// so JSON numbers such as 8.0 cannot be accepted and then silently
		// collapse to the zero-value/default duration.
		if req, err = applyRawDuration(req, raw); err != nil {
			return relaycommon.TaskSubmitReq{}, err
		}
	}
	if err := validateMetadataMap(req.Metadata); err != nil {
		return relaycommon.TaskSubmitReq{}, err
	}

	images, err := collectImageReferences(raw)
	if err != nil {
		return relaycommon.TaskSubmitReq{}, err
	}
	if len(images) == 0 {
		if strings.TrimSpace(req.InputReference) != "" {
			images = append(images, req.InputReference)
		}
		images = append(images, req.Images...)
		if strings.TrimSpace(req.Image) != "" {
			images = append(images, req.Image)
		}
	}

	normalized := make([]string, 0, len(images))
	for _, image := range images {
		image, err = normalizeImageReference(image)
		if err != nil {
			return relaycommon.TaskSubmitReq{}, err
		}
		normalized = append(normalized, image)
	}
	if len(normalized) < minImages {
		return relaycommon.TaskSubmitReq{}, errors.New("minimax-h3 requires at least one input image")
	}
	if len(normalized) > maxImages {
		return relaycommon.TaskSubmitReq{}, fmt.Errorf("minimax-h3 accepts at most %d input images", maxImages)
	}

	if strings.TrimSpace(req.Prompt) == "" {
		return relaycommon.TaskSubmitReq{}, errors.New("prompt is required")
	}
	if model := strings.TrimSpace(req.Model); model != "" && model != ModelName {
		return relaycommon.TaskSubmitReq{}, fmt.Errorf("unsupported minimax-h3 model: %s", model)
	}
	if len([]rune(req.Prompt)) > 2000 {
		return relaycommon.TaskSubmitReq{}, errors.New("prompt exceeds minimax-h3 limit of 2000 characters")
	}
	if mode := strings.TrimSpace(strings.ToLower(req.Mode)); mode != "" && mode != "image-to-video" && mode != "image2video" {
		return relaycommon.TaskSubmitReq{}, fmt.Errorf("unsupported minimax-h3 mode: %s", req.Mode)
	}
	if strings.TrimSpace(req.Size) != "" {
		return relaycommon.TaskSubmitReq{}, errors.New("resolution/size is not verified for minimax-h3")
	}

	duration, err := resolveDuration(req)
	if err != nil {
		return relaycommon.TaskSubmitReq{}, err
	}
	if duration < minDuration || duration > maxDuration {
		return relaycommon.TaskSubmitReq{}, fmt.Errorf("minimax-h3 duration must be between %d and %d seconds", minDuration, maxDuration)
	}
	req.Model = ModelName
	req.Images = normalized
	req.InputReference = ""
	req.Image = ""
	req.Duration = duration
	req.Seconds = ""
	req.Size = ""
	req.Metadata = nil
	return req, nil
}

func resolveDuration(req relaycommon.TaskSubmitReq) (int, error) {
	duration := req.Duration
	if req.Seconds != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(req.Seconds))
		if err != nil {
			return 0, fmt.Errorf("invalid seconds: %w", err)
		}
		duration = parsed
	}
	if req.Metadata != nil {
		for key, value := range req.Metadata {
			if strings.EqualFold(key, "duration") {
				parsed, err := integerValue(value)
				if err != nil {
					return 0, fmt.Errorf("invalid metadata duration: %w", err)
				}
				duration = parsed
			}
		}
	}
	if duration == 0 {
		duration = minDuration
	}
	return duration, nil
}

func integerValue(value interface{}) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, errors.New("must be an integer")
		}
		return int(v), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(v))
	default:
		return 0, fmt.Errorf("unsupported integer type %T", value)
	}
}

func readRawTaskRequest(c *gin.Context) (rawTaskRequest, error) {
	raw := rawTaskRequest{}
	if c == nil || c.Request == nil {
		return raw, nil
	}
	contentType := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return raw, err
		}
		raw.Values = form.Value
		raw.Files = form.File
		raw.HasRaw = true
		return raw, nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return raw, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return raw, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return raw, nil
	}
	if err := common.Unmarshal(body, &raw.JSON); err != nil {
		return raw, err
	}
	raw.HasRaw = true
	return raw, nil
}

func validateRawFields(raw rawTaskRequest) error {
	if raw.JSON != nil {
		for key, value := range raw.JSON {
			lower := strings.ToLower(strings.TrimSpace(key))
			if message, ok := unsupportedFieldNames[lower]; ok {
				return errors.New(message)
			}
			if _, ok := supportedTopLevelFields[lower]; !ok {
				return fmt.Errorf("unsupported minimax-h3 field: %s", key)
			}
			if lower == "size" && hasNonEmptyValue(value) {
				return errors.New("resolution/size is not verified for minimax-h3")
			}
			if lower == "model" {
				model, ok := value.(string)
				if !ok {
					return errors.New("model must be a string for minimax-h3")
				}
				model = strings.TrimSpace(model)
				if model != "" && model != ModelName {
					return fmt.Errorf("unsupported minimax-h3 model: %s", model)
				}
			}
			if lower == "duration" || lower == "seconds" {
				if err := validateExplicitDuration(value); err != nil {
					return err
				}
			}
			if lower == "metadata" {
				if err := validateMetadataValue(value); err != nil {
					return err
				}
			}
		}
	}
	for key, values := range raw.Values {
		lower := strings.ToLower(strings.TrimSpace(key))
		if message, ok := unsupportedFieldNames[lower]; ok {
			return errors.New(message)
		}
		if lower == "size" && hasNonEmptyStrings(values) {
			return errors.New("resolution/size is not verified for minimax-h3")
		}
		if lower == "model" {
			if len(values) == 0 {
				return errors.New("model is required for minimax-h3")
			}
			model := strings.TrimSpace(values[0])
			if model != "" && model != ModelName {
				return fmt.Errorf("unsupported minimax-h3 model: %s", model)
			}
		}
		if lower == "duration" || lower == "seconds" {
			for _, value := range values {
				if err := validateExplicitDuration(value); err != nil {
					return err
				}
			}
		}
		if _, ok := supportedTopLevelFields[lower]; !ok {
			return fmt.Errorf("unsupported minimax-h3 field: %s", key)
		}
	}
	for key, files := range raw.Files {
		lower := strings.ToLower(strings.TrimSpace(key))
		if message, ok := unsupportedFieldNames[lower]; ok {
			return errors.New(message)
		}
		if _, ok := imageFieldNames[lower]; !ok {
			return fmt.Errorf("unsupported minimax-h3 file field: %s", key)
		}
		for _, file := range files {
			if file == nil {
				return errors.New("empty minimax-h3 image file")
			}
			contentType := strings.ToLower(strings.TrimSpace(file.Header.Get("Content-Type")))
			if strings.HasPrefix(contentType, "video/") {
				return errors.New("reference video is not enabled for minimax-h3 candidate")
			}
		}
	}
	return nil
}

func validateMetadataValue(value interface{}) error {
	metadata, ok := value.(map[string]interface{})
	if !ok {
		if text, ok := value.(string); ok {
			if strings.TrimSpace(text) == "" {
				return nil
			}
			var parsed map[string]interface{}
			if err := common.Unmarshal([]byte(text), &parsed); err != nil || parsed == nil {
				return errors.New("metadata must be an object for minimax-h3")
			}
			return validateMetadataMap(parsed)
		}
		return errors.New("metadata must be an object for minimax-h3")
	}
	return validateMetadataMap(metadata)
}

func validateMetadataMap(metadata map[string]interface{}) error {
	for key, value := range metadata {
		lower := strings.ToLower(strings.TrimSpace(key))
		if message, ok := unsupportedFieldNames[lower]; ok {
			return errors.New(message)
		}
		if lower != "duration" {
			if lower == "resolution" || lower == "size" {
				return errors.New("resolution/size is not verified for minimax-h3")
			}
			return fmt.Errorf("unsupported minimax-h3 metadata field: %s", key)
		}
		parsed, err := integerValue(value)
		if err != nil {
			return fmt.Errorf("invalid metadata duration: %w", err)
		}
		if parsed < minDuration || parsed > maxDuration {
			return fmt.Errorf("minimax-h3 duration must be between %d and %d seconds", minDuration, maxDuration)
		}
	}
	return nil
}

func validateExplicitDuration(value interface{}) error {
	parsed, err := integerValue(value)
	if err != nil {
		return fmt.Errorf("invalid minimax-h3 duration: %w", err)
	}
	if parsed < minDuration || parsed > maxDuration {
		return fmt.Errorf("minimax-h3 duration must be between %d and %d seconds", minDuration, maxDuration)
	}
	return nil
}

func applyRawDuration(req relaycommon.TaskSubmitReq, raw rawTaskRequest) (relaycommon.TaskSubmitReq, error) {
	if raw.JSON != nil {
		for key, value := range raw.JSON {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "duration":
				parsed, err := integerValue(value)
				if err != nil {
					return relaycommon.TaskSubmitReq{}, fmt.Errorf("invalid minimax-h3 duration: %w", err)
				}
				req.Duration = parsed
			case "seconds":
				parsed, err := integerValue(value)
				if err != nil {
					return relaycommon.TaskSubmitReq{}, fmt.Errorf("invalid minimax-h3 seconds: %w", err)
				}
				req.Seconds = strconv.Itoa(parsed)
			}
		}
	}
	for key, values := range raw.Values {
		if len(values) == 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "duration":
			parsed, err := strconv.Atoi(strings.TrimSpace(values[len(values)-1]))
			if err != nil {
				return relaycommon.TaskSubmitReq{}, fmt.Errorf("invalid minimax-h3 duration: %w", err)
			}
			req.Duration = parsed
		case "seconds":
			req.Seconds = strings.TrimSpace(values[len(values)-1])
		}
	}
	return req, nil
}

func collectImageReferences(raw rawTaskRequest) ([]string, error) {
	var images []string
	if raw.JSON != nil {
		for key, value := range raw.JSON {
			if _, ok := imageFieldNames[strings.ToLower(strings.TrimSpace(key))]; !ok {
				continue
			}
			values, err := stringValues(value)
			if err != nil {
				return nil, fmt.Errorf("invalid %s: %w", key, err)
			}
			images = append(images, values...)
		}
	}
	for key, values := range raw.Values {
		if _, ok := imageFieldNames[strings.ToLower(strings.TrimSpace(key))]; ok {
			images = append(images, values...)
		}
	}
	for key, files := range raw.Files {
		if _, ok := imageFieldNames[strings.ToLower(strings.TrimSpace(key))]; !ok {
			continue
		}
		for _, fileHeader := range files {
			if fileHeader.Size > maxImageFileSize {
				return nil, fmt.Errorf("image file %s exceeds %d MiB limit", fileHeader.Filename, maxImageFileSize/(1024*1024))
			}
			file, err := fileHeader.Open()
			if err != nil {
				return nil, fmt.Errorf("open image file: %w", err)
			}
			fileBytes, readErr := io.ReadAll(file)
			_ = file.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read image file: %w", readErr)
			}
			contentType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
			if contentType == "" || contentType == "application/octet-stream" {
				contentType = http.DetectContentType(fileBytes)
			}
			if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
				return nil, fmt.Errorf("file %s is not an image", fileHeader.Filename)
			}
			images = append(images, "data:"+contentType+";base64,"+base64.StdEncoding.EncodeToString(fileBytes))
		}
	}
	return images, nil
}

func stringValues(value interface{}) ([]string, error) {
	switch v := value.(type) {
	case string:
		return []string{v}, nil
	case []string:
		return append([]string(nil), v...), nil
	case []interface{}:
		values := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("image reference must be a string, got %T", item)
			}
			values = append(values, text)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("image reference must be a string or array, got %T", value)
	}
}

func normalizeImageReference(image string) (string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", errors.New("image reference is empty")
	}
	if strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		return image, nil
	}
	if !strings.HasPrefix(strings.ToLower(image), "data:image/") {
		return "", errors.New("image reference must be an http(s) URL or image data URL")
	}
	comma := strings.IndexByte(image, ',')
	if comma <= 0 || comma == len(image)-1 || !strings.Contains(strings.ToLower(image[:comma]), ";base64") {
		return "", errors.New("image data URL must contain base64 payload")
	}
	encoded := image[comma+1:]
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		if _, rawErr := base64.RawStdEncoding.DecodeString(encoded); rawErr != nil {
			return "", errors.New("image data URL has invalid base64 payload")
		}
	}
	return image, nil
}

func hasNonEmptyValue(value interface{}) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case []interface{}:
		return len(v) > 0
	case []string:
		return hasNonEmptyStrings(v)
	default:
		return true
	}
}

func hasNonEmptyStrings(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}
