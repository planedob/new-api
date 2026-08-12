package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupDistributorErrorLogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousSQLite := common.UsingSQLite
	previousRedisEnabled := common.RedisEnabled
	previousErrorLogEnabled := constant.ErrorLogEnabled

	dsn := fmt.Sprintf("file:distributor-error-log-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db
	common.UsingSQLite = true
	common.RedisEnabled = false
	constant.ErrorLogEnabled = true

	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.UsingSQLite = previousSQLite
		common.RedisEnabled = previousRedisEnabled
		constant.ErrorLogEnabled = previousErrorLogEnabled
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestDistributorChannelSelectionFailureCreatesSearchableErrorLog(t *testing.T) {
	db := setupDistributorErrorLogTestDB(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("id", 9101)
	c.Set("username", "selection-user")
	c.Set("token_id", 9201)
	c.Set("token_name", "selection-token")
	c.Set(common.RequestIdKey, "selection-request-id")
	c.Set("use_channel", []string{})

	abortWithOpenAiMessageAndRecordSelection(
		c,
		http.StatusServiceUnavailable,
		"no available channel for selected model and group",
		types.ErrorCodeModelNotFound,
		"gpt-5.5",
		"gpt-full",
	)

	require.True(t, c.IsAborted())
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)

	var rows []model.Log
	require.NoError(t, db.Where("request_id = ?", "selection-request-id").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, model.LogTypeError, rows[0].Type)
	require.Equal(t, 0, rows[0].ChannelId)
	require.Equal(t, "gpt-5.5", rows[0].ModelName)
	require.Equal(t, "gpt-full", rows[0].Group)

	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Other, &other))
	require.Equal(t, "channel_selection", other["error_stage"])
	require.Equal(t, "gpt-full", other["selection_group"])
	require.Equal(t, "gpt-5.5", other["requested_model"])
}

func TestAuthenticatedMiddlewareFailureCreatesSearchableErrorLog(t *testing.T) {
	db := setupDistributorErrorLogTestDB(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("id", 9102)
	c.Set("username", "middleware-user")
	c.Set("token_id", 9202)
	c.Set("token_name", "middleware-token")
	c.Set("group", "default")
	c.Set("original_model", "gpt-5.5")
	c.Set(common.RequestIdKey, "middleware-request-id")
	setRelayErrorStage(c, "rate_limit")

	abortWithOpenAiMessage(c, http.StatusTooManyRequests, "request rate limit reached")

	var rows []model.Log
	require.NoError(t, db.Where("request_id = ?", "middleware-request-id").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, model.LogTypeError, rows[0].Type)
	require.Equal(t, 0, rows[0].ChannelId)
	require.Equal(t, "gpt-5.5", rows[0].ModelName)
	require.Equal(t, "default", rows[0].Group)

	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Other, &other))
	require.Equal(t, "rate_limit", other["error_stage"])
}

func TestRelayErrorAuditCatchesDirectAuthenticatedHTTPFailure(t *testing.T) {
	db := setupDistributorErrorLogTestDB(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 9103)
		c.Set("username", "audit-user")
		c.Set("token_id", 9203)
		c.Set("token_name", "audit-token")
		c.Set("group", "video")
		c.Set("original_model", "video-model")
		c.Set(common.RequestIdKey, "audit-request-id")
		c.Next()
	})
	router.Use(RelayErrorAudit())
	router.POST("/v1/videos", func(c *gin.Context) {
		c.Status(http.StatusBadGateway)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadGateway, recorder.Code)

	var rows []model.Log
	require.NoError(t, db.Where("request_id = ?", "audit-request-id").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, 0, rows[0].ChannelId)
	require.Equal(t, "video-model", rows[0].ModelName)
	require.Equal(t, "video", rows[0].Group)

	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Other, &other))
	require.Equal(t, "response_audit", other["error_stage"])
	require.Equal(t, true, other["fallback_audit"])
	require.Equal(t, float64(http.StatusBadGateway), other["response_status"])
}

func TestAnonymousClientErrorDoesNotFloodCustomerErrorLog(t *testing.T) {
	db := setupDistributorErrorLogTestDB(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "anonymous-invalid-token")
	setRelayErrorStage(c, "authentication")

	abortWithOpenAiMessage(c, http.StatusUnauthorized, "invalid token")

	var count int64
	require.NoError(t, db.Model(&model.Log{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestRelayErrorAuditDoesNotDuplicateStructuredMiddlewareError(t *testing.T) {
	db := setupDistributorErrorLogTestDB(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 9104)
		c.Set("token_id", 9204)
		c.Set(common.RequestIdKey, "no-duplicate-request-id")
		c.Next()
	})
	router.Use(RelayErrorAudit())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		setRelayErrorStage(c, "distribution")
		abortWithOpenAiMessage(c, http.StatusForbidden, "model access denied", types.ErrorCodeAccessDenied)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)

	var rows []model.Log
	require.NoError(t, db.Where("request_id = ?", "no-duplicate-request-id").Find(&rows).Error)
	require.Len(t, rows, 1)
	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Other, &other))
	require.Equal(t, "distribution", other["error_stage"])
}

func TestRelayErrorAuditMakesNoRecordFailureSearchableWithoutSensitiveBody(t *testing.T) {
	db := setupDistributorErrorLogTestDB(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 9105)
		c.Set("token_id", 9205)
		c.Set("group", "default")
		c.Set(common.RequestIdKey, "no-record-fallback-request-id")
		c.Next()
	})
	router.Use(RelayErrorAudit())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		quotaErr := types.NewErrorWithStatusCode(
			errors.New("sensitive quota detail must not enter fallback content"),
			types.ErrorCodeInsufficientUserQuota,
			http.StatusForbidden,
			types.ErrOptionWithNoRecordErrorLog(),
		)
		require.False(t, service.RecordRelayErrorLog(c, quotaErr, service.RelayErrorLogOptions{Stage: "pre_consume"}))
		c.Status(http.StatusForbidden)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)

	var rows []model.Log
	require.NoError(t, db.Where("request_id = ?", "no-record-fallback-request-id").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.NotContains(t, rows[0].Content, "sensitive quota detail")
	require.Contains(t, rows[0].Content, "HTTP 403")
	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Other, &other))
	require.Equal(t, "response_audit", other["error_stage"])
}
