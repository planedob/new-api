package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	image2AsyncJobTTL        = 30 * time.Minute
	image2AsyncJobMaxCount   = 128
	image2AsyncJobMaxRuntime = 15 * time.Minute
	image2AsyncJobLeaseGrace = 1 * time.Minute
)

// image2AsyncJobSnapshot is the customer-facing subset of the durable model.
// Request bodies, lease owners, and idempotency keys never leave the server.
type image2AsyncJobSnapshot struct {
	ID         string
	Operation  string
	RequestID  string
	Status     string
	HTTPStatus int
	CreatedAt  time.Time
	FinishedAt time.Time
	Response   []byte
}

// The lease owner is unique per process. It is used only in the conditional
// completion update; a second node cannot complete or replay another node's
// running job. The request body remains in the submit goroutine's memory and is
// deliberately absent from the database.
var image2AsyncLeaseOwner = "image2-async-" + uuid.NewString()

// SubmitImage2Job accepts exactly one image request and executes the existing
// relay once in a detached server-side context. The client receives a job ID
// before a slow upstream response can hit an edge timeout; polling never calls
// an upstream and therefore cannot duplicate billing or image generation.
func SubmitImage2Job(c *gin.Context) {
	operation := strings.ToLower(strings.TrimSpace(c.Param("operation")))
	if operation != "generations" && operation != "edits" {
		c.JSON(http.StatusBadRequest, gin.H{"error": map[string]any{
			"message": "operation must be generations or edits",
			"type":    "invalid_request_error",
			"code":    "invalid_image2_operation",
		}})
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("X-Image2-Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": map[string]any{
			"message": "X-Image2-Idempotency-Key is required for asynchronous Image2 delivery",
			"type":    "invalid_request_error",
			"code":    "missing_image2_idempotency_key",
		}})
		return
	}
	if err := model.CheckImage2AsyncJobSchema(); err != nil {
		writeImage2AsyncStoreUnavailable(c)
		return
	}

	// Recovery and cleanup are bounded and lazy. This also lets a node that was
	// restarted between requests reconcile stale leases without replaying them.
	_ = recoverAndPruneImage2AsyncJobs()

	userID := c.GetInt("id")
	requestID := strings.TrimSpace(c.GetString(common.RequestIdKey))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	scopeKey := image2AsyncIdempotencyKey(userID, operation, idempotencyKey)
	if existing, err := model.GetImage2AsyncJobByScope(scopeKey); err == nil {
		writeImage2AsyncJobAccepted(c, snapshotImage2AsyncJob(existing))
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		writeImage2AsyncStoreUnavailable(c)
		return
	}

	activeCount, err := model.CountActiveImage2AsyncJobs(time.Now().UTC())
	if err != nil {
		writeImage2AsyncStoreUnavailable(c)
		return
	}
	if activeCount >= image2AsyncJobMaxCount {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": map[string]any{
			"message": "Image2 asynchronous delivery queue is full; try again later",
			"type":    "server_error",
			"code":    "image2_async_queue_full",
		}})
		return
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": map[string]any{
			"message": "failed to read Image2 request body",
			"type":    "invalid_request_error",
			"code":    "image2_async_body_unreadable",
		}})
		return
	}
	body, err := storage.Bytes()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": map[string]any{
			"message": "failed to snapshot Image2 request body",
			"type":    "invalid_request_error",
			"code":    "image2_async_body_unreadable",
		}})
		return
	}
	bodyCopy := append([]byte(nil), body...)
	createdAt := time.Now().UTC()
	job := &model.Image2AsyncJob{
		ID:             uuid.NewString(),
		ScopeKey:       scopeKey,
		IdempotencyKey: idempotencyKey,
		UserID:         userID,
		Operation:      operation,
		RequestID:      requestID,
		Status:         model.Image2AsyncJobStatusPending,
		CreatedAt:      createdAt,
		ExpiresAt:      createdAt.Add(image2AsyncJobTTL),
	}
	if err := model.CreateImage2AsyncJob(job); err != nil {
		// A concurrent submit on another node may have won the unique scope. In
		// that case return the existing job and never start a second relay.
		if existing, lookupErr := model.GetImage2AsyncJobByScope(scopeKey); lookupErr == nil {
			writeImage2AsyncJobAccepted(c, snapshotImage2AsyncJob(existing))
			return
		}
		writeImage2AsyncStoreUnavailable(c)
		return
	}

	// Copy only immutable request metadata and body bytes before the original
	// request's BodyStorageCleanup middleware closes its storage.
	keys := make(map[string]any, len(c.Keys))
	for key, value := range c.Keys {
		if key == common.KeyBodyStorage || key == common.KeyRequestBody {
			continue
		}
		keys[key] = value
	}
	headers := c.Request.Header.Clone()
	remoteAddr := c.Request.RemoteAddr
	leaseUntil := createdAt.Add(image2AsyncJobMaxRuntime + image2AsyncJobLeaseGrace)
	claimed, err := model.ClaimImage2AsyncJob(job.ID, image2AsyncLeaseOwner, time.Now().UTC(), leaseUntil)
	if err != nil {
		// The metadata row is retained for idempotency and will be safely
		// reconciled by stale recovery; do not risk an unguarded upstream call.
		writeImage2AsyncStoreUnavailable(c)
		return
	}
	if claimed {
		go executeImage2AsyncJob(job.ID, job.Operation, job.RequestID, keys, headers, remoteAddr, bodyCopy)
	}

	writeImage2AsyncJobAccepted(c, snapshotImage2AsyncJob(job))
}

// GetImage2Job returns a user-scoped durable snapshot. It never retries or
// touches an upstream channel; a completed response is replayed from the
// database until the bounded TTL expires.
func GetImage2Job(c *gin.Context) {
	if err := model.CheckImage2AsyncJobSchema(); err != nil {
		writeImage2AsyncStoreUnavailable(c)
		return
	}
	_ = recoverAndPruneImage2AsyncJobs()
	id := strings.TrimSpace(c.Param("id"))
	job, err := model.GetImage2AsyncJob(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeImage2AsyncJobNotFound(c)
		return
	}
	if err != nil {
		writeImage2AsyncStoreUnavailable(c)
		return
	}
	if job.UserID != c.GetInt("id") {
		writeImage2AsyncJobNotFound(c)
		return
	}

	snapshot := snapshotImage2AsyncJob(job)
	payload := gin.H{
		"id":          snapshot.ID,
		"object":      "image2.job",
		"operation":   snapshot.Operation,
		"request_id":  snapshot.RequestID,
		"status":      snapshot.Status,
		"http_status": snapshot.HTTPStatus,
		"created_at":  snapshot.CreatedAt,
	}
	if !snapshot.FinishedAt.IsZero() {
		payload["finished_at"] = snapshot.FinishedAt
	}
	if len(snapshot.Response) > 0 {
		var response json.RawMessage
		if json.Valid(snapshot.Response) {
			response = append(json.RawMessage(nil), snapshot.Response...)
		} else {
			response, _ = json.Marshal(map[string]string{"message": "Image2 job returned a malformed response"})
		}
		payload["response"] = response
	}
	c.JSON(http.StatusOK, payload)
}

func writeImage2AsyncJobAccepted(c *gin.Context, snapshot image2AsyncJobSnapshot) {
	status := http.StatusAccepted
	if snapshot.Status != "processing" {
		status = http.StatusOK
	}
	c.Header("X-Oneapi-Request-Id", snapshot.RequestID)
	c.JSON(status, gin.H{
		"id":         snapshot.ID,
		"object":     "image2.job",
		"operation":  snapshot.Operation,
		"request_id": snapshot.RequestID,
		"status":     snapshot.Status,
		"poll_url":   "/pg/images/jobs/" + snapshot.ID,
		"created_at": snapshot.CreatedAt,
	})
}

func writeImage2AsyncJobNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"error": map[string]any{
		"message": "Image2 asynchronous job not found",
		"type":    "invalid_request_error",
		"code":    "image2_async_job_not_found",
	}})
}

func writeImage2AsyncStoreUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": map[string]any{
		"message": "Image2 asynchronous delivery is temporarily unavailable",
		"type":    "server_error",
		"code":    "image2_async_store_unavailable",
	}})
}

func executeImage2AsyncJob(jobID, operation, requestID string, keys map[string]any, headers http.Header, remoteAddr string, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), image2AsyncJobMaxRuntime)
	defer cancel()
	recorder := httptest.NewRecorder()
	clone, _ := gin.CreateTestContext(recorder)
	for key, value := range keys {
		clone.Set(key, value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/pg/images/"+operation, bytes.NewReader(body))
	if err != nil {
		finishImage2AsyncJob(jobID, http.StatusInternalServerError, []byte(`{"error":{"message":"failed to create asynchronous request"}}`))
		return
	}
	request.Header = headers.Clone()
	request.Header.Del("X-Image2-Idempotency-Key")
	request.RemoteAddr = remoteAddr
	request.ContentLength = int64(len(body))
	clone.Request = request
	clone.Set(common.RequestIdKey, requestID)
	storage, storageErr := common.CreateBodyStorage(body)
	if storageErr != nil {
		finishImage2AsyncJob(jobID, http.StatusInternalServerError, []byte(`{"error":{"message":"failed to stage asynchronous request body"}}`))
		return
	}
	clone.Set(common.KeyBodyStorage, storage)
	defer common.CleanupBodyStorage(clone)

	defer func() {
		if recovered := recover(); recovered != nil {
			finishImage2AsyncJob(jobID, http.StatusInternalServerError, []byte(`{"error":{"message":"Image2 asynchronous delivery failed"}}`))
		}
	}()
	Playground(clone)
	status := recorder.Code
	if status == 0 {
		status = http.StatusInternalServerError
	}
	finishImage2AsyncJob(jobID, status, append([]byte(nil), recorder.Body.Bytes()...))
}

func finishImage2AsyncJob(jobID string, status int, body []byte) {
	errorCode := ""
	errorMessage := ""
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		errorCode = "image2_async_upstream_failure"
		errorMessage = "Image2 asynchronous delivery failed"
	}
	_, _ = model.CompleteImage2AsyncJob(jobID, image2AsyncLeaseOwner, status, body, errorCode, errorMessage, time.Now().UTC())
}

func snapshotImage2AsyncJob(job *model.Image2AsyncJob) image2AsyncJobSnapshot {
	if job == nil {
		return image2AsyncJobSnapshot{}
	}
	status := job.Status
	if status == model.Image2AsyncJobStatusPending || status == model.Image2AsyncJobStatusRunning {
		status = "processing"
	}
	snapshot := image2AsyncJobSnapshot{
		ID:         job.ID,
		Operation:  job.Operation,
		RequestID:  job.RequestID,
		Status:     status,
		HTTPStatus: job.HTTPStatus,
		CreatedAt:  job.CreatedAt,
		Response:   append([]byte(nil), []byte(job.ResponseBody)...),
	}
	if job.FinishedAt != nil {
		snapshot.FinishedAt = *job.FinishedAt
	}
	return snapshot
}

func recoverAndPruneImage2AsyncJobs() error {
	now := time.Now().UTC()
	if _, err := model.RecoverImage2AsyncJobs(now, now.Add(-image2AsyncJobMaxRuntime), image2AsyncJobMaxCount); err != nil {
		return err
	}
	_, err := model.PruneExpiredImage2AsyncJobs(now, image2AsyncJobMaxCount)
	return err
}

// RecoverImage2AsyncJobsOnStartup reconciles rows left by a process restart.
// It is safe to call before database initialization (the no-op path is used by
// isolated router tests); request handlers repeat the bounded check lazily.
func RecoverImage2AsyncJobsOnStartup() error {
	if model.DB == nil {
		return nil
	}
	if err := model.CheckImage2AsyncJobSchema(); err != nil {
		return err
	}
	return recoverAndPruneImage2AsyncJobs()
}

func image2AsyncIdempotencyKey(userID int, operation, idempotencyKey string) string {
	return fmt.Sprintf("%d\x00%s\x00%s", userID, operation, idempotencyKey)
}
