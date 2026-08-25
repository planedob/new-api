package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func relayErrorLogTestContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	c.Set("id", 4242)
	c.Set("username", "relay-error-user")
	c.Set("token_id", 77)
	c.Set("token_name", "relay-error-token")
	c.Set("group", "gpt-image-2")
	c.Set("original_model", "gpt-image-2")
	c.Set(common.RequestIdKey, "relay-error-request-id")
	c.Set("use_channel", []string{})
	return c
}

func TestRecordRelayErrorLogPersistsPreUpstreamFailure(t *testing.T) {
	truncate(t)
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })

	c := relayErrorLogTestContext()
	relayErr := types.NewErrorWithStatusCode(
		errors.New("provider-secret-response: unsupported Image2 request configuration"),
		types.ErrorCodeUnsupportedImageConfiguration,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
	recorded := RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage: "channel_selection",
		Extra: map[string]interface{}{
			"image2_request_capability":  "operation=generations resolution=2048 quality=high n=1",
			"image2_candidate_decisions": "44:quality_unsupported,32:resolution_unsupported",
			"token_name":                 "token-name-from-context",
			"provider_body":              "provider-secret-response",
		},
	})
	require.True(t, recorded)
	require.True(t, WasRelayErrorLogged(c, relayErr))

	var rows []model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "relay-error-request-id").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, model.LogTypeError, rows[0].Type)
	require.Equal(t, 0, rows[0].ChannelId)
	require.Equal(t, "gpt-image-2", rows[0].ModelName)
	require.Equal(t, "gpt-image-2", rows[0].Group)
	require.Zero(t, rows[0].Quota)
	require.Empty(t, rows[0].TokenName)
	require.Zero(t, rows[0].TokenId)
	require.NotContains(t, rows[0].Content, "provider-secret-response")

	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Other, &other))
	require.Equal(t, "channel_selection", other["error_stage"])
	require.Equal(t, false, other["upstream_called"])
	require.Equal(t, string(types.ErrorCodeUnsupportedImageConfiguration), other["error_code"])
	require.Equal(t, "44:quality_unsupported,32:resolution_unsupported", other["image2_candidate_decisions"])
	require.NotContains(t, rows[0].Other, "token-name-from-context")
	require.NotContains(t, rows[0].Other, "provider-secret-response")
}

func TestRecordRelayErrorLogMarksUpstreamCallExplicitly(t *testing.T) {
	truncate(t)
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })

	c := relayErrorLogTestContext()
	relayErr := types.NewErrorWithStatusCode(errors.New("upstream rejected request"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	recorded := RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage:          "upstream",
		UpstreamCalled: true,
		Channel:        &types.ChannelError{ChannelId: 19, ChannelType: constant.ChannelTypeOpenAI, ChannelName: "test-provider"},
	})
	require.True(t, recorded)

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "relay-error-request-id").First(&row).Error)
	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	require.Equal(t, true, other["upstream_called"])
	require.Equal(t, float64(19), other["channel_id"])
	require.Equal(t, "upstream_server", other["error_class"])
}

func TestRecordRelayErrorLogStoresOnlyAllowlistedImage2ResponseFailure(t *testing.T) {
	truncate(t)
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })

	c := relayErrorLogTestContext()
	relayErr := types.NewErrorWithStatusCode(
		errors.New("provider body and image material must not be persisted"),
		types.ErrorCodeUpstreamImageMissing,
		http.StatusBadGateway,
	)
	recorded := RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage:                 "upstream",
		UpstreamCalled:        true,
		Image2ResponseFailure: "text_only",
	})
	require.True(t, recorded)

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "relay-error-request-id").First(&row).Error)
	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	require.Equal(t, "upstream_response", other["failure_scope"])
	require.Equal(t, "text_only", other["image2_response_failure"])
	require.Equal(t, "images", other["request_route"])
	require.NotContains(t, other, "request_path")
	require.NotContains(t, row.Content, "provider body")
	require.NotContains(t, row.Other, "image material")

	SetImage2ResponseFailure(c, "https://supplier.invalid/?token=secret")
	require.Empty(t, Image2ResponseFailure(c))
}

func TestRecordRelayErrorLogStoresOnlySafeProviderClassification(t *testing.T) {
	truncate(t)
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })

	c := relayErrorLogTestContext()
	relayErr := types.WithOpenAIError(types.OpenAIError{
		Message: "prompt=customer-secret token=provider-secret",
		Type:    "server_error",
		Code:    "capacity_exhausted",
	}, http.StatusBadGateway)
	require.True(t, RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage:          "upstream",
		UpstreamCalled: true,
	}))

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "relay-error-request-id").First(&row).Error)
	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	require.Equal(t, "server_error", other["provider_error_type"])
	require.Equal(t, "capacity_exhausted", other["provider_error_code"])
	require.Equal(t, "upstream_server", other["error_class"])
	require.NotContains(t, row.Content, "customer-secret")
	require.NotContains(t, row.Other, "customer-secret")
	require.NotContains(t, row.Other, "provider-secret")

	unsafe := types.WithOpenAIError(types.OpenAIError{
		Message: "do not persist",
		Type:    "https://supplier.example/error?token=secret",
		Code:    "bad code with secret data",
	}, http.StatusBadGateway)
	require.Equal(t, "", safeRelayErrorClassificationToken(unsafe.RelayError.(types.OpenAIError).Type))
	require.Equal(t, "", safeRelayErrorClassificationToken(unsafe.RelayError.(types.OpenAIError).Code.(string)))
}

func TestRecordRelayErrorLogRespectsNoRecordOption(t *testing.T) {
	truncate(t)
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })

	c := relayErrorLogTestContext()
	relayErr := types.NewErrorWithStatusCode(
		errors.New("quota failure"),
		types.ErrorCodeInsufficientUserQuota,
		http.StatusForbidden,
		types.ErrOptionWithNoRecordErrorLog(),
	)
	require.False(t, RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{Stage: "pre_consume"}))

	var count int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&count).Error)
	require.Zero(t, count)
}
