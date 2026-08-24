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

var ErrRequestBodyTooLarge = errors.New("request body too large")
var ErrRequestBodyUnavailable = errors.New("request body is unavailable")

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
		syncMultipartFormValues(c.Request, c.Request.MultipartForm)
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
	if saved, ok := c.Get("_original_multipart_ct"); ok {
		contentType = saved.(string)
	} else {
		contentType = c.Request.Header.Get("Content-Type")
		c.Set("_original_multipart_ct", contentType)
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
	syncMultipartFormValues(c.Request, form)

	// Reset request body
	if _, seekErr := storage.Seek(0, io.SeekStart); seekErr != nil {
		return nil, seekErr
	}
	c.Request.Body = io.NopCloser(storage)
	return form, nil
}

func syncMultipartFormValues(request *http.Request, form *multipart.Form) {
	if request == nil || form == nil {
		return
	}
	request.MultipartForm = form
	if request.PostForm == nil {
		request.PostForm = make(url.Values)
	}
	mergeFormValuesOnce(request.PostForm, form.Value)

	// Request.Form is the union of query and post values. Preserve any values
	// already installed by another middleware and add only missing occurrences,
	// so repeated reusable parses remain idempotent.
	desired := make(url.Values)
	for key, values := range request.URL.Query() {
		desired[key] = append(desired[key], values...)
	}
	for key, values := range form.Value {
		desired[key] = append(desired[key], values...)
	}
	if request.Form == nil {
		request.Form = make(url.Values)
	}
	mergeFormValuesOnce(request.Form, desired)
}

func mergeFormValuesOnce(destination url.Values, source map[string][]string) {
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
	if saved, ok := c.Get("_original_multipart_ct"); ok {
		contentType = saved.(string)
	} else {
		contentType = c.Request.Header.Get("Content-Type")
		c.Set("_original_multipart_ct", contentType)
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
