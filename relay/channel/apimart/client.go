// Package apimart contains the APIMart asynchronous image protocol.
//
// APIMart accepts an image request, returns a task id, and exposes the result
// through a later GET.  The provider task id is intentionally kept inside
// TaskRef instead of being JSON-marshallable or included in error strings.
// Callers which persist a task may explicitly extract it with ID(); public
// responses should use their own local job id.
package apimart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/service"
)

const (
	// DefaultMaxPollAttempts bounds the total number of provider GETs made by
	// Poll.  A submit is never retried because replaying it could create and
	// charge a second image task.
	DefaultMaxPollAttempts = 20
	DefaultInitialBackoff  = 2 * time.Second
	DefaultMaxBackoff      = 10 * time.Second
	DefaultRequestTimeout  = 30 * time.Second
	DefaultMaxResponseSize = 4 << 20
)

// Status is the normalized APIMart task state.  Unknown provider states are
// never treated as successful.
type Status string

const (
	StatusUnknown    Status = "unknown"
	StatusSubmitted  Status = "submitted"
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusCanceled   Status = "canceled"
)

func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

// ErrorKind is a provider-neutral error category.  It deliberately does not
// carry provider messages or ids, since APIMart sometimes includes task ids in
// those fields.
type ErrorKind string

const (
	ErrorKindInvalidRequest  ErrorKind = "invalid_request"
	ErrorKindSubmit          ErrorKind = "submit_failed"
	ErrorKindPoll            ErrorKind = "poll_failed"
	ErrorKindTaskFailed      ErrorKind = "task_failed"
	ErrorKindTaskCanceled    ErrorKind = "task_canceled"
	ErrorKindInvalidResponse ErrorKind = "invalid_response"
	ErrorKindTimeout         ErrorKind = "timeout"
)

// ProviderError is safe to pass to a public error mapper.  Message is a
// stable generic phrase; upstream response text is intentionally discarded.
type ProviderError struct {
	Kind       ErrorKind
	StatusCode int
	Retryable  bool
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "apimart error"
	}
	switch e.Kind {
	case ErrorKindTimeout:
		return "apimart task polling timed out"
	case ErrorKindTaskFailed:
		return "apimart task failed"
	case ErrorKindTaskCanceled:
		return "apimart task canceled"
	case ErrorKindInvalidRequest:
		return "invalid apimart request"
	case ErrorKindInvalidResponse:
		return "invalid apimart response"
	case ErrorKindSubmit:
		return "apimart task submission failed"
	case ErrorKindPoll:
		return "apimart task polling failed"
	default:
		return "apimart upstream error"
	}
}

// TaskRef is an opaque provider task reference.  It has no exported fields,
// so encoding/json and fmt's default formatting cannot accidentally put the
// provider id in a customer response or an ordinary structured log.
type TaskRef struct {
	id string
}

// NewTaskRef reconstructs a reference from a value persisted by the server.
// It is intentionally explicit at the persistence boundary.
func NewTaskRef(id string) (TaskRef, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 512 || strings.ContainsAny(id, "\r\n") {
		return TaskRef{}, &ProviderError{Kind: ErrorKindInvalidRequest}
	}
	return TaskRef{id: id}, nil
}

func (r TaskRef) Valid() bool { return strings.TrimSpace(r.id) != "" }

// ID is the only deliberate provider-id extraction point.  Persist it in a
// private task column and never use it as the public task/job id.
func (r TaskRef) ID() string { return r.id }

// Image is the normalized result item.  Exactly one of URL or B64JSON is
// normally populated, but both are retained if the provider returns both.
type Image struct {
	URL     string
	B64JSON string
}

type TaskResult struct {
	Status   Status
	Progress int
	Images   []Image
	Error    *ProviderError
}

type Submission struct {
	Task   TaskRef
	Status Status
}

// SubmitRequest contains everything needed for one provider submission.
// Body is the already converted APIMart/OpenAI-compatible JSON payload.
type SubmitRequest struct {
	BaseURL   string
	APIKey    string
	Operation string // generations or edits; defaults to generations
	Body      []byte
	Proxy     string
}

// PollRequest identifies one already accepted provider task.  It is separate
// from SubmitRequest to make accidental POST replay difficult for callers.
type PollRequest struct {
	BaseURL string
	APIKey  string
	Task    TaskRef
	Proxy   string
}

// Config bounds all provider work.  A zero Config receives safe defaults.
type Config struct {
	MaxPollAttempts int
	InitialBackoff  time.Duration
	MaxBackoff      time.Duration
	RequestTimeout  time.Duration
	MaxResponseSize int64
}

func (c Config) withDefaults() Config {
	if c.MaxPollAttempts <= 0 {
		c.MaxPollAttempts = DefaultMaxPollAttempts
	}
	if c.InitialBackoff < 0 {
		c.InitialBackoff = 0
	}
	if c.InitialBackoff == 0 {
		c.InitialBackoff = DefaultInitialBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = DefaultMaxBackoff
	}
	if c.MaxBackoff < c.InitialBackoff {
		c.MaxBackoff = c.InitialBackoff
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = DefaultRequestTimeout
	}
	if c.MaxResponseSize <= 0 {
		c.MaxResponseSize = DefaultMaxResponseSize
	}
	return c
}

// HTTPDoer is intentionally small so provider behavior can be tested without
// a real upstream or credentials.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	HTTPClient HTTPDoer
	Config     Config
}

func NewClient(config Config) *Client {
	return &Client{Config: config.withDefaults()}
}

func (c *Client) config() Config {
	if c == nil {
		return (Config{}).withDefaults()
	}
	return c.Config.withDefaults()
}

func (c *Client) doer(proxy string) (HTTPDoer, error) {
	if c != nil && c.HTTPClient != nil && strings.TrimSpace(proxy) == "" {
		return c.HTTPClient, nil
	}
	if strings.TrimSpace(proxy) != "" {
		client, err := service.GetHttpClientWithProxy(proxy)
		if err != nil {
			return nil, fmt.Errorf("create apimart proxy client: %w", err)
		}
		if client != nil {
			return client, nil
		}
	}
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient, nil
	}
	if client := service.GetHttpClient(); client != nil {
		return client, nil
	}
	return http.DefaultClient, nil
}

// Submit performs exactly one POST.  It does not retry transport errors or
// non-2xx responses, because the provider may have accepted a request even if
// the client did not receive the response.
func (c *Client) Submit(ctx context.Context, request SubmitRequest) (Submission, error) {
	operation := normalizeOperation(request.Operation)
	if operation == "" {
		return Submission{}, &ProviderError{Kind: ErrorKindInvalidRequest}
	}
	if strings.TrimSpace(request.BaseURL) == "" || strings.TrimSpace(request.APIKey) == "" {
		return Submission{}, &ProviderError{Kind: ErrorKindInvalidRequest}
	}
	endpoint, err := buildSubmitURL(request.BaseURL, operation)
	if err != nil {
		return Submission{}, &ProviderError{Kind: ErrorKindInvalidRequest}
	}
	requestCtx, cancel := withTimeout(ctx, c.config().RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(string(request.Body)))
	if err != nil {
		return Submission{}, &ProviderError{Kind: ErrorKindInvalidRequest}
	}
	req.Header.Set("Authorization", "Bearer "+request.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	doer, err := c.doer(request.Proxy)
	if err != nil {
		return Submission{}, &ProviderError{Kind: ErrorKindSubmit}
	}
	resp, err := doer.Do(req)
	if err != nil {
		return Submission{}, &ProviderError{Kind: ErrorKindSubmit, Retryable: false}
	}
	if resp == nil {
		return Submission{}, &ProviderError{Kind: ErrorKindSubmit, Retryable: false}
	}
	body, readErr := readBody(resp.Body, c.config().MaxResponseSize)
	_ = resp.Body.Close()
	if readErr != nil {
		return Submission{}, &ProviderError{Kind: ErrorKindSubmit}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Submission{}, &ProviderError{Kind: ErrorKindSubmit, StatusCode: resp.StatusCode}
	}
	submission, err := parseSubmissionResponse(body)
	if err != nil {
		return Submission{}, err
	}
	return submission, nil
}

// Fetch performs one safe GET for an already accepted task.  It never creates
// a task and is therefore safe for bounded retries by Poll.
func (c *Client) Fetch(ctx context.Context, request PollRequest) (TaskResult, error) {
	if strings.TrimSpace(request.BaseURL) == "" || strings.TrimSpace(request.APIKey) == "" || !request.Task.Valid() {
		return TaskResult{}, &ProviderError{Kind: ErrorKindInvalidRequest}
	}
	endpoint, err := buildTaskURL(request.BaseURL, request.Task)
	if err != nil {
		return TaskResult{}, &ProviderError{Kind: ErrorKindInvalidRequest}
	}
	requestCtx, cancel := withTimeout(ctx, c.config().RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TaskResult{}, &ProviderError{Kind: ErrorKindInvalidRequest}
	}
	req.Header.Set("Authorization", "Bearer "+request.APIKey)
	req.Header.Set("Accept", "application/json")
	doer, err := c.doer(request.Proxy)
	if err != nil {
		return TaskResult{}, &ProviderError{Kind: ErrorKindPoll}
	}
	resp, err := doer.Do(req)
	if err != nil {
		return TaskResult{}, &ProviderError{Kind: ErrorKindPoll, Retryable: true}
	}
	if resp == nil {
		return TaskResult{}, &ProviderError{Kind: ErrorKindPoll, Retryable: true}
	}
	body, readErr := readBody(resp.Body, c.config().MaxResponseSize)
	_ = resp.Body.Close()
	if readErr != nil {
		return TaskResult{}, &ProviderError{Kind: ErrorKindPoll, Retryable: true}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return TaskResult{}, &ProviderError{
			Kind:       ErrorKindPoll,
			StatusCode: resp.StatusCode,
			Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
		}
	}
	result, err := parseTaskResponse(body)
	if err != nil {
		return TaskResult{}, err
	}
	return result, nil
}

// Poll repeatedly GETs a task until a terminal normalized state is observed.
// Only GET failures and pending states are retried; POST is never replayed.
func (c *Client) Poll(ctx context.Context, request PollRequest) (TaskResult, error) {
	config := c.config()
	var last TaskResult
	var lastErr error
	for attempt := 0; attempt < config.MaxPollAttempts; attempt++ {
		result, err := c.Fetch(ctx, request)
		if err == nil {
			last = result
			if result.Status.IsTerminal() {
				return result, nil
			}
			if result.Status == StatusUnknown {
				lastErr = &ProviderError{Kind: ErrorKindInvalidResponse}
			} else {
				lastErr = nil
			}
		} else {
			lastErr = err
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || !providerErr.Retryable {
				return last, err
			}
		}

		if attempt == config.MaxPollAttempts-1 {
			if lastErr != nil {
				return last, lastErr
			}
			return last, &ProviderError{Kind: ErrorKindTimeout}
		}
		if err := waitBackoff(ctx, backoffDuration(config, attempt)); err != nil {
			return last, err
		}
	}
	return last, &ProviderError{Kind: ErrorKindTimeout}
}

func normalizeOperation(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "", "generations":
		return "generations"
	case "edits":
		return "edits"
	default:
		return ""
	}
}

func buildSubmitURL(baseURL, operation string) (string, error) {
	operation = normalizeOperation(operation)
	if operation == "" {
		return "", errors.New("unsupported operation")
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid base url")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/images/" + operation
	return base.String(), nil
}

func buildTaskURL(baseURL string, task TaskRef) (string, error) {
	if !task.Valid() {
		return "", errors.New("task reference is empty")
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid base url")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/tasks/" + url.PathEscape(task.id)
	query := base.Query()
	query.Set("language", "en")
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func waitBackoff(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func backoffDuration(config Config, attempt int) time.Duration {
	if attempt < 0 || config.InitialBackoff <= 0 {
		return 0
	}
	duration := config.InitialBackoff
	for i := 0; i < attempt && duration < config.MaxBackoff; i++ {
		if duration > config.MaxBackoff/2 {
			duration = config.MaxBackoff
			break
		}
		duration *= 2
	}
	if duration > config.MaxBackoff {
		return config.MaxBackoff
	}
	return duration
}

func readBody(body io.ReadCloser, maxBytes int64) ([]byte, error) {
	if body == nil {
		return nil, errors.New("empty response body")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseSize
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("response body too large")
	}
	return data, nil
}

type rawProviderEnvelope struct {
	Code  json.RawMessage `json:"code"`
	Data  json.RawMessage `json:"data"`
	Error json.RawMessage `json:"error"`
}

type rawSubmitItem struct {
	Status string `json:"status"`
	TaskID string `json:"task_id"`
	ID     string `json:"id"`
}

func parseSubmissionResponse(body []byte) (Submission, error) {
	var envelope rawProviderEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Submission{}, &ProviderError{Kind: ErrorKindInvalidResponse}
	}
	if hasJSONValue(envelope.Error) {
		return Submission{}, &ProviderError{Kind: ErrorKindSubmit}
	}
	if !isProviderSuccessCode(envelope.Code) {
		return Submission{}, &ProviderError{Kind: ErrorKindSubmit}
	}
	items, err := decodeSubmitItems(envelope.Data)
	if err != nil || len(items) != 1 {
		return Submission{}, &ProviderError{Kind: ErrorKindInvalidResponse}
	}
	providerID := strings.TrimSpace(items[0].TaskID)
	if providerID == "" {
		providerID = strings.TrimSpace(items[0].ID)
	}
	ref, err := NewTaskRef(providerID)
	if err != nil {
		return Submission{}, &ProviderError{Kind: ErrorKindInvalidResponse}
	}
	status := normalizeStatus(items[0].Status)
	if status == StatusFailed {
		return Submission{}, &ProviderError{Kind: ErrorKindTaskFailed}
	}
	if status == StatusCanceled {
		return Submission{}, &ProviderError{Kind: ErrorKindTaskCanceled}
	}
	if status == StatusUnknown {
		status = StatusSubmitted
	}
	return Submission{Task: ref, Status: status}, nil
}

func decodeSubmitItems(raw json.RawMessage) ([]rawSubmitItem, error) {
	if !hasJSONValue(raw) {
		return nil, errors.New("missing submit data")
	}
	var items []rawSubmitItem
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	var item rawSubmitItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	return []rawSubmitItem{item}, nil
}

func parseTaskResponse(body []byte) (TaskResult, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return TaskResult{}, &ProviderError{Kind: ErrorKindInvalidResponse}
	}
	if rawError, ok := root["error"]; ok && hasJSONValue(rawError) {
		return TaskResult{}, &ProviderError{Kind: ErrorKindPoll}
	}
	data := rawObject(root["data"])
	if len(data) == 0 {
		data = root
	}
	status := normalizeStatus(rawString(data["status"]))
	if status == StatusUnknown {
		status = normalizeStatus(rawString(root["status"]))
	}
	result := rawObject(data["result"])
	if len(result) == 0 {
		result = rawObject(data["output"])
	}
	imagesRaw := result["images"]
	if !hasJSONValue(imagesRaw) {
		imagesRaw = data["images"]
	}
	images := decodeImages(imagesRaw)
	progress := parseProgress(data["progress"])
	if status == StatusUnknown && len(images) > 0 {
		status = StatusCompleted
	}
	if status == StatusUnknown {
		return TaskResult{}, &ProviderError{Kind: ErrorKindInvalidResponse}
	}
	if status == StatusFailed {
		return TaskResult{Status: status, Progress: progress, Error: &ProviderError{Kind: ErrorKindTaskFailed}}, nil
	}
	if status == StatusCanceled {
		return TaskResult{Status: status, Progress: progress, Error: &ProviderError{Kind: ErrorKindTaskCanceled}}, nil
	}
	if status == StatusCompleted && len(images) == 0 {
		return TaskResult{}, &ProviderError{Kind: ErrorKindInvalidResponse}
	}
	return TaskResult{Status: status, Progress: progress, Images: images}, nil
}

func normalizeStatus(status string) Status {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "submitted", "accepted":
		return StatusSubmitted
	case "queued", "pending", "waiting":
		return StatusQueued
	case "processing", "in_progress", "in-progress", "running":
		return StatusProcessing
	case "completed", "complete", "success", "succeeded":
		return StatusCompleted
	case "failed", "failure", "error", "expired":
		return StatusFailed
	case "canceled", "cancelled", "cancellation":
		return StatusCanceled
	default:
		return StatusUnknown
	}
}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	if !hasJSONValue(raw) || len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func rawString(raw json.RawMessage) string {
	if !hasJSONValue(raw) {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return ""
}

func parseProgress(raw json.RawMessage) int {
	if !hasJSONValue(raw) {
		return 0
	}
	var integer int
	if json.Unmarshal(raw, &integer) == nil {
		if integer < 0 {
			return 0
		}
		if integer > 100 {
			return 100
		}
		return integer
	}
	value := strings.TrimSuffix(strings.TrimSpace(rawString(raw)), "%")
	integer, _ = strconv.Atoi(value)
	if integer < 0 {
		return 0
	}
	if integer > 100 {
		return 100
	}
	return integer
}

func decodeImages(raw json.RawMessage) []Image {
	if !hasJSONValue(raw) || len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	images := make([]Image, 0, len(values))
	for _, value := range values {
		if rawURL := strings.TrimSpace(rawString(value)); rawURL != "" {
			images = append(images, Image{URL: rawURL})
			continue
		}
		object := rawObject(value)
		if len(object) == 0 {
			continue
		}
		urls := decodeStringOrArray(object["url"])
		if len(urls) == 0 {
			urls = decodeStringOrArray(object["image_url"])
		}
		if len(urls) == 0 {
			if b64 := strings.TrimSpace(rawString(object["b64_json"])); b64 != "" {
				images = append(images, Image{B64JSON: b64})
			}
			continue
		}
		for _, imageURL := range urls {
			if imageURL != "" {
				images = append(images, Image{URL: imageURL})
			}
		}
	}
	return images
}

func decodeStringOrArray(raw json.RawMessage) []string {
	if value := strings.TrimSpace(rawString(raw)); value != "" {
		return []string{value}
	}
	var values []string
	if !hasJSONValue(raw) || json.Unmarshal(raw, &values) != nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func hasJSONValue(raw json.RawMessage) bool {
	return len(strings.TrimSpace(string(raw))) > 0 && string(raw) != "null"
}

func isProviderSuccessCode(raw json.RawMessage) bool {
	if !hasJSONValue(raw) {
		return true
	}
	if rawString(raw) == "success" {
		return true
	}
	value := strings.TrimSpace(string(raw))
	if value == "0" || value == "200" {
		return true
	}
	if number, err := strconv.Atoi(value); err == nil {
		return number >= 200 && number < 300
	}
	return false
}
