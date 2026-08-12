package middleware

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	codeStr := ""
	if len(code) > 0 {
		codeStr = string(code[0])
	}
	userId := c.GetInt("id")
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
			"type":    "new_api_error",
			"code":    codeStr,
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
	relayErr := types.NewErrorWithStatusCode(errors.New(message), code, statusCode, types.ErrOptionWithSkipRetry())
	service.RecordRelayErrorLog(c, relayErr, service.RelayErrorLogOptions{
		Stage:     "channel_selection",
		ModelName: modelName,
		Group:     group,
		Extra: map[string]interface{}{
			"selection_group": group,
			"requested_model": modelName,
		},
	})
	abortWithOpenAiMessage(c, statusCode, message, code)
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
