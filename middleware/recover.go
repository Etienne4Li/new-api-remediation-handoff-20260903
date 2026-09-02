package middleware

import (
	"fmt"
	"runtime/debug"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func RelayPanicRecover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				common.SysError(fmt.Sprintf("panic detected meta=%s", common.SensitiveLogMeta(fmt.Sprint(err))))
				common.SysError("stacktrace from panic: " + common.MaskSensitiveInfo(string(debug.Stack())))
				common.WritePanicResponse(c)
			}
		}()
		c.Next()
	}
}
