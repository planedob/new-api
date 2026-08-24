package common

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/pkg/errors"

	"github.com/gin-gonic/gin"
)

const KeyRequestBody = "key_request_body"
const KeyBodyStorage = "key_body_storage"
const keyOriginalMultipartContentType = "original_multipart_content_type"

var ErrRequestBodyTooLarge = errors.New("request body too large")
var ErrRequestBodyUnavailable = errors.New("request body is unavailable")
var ErrImageInputRequired = errors.New("image is required")
var ErrImageInputEmpty = errors.New("image is empty")
var ErrImageInputUnsupported = errors.New("image content is unsupported")

const (
	Image2ValidationMissingBoundary = "image_edit_missing_boundary"
	Image2ValidationTruncatedBody   = "image_edit_truncated_multipart"
	Image2ValidationBodyUnavailable = "image_edit_body_unavailable"
	Image2ValidationMissingImage    = "image_edit_missing_image"
	Image2ValidationEmptyImage      = "image_edit_empty_image"
	Image2ValidationUnsupported     = "image_edit_unsupported_image"
	Image2ValidationTooLarge        = "image_edit_request_too_large"
	Image2ValidationMalformed       = "image_edit_malformed_multipart"
)

// ClassifyImage2RequestValidationError converts parser and file-validation
// errors into stable, non-sensitive client/log contracts. Raw multipart errors
// can contain parser internals and must never cross the request boundary.
func ClassifyImage2RequestValidationError(err error) (code string, message string, statusCode int) {
	switch {
	case IsRequestBodyTooLargeError(err):
		return Image2ValidationTooLarge, "image edit request is too large", http.StatusRequestEntityTooLarge
	case errors.Is(err, errBoundaryNotFound):
		return Image2ValidationMissingBoundary, "image edit multipart boundary is missing", http.StatusBadRequest
	case errors.Is(err, ErrRequestBodyUnavailable):
		return Image2ValidationBodyUnavailable, "image edit request body is unavailable", http.StatusBadRequest
	case errors.Is(err, ErrImageInputRequired):
		return Image2ValidationMissingImage, "image edit image is required", http.StatusBadRequest
	case errors.Is(err, ErrImageInputEmpty):
		return Image2ValidationEmptyImage, "image edit image is empty", http.StatusBadRequest
	case errors.Is(err, ErrImageInputUnsupported):
		return Image2ValidationUnsupported, "image edit image format is unsupported", http.StatusBadRequest
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF),
		strings.Contains(strings.ToLower(err.Error()), "unexpected eof"),
		strings.Contains(strings.ToLower(err.Error()), "nextpart: eof"):
		return Image2ValidationTruncatedBody, "image edit multipart body is incomplete", http.StatusBadRequest
	default:
		return Image2ValidationMalformed, "image edit multipart request is malformed", http.StatusBadRequest
	}
}

// IsMultipartFormData recognizes the media type case-insensitively, including
// malformed parameter sections that still need multipart-specific fail-closed
// handling rather than an attempted JSON decode.
func IsMultipartFormData(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		return strings.EqualFold(mediaType, "multipart/form-data")
	}
	mediaType, _, _ = strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "multipart/form-data")
}

func IsRequestBodyTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRequestBodyTooLarge) {
		return true
	}
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

func GetRequestBody(c *gin.Context) (io.Seeker, error) {
	// 首先检查是否有 BodyStorage 缓存
	if storage, exists := c.Get(KeyBodyStorage); exists && storage != nil {
		if bs, ok := storage.(BodyStorage); ok {
			if _, err := bs.Seek(0, io.SeekStart); err != nil {
				return nil, fmt.Errorf("failed to seek body storage: %w", err)
			}
			return bs, nil
		}
	}

	// 检查旧的缓存方式
	cached, exists := c.Get(KeyRequestBody)
	if exists && cached != nil {
		if b, ok := cached.([]byte); ok {
			bs, err := CreateBodyStorage(b)
			if err != nil {
				return nil, err
			}
			c.Set(KeyBodyStorage, bs)
			return bs, nil
		}
	}

	maxMB := constant.MaxRequestBodyMB
	if maxMB <= 0 {
		maxMB = 128 // 默认 128MB
	}
	maxBytes := int64(maxMB) << 20

	contentLength := c.Request.ContentLength

	// 使用新的存储系统
	storage, err := CreateBodyStorageFromReader(c.Request.Body, contentLength, maxBytes)
	_ = c.Request.Body.Close()

	if err != nil {
		if IsRequestBodyTooLargeError(err) {
			return nil, errors.Wrap(ErrRequestBodyTooLarge, fmt.Sprintf("request body exceeds %d MB", maxMB))
		}
		return nil, err
	}

	// 缓存存储对象
	c.Set(KeyBodyStorage, storage)
	// A positive Content-Length with no readable bytes means an earlier
	// consumer drained the request without preserving it. Treat that as a
	// malformed request instead of caching an empty body and surfacing a
	// misleading multipart EOF later. Do not compare the two lengths: request
	// decompression legitimately changes the number of readable bytes.
	if contentLength > 0 && storage.Size() == 0 {
		_ = storage.Close()
		c.Set(KeyBodyStorage, nil)
		return nil, ErrRequestBodyUnavailable
	}

	return storage, nil
}

// GetBodyStorage 获取请求体存储对象（用于需要多次读取的场景）
func GetBodyStorage(c *gin.Context) (BodyStorage, error) {
	seeker, err := GetRequestBody(c)
	if err != nil {
		return nil, err
	}
	bs, ok := seeker.(BodyStorage)
	if !ok {
		return nil, errors.New("unexpected body storage type")
	}
	return bs, nil
}

// CleanupBodyStorage 清理请求体存储（应在请求结束时调用）
func CleanupBodyStorage(c *gin.Context) {
	if storage, exists := c.Get(KeyBodyStorage); exists && storage != nil {
		if bs, ok := storage.(BodyStorage); ok {
			bs.Close()
		}
		c.Set(KeyBodyStorage, nil)
	}
	c.Set(keyOriginalMultipartContentType, nil)
}

func UnmarshalBodyReusable(c *gin.Context, v any) error {
	storage, err := GetBodyStorage(c)
	if err != nil {
		return err
	}
	requestBody, err := storage.Bytes()
	if err != nil {
		return err
	}
	contentType := c.Request.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		err = Unmarshal(requestBody, v)
	} else if strings.Contains(contentType, gin.MIMEPOSTForm) {
		err = parseFormData(requestBody, v)
	} else if strings.Contains(contentType, gin.MIMEMultipartPOSTForm) {
		err = parseMultipartFormData(c, requestBody, v)
	} else {
		// skip for now
		// TODO: someday non json request have variant model, we will need to implementation this
	}
	if err != nil {
		return err
	}
	// Reset request body
	if _, seekErr := storage.Seek(0, io.SeekStart); seekErr != nil {
		return seekErr
	}
	c.Request.Body = io.NopCloser(storage)
	return nil
}

func SetContextKey(c *gin.Context, key constant.ContextKey, value any) {
	c.Set(string(key), value)
}

func GetContextKey(c *gin.Context, key constant.ContextKey) (any, bool) {
	return c.Get(string(key))
}

func GetContextKeyString(c *gin.Context, key constant.ContextKey) string {
	return c.GetString(string(key))
}

func GetContextKeyInt(c *gin.Context, key constant.ContextKey) int {
	return c.GetInt(string(key))
}

func GetContextKeyBool(c *gin.Context, key constant.ContextKey) bool {
	return c.GetBool(string(key))
}

func GetContextKeyStringSlice(c *gin.Context, key constant.ContextKey) []string {
	return c.GetStringSlice(string(key))
}

func GetContextKeyStringMap(c *gin.Context, key constant.ContextKey) map[string]any {
	return c.GetStringMap(string(key))
}

func GetContextKeyTime(c *gin.Context, key constant.ContextKey) time.Time {
	return c.GetTime(string(key))
}

func GetContextKeyType[T any](c *gin.Context, key constant.ContextKey) (T, bool) {
	if value, ok := c.Get(string(key)); ok {
		if v, ok := value.(T); ok {
			return v, true
		}
	}
	var t T
	return t, false
}

func ApiError(c *gin.Context, err error) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": err.Error(),
	})
}

func ApiErrorMsg(c *gin.Context, msg string) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": msg,
	})
}

func ApiSuccess(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

// ApiErrorI18n returns a translated error message based on the user's language preference
// key is the i18n message key, args is optional template data
func ApiErrorI18n(c *gin.Context, key string, args ...map[string]any) {
	msg := TranslateMessage(c, key, args...)
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": msg,
	})
}

// ApiSuccessI18n returns a translated success message based on the user's language preference
func ApiSuccessI18n(c *gin.Context, key string, data any, args ...map[string]any) {
	msg := TranslateMessage(c, key, args...)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": msg,
		"data":    data,
	})
}

// TranslateMessage is a helper function that calls i18n.T
// This function is defined here to avoid circular imports
// The actual implementation will be set during init
var TranslateMessage func(c *gin.Context, key string, args ...map[string]any) string

func init() {
	// Default implementation that returns the key as-is
	// This will be replaced by i18n.T during i18n initialization
	TranslateMessage = func(c *gin.Context, key string, args ...map[string]any) string {
		c.Header("X-Translate-id", "d5e7afdfc7f03414b941f9c1e7096be9966510e7")
		return key
	}
}

func ParseMultipartFormReusable(c *gin.Context) (*multipart.Form, error) {
	if c.Request.MultipartForm != nil {
		syncExistingMultipartFormValues(c.Request, c.Request.MultipartForm)
		return c.Request.MultipartForm, nil
	}

	storage, err := GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	requestBody, err := storage.Bytes()
	if err != nil {
		return nil, err
	}

	// Use the original Content-Type saved on first call to avoid boundary
	// mismatch when callers overwrite c.Request.Header after multipart rebuild.
	var contentType string
	if saved, ok := c.Get(keyOriginalMultipartContentType); ok && saved != nil {
		contentType, _ = saved.(string)
	} else {
		contentType = c.Request.Header.Get("Content-Type")
		c.Set(keyOriginalMultipartContentType, contentType)
	}
	boundary, err := parseBoundary(contentType)
	if err != nil {
		return nil, err
	}

	reader := multipart.NewReader(bytes.NewReader(requestBody), boundary)
	form, err := reader.ReadForm(multipartMemoryLimit())
	if err != nil {
		return nil, err
	}

	// Install one shared form before resetting the body. All downstream
	// middleware and adaptors must reuse this object instead of parsing again.
	installMultipartFormValues(c.Request, form)

	// Reset request body
	if _, seekErr := storage.Seek(0, io.SeekStart); seekErr != nil {
		return nil, seekErr
	}
	c.Request.Body = io.NopCloser(storage)
	return form, nil
}

func installMultipartFormValues(request *http.Request, form *multipart.Form) {
	if request == nil || form == nil {
		return
	}
	request.MultipartForm = form
	if request.Form == nil {
		request.Form = make(url.Values)
	}
	mergeFormValuesAtLeast(request.Form, request.URL.Query())
	request.PostForm = appendFormValues(request.PostForm, form.Value)
	request.Form = appendFormValues(request.Form, form.Value)
}

func syncExistingMultipartFormValues(request *http.Request, form *multipart.Form) {
	if request == nil || form == nil {
		return
	}
	if request.PostForm == nil {
		request.PostForm = appendFormValues(nil, form.Value)
	}
	if request.Form == nil {
		request.Form = appendFormValues(nil, form.Value)
	}
	mergeFormValuesAtLeast(request.Form, request.URL.Query())
}

func appendFormValues(destination url.Values, source map[string][]string) url.Values {
	if destination == nil {
		destination = make(url.Values)
	}
	for key, values := range source {
		destination[key] = append(destination[key], values...)
	}
	return destination
}

// mergeFormValuesAtLeast ensures the destination contains every query-value
// occurrence without duplicating values already installed by net/http or an
// earlier middleware. Multipart body values deliberately use appendFormValues
// instead: they must be appended in full exactly once, even when equal to an
// existing value.
func mergeFormValuesAtLeast(destination url.Values, source map[string][]string) {
	for key, values := range source {
		existingCounts := make(map[string]int, len(destination[key]))
		for _, value := range destination[key] {
			existingCounts[value]++
		}
		requiredCounts := make(map[string]int, len(values))
		for _, value := range values {
			requiredCounts[value]++
			if existingCounts[value] < requiredCounts[value] {
				destination[key] = append(destination[key], value)
				existingCounts[value]++
			}
		}
	}
}

func processFormMap(formMap map[string]any, v any) error {
	jsonData, err := Marshal(formMap)
	if err != nil {
		return err
	}

	err = Unmarshal(jsonData, v)
	if err != nil {
		return err
	}

	return nil
}

func parseFormData(data []byte, v any) error {
	values, err := url.ParseQuery(string(data))
	if err != nil {
		return err
	}
	formMap := make(map[string]any)
	for key, vals := range values {
		if len(vals) == 1 {
			formMap[key] = vals[0]
		} else {
			formMap[key] = vals
		}
	}

	return processFormMap(formMap, v)
}

func parseMultipartFormData(c *gin.Context, data []byte, v any) error {
	var contentType string
	if saved, ok := c.Get(keyOriginalMultipartContentType); ok && saved != nil {
		contentType, _ = saved.(string)
	} else {
		contentType = c.Request.Header.Get("Content-Type")
		c.Set(keyOriginalMultipartContentType, contentType)
	}
	boundary, err := parseBoundary(contentType)
	if err != nil {
		if errors.Is(err, errBoundaryNotFound) {
			return Unmarshal(data, v) // Fallback to JSON
		}
		return err
	}

	reader := multipart.NewReader(bytes.NewReader(data), boundary)
	form, err := reader.ReadForm(multipartMemoryLimit())
	if err != nil {
		return err
	}
	defer form.RemoveAll()
	formMap := make(map[string]any)
	for key, vals := range form.Value {
		if len(vals) == 1 {
			formMap[key] = vals[0]
		} else {
			formMap[key] = vals
		}
	}

	return processFormMap(formMap, v)
}

var errBoundaryNotFound = errors.New("multipart boundary not found")

// parseBoundary extracts the multipart boundary from the Content-Type header using mime.ParseMediaType
func parseBoundary(contentType string) (string, error) {
	if contentType == "" {
		return "", errBoundaryNotFound
	}
	// Boundary-UUID / boundary-------xxxxxx
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", err
	}
	boundary, ok := params["boundary"]
	if !ok || boundary == "" {
		return "", errBoundaryNotFound
	}
	return boundary, nil
}

// multipartMemoryLimit returns the configured multipart memory limit in bytes
func multipartMemoryLimit() int64 {
	limitMB := constant.MaxFileDownloadMB
	if limitMB <= 0 {
		limitMB = 32
	}
	return int64(limitMB) << 20
}
