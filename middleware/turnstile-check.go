package middleware

import (
	"net/http"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

type turnstileCheckResponse struct {
	Success bool `json:"success"`
}

func TurnstileCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		securityConfig := common.GetSecurityRuntimeConfig()
		if securityConfig.TurnstileCheckEnabled {
			response := c.Query("turnstile")
			if response == "" {
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": "Turnstile token 为空",
				})
				c.Abort()
				return
			}
			rawRes, err := http.PostForm("https://challenges.cloudflare.com/turnstile/v0/siteverify", url.Values{
				"secret":   {securityConfig.TurnstileSecretKey},
				"response": {response},
				"remoteip": {c.ClientIP()},
			})
			if err != nil {
				common.SysLog("turnstile verification request failed error_meta=" + common.SensitiveLogMeta(err.Error()))
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": "Turnstile 校验服务暂时不可用，请稍后重试",
				})
				c.Abort()
				return
			}
			defer rawRes.Body.Close()
			var res turnstileCheckResponse
			err = common.DecodeJsonLimited(rawRes.Body, 64<<10, &res)
			if err != nil {
				common.SysLog("turnstile verification response decode failed error_meta=" + common.SensitiveLogMeta(err.Error()))
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": "Turnstile 校验服务返回无效响应",
				})
				c.Abort()
				return
			}
			if !res.Success {
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": "Turnstile 校验失败，请刷新重试！",
				})
				c.Abort()
				return
			}
		}
		c.Next()
	}
}
