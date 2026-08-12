package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
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
