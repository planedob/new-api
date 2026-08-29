package service

import (
	"bytes"
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

func TestBuildRelayErrorEventJoinsRequestAndLifecycleSafely(t *testing.T) {
	c := relayErrorLogTestContext()
	accepted := true
	responseWritten := false
	retry := false
	err := types.NewErrorWithStatusCode(
		errors.New("provider-secret-response must not enter application logs"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	event := buildRelayErrorEvent(c, 4242, 19, "gpt-image-2", "image2-test", err, map[string]interface{}{
		"upstream_called":         true,
		"upstream_accepted_known": true,
		"upstream_accepted":       accepted,
		"response_written_known":  true,
		"response_written":        responseWritten,
		"retry_known":             true,
		"retry":                   retry,
		"billing_state":           RelayErrorBillingPreConsumed,
		"charge_known":            true,
		"charged":                 false,
		"request_path":            "/v1/images/edits?token=must-not-enter-log",
		"provider_body":           "provider-secret-response",
	}, true)

	require.Equal(t, relayErrorEventName, event["event"])
	require.Equal(t, true, event["request_id_known"])
	require.Equal(t, "relay-error-request-id", event["request_id"])
	require.Equal(t, "upstream_server", event["error_class"])
	require.Equal(t, true, event["upstream_called"])
	require.Equal(t, true, event["upstream_accepted"])
	require.Equal(t, false, event["response_written"])
	require.Equal(t, false, event["retry"])
	require.Equal(t, "pre_consumed", event["billing_state"])
	require.Equal(t, false, event["charged"])
	require.NotContains(t, event, "request_path")
	require.NotContains(t, event, "provider_body")
	require.NotContains(t, common.MapToJsonStr(event), "provider-secret-response")
	require.NotContains(t, common.MapToJsonStr(event), "token=must-not-enter-log")
}

func TestBuildRelayErrorEventRejectsProviderControlledClassification(t *testing.T) {
	c := relayErrorLogTestContext()
	err := types.WithOpenAIError(types.OpenAIError{
		Message: "provider response must not enter application logs",
		Type:    "server_error",
		Code:    "https://provider.invalid/secret?token=must-not-enter-log",
	}, http.StatusBadGateway)
	event := buildRelayErrorEvent(c, 4242, 19, "gpt-image-2", "image2-test", err, nil, true)
	serialized := common.MapToJsonStr(event)

	require.NotContains(t, event, "error_code")
	require.NotContains(t, serialized, "provider.invalid")
	require.NotContains(t, serialized, "token=must-not-enter-log")
	require.Equal(t, "openai_error", event["error_type"])
}

func TestRelayErrorLogSummaryDoesNotIncludeProviderMaterial(t *testing.T) {
	err := types.WithOpenAIError(types.OpenAIError{
		Message: "provider body must not enter direct application logs",
		Type:    "server_error",
		Code:    "https://provider.invalid/error?token=must-not-enter-log",
	}, http.StatusBadGateway)

	summary := RelayErrorLogSummary(err)
	require.Contains(t, summary, "status_code=502")
	require.Contains(t, summary, "error_class=upstream_server")
	require.NotContains(t, summary, "provider.invalid")
	require.NotContains(t, summary, "must-not-enter-log")
}

func TestRecordRelayErrorLogRedactsProviderControlledCodeEndToEnd(t *testing.T) {
	truncate(t)
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })

	c := relayErrorLogTestContext()
	relayErr := types.WithOpenAIError(types.OpenAIError{
		Message: "provider response must not enter application logs",
		Type:    "server_error",
		Code:    "https://provider.invalid/secret?token=must-not-enter-log",
	}, http.StatusBadGateway)
	require.True(t, RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage:          "upstream",
		UpstreamCalled: true,
	}))

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "relay-error-request-id").First(&row).Error)
	require.Contains(t, row.Content, "error_code=unclassified_error")
	require.NotContains(t, row.Content, "provider.invalid")
	require.NotContains(t, row.Content, "token=must-not-enter-log")
	require.NotContains(t, row.Other, "provider.invalid")
	require.NotContains(t, row.Other, "token=must-not-enter-log")

	var other map[string]interface{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	require.Equal(t, "unclassified_error", other["error_code"])
	require.NotContains(t, other, "provider_error_code")
}

func TestRecordRelayErrorLogRedactsUntrustedDimensionsEndToEnd(t *testing.T) {
	truncate(t)
	previousErrorLog := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previousErrorLog })

	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultWriter
	previousErrorWriter := gin.DefaultErrorWriter
	gin.DefaultWriter = &output
	gin.DefaultErrorWriter = &output
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter = previousWriter
		gin.DefaultErrorWriter = previousErrorWriter
		common.LogWriterMu.Unlock()
	})

	c := relayErrorLogTestContext()
	c.Set(ginKeyChannelAffinityLogInfo, map[string]interface{}{
		"rule_name":    "affinity-rule",
		"model":        "https://provider.invalid/model?token=must-not-enter-log",
		"request_path": "/v1/images/generations/provider-secret",
		"key_hint":     "provider-secret",
		"key_fp":       "safe-fp",
		"override_template": map[string]interface{}{
			"applied":             true,
			"param_override_keys": 1,
		},
	})
	relayErr := types.NewErrorWithStatusCode(
		errors.New("provider body https://provider.invalid/secret?token=must-not-enter-log"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	require.True(t, RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage:     "upstream",
		ModelName: "sk-provider-secret-0123456789abcdef",
		Group:     "https://provider.invalid/group?token=must-not-enter-log",
		Extra: map[string]interface{}{
			"requested_model":            "AIzaSy-provider-secret-0123456789abcdef",
			"selection_group":            "group-safe",
			"image2_candidate_decisions": "44:quality_unsupported,32:resolution_unsupported",
			"image2_request_capability":  "operation=generations resolution=2048 quality=high n=1",
		},
	}))

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "relay-error-request-id").First(&row).Error)
	require.Empty(t, row.ModelName)
	require.Empty(t, row.Group)
	require.NotContains(t, row.Content, "provider.invalid")
	require.NotContains(t, row.Other, "provider.invalid")
	require.NotContains(t, row.Other, "provider-secret")
	require.NotContains(t, output.String(), "provider.invalid")
	require.NotContains(t, output.String(), "provider-secret")

	var other map[string]interface{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	require.Equal(t, "group-safe", other["selection_group"])
	require.Equal(t, "44:quality_unsupported,32:resolution_unsupported", other["image2_candidate_decisions"])
	require.NotContains(t, other, "requested_model")
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

func TestRecordRelayErrorLogPersistsOnlyNormalizedLifecycleStates(t *testing.T) {
	truncate(t)
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })

	c := relayErrorLogTestContext()
	accepted := true
	responseWritten := false
	retry := true
	relayErr := types.NewErrorWithStatusCode(errors.New("provider body must not be stored"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	require.True(t, RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage:            "upstream",
		UpstreamCalled:   true,
		UpstreamAccepted: &accepted,
		ResponseWritten:  &responseWritten,
		RetryDecision:    &retry,
		RetryIndex:       1,
		TaskState:        "failed",
		RefundState:      "pending",
		BillingState:     RelayErrorBillingPreConsumed,
	}))

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "relay-error-request-id").First(&row).Error)
	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	require.Equal(t, "accepted", other["upstream_state"])
	require.Equal(t, true, other["upstream_accepted_known"])
	require.Equal(t, true, other["upstream_accepted"])
	require.Equal(t, true, other["response_written_known"])
	require.Equal(t, false, other["response_written"])
	require.Equal(t, true, other["retry_known"])
	require.Equal(t, true, other["retry"])
	require.Equal(t, float64(1), other["retry_index"])
	require.Equal(t, "failed", other["task_state"])
	require.Equal(t, "pending", other["refund_state"])
	require.Equal(t, "pre_consumed", other["billing_state"])
	require.Equal(t, false, other["charge_known"])
	require.NotContains(t, row.Other, "provider body")

	require.Equal(t, "unknown", normalizeRelayTaskState("https://supplier.invalid/task?token=secret"))
	require.Equal(t, "unknown", normalizeRelayRefundState("refund secret body"))
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
