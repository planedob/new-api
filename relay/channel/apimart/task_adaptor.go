package apimart

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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
	ModelName = "gpt-image-2"

	// rawTaskRequestKey stores an immutable copy of the request body.  It is
	// deliberately package-scoped and never serialized into model.Task.Data;
	// the provider response is represented by imageJobSubmission instead.
	rawTaskRequestKey = "apimart_raw_image_task_request"
)

var ModelList = []string{
	ModelName,
	"gpt-image-2-official",
}

var ChannelName = "apimart-image"

// ImageJob is the public submission acknowledgement.  It contains only the
// local public task id; APIMart's provider task id is returned separately to
// the task persistence layer and never written to this response.
type ImageJob struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Model     string `json:"model,omitempty"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// imageJobSubmission is the small, provider-neutral task payload persisted in
// model.Task.Data.  Polling obtains the actual image result through FetchTask;
// no raw provider response or provider task id is persisted here.
type imageJobSubmission struct {
	Object string `json:"object"`
	Model  string `json:"model,omitempty"`
	Status Status `json:"status"`
}

// TaskAdaptor implements channel.TaskAdaptor for APIMart's non-idempotent
// submit + idempotent GET protocol.  DoResponse only acknowledges the task;
// the existing persistent task polling loop performs the later GETs.
type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
	apiKey      string
	baseURL     string
	proxy       string
	operation   string
	Client      *Client
}

var _ channel.TaskAdaptor = (*TaskAdaptor)(nil)

func NewTaskAdaptor(client *Client) *TaskAdaptor {
	return &TaskAdaptor{Client: client}
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	if info == nil {
		return
	}
	a.ChannelType = info.ChannelType
	a.apiKey = info.ApiKey
	a.baseURL = info.ChannelBaseUrl
	a.proxy = info.ChannelSetting.Proxy
	a.operation = "generations"
	if strings.Contains(strings.ToLower(info.RequestURLPath), "/images/edits") {
		a.operation = "edits"
	}
	if a.Client == nil {
		a.Client = NewClient(Config{})
	}
}

// ValidateRequestAndSetAction accepts the JSON OpenAI Images payload used by
// the jobs route.  The body is cached once so RelayTaskSubmit can build the
// provider payload without reading a closed request body a second time.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if c == nil || c.Request == nil || info == nil {
		return localTaskError("invalid request context")
	}
	contentType := strings.ToLower(c.GetHeader("Content-Type"))
	if contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		return service.TaskErrorWrapperLocal(fmt.Errorf("APIMart image jobs require application/json"), "invalid_request", http.StatusBadRequest)
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	body, err := storage.Bytes()
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	bodyCopy := append([]byte(nil), body...)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(bodyCopy, &payload); err != nil || payload == nil {
		return service.TaskErrorWrapperLocal(fmt.Errorf("request body must be a JSON object"), "invalid_request", http.StatusBadRequest)
	}
	prompt := rawString(payload["prompt"])
	if strings.TrimSpace(prompt) == "" {
		return service.TaskErrorWrapperLocal(fmt.Errorf("field prompt is required"), "invalid_request", http.StatusBadRequest)
	}
	modelName := strings.TrimSpace(info.OriginModelName)
	if modelName == "" {
		modelName = strings.TrimSpace(rawString(payload["model"]))
	}
	if modelName == "" {
		modelName = ModelName
	}
	if info.OriginModelName == "" {
		info.OriginModelName = modelName
	}
	if info.TaskRelayInfo == nil {
		info.TaskRelayInfo = &relaycommon.TaskRelayInfo{}
	}
	c.Set(rawTaskRequestKey, bodyCopy)
	// Generic billing/diagnostic helpers can still read the normalized task
	// request, while the full payload remains private to this adaptor.
	var taskRequest relaycommon.TaskSubmitReq
	if err := json.Unmarshal(bodyCopy, &taskRequest); err == nil {
		c.Set("task_request", taskRequest)
	}
	info.Action = constant.TaskActionGenerate
	return nil
}

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	if strings.TrimSpace(a.baseURL) == "" {
		return "", fmt.Errorf("APIMart base URL is required")
	}
	endpoint, err := buildSubmitURL(a.baseURL, a.operation)
	if err != nil {
		return "", fmt.Errorf("build APIMart submit URL: %w", err)
	}
	return endpoint, nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	if req == nil {
		return fmt.Errorf("APIMart request is nil")
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	if c == nil {
		return nil, fmt.Errorf("APIMart request context is nil")
	}
	var body []byte
	if value, ok := c.Get(rawTaskRequestKey); ok {
		if cached, ok := value.([]byte); ok {
			body = append([]byte(nil), cached...)
		}
	}
	if len(body) == 0 {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return nil, fmt.Errorf("read APIMart image request: %w", err)
		}
		body, err = storage.Bytes()
		if err != nil {
			return nil, fmt.Errorf("read APIMart image request: %w", err)
		}
		body = append([]byte(nil), body...)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, fmt.Errorf("APIMart image request must be JSON object")
	}
	modelName := ""
	if info != nil {
		if info.ChannelMeta != nil {
			modelName = strings.TrimSpace(info.UpstreamModelName)
		}
		if modelName == "" {
			modelName = strings.TrimSpace(info.OriginModelName)
		}
	}
	if modelName == "" {
		modelName = ModelName
	}
	modelBytes, err := json.Marshal(modelName)
	if err != nil {
		return nil, err
	}
	payload["model"] = modelBytes
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal APIMart image request: %w", err)
	}
	return bytes.NewReader(encoded), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	if resp == nil || resp.Body == nil {
		return "", nil, service.TaskErrorWrapperLocal(fmt.Errorf("empty APIMart response"), "invalid_response", http.StatusBadGateway)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusBadGateway)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", nil, service.TaskErrorWrapperLocal(fmt.Errorf("APIMart task submission failed"), "upstream_submit_failed", resp.StatusCode)
	}
	submission, err := parseSubmissionResponse(body)
	if err != nil {
		return "", nil, taskErrorFromProvider(err, http.StatusBadGateway)
	}
	if !submission.Task.Valid() {
		return "", nil, service.TaskErrorWrapperLocal(fmt.Errorf("APIMart task id is missing"), "invalid_response", http.StatusBadGateway)
	}
	modelName := ModelName
	if info != nil {
		if strings.TrimSpace(info.OriginModelName) != "" {
			modelName = info.OriginModelName
		}
		if info.ChannelMeta != nil && strings.TrimSpace(info.UpstreamModelName) != "" {
			modelName = info.UpstreamModelName
		}
	}
	publicID := ""
	if info != nil && info.TaskRelayInfo != nil {
		publicID = info.PublicTaskID
	}
	if strings.TrimSpace(publicID) == "" {
		return "", nil, service.TaskErrorWrapperLocal(fmt.Errorf("public task id is missing"), "invalid_response", http.StatusInternalServerError)
	}
	job := ImageJob{
		ID:     publicID,
		Object: "image_generation.job",
		Model:  modelName,
		Status: string(submission.Status),
	}
	if info != nil {
		job.CreatedAt = info.StartTime.Unix()
	}
	if err := writeJSON(c, http.StatusOK, job); err != nil {
		return "", nil, service.TaskErrorWrapper(err, "write_response_failed", http.StatusInternalServerError)
	}
	taskData, err = json.Marshal(imageJobSubmission{
		Object: "image_generation.job",
		Model:  modelName,
		Status: submission.Status,
	})
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "marshal_task_data_failed", http.StatusInternalServerError)
	}
	return submission.Task.ID(), taskData, nil
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	providerID, ok := body["task_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid APIMart task reference")
	}
	task, err := NewTaskRef(providerID)
	if err != nil {
		return nil, fmt.Errorf("invalid APIMart task reference")
	}
	endpoint, err := buildTaskURL(baseURL, task)
	if err != nil {
		return nil, fmt.Errorf("build APIMart task URL: %w", err)
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("create APIMart proxy client: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

func (a *TaskAdaptor) GetModelList() []string { return append([]string(nil), ModelList...) }

func (a *TaskAdaptor) GetChannelName() string { return ChannelName }

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	result, err := parseTaskResponse(respBody)
	if err != nil {
		return nil, err
	}
	parsed := &relaycommon.TaskInfo{Code: 0}
	switch result.Status {
	case StatusSubmitted:
		parsed.Status = model.TaskStatusSubmitted
	case StatusQueued:
		parsed.Status = model.TaskStatusQueued
	case StatusProcessing:
		parsed.Status = model.TaskStatusInProgress
	case StatusCompleted:
		parsed.Status = model.TaskStatusSuccess
		parsed.Progress = "100%"
		if len(result.Images) > 0 {
			parsed.Url = result.Images[0].URL
			if parsed.Url == "" {
				parsed.Url = result.Images[0].B64JSON
			}
		}
	case StatusFailed:
		parsed.Status = model.TaskStatusFailure
		parsed.Reason = "APIMart task failed"
	case StatusCanceled:
		parsed.Status = model.TaskStatusFailure
		parsed.Reason = "APIMart task canceled"
	default:
		return nil, fmt.Errorf("unknown APIMart task status")
	}
	if result.Progress > 0 && result.Progress < 100 {
		parsed.Progress = fmt.Sprintf("%d%%", result.Progress)
	}
	if result.Error != nil {
		parsed.Reason = result.Error.Error()
	}
	return parsed, nil
}

// ParseTaskResult is the provider-neutral form used by the image fetch
// converter.  Unlike model.TaskInfo it preserves every normalized image item
// without exposing APIMart's task id.
func ParseTaskResult(respBody []byte) (TaskResult, error) {
	return parseTaskResponse(respBody)
}

// NormalizeImageResponse converts a completed normalized result to the normal
// OpenAI Images response.  It is useful to the shared task fetch converter.
func NormalizeImageResponse(result TaskResult, created int64) (*dto.ImageResponse, error) {
	if result.Status != StatusCompleted || len(result.Images) == 0 {
		return nil, fmt.Errorf("APIMart task has no completed image result")
	}
	response := &dto.ImageResponse{Created: created, Data: make([]dto.ImageData, 0, len(result.Images))}
	for _, image := range result.Images {
		if strings.TrimSpace(image.URL) == "" && strings.TrimSpace(image.B64JSON) == "" {
			continue
		}
		response.Data = append(response.Data, dto.ImageData{Url: image.URL, B64Json: image.B64JSON})
	}
	if len(response.Data) == 0 {
		return nil, fmt.Errorf("APIMart task returned no image data")
	}
	return response, nil
}

func writeJSON(c *gin.Context, status int, value any) error {
	if c == nil {
		return fmt.Errorf("response context is nil")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.Header("Content-Type", "application/json")
	c.Writer.WriteHeader(status)
	_, err = c.Writer.Write(body)
	return err
}

func localTaskError(message string) *dto.TaskError {
	return service.TaskErrorWrapperLocal(fmt.Errorf("%s", message), "invalid_request", http.StatusBadRequest)
}

func taskErrorFromProvider(err error, fallback int) *dto.TaskError {
	status := fallback
	if providerErr, ok := err.(*ProviderError); ok && providerErr.StatusCode >= 400 && providerErr.StatusCode <= 599 {
		status = providerErr.StatusCode
	}
	return service.TaskErrorWrapperLocal(err, "upstream_task_error", status)
}
