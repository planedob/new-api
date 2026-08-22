package service

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const (
	relayErrorLogRecordedKey          = "relay_error_log_recorded_error"
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
	"image2_candidate_decisions": {},
	"image2_request_capability":  {},
	"selection_group":            {},
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

func safeRelayErrorLogExtra(extra map[string]interface{}) map[string]interface{} {
	if len(extra) == 0 {
		return nil
	}
	safe := make(map[string]interface{}, len(relayErrorLogExtraAllowlist))
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

func normalizedRelayErrorStage(stage string) string {
	switch stage {
	case "channel_selection":
		return stage
	default:
		return "unknown"
	}
}

func normalizeRelayErrorBillingState(state RelayErrorBillingState) RelayErrorBillingState {
	switch state {
	case RelayErrorBillingNotStarted, RelayErrorBillingNotApplicable, RelayErrorBillingPreConsumed:
		return state
	default:
		return RelayErrorBillingUnknown
	}
}

// RecordRelayErrorLog persists a safe, searchable pre-route error row. It is
// intentionally narrow: this candidate only records Image2 no-candidate
// failures and does not replace the existing channel-attempt logger.
func RecordRelayErrorLog(c *gin.Context, err *types.NewAPIError, options RelayErrorLogOptions) bool {
	if c == nil || err == nil {
		return false
	}
	if options.Image2 != nil && normalizedRelayErrorStage(options.Stage) == "channel_selection" && options.Channel == nil {
		ObserveImage2PreRouteFailure(c, *options.Image2, err.StatusCode, err.GetErrorCode())
	}
	if !constant.ErrorLogEnabled || !types.IsRecordErrorLog(err) {
		return false
	}

	modelName := options.ModelName
	if modelName == "" {
		modelName = c.GetString("original_model")
	}
	group := options.Group
	if group == "" {
		group = c.GetString("group")
	}
	modelName = boundedRelayErrorLogText(modelName, maxRelayErrorLogDimensionRunes)
	group = boundedRelayErrorLogText(group, maxRelayErrorLogDimensionRunes)

	channelID := 0
	if options.Channel != nil {
		channelID = options.Channel.ChannelId
	}

	other := map[string]interface{}{
		"event_version":   relayErrorEventVersion,
		"error_stage":     normalizedRelayErrorStage(options.Stage),
		"error_type":      err.GetErrorType(),
		"error_code":      err.GetErrorCode(),
		"status_code":     err.StatusCode,
		"channel_id":      channelID,
		"upstream_called": options.UpstreamCalled,
		"failure_scope":   "pre_upstream",
		"billing_state":   normalizeRelayErrorBillingState(options.BillingState),
		"request_route":   "images",
		"request_method":  requestMethod(c),
		"elapsed_ms":      relayPassiveElapsed(c).Milliseconds(),
		"admin_info":      map[string]interface{}{"use_channel": c.GetStringSlice("use_channel")},
	}
	if options.Charged != nil {
		other["charge_known"] = true
		other["charged"] = *options.Charged
	} else if options.BillingState == RelayErrorBillingNotStarted || options.BillingState == RelayErrorBillingNotApplicable {
		other["charge_known"] = true
		other["charged"] = false
	} else {
		other["charge_known"] = false
	}

	safeExtra := safeRelayErrorLogExtra(options.Extra)
	adminInfo := other["admin_info"].(map[string]interface{})
	for key, value := range safeExtra {
		if key == "image2_candidate_decisions" || key == "image2_request_capability" {
			adminInfo[key] = value
		}
	}
	if options.Image2 != nil {
		adminInfo["image2_request_capability"] = image2CapabilitySummary(*options.Image2)
	}
	other["admin_info"] = adminInfo

	// Persist only the stable error classification. Provider responses and
	// client bodies can contain arbitrary sensitive text even after masking.
	if persistErr := model.RecordErrorLogWithResult(
		c,
		c.GetInt("id"),
		channelID,
		modelName,
		"", // error rows are not token-audit rows
		fmt.Sprintf("error_code=%s status_code=%d", err.GetErrorCode(), err.StatusCode),
		0,
		int(relayPassiveElapsed(c)/time.Second),
		common.GetContextKeyBool(c, constant.ContextKeyIsStream),
		group,
		other,
	); persistErr != nil {
		c.Set(relayErrorLogPersistenceFailedKey, true)
		return false
	}
	c.Set(relayErrorLogRecordedKey, err)
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

func WasRelayErrorLogged(c *gin.Context, err *types.NewAPIError) bool {
	if c == nil || err == nil {
		return false
	}
	recorded, ok := c.Get(relayErrorLogRecordedKey)
	return ok && recorded == err
}

func RelayErrorLogPersistenceFailed(c *gin.Context) bool {
	return c != nil && c.GetBool(relayErrorLogPersistenceFailedKey)
}
