package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func resetImage2AsyncJobsForTest(t *testing.T) {
	t.Helper()
	image2AsyncJobs.Lock()
	image2AsyncJobs.byID = make(map[string]*image2AsyncJob)
	image2AsyncJobs.byIdempotency = make(map[string]string)
	image2AsyncJobs.Unlock()
}

func TestSubmitImage2JobRequiresIdempotencyKey(t *testing.T) {
	resetImage2AsyncJobsForTest(t)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/pg/images/jobs/generations", nil)
	ctx.Params = gin.Params{{Key: "operation", Value: "generations"}}

	SubmitImage2Job(ctx)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "missing_image2_idempotency_key")
}

func TestImage2AsyncIdempotencyKeyScopesUserAndOperation(t *testing.T) {
	require.NotEqual(t,
		image2AsyncIdempotencyKey(1, "generations", "same"),
		image2AsyncIdempotencyKey(2, "generations", "same"),
	)
	require.NotEqual(t,
		image2AsyncIdempotencyKey(1, "generations", "same"),
		image2AsyncIdempotencyKey(1, "edits", "same"),
	)
}

func TestGetImage2JobIsUserScopedAndReturnsStoredResponse(t *testing.T) {
	resetImage2AsyncJobsForTest(t)
	job := &image2AsyncJob{
		id:           "job-user-scope",
		userID:       10,
		operation:    "generations",
		requestID:    "request-1",
		createdAt:    time.Now().UTC(),
		finishedAt:   time.Now().UTC(),
		status:       "succeeded",
		httpStatus:   http.StatusOK,
		responseBody: []byte(`{"data":[{"url":"https://example.invalid/image.png"}]}`),
	}
	image2AsyncJobs.Lock()
	image2AsyncJobs.byID[job.id] = job
	image2AsyncJobs.Unlock()

	unauthorizedRecorder := httptest.NewRecorder()
	unauthorized, _ := gin.CreateTestContext(unauthorizedRecorder)
	unauthorized.Request = httptest.NewRequest(http.MethodGet, "/pg/images/jobs/job-user-scope", nil)
	unauthorized.Params = gin.Params{{Key: "id", Value: job.id}}
	unauthorized.Set("id", 11)
	GetImage2Job(unauthorized)
	require.Equal(t, http.StatusNotFound, unauthorizedRecorder.Code)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/pg/images/jobs/job-user-scope", nil)
	ctx.Params = gin.Params{{Key: "id", Value: job.id}}
	ctx.Set("id", 10)
	GetImage2Job(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "https://example.invalid/image.png")
}

func TestImage2AsyncJobPruningKeepsProcessingAndRecentResults(t *testing.T) {
	resetImage2AsyncJobsForTest(t)
	now := time.Now()
	old := &image2AsyncJob{id: "old", userID: 1, status: "succeeded", finishedAt: now.Add(-image2AsyncJobTTL - time.Second)}
	recent := &image2AsyncJob{id: "recent", userID: 1, status: "succeeded", finishedAt: now.Add(-time.Second)}
	processing := &image2AsyncJob{id: "processing", userID: 1, status: "processing"}
	image2AsyncJobs.Lock()
	image2AsyncJobs.byID[old.id] = old
	image2AsyncJobs.byID[recent.id] = recent
	image2AsyncJobs.byID[processing.id] = processing
	image2AsyncJobs.byIdempotency[image2AsyncIdempotencyKey(1, "generations", "old")] = old.id
	pruneImage2AsyncJobsLocked(now)
	_, oldExists := image2AsyncJobs.byID[old.id]
	_, recentExists := image2AsyncJobs.byID[recent.id]
	_, processingExists := image2AsyncJobs.byID[processing.id]
	image2AsyncJobs.Unlock()
	require.False(t, oldExists)
	require.True(t, recentExists)
	require.True(t, processingExists)
}
