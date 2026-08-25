package service

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

var relayErrorClassificationToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

const (
	relayErrorLogRecordedKey          = "relay_error_log_recorded_error"
	relayErrorLogRecordedAnyKey       = "relay_error_log_recorded_any"
	relayErrorLogPersistenceFailedKey = "relay_error_log_persistence_failed"
	relayErrorEventVersion            = 1
	maxRelayErrorLogDimensionRunes    = 128
	maxRelayErrorLogExtraRunes        = 512
)

type RelayErrorBillingState string

const (
	RelayErrorBillingUnknown       RelayErrorBillingState = "unknown"
	RelayErrorBillingNotStarted    RelayErrorBillingState = "not_started"
	RelayErrorBillingNotApplicable RelayErrorBillingState = "not_applicable"
	RelayErrorBillingPreConsumed   RelayErrorBillingState = "pre_consumed"
)

// RelayErrorLogOptions describes where a relay request failed. Channel is nil
// when the request never reached an upstream; such failures are still useful
// operational evidence and are recorded with channel_id=0.
type RelayErrorLogOptions struct {
	Stage          string
	Channel        *types.ChannelError
	ModelName      string
	Group          string
	UpstreamCalled bool
	BillingState   RelayErrorBillingState
	Charged        *bool
	Image2         *Image2RequestCapability
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
		if _, ok := relayErrorLogExtraAllowlist[key]; !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			safe[key] = boundedRelayErrorLogText(typed, maxRelayErrorLogExtraRunes)
		case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			safe[key] = typed
		case float32:
			if !math.IsNaN(float64(typed)) && !math.IsInf(float64(typed), 0) {
				safe[key] = typed
			}
		case float64:
			if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
				safe[key] = typed
			}
		}
	}
	return safe
}

func boundedRelayErrorLogText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(value))
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes]) + "...[truncated]"
}

func safeRelayErrorClassificationToken(value string) string {
	value = strings.TrimSpace(value)
	if !relayErrorClassificationToken.MatchString(value) {
		return ""
	}
	return value
}

func relayErrorClass(err *types.NewAPIError) string {
	if err == nil {
		return "unknown"
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeDoRequestFailed:
		return "transport"
	case types.ErrorCodeUpstreamImageMissing, types.ErrorCodeUpstreamImageInvalid:
		return "upstream_output"
	case types.ErrorCodePromptBlocked:
		return "content_safety"
	}
	switch err.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "upstream_auth"
	case http.StatusNotFound:
		return "upstream_not_found"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "upstream_timeout"
	case http.StatusTooManyRequests:
		return "upstream_rate_limit"
	}
	if err.StatusCode >= 500 && err.StatusCode <= 599 {
		return "upstream_server"
	}
	if err.StatusCode >= 400 && err.StatusCode <= 499 {
		return "request_rejected"
	}
	return "unknown"
}

func safeProviderErrorDimensions(err *types.NewAPIError) (string, string) {
	if err == nil {
		return "", ""
	}
	var errorType, errorCode string
	switch relayErr := err.RelayError.(type) {
	case types.OpenAIError:
		errorType = safeRelayErrorClassificationToken(relayErr.Type)
		if code, ok := relayErr.Code.(string); ok {
			errorCode = safeRelayErrorClassificationToken(code)
		}
	case types.ClaudeError:
		errorType = safeRelayErrorClassificationToken(relayErr.Type)
	}
	if errorType == "" {
		errorType = safeRelayErrorClassificationToken(string(err.GetErrorType()))
	}
	if errorCode == "" {
		errorCode = safeRelayErrorClassificationToken(string(err.GetErrorCode()))
	}
	return errorType, errorCode
}

func normalizeRelayErrorBillingState(state RelayErrorBillingState) RelayErrorBillingState {
	switch state {
	case RelayErrorBillingNotStarted, RelayErrorBillingNotApplicable, RelayErrorBillingPreConsumed:
		return state
	default:
		return RelayErrorBillingUnknown
	}
}

// RecordRelayErrorLog persists both channel-attempt errors and failures that
// happen before any upstream is selected. It deliberately respects the
// existing global log switch and per-error no-record option.
func RecordRelayErrorLog(c *gin.Context, err *types.NewAPIError, options RelayErrorLogOptions) bool {
	if c == nil || err == nil {
		return false
	}
	if options.Image2 != nil && options.Stage == "channel_selection" && options.Channel == nil {
		ObserveImage2PreRouteFailure(c, *options.Image2, err.StatusCode, err.GetErrorCode())
	}
	if !constant.ErrorLogEnabled || !types.IsRecordErrorLog(err) {
		return false
	}

	userID := c.GetInt("id")
	modelName := options.ModelName
	if modelName == "" {
		modelName = c.GetString("original_model")
	}
	modelName = boundedRelayErrorLogText(modelName, maxRelayErrorLogDimensionRunes)
	group := options.Group
	if group == "" {
		group = c.GetString("group")
	}
	group = boundedRelayErrorLogText(group, maxRelayErrorLogDimensionRunes)

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

	other := make(map[string]interface{}, len(options.Extra)+16)
	// Image2 pre-route evidence intentionally omits the URL because query
	// strings can carry client material. Existing non-Image2 diagnostics keep
	// their established request_path field.
	if options.Image2 == nil && c.Request != nil && c.Request.URL != nil {
		other["request_path"] = c.Request.URL.Path
	}
	other["event_version"] = relayErrorEventVersion
	other["error_stage"] = options.Stage
	other["error_type"] = err.GetErrorType()
	other["error_code"] = err.GetErrorCode()
	other["error_class"] = relayErrorClass(err)
	providerErrorType, providerErrorCode := safeProviderErrorDimensions(err)
	if providerErrorType != "" {
		other["provider_error_type"] = providerErrorType
	}
	if providerErrorCode != "" {
		other["provider_error_code"] = providerErrorCode
	}
	other["status_code"] = err.StatusCode
	other["channel_id"] = channelID
	other["channel_name"] = channelName
	other["channel_type"] = channelType
	// This is explicit rather than inferred from channel_id: a selected channel
	// can still fail before its adapter sends anything upstream.
	other["upstream_called"] = options.UpstreamCalled
	if options.Image2 != nil {
		other["failure_scope"] = "pre_upstream"
		other["billing_state"] = normalizeRelayErrorBillingState(options.BillingState)
		other["request_route"] = "images"
		other["request_method"] = requestMethod(c)
		other["elapsed_ms"] = relayPassiveElapsed(c).Milliseconds()
		if options.Charged != nil {
			other["charge_known"] = true
			other["charged"] = *options.Charged
		} else if options.BillingState == RelayErrorBillingNotStarted || options.BillingState == RelayErrorBillingNotApplicable {
			other["charge_known"] = true
			other["charged"] = false
		} else {
			other["charge_known"] = false
		}
	}
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
	if options.Image2 != nil {
		adminInfo["image2_request_capability"] = image2CapabilitySummary(*options.Image2)
	}
	AppendChannelAffinityAdminInfo(c, adminInfo)
	other["admin_info"] = adminInfo

	startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startTime.IsZero() {
		startTime = time.Now()
	}
	useTimeSeconds := int(time.Since(startTime).Seconds())
	if persistErr := model.RecordErrorLogWithResult(
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
	); persistErr != nil {
		c.Set(relayErrorLogPersistenceFailedKey, true)
		return false
	}
	c.Set(relayErrorLogRecordedKey, err)
	c.Set(relayErrorLogRecordedAnyKey, true)
	return true
}

func requestMethod(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return boundedRelayErrorLogText(c.Request.Method, 16)
}

func image2CapabilitySummary(capability Image2RequestCapability) string {
	return fmt.Sprintf("operation=%s resolution=%s quality=%s n=%d",
		boundedRelayErrorLogText(capability.Operation, 32),
		boundedRelayErrorLogText(capability.Resolution, 32),
		boundedRelayErrorLogText(capability.Quality, 32),
		capability.N,
	)
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

// RelayErrorLogPersistenceFailed lets callers surface a fail-closed
// observability decision without exposing the database error itself.
func RelayErrorLogPersistenceFailed(c *gin.Context) bool {
	return c != nil && c.GetBool(relayErrorLogPersistenceFailedKey)
}
