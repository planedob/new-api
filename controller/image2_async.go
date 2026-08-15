package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	image2AsyncJobTTL        = 30 * time.Minute
	image2AsyncJobMaxCount   = 128
	image2AsyncJobMaxRuntime = 15 * time.Minute
)

type image2AsyncJob struct {
	id             string
	idempotencyKey string
	userID         int
	operation      string
	requestID      string
	createdAt      time.Time
	finishedAt     time.Time
	status         string
	httpStatus     int
	responseBody   []byte
	responseHeader http.Header
}

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

var image2AsyncJobs = struct {
	sync.Mutex
	byID          map[string]*image2AsyncJob
	byIdempotency map[string]string
}{
	byID:          make(map[string]*image2AsyncJob),
	byIdempotency: make(map[string]string),
}

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
	userID := c.GetInt("id")
	requestID := strings.TrimSpace(c.GetString(common.RequestIdKey))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	key := image2AsyncIdempotencyKey(userID, operation, idempotencyKey)

	image2AsyncJobs.Lock()
	pruneImage2AsyncJobsLocked(time.Now())
	if existingID := image2AsyncJobs.byIdempotency[key]; existingID != "" {
		if existing := image2AsyncJobs.byID[existingID]; existing != nil {
			snapshot := snapshotImage2AsyncJob(existing)
			image2AsyncJobs.Unlock()
			writeImage2AsyncJobAccepted(c, snapshot)
			return
		}
		delete(image2AsyncJobs.byIdempotency, key)
	}
	if len(image2AsyncJobs.byID) >= image2AsyncJobMaxCount {
		image2AsyncJobs.Unlock()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": map[string]any{
			"message": "Image2 asynchronous delivery queue is full; try again later",
			"type":    "server_error",
			"code":    "image2_async_queue_full",
		}})
		return
	}
	job := &image2AsyncJob{
		id:             uuid.NewString(),
		idempotencyKey: key,
		userID:         userID,
		operation:      operation,
		requestID:      requestID,
		createdAt:      time.Now().UTC(),
		status:         "processing",
	}
	image2AsyncJobs.byID[job.id] = job
	image2AsyncJobs.byIdempotency[key] = job.id
	acceptedSnapshot := snapshotImage2AsyncJob(job)
	image2AsyncJobs.Unlock()

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
	go executeImage2AsyncJob(job, keys, headers, remoteAddr, bodyCopy)

	writeImage2AsyncJobAccepted(c, acceptedSnapshot)
}

// GetImage2Job returns a user-scoped snapshot. It never retries or touches an
// upstream channel; a completed response is replayed from the single job
// result kept in memory until the bounded TTL expires.
func GetImage2Job(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	image2AsyncJobs.Lock()
	pruneImage2AsyncJobsLocked(time.Now())
	job := image2AsyncJobs.byID[id]
	if job == nil || job.userID != c.GetInt("id") {
		image2AsyncJobs.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": map[string]any{
			"message": "Image2 asynchronous job not found",
			"type":    "invalid_request_error",
			"code":    "image2_async_job_not_found",
		}})
		return
	}
	snapshot := snapshotImage2AsyncJob(job)
	image2AsyncJobs.Unlock()

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

func executeImage2AsyncJob(job *image2AsyncJob, keys map[string]any, headers http.Header, remoteAddr string, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), image2AsyncJobMaxRuntime)
	defer cancel()
	recorder := httptest.NewRecorder()
	clone, _ := gin.CreateTestContext(recorder)
	for key, value := range keys {
		clone.Set(key, value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/pg/images/"+job.operation, bytes.NewReader(body))
	if err != nil {
		finishImage2AsyncJob(job, http.StatusInternalServerError, []byte(`{"error":{"message":"failed to create asynchronous request"}}`), nil)
		return
	}
	request.Header = headers.Clone()
	request.Header.Del("X-Image2-Idempotency-Key")
	request.RemoteAddr = remoteAddr
	request.ContentLength = int64(len(body))
	clone.Request = request
	clone.Set(common.RequestIdKey, job.requestID)
	storage, storageErr := common.CreateBodyStorage(body)
	if storageErr != nil {
		finishImage2AsyncJob(job, http.StatusInternalServerError, []byte(`{"error":{"message":"failed to stage asynchronous request body"}}`), nil)
		return
	}
	clone.Set(common.KeyBodyStorage, storage)
	defer common.CleanupBodyStorage(clone)

	defer func() {
		if recovered := recover(); recovered != nil {
			finishImage2AsyncJob(job, http.StatusInternalServerError, []byte(`{"error":{"message":"Image2 asynchronous delivery failed"}}`), nil)
		}
	}()
	Playground(clone)
	status := recorder.Code
	if status == 0 {
		status = http.StatusInternalServerError
	}
	finishImage2AsyncJob(job, status, append([]byte(nil), recorder.Body.Bytes()...), recorder.Header().Clone())
}

func finishImage2AsyncJob(job *image2AsyncJob, status int, body []byte, headers http.Header) {
	image2AsyncJobs.Lock()
	defer image2AsyncJobs.Unlock()
	current := image2AsyncJobs.byID[job.id]
	if current == nil {
		return
	}
	current.httpStatus = status
	current.responseBody = append([]byte(nil), body...)
	current.responseHeader = headers.Clone()
	current.finishedAt = time.Now().UTC()
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		current.status = "succeeded"
	} else {
		current.status = "failed"
	}
}

func snapshotImage2AsyncJob(job *image2AsyncJob) image2AsyncJobSnapshot {
	snapshot := image2AsyncJobSnapshot{
		ID:         job.id,
		Operation:  job.operation,
		RequestID:  job.requestID,
		Status:     job.status,
		HTTPStatus: job.httpStatus,
		CreatedAt:  job.createdAt,
		FinishedAt: job.finishedAt,
		Response:   append([]byte(nil), job.responseBody...),
	}
	return snapshot
}

func pruneImage2AsyncJobsLocked(now time.Time) {
	for id, job := range image2AsyncJobs.byID {
		if job == nil || job.finishedAt.IsZero() || now.Sub(job.finishedAt) <= image2AsyncJobTTL {
			continue
		}
		delete(image2AsyncJobs.byID, id)
		delete(image2AsyncJobs.byIdempotency, job.idempotencyKey)
	}
}

func image2AsyncIdempotencyKey(userID int, operation, idempotencyKey string) string {
	return fmt.Sprintf("%d\x00%s\x00%s", userID, operation, idempotencyKey)
}
