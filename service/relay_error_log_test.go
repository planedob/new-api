package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
		errors.New("unsupported Image2 request configuration"),
		types.ErrorCodeUnsupportedImageConfiguration,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
	recorded := RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage: "channel_selection",
		Extra: map[string]interface{}{
			"image2_request_capability":  "operation=generations resolution=2048 quality=high n=1",
			"image2_candidate_decisions": "44:quality_unsupported,32:resolution_unsupported",
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

	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].Other, &other))
	require.Equal(t, "channel_selection", other["error_stage"])
	require.Equal(t, string(types.ErrorCodeUnsupportedImageConfiguration), other["error_code"])
	require.Equal(t, "44:quality_unsupported,32:resolution_unsupported", other["image2_candidate_decisions"])
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

func TestRecordRelayErrorLogSanitizesSensitiveContentAndExtra(t *testing.T) {
	truncate(t)
	previous := constant.ErrorLogEnabled
	constant.ErrorLogEnabled = true
	t.Cleanup(func() { constant.ErrorLogEnabled = previous })

	c := relayErrorLogTestContext()
	secret := "sk-test-sensitive-value"
	relayErr := types.NewErrorWithStatusCode(
		errors.New("upstream rejected Authorization: Bearer "+secret+"\napi_key=AIzaSensitive\x00"),
		types.ErrorCodeChannelResponseTimeExceeded,
		http.StatusBadGateway,
	)
	require.True(t, RecordRelayErrorLog(c, relayErr, RelayErrorLogOptions{
		Stage: "channel_selection",
		Extra: map[string]interface{}{
			"upstream_detail": "token=" + secret + "\r\nnext",
		},
	}))

	var row model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", "relay-error-request-id").First(&row).Error)
	require.NotContains(t, row.Content, secret)
	require.NotContains(t, row.Content, "AIzaSensitive")
	require.NotContains(t, row.Content, "\n")
	require.NotContains(t, row.Content, "\r")
	require.NotContains(t, row.Content, "\x00")

	other := map[string]interface{}{}
	require.NoError(t, common.UnmarshalJsonStr(row.Other, &other))
	otherText := common.MapToJsonStr(other)
	require.NotContains(t, otherText, secret)
	require.NotContains(t, otherText, "\n")
	require.NotContains(t, otherText, "\r")
}

func TestSanitizeRelayErrorLogTextTruncatesLongUntrustedInput(t *testing.T) {
	result := sanitizeRelayErrorLogText(strings.Repeat("x", maxRelayErrorLogTextLength+1))
	require.Equal(t, maxRelayErrorLogTextLength+len("...[truncated]"), len(result))
	require.True(t, strings.HasSuffix(result, "...[truncated]"))
}
