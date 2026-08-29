// Package tianyi_h3 implements the development-only Tianyi TokenHub MiniMax
// H3 adapter.  Tianyi exposes a native asynchronous JSON contract that is
// different from both the existing SecureSkill adapters and legacy Hailuo.
//
// The public Tianyi documentation available for this candidate specifies a
// text content item, resolution, duration and ratio.  Image/audio/video
// content items are deliberately rejected until their exact wire shape is
// independently documented and tested.
package tianyi_h3

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
)

const (
	ModelName          = "minimax-h3"
	DefaultResolution  = "2K"
	DefaultRatio       = "16:9"
	MinDuration        = 5
	MaxDuration        = 15
	videoGenerationURL = "/v1/video_generation"
	queryGenerationURL = "/v1/query/video_generation"
)

var _ channel.TaskAdaptor = (*TaskAdaptor)(nil)
var _ channel.OpenAIVideoConverter = (*TaskAdaptor)(nil)

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type requestPayload struct {
	Model      string        `json:"model"`
	Content    []textContent `json:"content"`
	Resolution string        `json:"resolution"`
	Duration   int           `json:"duration"`
	Ratio      string        `json:"ratio"`
}

type developmentRequest struct {
	Model      string
	Prompt     string
	Resolution string
	Duration   int
	Ratio      string
}

type providerError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type providerResponse struct {
	ID       string          `json:"id"`
	TaskID   string          `json:"task_id"`
	Status   string          `json:"status"`
	Message  string          `json:"message"`
	VideoURL string          `json:"video_url"`
	URL      string          `json:"url"`
	FileURL  string          `json:"file_url"`
	Error    *providerError  `json:"error"`
	Data     json.RawMessage `json:"data"`
	Result   json.RawMessage `json:"result"`
}

type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	if info == nil {
		return
	}
	a.apiKey = info.ApiKey
	a.baseURL = info.ChannelBaseUrl
}

// ValidateRequestAndSetAction accepts only the documented text generation
// request.  It is intentionally stricter than the generic task parser so an
// unverified multimodal payload cannot be silently converted to text.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if c == nil || c.Request == nil {
		return localError(errors.New("request is required"), "invalid_request")
	}
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		return localError(errors.New("TY H3 development adapter accepts documented JSON only"), "unsupported_input")
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return localError(err, "invalid_request")
	}
	body, err := storage.Bytes()
	if err != nil {
		return localError(err, "invalid_request")
	}
	req, err := parseDevelopmentRequest(body)
	if err != nil {
		return localError(err, "unsupported_tianyi_h3_request")
	}

	c.Set("tianyi_h3_request", req)
	// Keep the shared task request available to pricing/task bookkeeping.
	c.Set("task_request", relaycommon.TaskSubmitReq{
		Model:    req.Model,
		Prompt:   req.Prompt,
		Duration: req.Duration,
		Size:     req.Resolution,
	})
	if info != nil {
		info.Action = constant.TaskActionGenerate
	}
	return nil
}

func (a *TaskAdaptor) EstimateBilling(_ *gin.Context, _ *relaycommon.RelayInfo) map[string]float64 {
	// The public service page exposes input/output pricing, but this adapter
	// does not invent a duration/resolution multiplier.  Product pricing must
	// be configured and reviewed separately from the wire adapter.
	return nil
}

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	if strings.TrimSpace(a.baseURL) == "" {
		return "", errors.New("TY H3 base URL is required")
	}
	return joinTianyiPath(a.baseURL, videoGenerationURL), nil
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	if req == nil {
		return errors.New("request is required")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, _ *relaycommon.RelayInfo) (io.Reader, error) {
	v, ok := c.Get("tianyi_h3_request")
	if !ok {
		return nil, errors.New("TY H3 request not found in context")
	}
	req, ok := v.(developmentRequest)
	if !ok {
		return nil, errors.New("invalid TY H3 request in context")
	}
	payload := requestPayload{
		Model:      ModelName,
		Content:    []textContent{{Type: "text", Text: req.Prompt}},
		Resolution: req.Resolution,
		Duration:   req.Duration,
		Ratio:      req.Ratio,
	}
	data, err := common.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal TY H3 request: %w", err)
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	if resp == nil || resp.Body == nil {
		return "", nil, service.TaskErrorWrapper(errors.New("empty TY H3 response"), "invalid_response", http.StatusBadGateway)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusBadGateway)
	}
	parsed, err := decodeProviderResponse(body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "unmarshal_response_failed", http.StatusBadGateway)
	}
	if isFailureStatus(parsed.Status) {
		return "", nil, service.TaskErrorWrapperLocal(errors.New(providerFailureMessage(parsed)), "upstream_task_failed", http.StatusBadGateway)
	}
	if parsed.Status != "" && !isAcceptedStatus(parsed.Status) {
		return "", nil, service.TaskErrorWrapper(fmt.Errorf("unsupported TY H3 initial status: %q", parsed.Status), "invalid_response", http.StatusBadGateway)
	}
	taskID = responseTaskID(parsed)
	if taskID == "" {
		return "", nil, service.TaskErrorWrapper(errors.New("TY H3 task id is empty"), "invalid_response", http.StatusBadGateway)
	}
	if info != nil && c != nil {
		public := dto.NewOpenAIVideo()
		public.ID = info.PublicTaskID
		public.TaskID = info.PublicTaskID
		public.CreatedAt = time.Now().Unix()
		public.Model = publicModelName(info)
		c.JSON(http.StatusOK, public)
	}
	return taskID, body, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, errors.New("invalid task_id")
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("TY H3 base URL is required")
	}
	uri := joinTianyiPath(baseURL, queryGenerationURL+"/"+url.PathEscape(taskID))
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) GetModelList() []string { return []string{ModelName} }

func (a *TaskAdaptor) GetChannelName() string { return "TY" }

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	parsed, err := decodeProviderResponse(respBody)
	if err != nil {
		return nil, fmt.Errorf("unmarshal TY H3 task result: %w", err)
	}
	status := strings.ToLower(strings.TrimSpace(parsed.Status))
	if status == "" {
		return nil, errors.New("TY H3 task status is empty")
	}
	result := &relaycommon.TaskInfo{}
	switch {
	case status == "queued" || status == "pending" || status == "submitted":
		result.Status = model.TaskStatusQueued
		result.Progress = "20%"
	case status == "running" || status == "processing" || status == "in_progress":
		result.Status = model.TaskStatusInProgress
		result.Progress = "30%"
	case status == "success" || status == "succeeded" || status == "completed" || status == "finished":
		result.Status = model.TaskStatusSuccess
		result.Progress = "100%"
		result.Url = responseVideoURL(parsed)
	case isFailureStatus(status):
		result.Status = model.TaskStatusFailure
		result.Progress = "100%"
		result.Reason = providerFailureMessage(parsed)
	default:
		return nil, fmt.Errorf("unknown TY H3 task status: %q", parsed.Status)
	}
	return result, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	if task == nil {
		return nil, errors.New("task is required")
	}
	video := task.ToOpenAIVideo()
	video.Model = task.Properties.OriginModelName
	if video.Model == "" {
		video.Model = ModelName
	}
	video.SetMetadata("url", taskcommon.BuildProxyURL(task.TaskID))
	return common.Marshal(video)
}

func parseDevelopmentRequest(body []byte) (developmentRequest, error) {
	var raw map[string]json.RawMessage
	if err := common.Unmarshal(body, &raw); err != nil {
		return developmentRequest{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(raw) == 0 {
		return developmentRequest{}, errors.New("request body is empty")
	}
	for key := range raw {
		lower := strings.ToLower(strings.TrimSpace(key))
		if key != lower {
			return developmentRequest{}, fmt.Errorf("unsupported TY H3 field casing: %s", key)
		}
		switch lower {
		case "model", "prompt", "duration", "seconds", "resolution", "ratio", "size", "mode":
		case "image", "images", "input_reference", "video", "audio", "audio_url", "reference_video", "reference_video_url":
			return developmentRequest{}, errors.New("TY H3 image/audio/video inputs are not enabled in development adapter")
		default:
			return developmentRequest{}, fmt.Errorf("unsupported TY H3 field: %s", key)
		}
	}
	result := developmentRequest{Model: ModelName, Resolution: DefaultResolution, Ratio: DefaultRatio, Duration: MinDuration}
	if _, hasDuration := raw["duration"]; hasDuration {
		if _, hasSeconds := raw["seconds"]; hasSeconds {
			return developmentRequest{}, errors.New("duration and seconds cannot both be set")
		}
	}
	if _, hasResolution := raw["resolution"]; hasResolution {
		if _, hasSize := raw["size"]; hasSize {
			return developmentRequest{}, errors.New("resolution and size cannot both be set")
		}
	}
	if value, ok := raw["model"]; ok {
		if err := common.Unmarshal(value, &result.Model); err != nil {
			return developmentRequest{}, errors.New("model must be a string")
		}
		result.Model = strings.TrimSpace(result.Model)
		if result.Model != "" && result.Model != ModelName {
			return developmentRequest{}, fmt.Errorf("unsupported TY H3 model: %s", result.Model)
		}
		result.Model = ModelName
	}
	if value, ok := raw["prompt"]; ok {
		if err := common.Unmarshal(value, &result.Prompt); err != nil {
			return developmentRequest{}, errors.New("prompt must be a string")
		}
	}
	if strings.TrimSpace(result.Prompt) == "" {
		return developmentRequest{}, errors.New("prompt is required")
	}
	if len([]rune(result.Prompt)) > 2000 {
		return developmentRequest{}, errors.New("prompt exceeds TY H3 limit of 2000 characters")
	}
	if value, ok := raw["mode"]; ok {
		var mode string
		if err := common.Unmarshal(value, &mode); err != nil {
			return developmentRequest{}, errors.New("mode must be a string")
		}
		mode = strings.ToLower(strings.TrimSpace(mode))
		if mode != "" && mode != "text-to-video" && mode != "text2video" {
			return developmentRequest{}, errors.New("TY H3 development adapter supports text-to-video only")
		}
	}
	if value, ok := raw["duration"]; ok {
		if err := decodeStrictInt(value, &result.Duration); err != nil {
			return developmentRequest{}, fmt.Errorf("invalid duration: %w", err)
		}
	}
	if value, ok := raw["seconds"]; ok {
		if err := decodeStrictInt(value, &result.Duration); err != nil {
			return developmentRequest{}, fmt.Errorf("invalid seconds: %w", err)
		}
	}
	if result.Duration < MinDuration || result.Duration > MaxDuration {
		return developmentRequest{}, fmt.Errorf("TY H3 duration must be between %d and %d seconds", MinDuration, MaxDuration)
	}
	if value, ok := raw["resolution"]; ok {
		if err := common.Unmarshal(value, &result.Resolution); err != nil {
			return developmentRequest{}, errors.New("resolution must be a string")
		}
	}
	if value, ok := raw["size"]; ok {
		var size string
		if err := common.Unmarshal(value, &size); err != nil {
			return developmentRequest{}, errors.New("size must be a string")
		}
		if _, hasResolution := raw["resolution"]; !hasResolution {
			result.Resolution = size
		}
	}
	result.Resolution = strings.TrimSpace(result.Resolution)
	if result.Resolution != "2K" && result.Resolution != "768P" {
		return developmentRequest{}, fmt.Errorf("unsupported TY H3 resolution: %s", result.Resolution)
	}
	if value, ok := raw["ratio"]; ok {
		if err := common.Unmarshal(value, &result.Ratio); err != nil {
			return developmentRequest{}, errors.New("ratio must be a string")
		}
	}
	result.Ratio = strings.TrimSpace(result.Ratio)
	if result.Ratio != DefaultRatio {
		return developmentRequest{}, fmt.Errorf("unsupported TY H3 ratio: %s", result.Ratio)
	}
	return result, nil
}

func decodeStrictInt(raw json.RawMessage, target *int) error {
	var value int
	if err := common.Unmarshal(raw, &value); err == nil {
		*target = value
		return nil
	}
	var text string
	if err := common.Unmarshal(raw, &text); err != nil || strings.TrimSpace(text) == "" {
		return errors.New("must be an integer")
	}
	var parsed int
	if _, err := fmt.Sscanf(strings.TrimSpace(text), "%d", &parsed); err != nil || fmt.Sprintf("%d", parsed) != strings.TrimSpace(text) {
		return errors.New("must be an integer")
	}
	*target = parsed
	return nil
}

func decodeProviderResponse(body []byte) (providerResponse, error) {
	var parsed providerResponse
	if err := common.Unmarshal(body, &parsed); err != nil {
		return providerResponse{}, err
	}
	return parsed, nil
}

func responseTaskID(resp providerResponse) string {
	if id := strings.TrimSpace(resp.ID); id != "" {
		return id
	}
	if id := strings.TrimSpace(resp.TaskID); id != "" {
		return id
	}
	for _, raw := range []json.RawMessage{resp.Data, resp.Result} {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var nested providerResponse
		if common.Unmarshal(raw, &nested) == nil {
			if id := responseTaskID(nested); id != "" {
				return id
			}
		}
	}
	return ""
}

func responseVideoURL(resp providerResponse) string {
	for _, value := range []string{resp.VideoURL, resp.URL, resp.FileURL} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	for _, raw := range []json.RawMessage{resp.Data, resp.Result} {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var nested providerResponse
		if common.Unmarshal(raw, &nested) == nil {
			if value := responseVideoURL(nested); value != "" {
				return value
			}
		}
	}
	return ""
}

func providerFailureMessage(resp providerResponse) string {
	if resp.Error != nil && strings.TrimSpace(resp.Error.Message) != "" {
		return resp.Error.Message
	}
	if strings.TrimSpace(resp.Message) != "" {
		return resp.Message
	}
	return "TY H3 task failed"
}

func isFailureStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "failure", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func isAcceptedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending", "submitted", "running", "processing", "in_progress":
		return true
	default:
		return false
	}
}

func joinTianyiPath(baseURL, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1") && strings.HasPrefix(path, "/v1/") {
		return baseURL + strings.TrimPrefix(path, "/v1")
	}
	if baseURL == "" {
		return path
	}
	return baseURL + path
}

func publicModelName(info *relaycommon.RelayInfo) string {
	if info != nil && strings.TrimSpace(info.OriginModelName) != "" {
		return info.OriginModelName
	}
	return ModelName
}

func localError(err error, code string) *dto.TaskError {
	return service.TaskErrorWrapperLocal(err, code, http.StatusBadRequest)
}
