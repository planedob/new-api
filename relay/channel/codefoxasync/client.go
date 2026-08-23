// Package codefoxasync implements CodeFox's native e-commerce batch image
// protocol.  The provider accepts a batch, returns a task id immediately and
// exposes a separate task endpoint for progress/results.  This package is a
// transport and protocol boundary only: persistence, channel selection,
// customer billing and the public Aibuff job API stay in their respective
// layers.
package codefoxasync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	DefaultModel   = "gpt-image-2"
	DefaultSize    = "1024x1024"
	DefaultQuality = "standard"

	maxPrompts       = 100
	maxPromptRunes   = 1000
	maxIDKeyRunes    = 256
	maxJSONBodyBytes = 4 << 20

	defaultPollAttempts = 60
	defaultPollInterval = 5 * time.Second
)

const (
	StatusPending        Status = "PENDING"
	StatusProcessing     Status = "PROCESSING"
	StatusCompleted      Status = "COMPLETED"
	StatusPartialSuccess Status = "PARTIAL_SUCCESS"
	StatusFailed         Status = "FAILED"
)

// Status is the normalized CodeFox task state.  The upstream contract is
// case-insensitive in practice, but Aibuff persists these canonical values so
// jobs and billing can make deterministic decisions.
type Status string

// Terminal reports whether the provider has finished every item in a task.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusPartialSuccess, StatusFailed:
		return true
	default:
		return false
	}
}

// Valid reports whether s belongs to the documented task state machine.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusProcessing, StatusCompleted, StatusPartialSuccess, StatusFailed:
		return true
	default:
		return false
	}
}

// NormalizeStatus converts provider spelling/casing into the canonical state.
func NormalizeStatus(value string) (Status, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(StatusPending):
		return StatusPending, nil
	case string(StatusProcessing):
		return StatusProcessing, nil
	case string(StatusCompleted):
		return StatusCompleted, nil
	case string(StatusPartialSuccess):
		return StatusPartialSuccess, nil
	case string(StatusFailed):
		return StatusFailed, nil
	default:
		return "", fmt.Errorf("unsupported CodeFox task status %q", strings.TrimSpace(value))
	}
}

// BatchRequest is the native CodeFox e-commerce batch request.  Pointer seed
// preserves an explicitly supplied zero, which is meaningful to callers.
type BatchRequest struct {
	IdempotencyKey    string   `json:"idempotency_key,omitempty"`
	ProductID         string   `json:"product_id,omitempty"`
	Prompts           []string `json:"prompts"`
	Model             string   `json:"model,omitempty"`
	Size              string   `json:"size,omitempty"`
	Quality           string   `json:"quality,omitempty"`
	Seed              *uint32  `json:"seed,omitempty"`
	ReferenceImageURL string   `json:"reference_image_url,omitempty"`
	CallbackURL       string   `json:"callback_url,omitempty"`
}

// Normalize applies the documented defaults without changing caller-owned
// slices.  It is exported so the durable jobs layer can persist exactly the
// normalized request that was submitted upstream.
func (r BatchRequest) Normalize() (BatchRequest, error) {
	if err := r.Validate(); err != nil {
		return BatchRequest{}, err
	}

	normalized := r
	normalized.IdempotencyKey = strings.TrimSpace(normalized.IdempotencyKey)
	normalized.ProductID = strings.TrimSpace(normalized.ProductID)
	normalized.Model = strings.TrimSpace(normalized.Model)
	normalized.Size = strings.TrimSpace(normalized.Size)
	normalized.Quality = strings.TrimSpace(normalized.Quality)
	normalized.ReferenceImageURL = strings.TrimSpace(normalized.ReferenceImageURL)
	normalized.CallbackURL = strings.TrimSpace(normalized.CallbackURL)
	normalized.Prompts = append([]string(nil), normalized.Prompts...)
	for i := range normalized.Prompts {
		normalized.Prompts[i] = strings.TrimSpace(normalized.Prompts[i])
	}
	if normalized.Model == "" {
		normalized.Model = DefaultModel
	}
	if normalized.Size == "" {
		normalized.Size = DefaultSize
	}
	if normalized.Quality == "" {
		normalized.Quality = DefaultQuality
	}
	return normalized, nil
}

// Validate checks only provider-contract invariants.  Model/size/quality
// defaults are applied by Normalize.  No upstream request is issued when this
// method fails.
func (r BatchRequest) Validate() error {
	if len(r.Prompts) == 0 {
		return errors.New("prompts must contain at least one item")
	}
	if len(r.Prompts) > maxPrompts {
		return fmt.Errorf("prompts must contain at most %d items", maxPrompts)
	}
	for i, prompt := range r.Prompts {
		if strings.TrimSpace(prompt) == "" {
			return fmt.Errorf("prompts[%d] must not be empty", i)
		}
		if len([]rune(prompt)) > maxPromptRunes {
			return fmt.Errorf("prompts[%d] exceeds %d characters", i, maxPromptRunes)
		}
	}
	if key := strings.TrimSpace(r.IdempotencyKey); len([]rune(key)) > maxIDKeyRunes {
		return fmt.Errorf("idempotency_key exceeds %d characters", maxIDKeyRunes)
	}
	if modelName := strings.TrimSpace(r.Model); modelName != "" && modelName != DefaultModel {
		return fmt.Errorf("model must be %s", DefaultModel)
	}
	if size := strings.TrimSpace(r.Size); size != "" && size != "1024x1024" && size != "1024x1792" && size != "1792x1024" {
		return fmt.Errorf("unsupported size %q", size)
	}
	if quality := strings.TrimSpace(r.Quality); quality != "" && quality != "standard" && quality != "hd" {
		return fmt.Errorf("unsupported quality %q", quality)
	}
	if r.ReferenceImageURL != "" {
		if err := validateHTTPURL(r.ReferenceImageURL, "reference_image_url"); err != nil {
			return err
		}
	}
	if r.CallbackURL != "" {
		if err := validateHTTPURL(r.CallbackURL, "callback_url"); err != nil {
			return err
		}
	}
	return nil
}

func validateHTTPURL(value, field string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an http or https URL", field)
	}
	return nil
}

// BatchSubmission is the immediate response after CodeFox accepts a batch.
// It intentionally contains no billing decision; submit acceptance is not a
// successful image result.
type BatchSubmission struct {
	TaskID     string `json:"task_id"`
	Status     Status `json:"status"`
	TotalCount int    `json:"total_count"`
	CreatedAt  int64  `json:"created_at"`
	// ExistingTask is true when the provider returned an already-created task
	// for an idempotency key.  CodeFox does not currently expose an explicit
	// flag, so callers may set this when they reconcile their persisted key.
	ExistingTask bool `json:"-"`
}

// Progress is the provider's per-task counter snapshot.
type Progress struct {
	Total   int `json:"total"`
	Success int `json:"success"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
}

// ItemResult is one successful generated image.  ImageURL is the provider's
// proxy URL; callers should expose a local Aibuff proxy rather than rewriting
// this URL directly into a customer response.
type ItemResult struct {
	ItemIndex int    `json:"item_index"`
	Prompt    string `json:"prompt"`
	ImageURL  string `json:"image_url"`
	Status    string `json:"status"`
}

// ItemError is one failed batch item.  ErrorMessage is sanitized before it is
// returned from the client and therefore safe to persist in a job record.
type ItemError struct {
	ItemIndex    int    `json:"item_index"`
	Prompt       string `json:"prompt"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// BatchTask is the normalized polling result.  Results/errors retain item
// indexes so the jobs layer can settle only successful items and preserve
// partial-failure detail without replaying the batch.
type BatchTask struct {
	TaskID      string       `json:"task_id"`
	ProductID   string       `json:"product_id,omitempty"`
	Status      Status       `json:"status"`
	Progress    Progress     `json:"progress"`
	Results     []ItemResult `json:"results,omitempty"`
	Errors      []ItemError  `json:"errors,omitempty"`
	CreatedAt   int64        `json:"created_at"`
	CompletedAt int64        `json:"completed_at,omitempty"`
}

// SucceededCount returns the number of successful items, preferring explicit
// provider progress and falling back to the normalized result list.
func (t BatchTask) SucceededCount() int {
	if t.Progress.Success > 0 || len(t.Results) == 0 {
		return t.Progress.Success
	}
	return len(t.Results)
}

// FailedCount returns the number of failed items, preferring explicit
// provider progress and falling back to the normalized error list.
func (t BatchTask) FailedCount() int {
	if t.Progress.Failed > 0 || len(t.Errors) == 0 {
		return t.Progress.Failed
	}
	return len(t.Errors)
}

// ChargeableResults returns a copy of successful items.  Aibuff's billing
// layer can use this as the exact per-item settlement input and must not infer
// charges from PENDING/PROCESSING or failed items.
func (t BatchTask) ChargeableResults() []ItemResult {
	results := make([]ItemResult, len(t.Results))
	copy(results, t.Results)
	return results
}

// Provider is the small interface consumed by durable jobs.  Implementations
// must treat SubmitBatch as a one-shot operation; polling GetTask never
// submits or retries the generation request.
type Provider interface {
	SubmitBatch(context.Context, BatchRequest) (BatchSubmission, error)
	GetTask(context.Context, string) (BatchTask, error)
	FetchImage(context.Context, string, int) (*http.Response, error)
}

// Client is a CodeFox native batch client.  APIKey is only used in an
// Authorization header and is never included in errors or response structs.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewClient validates the base URL.  The key is intentionally checked at
// request time so callers can construct a client before loading a key from a
// secret manager without ever logging or persisting that key here.
func NewClient(baseURL, apiKey string) (*Client, error) {
	if _, err := parseBaseURL(baseURL); err != nil {
		return nil, err
	}
	return &Client{BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), APIKey: apiKey, HTTPClient: http.DefaultClient}, nil
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func parseBaseURL(value string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("CodeFox base URL must be an http or https URL")
	}
	return u, nil
}

func (c *Client) endpoint(path string) (string, error) {
	if c == nil {
		return "", errors.New("CodeFox client is nil")
	}
	u, err := parseBaseURL(c.BaseURL)
	if err != nil {
		return "", err
	}
	// Provider endpoints are rooted at the host.  A configured base path is
	// deliberately discarded because /api/ecommerce is an absolute contract.
	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (c *Client) endpointWithTask(pathPrefix, taskID, suffix string) (string, error) {
	if c == nil {
		return "", errors.New("CodeFox client is nil")
	}
	u, err := parseBaseURL(c.BaseURL)
	if err != nil {
		return "", err
	}
	taskID = strings.TrimSpace(taskID)
	// Path is the decoded form while RawPath carries the escaped form. This
	// prevents a task id containing '/' or '?' from becoming a second path or
	// query component, and avoids the %25 double-escape caused by putting an
	// already escaped value directly in URL.Path.
	u.Path = pathPrefix + "/" + taskID + suffix
	u.RawPath = pathPrefix + "/" + url.PathEscape(taskID) + suffix
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body []byte) (*http.Request, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, ErrMissingAPIKey
	}
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// SubmitBatch performs exactly one native batch POST.  It never waits for
// completion and never retries the POST, leaving retry/lease policy to the
// durable jobs layer.
func (c *Client) SubmitBatch(ctx context.Context, request BatchRequest) (BatchSubmission, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return BatchSubmission{}, err
	}
	body, err := common.Marshal(normalized)
	if err != nil {
		return BatchSubmission{}, fmt.Errorf("marshal CodeFox batch request: %w", err)
	}
	endpoint, err := c.endpoint("/api/ecommerce/batch-generate")
	if err != nil {
		return BatchSubmission{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return BatchSubmission{}, err
	}
	if normalized.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", normalized.IdempotencyKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return BatchSubmission{}, fmt.Errorf("CodeFox batch submit request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := readJSONBody(resp.Body)
	if err != nil {
		return BatchSubmission{}, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return BatchSubmission{}, responseError(resp.StatusCode, responseBody, c.APIKey)
	}
	var envelope submitEnvelope
	if err := common.Unmarshal(responseBody, &envelope); err != nil {
		return BatchSubmission{}, errors.New("CodeFox batch submit returned invalid JSON")
	}
	if !envelope.Success {
		return BatchSubmission{}, sanitizedAPIError(resp.StatusCode, envelope.Message, "batch_submit_failed", c.APIKey)
	}
	status, err := NormalizeStatus(envelope.Data.Status)
	if err != nil {
		return BatchSubmission{}, err
	}
	if strings.TrimSpace(envelope.Data.TaskID) == "" {
		return BatchSubmission{}, errors.New("CodeFox batch submit response has no task_id")
	}
	return BatchSubmission{
		TaskID:     strings.TrimSpace(envelope.Data.TaskID),
		Status:     status,
		TotalCount: envelope.Data.TotalCount,
		CreatedAt:  envelope.Data.CreatedAt,
	}, nil
}

// GetTask performs one read-only task status query.  It does not submit or
// retry generation work and is therefore safe for a durable polling worker.
func (c *Client) GetTask(ctx context.Context, taskID string) (BatchTask, error) {
	if err := validateTaskID(taskID); err != nil {
		return BatchTask{}, err
	}
	endpoint, err := c.endpointWithTask("/api/ecommerce/tasks", taskID, "")
	if err != nil {
		return BatchTask{}, err
	}
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return BatchTask{}, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return BatchTask{}, fmt.Errorf("CodeFox task query request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := readJSONBody(resp.Body)
	if err != nil {
		return BatchTask{}, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return BatchTask{}, responseError(resp.StatusCode, responseBody, c.APIKey)
	}
	var envelope taskEnvelope
	if err := common.Unmarshal(responseBody, &envelope); err != nil {
		return BatchTask{}, errors.New("CodeFox task query returned invalid JSON")
	}
	if !envelope.Success {
		return BatchTask{}, sanitizedAPIError(resp.StatusCode, envelope.Message, "task_query_failed", c.APIKey)
	}
	return normalizeTask(envelope.Data, c.APIKey)
}

// ParseBatchTask decodes a previously persisted native task response without
// making another upstream request. It is intended for the public job/fetch
// converter after service.TaskPollingLoop has stored the latest response in
// model.Task.Data.
func ParseBatchTask(responseBody []byte) (BatchTask, error) {
	var envelope taskEnvelope
	if err := common.Unmarshal(responseBody, &envelope); err != nil {
		return BatchTask{}, errors.New("CodeFox task response is invalid JSON")
	}
	if !envelope.Success {
		return BatchTask{}, sanitizedAPIError(http.StatusBadGateway, envelope.Message, "task_query_failed", "")
	}
	return normalizeTask(envelope.Data, "")
}

// PollOptions controls a local convenience poller.  For production durable
// jobs, prefer calling GetTask once per lease and scheduling the next wakeup in
// the job system so a process restart cannot strand work.
type PollOptions struct {
	MaxAttempts int
	Interval    time.Duration
}

func (o PollOptions) normalized() PollOptions {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = defaultPollAttempts
	}
	if o.Interval < 0 {
		o.Interval = defaultPollInterval
	}
	return o
}

// Poll reads task status until a terminal state, context cancellation or the
// attempt limit.  It has no submit side effects.
func (c *Client) Poll(ctx context.Context, taskID string, options PollOptions) (BatchTask, error) {
	options = options.normalized()
	for attempt := 0; attempt < options.MaxAttempts; attempt++ {
		task, err := c.GetTask(ctx, taskID)
		if err != nil {
			return BatchTask{}, err
		}
		if task.Status.Terminal() {
			return task, nil
		}
		if attempt == options.MaxAttempts-1 {
			break
		}
		if err := wait(ctx, options.Interval); err != nil {
			return BatchTask{}, err
		}
	}
	return BatchTask{}, &PollTimeoutError{TaskID: strings.TrimSpace(taskID), Attempts: options.MaxAttempts}
}

func wait(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ImageURL returns the upstream image endpoint for an item.  The URL is for
// the provider-side request; customer-facing code should use its own proxy.
func (c *Client) ImageURL(taskID string, itemIndex int) (string, error) {
	if err := validateTaskID(taskID); err != nil {
		return "", err
	}
	if itemIndex < 0 {
		return "", errors.New("image item_index must be non-negative")
	}
	return c.endpointWithTask("/api/ecommerce/images", taskID, "/"+strconv.Itoa(itemIndex))
}

// FetchImage fetches one provider image.  On success the caller owns and must
// close the response body.  On failure the body is consumed/closed and the
// returned error is sanitized.
func (c *Client) FetchImage(ctx context.Context, taskID string, itemIndex int) (*http.Response, error) {
	endpoint, err := c.ImageURL(taskID, itemIndex)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("CodeFox image request failed: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := readJSONBody(resp.Body)
		_ = resp.Body.Close()
		return nil, responseError(resp.StatusCode, body, c.APIKey)
	}
	return resp, nil
}

func validateTaskID(taskID string) error {
	if strings.TrimSpace(taskID) == "" {
		return errors.New("CodeFox task_id must not be empty")
	}
	if strings.ContainsAny(taskID, "\r\n") {
		return errors.New("CodeFox task_id contains invalid characters")
	}
	return nil
}

type submitEnvelope struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		TaskID     string `json:"task_id"`
		Status     string `json:"status"`
		TotalCount int    `json:"total_count"`
		CreatedAt  int64  `json:"created_at"`
	} `json:"data"`
}

type taskEnvelope struct {
	Success bool     `json:"success"`
	Message string   `json:"message"`
	Data    taskData `json:"data"`
}

type taskData struct {
	TaskID      string       `json:"task_id"`
	ProductID   string       `json:"product_id"`
	Status      string       `json:"status"`
	Progress    Progress     `json:"progress"`
	Results     []taskResult `json:"results"`
	Errors      []taskError  `json:"errors"`
	CreatedAt   int64        `json:"created_at"`
	CompletedAt int64        `json:"completed_at"`
}

type taskResult struct {
	ItemIndex int    `json:"item_index"`
	Prompt    string `json:"prompt"`
	ImageURL  string `json:"image_url"`
	URL       string `json:"url"`
	Status    string `json:"status"`
}

type taskError struct {
	ItemIndex    int    `json:"item_index"`
	Prompt       string `json:"prompt"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	Message      string `json:"message"`
}

func normalizeTask(data taskData, apiKey string) (BatchTask, error) {
	status, err := NormalizeStatus(data.Status)
	if err != nil {
		return BatchTask{}, err
	}
	if strings.TrimSpace(data.TaskID) == "" {
		return BatchTask{}, errors.New("CodeFox task response has no task_id")
	}

	results := make([]ItemResult, 0, len(data.Results))
	for _, item := range data.Results {
		imageURL := strings.TrimSpace(item.ImageURL)
		if imageURL == "" {
			imageURL = strings.TrimSpace(item.URL)
		}
		results = append(results, ItemResult{
			ItemIndex: item.ItemIndex,
			Prompt:    item.Prompt,
			ImageURL:  imageURL,
			Status:    strings.TrimSpace(item.Status),
		})
	}
	errorsList := make([]ItemError, 0, len(data.Errors))
	for _, item := range data.Errors {
		message := item.ErrorMessage
		if strings.TrimSpace(message) == "" {
			message = item.Message
		}
		errorsList = append(errorsList, ItemError{
			ItemIndex:    item.ItemIndex,
			Prompt:       item.Prompt,
			ErrorCode:    sanitizeText(item.ErrorCode, apiKey),
			ErrorMessage: sanitizeText(message, apiKey),
		})
	}

	progress := data.Progress
	if progress.Total <= 0 {
		progress.Total = len(results) + len(errorsList) + progress.Pending
	}
	if progress.Success <= 0 && len(results) > 0 {
		progress.Success = len(results)
	}
	if progress.Failed <= 0 && len(errorsList) > 0 {
		progress.Failed = len(errorsList)
	}
	if progress.Pending <= 0 && progress.Total > progress.Success+progress.Failed {
		progress.Pending = progress.Total - progress.Success - progress.Failed
	}
	if progress.Total > 0 && progress.Success+progress.Failed+progress.Pending > progress.Total {
		return BatchTask{}, errors.New("CodeFox task progress counters are inconsistent")
	}

	return BatchTask{
		TaskID:      strings.TrimSpace(data.TaskID),
		ProductID:   strings.TrimSpace(data.ProductID),
		Status:      status,
		Progress:    progress,
		Results:     results,
		Errors:      errorsList,
		CreatedAt:   data.CreatedAt,
		CompletedAt: data.CompletedAt,
	}, nil
}

func readJSONBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxJSONBodyBytes+1))
	if err != nil {
		return nil, errors.New("CodeFox response body could not be read")
	}
	if len(body) > maxJSONBodyBytes {
		return nil, errors.New("CodeFox response body is too large")
	}
	return body, nil
}

// APIError deliberately contains only a sanitized provider code/message and
// HTTP status.  The response body is never exposed wholesale.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e == nil {
		return "CodeFox API error"
	}
	if e.Message == "" {
		return fmt.Sprintf("CodeFox API error (HTTP %d)", e.StatusCode)
	}
	if e.Code == "" {
		return fmt.Sprintf("CodeFox API error (HTTP %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("CodeFox API error (HTTP %d, %s): %s", e.StatusCode, e.Code, e.Message)
}

// PollTimeoutError identifies a non-terminal poll without exposing provider
// payloads or credentials.
type PollTimeoutError struct {
	TaskID   string
	Attempts int
}

func (e *PollTimeoutError) Error() string {
	return fmt.Sprintf("CodeFox task %s did not reach a terminal state after %d polls", e.TaskID, e.Attempts)
}

var ErrMissingAPIKey = errors.New("CodeFox API key is required")

func responseError(status int, body []byte, apiKey string) error {
	message := "upstream request failed"
	code := "http_error"
	var envelope struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if len(body) > 0 && common.Unmarshal(body, &envelope) == nil {
		if strings.TrimSpace(envelope.Message) != "" {
			message = envelope.Message
		}
		if strings.TrimSpace(envelope.Error.Message) != "" {
			message = envelope.Error.Message
		}
		if strings.TrimSpace(envelope.Error.Code) != "" {
			code = envelope.Error.Code
		}
	}
	return sanitizedAPIError(status, message, code, apiKey)
}

func sanitizedAPIError(status int, message, code, apiKey string) error {
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	return &APIError{
		StatusCode: status,
		Code:       sanitizeText(code, apiKey),
		Message:    sanitizeText(message, apiKey),
	}
}

var (
	bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`)
	secretPattern = regexp.MustCompile(`(?i)((?:api[-_ ]?key|token|authorization)\s*[:=]\s*)[^\s,;]+`)
)

func sanitizeText(value, apiKey string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if apiKey != "" {
		value = strings.ReplaceAll(value, apiKey, "[REDACTED]")
	}
	value = bearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = secretPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	if len([]rune(value)) > 1000 {
		value = string([]rune(value)[:1000])
	}
	return value
}
