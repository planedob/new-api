package service

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const relayErrorLogRecordedKey = "relay_error_log_recorded_error"

// RelayErrorLogOptions describes where a relay request failed. Channel is nil
// when the request never reached an upstream; such failures are still useful
// operational evidence and are recorded with channel_id=0.
type RelayErrorLogOptions struct {
	Stage     string
	Channel   *types.ChannelError
	ModelName string
	Group     string
	Extra     map[string]interface{}
}

// RecordRelayErrorLog persists both channel-attempt errors and failures that
// happen before any upstream is selected. It deliberately respects the
// existing global log switch and per-error no-record option.
func RecordRelayErrorLog(c *gin.Context, err *types.NewAPIError, options RelayErrorLogOptions) bool {
	if c == nil || err == nil || !constant.ErrorLogEnabled || !types.IsRecordErrorLog(err) {
		return false
	}

	userID := c.GetInt("id")
	tokenName := c.GetString("token_name")
	modelName := options.ModelName
	if modelName == "" {
		modelName = c.GetString("original_model")
	}
	tokenID := c.GetInt("token_id")
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
	for key, value := range options.Extra {
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
		tokenName,
		err.MaskSensitiveErrorWithStatusCode(),
		tokenID,
		useTimeSeconds,
		common.GetContextKeyBool(c, constant.ContextKeyIsStream),
		group,
		other,
	)
	c.Set(relayErrorLogRecordedKey, err)
	return true
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
