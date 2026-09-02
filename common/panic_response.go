package common

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// WritePanicResponse is the single HTTP boundary for recovered panics. The
// panic value is intentionally not included in the response: provider errors,
// database DSNs, file paths, and credentials can all appear in panic values.
// A request ID is safe to expose and gives operators a correlation handle.
func WritePanicResponse(c *gin.Context) {
	message := "Internal server error"
	if requestID := c.GetString(RequestIdKey); requestID != "" {
		message = MessageWithRequestId(message, requestID)
	}
	c.JSON(http.StatusInternalServerError, gin.H{
		"error": gin.H{
			"message": message,
			"type":    "new_api_panic",
		},
	})
	c.Abort()
}
