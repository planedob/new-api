package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImage2OversizeRequestFailsBeforeLegacyChannelSelection(t *testing.T) {
	old := common.Image2SmartRoutingEnabled
	common.Image2SmartRoutingEnabled = true
	t.Cleanup(func() { common.Image2SmartRoutingEnabled = old })
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesGenerations,
	}
	for _, size := range []string{"4097x4096", "8192x8192"} {
		t.Run(size, func(t *testing.T) {
			validationErr := image2SmartRequestValidationError(info, &dto.ImageRequest{Size: size})
			require.NotNil(t, validationErr)
			require.Equal(t, http.StatusBadRequest, validationErr.StatusCode)
			require.Equal(t, types.ErrorCodeInvalidRequest, validationErr.GetErrorCode())
			require.True(t, types.IsSkipRetryError(validationErr))
		})
	}
}

func TestRelayImage2OversizeRequestFailsAtRequestEntryWithoutSideEffects(t *testing.T) {
	old := common.Image2SmartRoutingEnabled
	common.Image2SmartRoutingEnabled = true
	t.Cleanup(func() { common.Image2SmartRoutingEnabled = old })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/images/generations",
		strings.NewReader(`{"model":"gpt-image-2","size":"8192x8192"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, constant.ContextKeyOriginalModel, "gpt-image-2")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	ctx.Set("use_channel", []string{})

	Relay(ctx, types.RelayFormatOpenAIImage)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, ctx.GetStringSlice("use_channel"), "invalid Image2 size must not select a channel")
	_, routerActive := ctx.Get("image2_smart_router_active")
	require.False(t, routerActive, "invalid Image2 size must not activate smart routing")
}

func TestRelayImage2ExplicitZeroNIs400BeforeRouting(t *testing.T) {
	old := common.Image2SmartRoutingEnabled
	common.Image2SmartRoutingEnabled = true
	t.Cleanup(func() { common.Image2SmartRoutingEnabled = old })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/images/generations",
		strings.NewReader(`{"model":"gpt-image-2","n":0}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(ctx, constant.ContextKeyOriginalModel, "gpt-image-2")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenGroup, "default")
	ctx.Set("use_channel", []string{})

	Relay(ctx, types.RelayFormatOpenAIImage)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, ctx.GetStringSlice("use_channel"))
	_, routerActive := ctx.Get("image2_smart_router_active")
	require.False(t, routerActive)
}

func TestImage2PreRouteErrorMetadataIsQueryableAndSanitized(t *testing.T) {
	for _, requestPath := range []string{"/v1/images/edits", "/pg/images/edits"} {
		t.Run(requestPath, func(t *testing.T) {
			request := service.Image2RequestCapability{Operation: "edits", Resolution: "1024", Quality: "auto", N: 1}
			routeErr := types.NewErrorWithStatusCode(
				fmt.Errorf("no compatible Image2 channel remains"),
				types.ErrorCodeGetChannelFailed,
				http.StatusInternalServerError,
				types.ErrOptionWithSkipRetry(),
			)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader("secret image payload"))
			common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "resolved-auto-group")
			metadata := image2PreRouteErrorMetadata(ctx, &relaycommon.RelayInfo{
				OriginModelName: "gpt-image-2",
				UsingGroup:      "stale-using-group",
				TokenGroup:      "auto",
			}, request, 0, "44:operation_unsupported", routeErr)

			require.Equal(t, "channel_selection", metadata["stage"])
			require.Equal(t, "edits", metadata["operation"])
			require.Equal(t, 0, metadata["candidate_count"])
			require.Equal(t, false, metadata["upstream_called"])
			require.Equal(t, false, metadata["charged"])
			require.Equal(t, 0, metadata["quota"])
			require.Equal(t, requestPath, metadata["request_path"])
			require.Equal(t, "resolved-auto-group", metadata["group"])
			encoded := common.MapToJsonStr(metadata)
			require.NotContains(t, encoded, "secret image payload")
		})
	}
}

func TestImage2PreRouteErrorPersistsWithoutTokenMetadata(t *testing.T) {
	oldDB := model.DB
	oldLogDB := model.LOG_DB
	oldSQLitePath := common.SQLitePath
	oldIsMasterNode := common.IsMasterNode
	oldUsingSQLite := common.UsingSQLite
	oldUsingMySQL := common.UsingMySQL
	oldUsingPostgreSQL := common.UsingPostgreSQL
	oldMemoryCache := common.MemoryCacheEnabled
	oldRedisEnabled := common.RedisEnabled
	oldImage2SmartRoutingEnabled := common.Image2SmartRoutingEnabled
	oldImage2EditsSmartRoutingEnabled := common.Image2EditsSmartRoutingEnabled
	oldErrorLogEnabled := constant.ErrorLogEnabled
	oldSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")
	t.Cleanup(func() {
		if db := model.DB; db != nil && db != oldDB {
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		model.DB = oldDB
		model.LOG_DB = oldLogDB
		common.SQLitePath = oldSQLitePath
		common.IsMasterNode = oldIsMasterNode
		common.UsingSQLite = oldUsingSQLite
		common.UsingMySQL = oldUsingMySQL
		common.UsingPostgreSQL = oldUsingPostgreSQL
		common.MemoryCacheEnabled = oldMemoryCache
		common.RedisEnabled = oldRedisEnabled
		common.Image2SmartRoutingEnabled = oldImage2SmartRoutingEnabled
		common.Image2EditsSmartRoutingEnabled = oldImage2EditsSmartRoutingEnabled
		constant.ErrorLogEnabled = oldErrorLogEnabled
		if hadSQLDSN {
			_ = os.Setenv("SQL_DSN", oldSQLDSN)
		} else {
			_ = os.Unsetenv("SQL_DSN")
		}
	})

	common.SQLitePath = fmt.Sprintf("file:image2_pre_route_%d?mode=memory&cache=shared", time.Now().UnixNano())
	common.IsMasterNode = false
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	constant.ErrorLogEnabled = true
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}, &model.Log{}))

	channel := model.Channel{Id: 77, Key: "test-key", Status: common.ChannelStatusEnabled}
	channel.SetSetting(dto.ChannelSettings{Image2Capability: &dto.Image2ChannelCapability{
		Enabled: true, Operations: []string{"generations"}, Resolutions: []string{"1024"},
	}})
	require.NoError(t, model.DB.Create(&channel).Error)
	priority := int64(1)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "image2-test", Model: "gpt-image-2", ChannelId: channel.Id,
		Enabled: true, Priority: &priority,
	}).Error)

	common.Image2SmartRoutingEnabled = true
	common.Image2EditsSmartRoutingEnabled = true
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{"prompt":"prompt secret","image":"image secret","key":"key secret"}`))
	ctx.Set(common.RequestIdKey, "req-image2-empty-no-token")
	ctx.Set("id", 42)
	ctx.Set("username", "image2-test-user")
	ctx.Set("token_name", "must-not-persist")
	ctx.Set("token_id", 9876)
	common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "image2-test")
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2",
		RelayMode:       relayconstant.RelayModeImagesEdits,
		UsingGroup:      "image2-test",
		TokenGroup:      "image2-test",
	}
	router, err := service.NewImage2SmartRouter(ctx, info, &dto.ImageRequest{Size: "1024x1024"})
	require.NoError(t, err)
	require.NotNil(t, router)
	require.Equal(t, 0, router.CandidateCount())

	routeErr := types.NewError(fmt.Errorf("no compatible Image2 channel remains"), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	recordImage2PreRouteError(ctx, info, router, routeErr)

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "req-image2-empty-no-token").First(&row).Error)
	require.Equal(t, model.LogTypeError, row.Type)
	require.Equal(t, "req-image2-empty-no-token", row.RequestId)
	require.Equal(t, "", row.TokenName)
	require.Zero(t, row.TokenId)
	require.Equal(t, 0, row.ChannelId)
	require.Equal(t, 0, row.Quota)
	require.Equal(t, "gpt-image-2", row.ModelName)
	require.Equal(t, "image2-test", row.Group)

	lowerOther := strings.ToLower(row.Other)
	for _, forbidden := range []string{"prompt", "image", "body", "key", "token", "token_name", "token_id"} {
		require.NotContains(t, lowerOther, `"`+forbidden+`"`, "other must not persist %s", forbidden)
	}
	require.NotContains(t, lowerOther, "prompt secret")
	require.NotContains(t, lowerOther, "image secret")
	require.NotContains(t, lowerOther, "key secret")
}
