package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openImage2AsyncControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB := model.DB
	dsn := fmt.Sprintf("file:image2_async_controller_%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Image2AsyncJob{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = oldDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestSubmitImage2JobRequiresIdempotencyKey(t *testing.T) {
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
	db := openImage2AsyncControllerTestDB(t)
	finished := time.Now().UTC()
	job := &model.Image2AsyncJob{
		ID:             "job-user-scope",
		ScopeKey:       image2AsyncIdempotencyKey(10, "generations", "stored"),
		IdempotencyKey: "stored",
		UserID:         10,
		Operation:      "generations",
		RequestID:      "request-1",
		Status:         model.Image2AsyncJobStatusSucceeded,
		HTTPStatus:     http.StatusOK,
		ResponseBody:   `{"data":[{"url":"https://example.invalid/image.png"}]}`,
		CreatedAt:      finished.Add(-time.Second),
		FinishedAt:     &finished,
		ExpiresAt:      finished.Add(image2AsyncJobTTL),
	}
	require.NoError(t, db.Create(job).Error)

	unauthorizedRecorder := httptest.NewRecorder()
	unauthorized, _ := gin.CreateTestContext(unauthorizedRecorder)
	unauthorized.Request = httptest.NewRequest(http.MethodGet, "/pg/images/jobs/job-user-scope", nil)
	unauthorized.Params = gin.Params{{Key: "id", Value: job.ID}}
	unauthorized.Set("id", 11)
	GetImage2Job(unauthorized)
	require.Equal(t, http.StatusNotFound, unauthorizedRecorder.Code)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/pg/images/jobs/job-user-scope", nil)
	ctx.Params = gin.Params{{Key: "id", Value: job.ID}}
	ctx.Set("id", 10)
	GetImage2Job(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "https://example.invalid/image.png")
}

func TestImage2AsyncJobPruningKeepsProcessingAndRecentResults(t *testing.T) {
	db := openImage2AsyncControllerTestDB(t)
	now := time.Now().UTC()
	oldFinished := now.Add(-image2AsyncJobTTL - time.Second)
	recentFinished := now.Add(-time.Second)
	rows := []*model.Image2AsyncJob{
		{ID: "old", ScopeKey: "old-scope", IdempotencyKey: "old", UserID: 1, Operation: "generations", RequestID: "old-req", Status: model.Image2AsyncJobStatusSucceeded, FinishedAt: &oldFinished, CreatedAt: oldFinished, ExpiresAt: oldFinished},
		{ID: "recent", ScopeKey: "recent-scope", IdempotencyKey: "recent", UserID: 1, Operation: "generations", RequestID: "recent-req", Status: model.Image2AsyncJobStatusSucceeded, FinishedAt: &recentFinished, CreatedAt: recentFinished, ExpiresAt: now.Add(time.Hour)},
		{ID: "processing", ScopeKey: "processing-scope", IdempotencyKey: "processing", UserID: 1, Operation: "generations", RequestID: "processing-req", Status: model.Image2AsyncJobStatusRunning, CreatedAt: now, ExpiresAt: oldFinished},
	}
	for _, row := range rows {
		require.NoError(t, db.Create(row).Error)
	}
	deleted, err := model.PruneExpiredImage2AsyncJobs(now, 128)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	var recent, processing model.Image2AsyncJob
	require.NoError(t, db.First(&recent, "id = ?", "recent").Error)
	require.NoError(t, db.First(&processing, "id = ?", "processing").Error)
	var old model.Image2AsyncJob
	require.Error(t, db.First(&old, "id = ?", "old").Error)
}

func TestGetImage2JobReturnsSafeFailureAfterRecovery(t *testing.T) {
	db := openImage2AsyncControllerTestDB(t)
	now := time.Now().UTC()
	created := now.Add(-image2AsyncJobMaxRuntime - time.Minute)
	leaseUntil := now.Add(-time.Second)
	require.NoError(t, db.Create(&model.Image2AsyncJob{
		ID: "stale-running", ScopeKey: "stale-running-scope", IdempotencyKey: "stale-running", UserID: 9,
		Operation: "edits", RequestID: "stale-request", Status: model.Image2AsyncJobStatusRunning,
		CreatedAt: created, ExpiresAt: now.Add(time.Hour), LeaseOwner: "dead-node", LeaseUntil: &leaseUntil,
	}).Error)

	changed, err := model.RecoverImage2AsyncJobs(now, created, 10)
	require.NoError(t, err)
	require.Equal(t, 1, changed)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/pg/images/jobs/stale-running", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "stale-running"}}
	ctx.Set("id", 9)
	GetImage2Job(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "image2_async_interrupted")
	require.Contains(t, recorder.Body.String(), "not replayed")
	require.NotContains(t, recorder.Body.String(), "dead-node")
	require.NotContains(t, strings.ToLower(recorder.Body.String()), "prompt")
}

func TestImage2AsyncSchemaMissingFailsClosed(t *testing.T) {
	oldDB := model.DB
	dsn := fmt.Sprintf("file:image2_async_missing_%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = oldDB
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/pg/images/jobs/missing", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "missing"}}
	ctx.Set("id", 10)
	GetImage2Job(ctx)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "image2_async_store_unavailable")
}
