package middleware

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	codeStr := ""
	if len(code) > 0 {
		codeStr = string(code[0])
	}
	// This helper is the last response boundary for a large portion of the
	// relay stack.  Callers often pass wrapped provider/transport errors, so do
	// not assume that their text is safe merely because it came from an error
	// value.  Keep ordinary validation text readable while redacting URLs,
	// credentials and secret-like key/value fields.
	safeMessage := common.MaskSensitiveInfo(message)
	if safeMessage == "" {
		safeMessage = "request failed"
	}
	userId := c.GetInt("id")
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": common.MessageWithRequestId(safeMessage, c.GetString(common.RequestIdKey)),
			"type":    "new_api_error",
			"code":    codeStr,
		},
	})
	c.Abort()
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	// Error text can contain arbitrary user/provider payloads that are not
	// covered by the formatter above.  Logs retain only non-reversible
	// metadata, avoiding a second exfiltration path.
	logger.LogError(ctx, fmt.Sprintf("user %d | status=%d code=%s message_meta=%s", userId, statusCode, codeStr, common.SensitiveLogMeta(message)))
}

func abortWithMidjourneyMessage(c *gin.Context, statusCode int, code int, description string) {
	safeDescription := common.MaskSensitiveInfo(description)
	if safeDescription == "" {
		safeDescription = "request failed"
	}
	c.JSON(statusCode, gin.H{
		"description": safeDescription,
		"type":        "new_api_error",
		"code":        code,
	})
	c.Abort()
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	logger.LogError(ctx, fmt.Sprintf("midjourney error status=%d code=%d description_meta=%s", statusCode, code, common.SensitiveLogMeta(description)))
}
