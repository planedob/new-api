package service

import (
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const relayErrorLogRecordedKey = "relay_error_log_recorded_error"
const relayErrorLogRecordedAnyKey = "relay_error_log_recorded_any"

const maxRelayErrorLogTextLength = 4096

var (
	relayErrorLogBearerPattern        = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)
	relayErrorLogAuthorizationPattern = regexp.MustCompile(`(?i)\bauthorization\s*:\s*[^\s,;]+`)
	relayErrorLogAPIKeyPattern        = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password)\s*[:=]\s*[^\s,;]+`)
	relayErrorLogSKPattern            = regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9._-]*`)
)

// SanitizeRelayErrorLogText is intentionally applied immediately before an
// error reaches persistent storage or the error-log logger. Upstream errors
// are untrusted input: they can include credentials and control characters.
func SanitizeRelayErrorLogText(text string) string {
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
	text = common.MaskSensitiveInfo(text)
	text = relayErrorLogAuthorizationPattern.ReplaceAllString(text, "Authorization: ***")
	text = relayErrorLogBearerPattern.ReplaceAllString(text, "Bearer ***")
	text = relayErrorLogAPIKeyPattern.ReplaceAllStringFunc(text, func(match string) string {
		key := strings.SplitN(match, ":", 2)[0]
		if strings.Contains(match, "=") && !strings.Contains(match, ":") {
			key = strings.SplitN(match, "=", 2)[0]
		}
		return key + ":***"
	})
	text = relayErrorLogSKPattern.ReplaceAllString(text, "sk-***")
	if len(text) > maxRelayErrorLogTextLength {
		return text[:maxRelayErrorLogTextLength] + "...[truncated]"
	}
	return text
}

func sanitizeRelayErrorLogValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case string:
		return SanitizeRelayErrorLogText(typed)
	case []string:
		result := make([]string, len(typed))
		for i, item := range typed {
			result[i] = SanitizeRelayErrorLogText(item)
		}
		return result
	case map[string]string:
		result := make(map[string]string, len(typed))
		for key, item := range typed {
			result[key] = SanitizeRelayErrorLogText(item)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for i, item := range typed {
			result[i] = sanitizeRelayErrorLogValue(item)
		}
		return result
	case map[string]interface{}:
		return sanitizeRelayErrorLogFields(typed)
	default:
		return value
	}
}

func sanitizeRelayErrorLogFields(fields map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		result[key] = sanitizeRelayErrorLogValue(value)
	}
	return result
}

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
		other[key] = sanitizeRelayErrorLogValue(value)
	}

	adminInfo := map[string]interface{}{
		"use_channel": c.GetStringSlice("use_channel"),
	}
	if isMultiKey {
		adminInfo["is_multi_key"] = true
		adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	}
	AppendChannelAffinityAdminInfo(c, adminInfo)
	other["admin_info"] = sanitizeRelayErrorLogValue(adminInfo)

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
		SanitizeRelayErrorLogText(err.MaskSensitiveErrorWithStatusCode()),
		tokenID,
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
