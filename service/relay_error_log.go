package service

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const relayErrorLogRecordedKey = "relay_error_log_recorded_error"
const relayErrorLogRecordedAnyKey = "relay_error_log_recorded_any"

// RelayErrorLogOptions describes where a relay request failed. Channel is nil
// when the request never reached an upstream; such failures are still useful
// operational evidence and are recorded with channel_id=0.
type RelayErrorLogOptions struct {
	Stage          string
	Channel        *types.ChannelError
	ModelName      string
	Group          string
	UpstreamCalled bool
	Extra          map[string]interface{}
}

var relayErrorLogExtraAllowlist = map[string]struct{}{
	"fallback_audit":             {},
	"image2_candidate_decisions": {},
	"image2_request_capability":  {},
	"relay_kind":                 {},
	"requested_model":            {},
	"response_status":            {},
	"selection_group":            {},
}

func safeRelayErrorLogExtra(extra map[string]interface{}) map[string]interface{} {
	if len(extra) == 0 {
		return nil
	}
	safe := make(map[string]interface{}, len(extra))
	for key, value := range extra {
		if _, ok := relayErrorLogExtraAllowlist[key]; ok {
			safe[key] = value
		}
	}
	return safe
}

// RecordRelayErrorLog persists both channel-attempt errors and failures that
// happen before any upstream is selected. It deliberately respects the
// existing global log switch and per-error no-record option.
func RecordRelayErrorLog(c *gin.Context, err *types.NewAPIError, options RelayErrorLogOptions) bool {
	if c == nil || err == nil || !constant.ErrorLogEnabled || !types.IsRecordErrorLog(err) {
		return false
	}

	userID := c.GetInt("id")
	modelName := options.ModelName
	if modelName == "" {
		modelName = c.GetString("original_model")
	}
	group := options.Group
	if group == "" {
		group = c.GetString("group")
	}

	channelID := 0
	channelName := ""
	channelType := 0
	isMultiKey := false
	if options.Channel != nil {
		channelID = options.Channel.ChannelId
		channelName = options.Channel.ChannelName
		channelType = options.Channel.ChannelType
		isMultiKey = options.Channel.IsMultiKey
	}

	other := make(map[string]interface{}, len(options.Extra)+8)
	if c.Request != nil && c.Request.URL != nil {
		other["request_path"] = c.Request.URL.Path
	}
	other["error_stage"] = options.Stage
	other["error_type"] = err.GetErrorType()
	other["error_code"] = err.GetErrorCode()
	other["status_code"] = err.StatusCode
	other["channel_id"] = channelID
	other["channel_name"] = channelName
	other["channel_type"] = channelType
	// This is explicit rather than inferred from channel_id: a selected channel
	// can still fail before its adapter sends anything upstream.
	other["upstream_called"] = options.UpstreamCalled
	for key, value := range safeRelayErrorLogExtra(options.Extra) {
		other[key] = value
	}

	adminInfo := map[string]interface{}{
		"use_channel": c.GetStringSlice("use_channel"),
	}
	if isMultiKey {
		adminInfo["is_multi_key"] = true
		adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	}
	AppendChannelAffinityAdminInfo(c, adminInfo)
	other["admin_info"] = adminInfo

	startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startTime.IsZero() {
		startTime = time.Now()
	}
	useTimeSeconds := int(time.Since(startTime).Seconds())
	model.RecordErrorLog(
		c,
		userID,
		channelID,
		modelName,
		// Error logs are operational diagnostics, not token-audit rows. Do not
		// persist token names or IDs here; consume logs retain those fields.
		"",
		// Persist only the stable error classification. Provider response bodies
		// can contain arbitrary sensitive content even after URL/key masking.
		fmt.Sprintf("error_code=%s status_code=%d", err.GetErrorCode(), err.StatusCode),
		0,
		useTimeSeconds,
		common.GetContextKeyBool(c, constant.ContextKeyIsStream),
		group,
		other,
	)
	c.Set(relayErrorLogRecordedKey, err)
	c.Set(relayErrorLogRecordedAnyKey, true)
	return true
}

// HasRelayErrorLog reports whether any structured error event has already
// been recorded for this request. It lets the response audit middleware fill
// genuine gaps without duplicating errors emitted by a richer code path.
func HasRelayErrorLog(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return c.GetBool(relayErrorLogRecordedAnyKey)
}

// WasRelayErrorLogged prevents the final response path from duplicating the
// same error already recorded for a concrete upstream attempt. A later,
// different selection error is intentionally recorded as a separate event.
func WasRelayErrorLogged(c *gin.Context, err *types.NewAPIError) bool {
	if c == nil || err == nil {
		return false
	}
	recorded, ok := c.Get(relayErrorLogRecordedKey)
	if !ok {
		return false
	}
	return recorded == err
}
