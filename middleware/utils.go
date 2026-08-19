package middleware

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const relayErrorStageContextKey = "relay_error_stage"

func setRelayErrorStage(c *gin.Context, stage string) {
	if c != nil {
		c.Set(relayErrorStageContextKey, stage)
	}
}

func currentRelayErrorStage(c *gin.Context) string {
	if c != nil {
		if stage := c.GetString(relayErrorStageContextKey); stage != "" {
			return stage
		}
	}
	return "request_middleware"
}

func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	errorCode := types.ErrorCode("")
	if len(code) > 0 {
		errorCode = code[0]
	}
	abortWithOpenAiMessageAndRecord(c, statusCode, message, errorCode, service.RelayErrorLogOptions{
		Stage: currentRelayErrorStage(c),
	})
}

func abortWithOpenAiMessageAndRecord(c *gin.Context, statusCode int, message string, code types.ErrorCode, options service.RelayErrorLogOptions) {
	userId := c.GetInt("id")
	// Anonymous 4xx requests are normally invalid credentials or untrusted
	// traffic. Keep them in the application/security log instead of allowing
	// them to flood the customer error table. Authenticated failures and every
	// 5xx are operationally searchable.
	if userId > 0 || statusCode >= http.StatusInternalServerError {
		relayErr := types.NewErrorWithStatusCode(errors.New(message), code, statusCode, types.ErrOptionWithSkipRetry())
		service.RecordRelayErrorLog(c, relayErr, options)
	}
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
			"type":    "new_api_error",
			"code":    string(code),
		},
	})
	c.Abort()
	logger.LogError(c.Request.Context(), fmt.Sprintf("user %d | %s", userId, message))
}

// abortWithOpenAiMessageAndRecordSelection is reserved for model/group
// selection failures. Authentication, quota and rate-limit rejections retain
// their existing logging policy, while an authenticated relay request that
// cannot reach any channel becomes visible in the same error-log table as an
// upstream failure.
func abortWithOpenAiMessageAndRecordSelection(c *gin.Context, statusCode int, message string, code types.ErrorCode, modelName string, group string) {
	abortWithOpenAiMessageAndRecord(c, statusCode, message, code, service.RelayErrorLogOptions{
		Stage:     "channel_selection",
		ModelName: modelName,
		Group:     group,
		Extra: map[string]interface{}{
			"selection_group": group,
			"requested_model": modelName,
		},
	})
}

// RelayErrorAudit is a final safety net for authenticated relay requests whose
// handler returned an error without using the structured error path. It does
// not inspect or persist response bodies, so credentials and prompt content
// cannot leak through this fallback. Anonymous 4xx traffic remains in the
// security/application log; anonymous 5xx is retained as an operational fault.
func RelayErrorAudit() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if c.Writer == nil || c.Writer.Status() < http.StatusBadRequest || service.HasRelayErrorLog(c) {
			return
		}
		statusCode := c.Writer.Status()
		if c.GetInt("id") <= 0 && statusCode < http.StatusInternalServerError {
			return
		}
		auditErr := types.NewErrorWithStatusCode(
			fmt.Errorf("relay request returned HTTP %d without a structured error event", statusCode),
			types.ErrorCodeUnhandledRelayFailure,
			statusCode,
			types.ErrOptionWithSkipRetry(),
		)
		service.RecordRelayErrorLog(c, auditErr, service.RelayErrorLogOptions{
			Stage:          "response_audit",
			UpstreamCalled: common.GetContextKeyBool(c, constant.ContextKeyUpstreamCalled),
			Extra: map[string]interface{}{
				"fallback_audit":  true,
				"response_status": statusCode,
			},
		})
	}
}

func abortWithMidjourneyMessage(c *gin.Context, statusCode int, code int, description string) {
	c.JSON(statusCode, gin.H{
		"description": description,
		"type":        "new_api_error",
		"code":        code,
	})
	c.Abort()
	logger.LogError(c.Request.Context(), description)
}
