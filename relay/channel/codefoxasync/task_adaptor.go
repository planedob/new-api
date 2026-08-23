package codefoxasync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
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
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

// TaskAdaptor bridges the native CodeFox batch contract to Aibuff's durable
// task submit/poll lifecycle. It deliberately does not submit work during
// polling: FetchTask is a read-only GET and ParseTaskResult only normalizes
// the provider response.
type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
	apiKey      string
	baseURL     string
}

var _ channel.TaskAdaptor = (*TaskAdaptor)(nil)

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	if info == nil {
		return
	}
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
	a.apiKey = info.ApiKey
}

// ValidateRequestAndSetAction accepts the native prompts array. A legacy
// single prompt is accepted as a one-item batch so the shared task endpoint
// remains backwards compatible while new batch routes can pass all options.
func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	request, err := requestFromContextOrBody(c)
	if err != nil {
		return newTaskError(err, "invalid_codefox_batch_request", http.StatusBadRequest)
	}
	normalized, err := request.Normalize()
	if err != nil {
		return newTaskError(err, "invalid_codefox_batch_request", http.StatusBadRequest)
	}
	// Passing a customer callback directly upstream would expose the provider
	// task id and produce a signature made with Aibuff's private upstream key.
	// Polling is the supported public completion mechanism until Aibuff owns a
	// verified webhook relay endpoint.
	if normalized.CallbackURL != "" {
		return newTaskError(fmt.Errorf("callback_url is not supported; poll the batch task instead"), "invalid_codefox_batch_request", http.StatusBadRequest)
	}
	if normalized.IdempotencyKey != "" && info != nil {
		normalized.IdempotencyKey = namespaceIdempotencyKey(info.UserId, normalized.IdempotencyKey)
	}
	c.Set(batchRequestContextKey, normalized)
	// The generic task plumbing expects a TaskSubmitReq to exist. Keep its
	// first prompt as a compatibility projection; BuildRequestBody uses the
	// lossless native request stored above.
	c.Set("task_request", relaycommon.TaskSubmitReq{
		Prompt: normalized.Prompts[0],
		Model:  normalized.Model,
		Size:   normalized.Size,
		Metadata: map[string]interface{}{
			"batch_count": len(normalized.Prompts),
		},
	})
	if info != nil {
		if info.TaskRelayInfo == nil {
			info.TaskRelayInfo = &relaycommon.TaskRelayInfo{}
		}
		info.Action = constant.TaskActionGenerate
	}
	return nil
}

func namespaceIdempotencyKey(userID int, customerKey string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", userID, customerKey)))
	return fmt.Sprintf("aibuff-%x", digest[:16])
}

// EstimateBilling returns the number of requested items as the n ratio. The
// shared per-call price helper multiplies the base item price by this ratio;
// final polling adjustment uses the successful item count.
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, _ *relaycommon.RelayInfo) map[string]float64 {
	request, err := requestFromContextOrBody(c)
	if err != nil || len(request.Prompts) == 0 {
		return nil
	}
	return map[string]float64{"n": float64(len(request.Prompts))}
}

// AdjustBillingOnSubmit has no adjustment: the requested item count is known
// before submission and is already part of the pre-consumption ratio.
func (a *TaskAdaptor) AdjustBillingOnSubmit(_ *relaycommon.RelayInfo, _ []byte) map[string]float64 {
	return nil
}

// AdjustBillingOnComplete returns the actual quota for a partial-success
// task. The shared polling loop invokes this before its token fallback. A
// completely failed task returns zero so the shared failure path refunds the
// entire pre-consume exactly once.
func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil || taskResult == nil || task.Quota <= 0 {
		return 0
	}
	succeeded := taskResult.TotalTokens
	requested := taskResult.CompletionTokens
	if succeeded <= 0 || requested <= 0 {
		return 0
	}
	if succeeded >= requested {
		return task.Quota
	}
	actual := int(float64(task.Quota) * float64(succeeded) / float64(requested))
	if actual <= 0 {
		actual = 1
	}
	return actual
}

func (a *TaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	client := &Client{BaseURL: a.baseURL}
	return client.endpoint("/api/ecommerce/batch-generate")
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	if strings.TrimSpace(a.apiKey) == "" {
		return ErrMissingAPIKey
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	if request, err := requestFromContextOrBody(c); err == nil && request.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", request.IdempotencyKey)
	}
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, _ *relaycommon.RelayInfo) (io.Reader, error) {
	request, err := requestFromContextOrBody(c)
	if err != nil {
		return nil, err
	}
	normalized, err := request.Normalize()
	if err != nil {
		return nil, err
	}
	body, err := common.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal CodeFox batch request: %w", err)
	}
	return bytes.NewReader(body), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	resp, err := channel.DoTaskApiRequest(a, c, info, requestBody)
	if err != nil || resp == nil || resp.Body == nil || resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return resp, err
	}
	// RelayTaskSubmit reads non-2xx bodies itself before calling DoResponse.
	// Sanitize that body here so an upstream diagnostic cannot leak a key into
	// the generic retry/error log path.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxJSONBodyBytes+1))
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if len(body) > maxJSONBodyBytes {
		return nil, fmt.Errorf("CodeFox submit response is too large")
	}
	safeBody := sanitizeProviderBody(body, a.apiKey)
	resp.Body = io.NopCloser(bytes.NewReader(safeBody))
	resp.ContentLength = int64(len(safeBody))
	return resp, nil
}

// DoResponse parses the immediate acceptance response and emits a public
// Aibuff task id. The upstream task id is returned to model.Task.PrivateData.
func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *dto.TaskError) {
	if resp == nil || resp.Body == nil {
		return "", nil, newTaskError(fmt.Errorf("empty CodeFox submit response"), "invalid_response", http.StatusBadGateway)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBodyBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return "", nil, newTaskError(err, "read_response_body_failed", http.StatusBadGateway)
	}
	if len(body) > maxJSONBodyBytes {
		return "", nil, newTaskError(fmt.Errorf("CodeFox submit response is too large"), "invalid_response", http.StatusBadGateway)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", nil, newTaskError(responseError(resp.StatusCode, body, a.apiKey), "upstream_submit_failed", resp.StatusCode)
	}
	var envelope submitEnvelope
	if err := common.Unmarshal(body, &envelope); err != nil {
		return "", nil, newTaskError(fmt.Errorf("invalid CodeFox submit response"), "invalid_response", http.StatusBadGateway)
	}
	if !envelope.Success {
		return "", nil, newTaskError(sanitizedAPIError(resp.StatusCode, envelope.Message, "batch_submit_failed", a.apiKey), "upstream_submit_failed", http.StatusBadGateway)
	}
	status, err := NormalizeStatus(envelope.Data.Status)
	if err != nil {
		return "", nil, newTaskError(err, "invalid_response", http.StatusBadGateway)
	}
	upstreamTaskID := strings.TrimSpace(envelope.Data.TaskID)
	if upstreamTaskID == "" {
		return "", nil, newTaskError(fmt.Errorf("CodeFox submit response has no task_id"), "invalid_response", http.StatusBadGateway)
	}
	publicTaskID := ""
	if info != nil && info.TaskRelayInfo != nil {
		publicTaskID = info.PublicTaskID
	}
	if publicTaskID == "" {
		publicTaskID = model.GenerateTaskID()
	}
	if c != nil {
		c.JSON(http.StatusAccepted, PublicBatchSubmission{
			ID:         publicTaskID,
			TaskID:     publicTaskID,
			Status:     status,
			TotalCount: envelope.Data.TotalCount,
			CreatedAt:  chooseCreatedAt(envelope.Data.CreatedAt),
			Object:     "image.batch",
		})
	}
	return upstreamTaskID, sanitizeProviderBody(body, a.apiKey), nil
}

// PublicBatchSubmission is the safe submit response. It contains only the
// Aibuff task id and counters; the provider task id never leaves this package.
type PublicBatchSubmission struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Object     string `json:"object"`
	Status     Status `json:"status"`
	TotalCount int    `json:"total_count"`
	CreatedAt  int64  `json:"created_at"`
}

func chooseCreatedAt(value int64) int64 {
	if value > 0 {
		return value
	}
	return time.Now().Unix()
}

func (a *TaskAdaptor) GetModelList() []string { return []string{DefaultModel} }

func (a *TaskAdaptor) GetChannelName() string { return "codefoxasync" }

type PublicBatchResult struct {
	ItemIndex int    `json:"item_index"`
	Prompt    string `json:"prompt,omitempty"`
	ImageURL  string `json:"image_url"`
	Status    string `json:"status"`
}

type PublicBatchError struct {
	ItemIndex int    `json:"item_index"`
	Prompt    string `json:"prompt,omitempty"`
	ErrorCode string `json:"error_code"`
	Message   string `json:"error_message"`
}

type PublicBatchTask struct {
	ID          string              `json:"id"`
	TaskID      string              `json:"task_id"`
	Object      string              `json:"object"`
	ProductID   string              `json:"product_id,omitempty"`
	Status      Status              `json:"status"`
	Progress    Progress            `json:"progress"`
	Results     []PublicBatchResult `json:"results,omitempty"`
	Errors      []PublicBatchError  `json:"errors,omitempty"`
	CreatedAt   int64               `json:"created_at"`
	CompletedAt int64               `json:"completed_at,omitempty"`
}

// ConvertToOpenAIImageTask renders only the persisted snapshot. It never
// polls CodeFox and replaces every provider image URL with an authenticated
// Aibuff content-proxy URL tied to the public task id.
func (a *TaskAdaptor) ConvertToOpenAIImageTask(task *model.Task) ([]byte, error) {
	if task == nil {
		return nil, fmt.Errorf("CodeFox task is nil")
	}
	public := PublicBatchTask{
		ID:        task.TaskID,
		TaskID:    task.TaskID,
		Object:    "image.batch",
		Status:    publicBatchStatus(task.Status),
		CreatedAt: task.CreatedAt,
	}
	if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
		providerTask, err := ParseBatchTaskResponse(task.Data)
		if err != nil {
			return nil, err
		}
		public.ProductID = providerTask.ProductID
		public.Status = providerTask.Status
		public.Progress = providerTask.Progress
		public.CompletedAt = task.FinishTime
		if providerTask.CompletedAt > 0 {
			public.CompletedAt = providerTask.CompletedAt
		}
		for _, item := range providerTask.Results {
			public.Results = append(public.Results, PublicBatchResult{
				ItemIndex: item.ItemIndex,
				Prompt:    item.Prompt,
				ImageURL:  batchImageProxyURL(task.TaskID, item.ItemIndex),
				Status:    "SUCCESS",
			})
		}
		for _, item := range providerTask.Errors {
			code := strings.TrimSpace(item.ErrorCode)
			if code == "" {
				code = "IMAGE_GENERATION_FAILED"
			}
			public.Errors = append(public.Errors, PublicBatchError{
				ItemIndex: item.ItemIndex,
				Prompt:    item.Prompt,
				ErrorCode: code,
				Message:   "image generation failed",
			})
		}
	}
	return common.Marshal(public)
}

func publicBatchStatus(status model.TaskStatus) Status {
	switch status {
	case model.TaskStatusInProgress:
		return StatusProcessing
	case model.TaskStatusSuccess:
		return StatusCompleted
	case model.TaskStatusFailure:
		return StatusFailed
	default:
		return StatusPending
	}
}

func batchImageProxyURL(publicTaskID string, itemIndex int) string {
	return fmt.Sprintf("%s/v1/images/batches/%s/items/%d/content",
		strings.TrimRight(system_setting.ServerAddress, "/"), publicTaskID, itemIndex)
}

// ParseBatchTaskResponse converts one persisted provider poll response into a
// normalized task. The provider id remains internal and callers must replace
// it before building public responses.
func ParseBatchTaskResponse(respBody []byte) (BatchTask, error) {
	var envelope taskEnvelope
	if err := common.Unmarshal(respBody, &envelope); err != nil || !envelope.Success {
		return BatchTask{}, fmt.Errorf("invalid CodeFox task response")
	}
	if strings.TrimSpace(envelope.Data.TaskID) == "" {
		envelope.Data.TaskID = "redacted"
	}
	return normalizeTask(envelope.Data, "")
}

// FetchTask is a read-only provider GET. It uses the key snapshot supplied by
// the persisted task so key rotation cannot redirect a historical task.
func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid CodeFox task_id")
	}
	client := &Client{BaseURL: baseURL, APIKey: key}
	if err := validateTaskID(taskID); err != nil {
		return nil, err
	}
	endpoint, err := client.endpointWithTask("/api/ecommerce/tasks", taskID, "")
	if err != nil {
		return nil, err
	}
	req, err := client.newRequest(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	httpClient, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new CodeFox proxy http client failed: %w", err)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	// The shared polling loop records the response body in debug logs before
	// calling ParseTaskResult. Sanitize the body at this boundary so an
	// upstream diagnostic that echoes the key cannot enter those logs.
	if resp != nil && resp.Body != nil {
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxJSONBodyBytes+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(bodyBytes) > maxJSONBodyBytes {
			return nil, fmt.Errorf("CodeFox task response is too large")
		}
		safeBody := sanitizeProviderBody(bodyBytes, key)
		resp.Body = io.NopCloser(bytes.NewReader(safeBody))
		resp.ContentLength = int64(len(safeBody))
	}
	return resp, nil
}

func sanitizeProviderBody(body []byte, apiKey string) []byte {
	if len(body) == 0 || apiKey == "" {
		return body
	}
	return []byte(sanitizeText(string(body), apiKey))
}

// ParseTaskResult converts a native task snapshot to the generic polling
// status. PARTIAL_SUCCESS is represented as generic SUCCESS so the shared
// terminal/CAS/settlement path runs, while the raw normalized result remains
// in model.Task.Data for the batch response/converter.
func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	var envelope taskEnvelope
	if err := common.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal CodeFox task response failed")
	}
	if !envelope.Success {
		return relaycommon.FailTaskInfo(sanitizeText(envelope.Message, a.apiKey)), nil
	}
	task, err := normalizeTask(envelope.Data, a.apiKey)
	if err != nil {
		return nil, err
	}
	result := &relaycommon.TaskInfo{
		TaskID:           task.TaskID,
		TotalTokens:      task.SucceededCount(),
		CompletionTokens: task.Progress.Total,
	}
	switch task.Status {
	case StatusPending:
		result.Status = string(model.TaskStatusQueued)
		result.Progress = progressString(task.Progress)
	case StatusProcessing:
		result.Status = string(model.TaskStatusInProgress)
		result.Progress = progressString(task.Progress)
	case StatusCompleted:
		result.Status = string(model.TaskStatusSuccess)
		result.Progress = "100%"
	case StatusPartialSuccess:
		result.Status = string(model.TaskStatusSuccess)
		result.Progress = "100%"
		result.Reason = "PARTIAL_SUCCESS"
	case StatusFailed:
		result.Status = string(model.TaskStatusFailure)
		result.Progress = "100%"
		result.Reason = taskFailureReason(task)
	default:
		return nil, fmt.Errorf("unsupported CodeFox task status %q", task.Status)
	}
	return result, nil
}

func progressString(progress Progress) string {
	if progress.Total <= 0 {
		return ""
	}
	done := progress.Success + progress.Failed
	if done < 0 {
		done = 0
	}
	if done > progress.Total {
		done = progress.Total
	}
	return fmt.Sprintf("%d%%", done*100/progress.Total)
}

func taskFailureReason(task BatchTask) string {
	for _, item := range task.Errors {
		if strings.TrimSpace(item.ErrorMessage) != "" {
			return item.ErrorMessage
		}
		if strings.TrimSpace(item.ErrorCode) != "" {
			return item.ErrorCode
		}
	}
	return "CodeFox batch task failed"
}

// ImageProxyReference is the minimal data needed by a public image proxy.
// Provider URLs remain optional because the proxy can fetch by task/item
// through Client.FetchImage without exposing the upstream host.
type ImageProxyReference struct {
	TaskID    string `json:"task_id"`
	ItemIndex int    `json:"item_index"`
	ImageURL  string `json:"image_url,omitempty"`
}

func (t BatchTask) ImageProxyReferences() []ImageProxyReference {
	references := make([]ImageProxyReference, 0, len(t.Results))
	for _, item := range t.Results {
		references = append(references, ImageProxyReference{
			TaskID:    t.TaskID,
			ItemIndex: item.ItemIndex,
			ImageURL:  item.ImageURL,
		})
	}
	return references
}

const batchRequestContextKey = "codefoxasync_batch_request"

func requestFromContextOrBody(c *gin.Context) (BatchRequest, error) {
	if c == nil {
		return BatchRequest{}, fmt.Errorf("request context is nil")
	}
	if value, exists := c.Get(batchRequestContextKey); exists {
		if request, ok := value.(BatchRequest); ok {
			return request, nil
		}
	}
	var input struct {
		BatchRequest
		Prompt string `json:"prompt,omitempty"`
	}
	if err := common.UnmarshalBodyReusable(c, &input); err != nil {
		return BatchRequest{}, err
	}
	if len(input.Prompts) == 0 && strings.TrimSpace(input.Prompt) != "" {
		input.Prompts = []string{input.Prompt}
	}
	return input.BatchRequest, nil
}

func newTaskError(err error, code string, statusCode int) *dto.TaskError {
	return service.TaskErrorWrapperLocal(err, code, statusCode)
}
