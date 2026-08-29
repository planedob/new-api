package service

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

var relayErrorClassificationToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// Relay diagnostics are classification-only. Reject clear URL, body and
// credential markers before values reach either the application log or the
// error-log table; length limiting alone is not a confidentiality boundary.
var relaySensitiveDiagnosticPattern = regexp.MustCompile(`(?i)(?:https?://|wss?://|data:[^\s,]+|-----begin|(?:api[_-]?key|access[_-]?token|authorization|bearer|cookie|password|passwd|secret|prompt|image|body)\s*[:=]|(?:^|[^a-z0-9])(sk-[a-z0-9_-]{16,}|AIza[a-z0-9_-]{20,}|gh[pousr]_[a-z0-9_]{20,}|xox[baprs]-[a-z0-9-]{20,})(?:$|[^a-z0-9]))`)

// A long compact scalar is not a useful diagnostic dimension and commonly
// represents a credential without a well-known provider prefix.
var relayOpaqueCredentialPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,}$`)

const (
	relayErrorLogRecordedKey          = "relay_error_log_recorded_error"
	relayErrorLogRecordedAnyKey       = "relay_error_log_recorded_any"
	relayErrorLogPersistenceFailedKey = "relay_error_log_persistence_failed"
	image2ResponseFailureContextKey   = "image2_upstream_response_failure"
	relayErrorEventVersion            = 1
	relayErrorEventName               = "relay_error_event"
	relayErrorUnclassifiedCode        = "unclassified_error"
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
	Stage                 string
	Channel               *types.ChannelError
	ModelName             string
	Group                 string
	UpstreamCalled        bool
	UpstreamAccepted      *bool
	ResponseWritten       *bool
	RetryDecision         *bool
	RetryIndex            int
	TaskState             string
	RefundState           string
	BillingState          RelayErrorBillingState
	Charged               *bool
	Image2                *Image2RequestCapability
	Image2ResponseFailure string
	Extra                 map[string]interface{}
}

func normalizeRelayTaskState(state string) string {
	switch state {
	case "not_applicable", "unknown", "not_created", "queued", "running", "succeeded", "failed", "cancelled":
		return state
	default:
		return "unknown"
	}
}

func normalizeRelayRefundState(state string) string {
	switch state {
	case "not_required", "pending", "completed", "failed", "unknown":
		return state
	default:
		return "unknown"
	}
}

var image2ResponseFailureAllowlist = map[string]struct{}{
	"invalid_json":       {},
	"text_only":          {},
	"empty_data":         {},
	"empty_image_fields": {},
	"invalid_schema":     {},
}

// SetImage2ResponseFailure stores only a fixed, non-sensitive classification.
// Provider bodies, image data, prompts, URLs, and credentials never enter the
// request context through this path.
func SetImage2ResponseFailure(c *gin.Context, classification string) {
	if c == nil {
		return
	}
	if _, ok := image2ResponseFailureAllowlist[classification]; !ok {
		classification = ""
	}
	c.Set(image2ResponseFailureContextKey, classification)
}

func Image2ResponseFailure(c *gin.Context) string {
	if c == nil {
		return ""
	}
	classification := c.GetString(image2ResponseFailureContextKey)
	if _, ok := image2ResponseFailureAllowlist[classification]; !ok {
		return ""
	}
	return classification
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
			if value := safeRelayDiagnosticText(typed, maxRelayErrorLogExtraRunes); value != "" {
				safe[key] = value
			}
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

var relayErrorEventFields = []string{
	"error_stage",
	"upstream_called",
	"upstream_accepted_known",
	"upstream_accepted",
	"upstream_state",
	"response_written_known",
	"response_written",
	"retry_known",
	"retry",
	"retry_index",
	"task_state",
	"billing_state",
	"charge_known",
	"charged",
	"refund_state",
	"failure_scope",
	"request_route",
	"request_method",
	"elapsed_ms",
	"image2_response_failure",
	"fallback_audit",
	"image2_candidate_decisions",
	"image2_request_capability",
	"relay_kind",
	"requested_model",
	"response_status",
	"selection_group",
}

// These are application-owned error codes. Provider response codes are not
// part of this set and must never be copied into the canonical error_code
// field, because an upstream can return arbitrary strings there.
var relayInternalErrorCodeAllowlist = map[types.ErrorCode]struct{}{
	types.ErrorCodeInvalidRequest:         {},
	types.ErrorCodeSensitiveWordsDetected: {},
	types.ErrorCodeViolationFeeGrokCSAM:   {},

	types.ErrorCodeCountTokenFailed:              {},
	types.ErrorCodeModelPriceError:               {},
	types.ErrorCodeInvalidApiType:                {},
	types.ErrorCodeJsonMarshalFailed:             {},
	types.ErrorCodeDoRequestFailed:               {},
	types.ErrorCodeGetChannelFailed:              {},
	types.ErrorCodeUnsupportedImageConfiguration: {},
	types.ErrorCodeGenRelayInfoFailed:            {},
	types.ErrorCodeUnhandledRelayFailure:         {},

	types.ErrorCodeChannelNoAvailableKey:        {},
	types.ErrorCodeChannelParamOverrideInvalid:  {},
	types.ErrorCodeChannelHeaderOverrideInvalid: {},
	types.ErrorCodeChannelModelMappedError:      {},
	types.ErrorCodeChannelAwsClientError:        {},
	types.ErrorCodeChannelInvalidKey:            {},
	types.ErrorCodeChannelResponseTimeExceeded:  {},

	types.ErrorCodeReadRequestBodyFailed: {},
	types.ErrorCodeConvertRequestFailed:  {},
	types.ErrorCodeAccessDenied:          {},

	types.ErrorCodeBadRequestBody:            {},
	types.ErrorCodeImageEditMissingBoundary:  {},
	types.ErrorCodeImageEditTruncatedBody:    {},
	types.ErrorCodeImageEditBodyUnavailable:  {},
	types.ErrorCodeImageEditMissingImage:     {},
	types.ErrorCodeImageEditEmptyImage:       {},
	types.ErrorCodeImageEditUnsupportedImage: {},
	types.ErrorCodeImageEditRequestTooLarge:  {},
	types.ErrorCodeImageEditMalformed:        {},

	types.ErrorCodeReadResponseBodyFailed: {},
	types.ErrorCodeBadResponseStatusCode:  {},
	types.ErrorCodeBadResponse:            {},
	types.ErrorCodeBadResponseBody:        {},
	types.ErrorCodeUpstreamImageInvalid:   {},
	types.ErrorCodeUpstreamImageMissing:   {},
	types.ErrorCodeEmptyResponse:          {},
	types.ErrorCodeAwsInvokeError:         {},
	types.ErrorCodeModelNotFound:          {},
	types.ErrorCodePromptBlocked:          {},

	types.ErrorCodeQueryDataError:  {},
	types.ErrorCodeUpdateDataError: {},

	types.ErrorCodeInsufficientUserQuota:      {},
	types.ErrorCodePreConsumeTokenQuotaFailed: {},
	types.ErrorCodeEntitlementRequired:        {},
	types.ErrorCodeEntitlementInactive:        {},
	types.ErrorCodeEntitlementDailyLimit:      {},
	types.ErrorCodeEntitlementTotalLimit:      {},
}

func safeRelayInternalErrorCode(code types.ErrorCode) string {
	if _, ok := relayInternalErrorCodeAllowlist[code]; !ok {
		return ""
	}
	return string(code)
}

// RelayErrorLogSummary is safe for direct application logging. It intentionally
// excludes NewAPIError.Error(), which may contain an upstream response body,
// prompt material, a URL, or a credential. Detailed lifecycle data is emitted
// separately by RecordRelayErrorLog when the existing error-log gate permits it.
func RelayErrorLogSummary(err *types.NewAPIError) string {
	if err == nil {
		return "relay error status_code=0 error_code=" + relayErrorUnclassifiedCode
	}
	errorCode := safeRelayInternalErrorCode(err.GetErrorCode())
	if errorCode == "" {
		errorCode = relayErrorUnclassifiedCode
	}
	return fmt.Sprintf("relay error status_code=%d error_code=%s error_class=%s", err.StatusCode, errorCode, relayErrorClass(err))
}

var relayInternalErrorTypeAllowlist = map[types.ErrorType]struct{}{
	types.ErrorTypeNewAPIError:     {},
	types.ErrorTypeOpenAIError:     {},
	types.ErrorTypeClaudeError:     {},
	types.ErrorTypeMidjourneyError: {},
	types.ErrorTypeGeminiError:     {},
	types.ErrorTypeRerankError:     {},
	types.ErrorTypeUpstreamError:   {},
}

func safeRelayInternalErrorType(errorType types.ErrorType) string {
	if _, ok := relayInternalErrorTypeAllowlist[errorType]; !ok {
		return ""
	}
	return string(errorType)
}

// SafeRelayErrorCode exposes only application-owned error codes to callers
// that need to return a diagnostic without copying a provider-controlled code.
func SafeRelayErrorCode(err *types.NewAPIError) string {
	if err == nil {
		return relayErrorUnclassifiedCode
	}
	if code := safeRelayInternalErrorCode(err.GetErrorCode()); code != "" {
		return code
	}
	return relayErrorUnclassifiedCode
}

func safeRelayDiagnosticText(value string, maxRunes int) string {
	value = boundedRelayErrorLogText(value, maxRunes)
	if value == "" || relaySensitiveDiagnosticPattern.MatchString(value) {
		return ""
	}
	if relayOpaqueCredentialPattern.MatchString(value) {
		return ""
	}
	return value
}

func safeRelayDimensionText(value string) string {
	return safeRelayDiagnosticText(value, maxRelayErrorLogDimensionRunes)
}

func safeRelayAdminInfo(info map[string]interface{}) map[string]interface{} {
	if len(info) == 0 {
		return nil
	}
	safe := make(map[string]interface{}, len(info))
	for key, value := range info {
		switch key {
		case "use_channel":
			ids, ok := value.([]string)
			if !ok {
				continue
			}
			safeIDs := make([]string, 0, len(ids))
			for _, id := range ids {
				if id != "" && relayChannelIDPattern.MatchString(id) {
					safeIDs = append(safeIDs, id)
				}
			}
			safe[key] = safeIDs
		case "is_multi_key", "local_count_tokens":
			if typed, ok := value.(bool); ok {
				safe[key] = typed
			}
		case "multi_key_index":
			if typed, ok := value.(int); ok {
				safe[key] = typed
			}
		case "channel_affinity":
			if typed, ok := value.(map[string]interface{}); ok {
				if affinity := safeRelayChannelAffinityInfo(typed); affinity != nil {
					safe[key] = affinity
				}
			}
		}
	}
	return safe
}

var relayChannelIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

func safeRelayChannelAffinityInfo(info map[string]interface{}) map[string]interface{} {
	if len(info) == 0 {
		return nil
	}
	safe := make(map[string]interface{}, len(info))
	for key, value := range info {
		switch typed := value.(type) {
		case string:
			// Do not persist the original request path or any key material. The
			// remaining labels are useful only after the same diagnostic boundary
			// as model/group values.
			if key == "request_path" || key == "key_hint" {
				continue
			}
			if value := safeRelayDimensionText(typed); value != "" {
				safe[key] = value
			}
		case int:
			if key == "channel_id" || key == "param_override_keys" {
				safe[key] = typed
			}
		case bool:
			if key == "applied" {
				safe[key] = typed
			}
		case map[string]interface{}:
			if key == "override_template" {
				if template := safeRelayChannelAffinityInfo(typed); template != nil {
					safe[key] = template
				}
			}
		}
	}
	return safe
}

var relayErrorStageAllowlist = map[string]struct{}{
	"authentication":     {},
	"distribution":       {},
	"request_middleware": {},
	"request_validation": {},
	"pre_consume":        {},
	"rate_limit":         {},
	"channel_selection":  {},
	"response_audit":     {},
	"upstream":           {},
	"relay_info":         {},
	"content_validation": {},
	"token_estimation":   {},
	"pricing":            {},
	"request_body":       {},
}

func safeRelayStage(stage string) string {
	if _, ok := relayErrorStageAllowlist[stage]; !ok {
		return "unknown"
	}
	return stage
}

// safeRelayErrorEventValue copies only scalar values from the already
// allowlisted error-log fields. The application log is an operational search
// surface, so it must not inherit arbitrary values from Extra or provider
// responses.
func safeRelayErrorEventValue(key string, value interface{}) (interface{}, bool) {
	switch typed := value.(type) {
	case string:
		switch key {
		case "error_stage":
			value := safeRelayStage(typed)
			return value, true
		case "upstream_state":
			if typed == "not_called" || typed == "called" || typed == "accepted" {
				return typed, true
			}
			return nil, false
		case "task_state":
			value := normalizeRelayTaskState(typed)
			return value, true
		case "refund_state":
			value := normalizeRelayRefundState(typed)
			return value, true
		case "billing_state":
			value := string(normalizeRelayErrorBillingState(RelayErrorBillingState(typed)))
			return value, true
		case "failure_scope":
			if typed == "pre_upstream" || typed == "upstream_response" {
				return typed, true
			}
			return nil, false
		case "request_route":
			if typed == "images" {
				return typed, true
			}
			return nil, false
		case "request_method":
			if typed == http.MethodGet || typed == http.MethodPost || typed == http.MethodPut || typed == http.MethodPatch || typed == http.MethodDelete {
				return typed, true
			}
			return nil, false
		case "image2_response_failure":
			if _, ok := image2ResponseFailureAllowlist[typed]; ok {
				return typed, true
			}
			return nil, false
		case "requested_model", "selection_group":
			value := safeRelayDimensionText(typed)
			return value, value != ""
		}
		value := safeRelayDiagnosticText(typed, maxRelayErrorLogExtraRunes)
		return value, value != ""
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return typed, true
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil, false
		}
		return typed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, false
		}
		return typed, true
	case RelayErrorBillingState:
		return string(normalizeRelayErrorBillingState(typed)), true
	default:
		return nil, false
	}
}

// buildRelayErrorEvent creates the stable, grep/JSON-friendly application log
// payload for an error row. It deliberately mirrors the persisted lifecycle
// fields and includes the same request ID that RecordErrorLogWithResult puts
// in model.Log.RequestId, making file logs and the admin error table
// joinable without storing prompts, images, URLs, tokens, cookies, or provider
// bodies.
func buildRelayErrorEvent(c *gin.Context, userID, channelID int, modelName, group string, err *types.NewAPIError, other map[string]interface{}, persisted bool) map[string]interface{} {
	event := map[string]interface{}{
		"event":         relayErrorEventName,
		"event_version": relayErrorEventVersion,
		"persisted":     persisted,
		"user_id":       userID,
		"channel_id":    channelID,
		"model":         safeRelayDimensionText(modelName),
		"group":         safeRelayDimensionText(group),
	}
	requestID := ""
	if c != nil {
		requestID = safeRelayErrorClassificationToken(c.GetString(common.RequestIdKey))
	}
	event["request_id_known"] = requestID != ""
	if requestID != "" {
		event["request_id"] = requestID
	}
	if err != nil {
		event["status_code"] = err.StatusCode
		if errorType := safeRelayInternalErrorType(err.GetErrorType()); errorType != "" {
			event["error_type"] = errorType
		}
		if errorCode := safeRelayInternalErrorCode(err.GetErrorCode()); errorCode != "" {
			event["error_code"] = errorCode
		}
		event["error_class"] = relayErrorClass(err)
	}
	for _, key := range relayErrorEventFields {
		value, ok := other[key]
		if !ok {
			continue
		}
		if safe, ok := safeRelayErrorEventValue(key, value); ok {
			event[key] = safe
		}
	}
	return event
}

func logRelayErrorEvent(c *gin.Context, userID, channelID int, modelName, group string, err *types.NewAPIError, other map[string]interface{}, persisted bool) {
	if c == nil || c.Request == nil {
		return
	}
	event := buildRelayErrorEvent(c, userID, channelID, modelName, group, err, other, persisted)
	ctx := c.Request.Context()
	// RequestId normally already lives in the request context, but keep the
	// join key intact for internal callers that only populated gin.Context.
	if requestID := c.GetString(common.RequestIdKey); requestID != "" && ctx.Value(common.RequestIdKey) == nil {
		ctx = context.WithValue(ctx, common.RequestIdKey, requestID)
	}
	logger.LogError(ctx, relayErrorEventName+"="+common.MapToJsonStr(event))
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
	modelName = safeRelayDimensionText(modelName)
	group := options.Group
	if group == "" {
		group = c.GetString("group")
	}
	group = safeRelayDimensionText(group)

	channelID := 0
	channelName := ""
	channelType := 0
	isMultiKey := false
	if options.Channel != nil {
		channelID = options.Channel.ChannelId
		channelName = safeRelayDimensionText(options.Channel.ChannelName)
		channelType = options.Channel.ChannelType
		isMultiKey = options.Channel.IsMultiKey
	}

	other := make(map[string]interface{}, len(options.Extra)+16)
	// Image2 pre-route evidence intentionally omits the URL because query
	// strings can carry client material. Existing non-Image2 diagnostics keep
	// their established request_path field.
	image2ResponseFailure := options.Image2ResponseFailure
	if _, ok := image2ResponseFailureAllowlist[image2ResponseFailure]; !ok {
		image2ResponseFailure = ""
	}
	if options.Image2 == nil && image2ResponseFailure == "" && c.Request != nil && c.Request.URL != nil {
		other["request_path"] = c.Request.URL.Path
	}
	other["event_version"] = relayErrorEventVersion
	other["error_stage"] = safeRelayStage(options.Stage)
	other["error_type"] = safeRelayInternalErrorType(err.GetErrorType())
	safeErrorCode := safeRelayInternalErrorCode(err.GetErrorCode())
	if safeErrorCode == "" {
		safeErrorCode = relayErrorUnclassifiedCode
	}
	other["error_code"] = safeErrorCode
	other["error_class"] = relayErrorClass(err)
	other["status_code"] = err.StatusCode
	other["channel_id"] = channelID
	other["channel_name"] = channelName
	other["channel_type"] = channelType
	// This is explicit rather than inferred from channel_id: a selected channel
	// can still fail before its adapter sends anything upstream.
	other["upstream_called"] = options.UpstreamCalled
	upstreamState := "not_called"
	if options.UpstreamCalled {
		upstreamState = "called"
	}
	other["upstream_accepted_known"] = options.UpstreamAccepted != nil
	if options.UpstreamAccepted != nil {
		other["upstream_accepted"] = *options.UpstreamAccepted
		if *options.UpstreamAccepted {
			upstreamState = "accepted"
		}
	}
	other["upstream_state"] = upstreamState
	responseWritten := false
	responseWrittenKnown := false
	if options.ResponseWritten != nil {
		responseWritten = *options.ResponseWritten
		responseWrittenKnown = true
	} else if c.Writer != nil {
		responseWritten = c.Writer.Written()
		responseWrittenKnown = true
	}
	other["response_written_known"] = responseWrittenKnown
	if responseWrittenKnown {
		other["response_written"] = responseWritten
	}
	other["retry_known"] = options.RetryDecision != nil
	if options.RetryDecision != nil {
		other["retry"] = *options.RetryDecision
		other["retry_index"] = options.RetryIndex
	}
	other["task_state"] = normalizeRelayTaskState(options.TaskState)
	other["refund_state"] = normalizeRelayRefundState(options.RefundState)
	other["billing_state"] = normalizeRelayErrorBillingState(options.BillingState)
	if options.Charged != nil {
		other["charge_known"] = true
		other["charged"] = *options.Charged
	} else if options.BillingState == RelayErrorBillingNotStarted || options.BillingState == RelayErrorBillingNotApplicable {
		other["charge_known"] = true
		other["charged"] = false
	} else {
		other["charge_known"] = false
	}
	if options.Image2 != nil {
		other["failure_scope"] = "pre_upstream"
		other["request_route"] = "images"
		other["request_method"] = requestMethod(c)
		other["elapsed_ms"] = relayPassiveElapsed(c).Milliseconds()
	}
	if image2ResponseFailure != "" {
		other["failure_scope"] = "upstream_response"
		other["request_route"] = "images"
		other["image2_response_failure"] = image2ResponseFailure
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
	other["admin_info"] = safeRelayAdminInfo(adminInfo)

	startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
	if startTime.IsZero() {
		startTime = time.Now()
	}
	useTimeSeconds := int(time.Since(startTime).Seconds())
	persistErr := model.RecordErrorLogWithResult(
		c,
		userID,
		channelID,
		modelName,
		// Error logs are operational diagnostics, not token-audit rows. Do not
		// persist token names or IDs here; consume logs retain those fields.
		"",
		// Persist only the stable error classification. Provider response bodies
		// can contain arbitrary sensitive content even after URL/key masking.
		fmt.Sprintf("error_code=%s status_code=%d", safeErrorCode, err.StatusCode),
		0,
		useTimeSeconds,
		common.GetContextKeyBool(c, constant.ContextKeyIsStream),
		group,
		other,
	)
	if persistErr != nil {
		c.Set(relayErrorLogPersistenceFailedKey, true)
		// The failure itself is already represented by the safe event below.
		// Mark it handled so response-audit middleware does not emit a second
		// event for the same Request ID.
		c.Set(relayErrorLogRecordedAnyKey, true)
		logRelayErrorEvent(c, userID, channelID, modelName, group, err, other, false)
		return false
	}
	logRelayErrorEvent(c, userID, channelID, modelName, group, err, other, true)
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
		safeRelayDiagnosticText(capability.Operation, 32),
		safeRelayDiagnosticText(capability.Resolution, 32),
		safeRelayDiagnosticText(capability.Quality, 32),
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
