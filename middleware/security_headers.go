package middleware

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const defaultHSTSMaxAge = 31536000

// SecurityHeaders adds a conservative browser policy to every application
// response. Route-specific handlers may tighten a value (for example the
// media proxy uses a sandbox CSP), but no handler needs to remember the basic
// nosniff, framing, referrer, and permissions protections.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.Writer.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		header.Set("X-Frame-Options", "SAMEORIGIN")
		header.Set("Content-Security-Policy", "object-src 'none'; base-uri 'self'; frame-ancestors 'self'")
		header.Set("X-Permitted-Cross-Domain-Policies", "none")
		if hstsHeaderValue(c.Request) != "" {
			header.Set("Strict-Transport-Security", hstsHeaderValue(c.Request))
		}
		c.Next()
	}
}

func hstsHeaderValue(request *http.Request) string {
	if !hstsEnabled(request) {
		return ""
	}
	maxAge := defaultHSTSMaxAge
	if raw := strings.TrimSpace(os.Getenv("SECURITY_HEADERS_HSTS_MAX_AGE")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed >= 0 {
			maxAge = parsed
		}
	}
	value := "max-age=" + strconv.Itoa(maxAge)
	if parseBoolEnv("SECURITY_HEADERS_HSTS_INCLUDE_SUBDOMAINS", false) {
		value += "; includeSubDomains"
	}
	if parseBoolEnv("SECURITY_HEADERS_HSTS_PRELOAD", false) {
		value += "; preload"
	}
	return value
}

func hstsEnabled(request *http.Request) bool {
	if raw, ok := os.LookupEnv("SECURITY_HEADERS_HSTS"); ok {
		return parseBoolEnvValue(raw, false)
	}
	// HSTS is useful for public hosts and harmless on plain HTTP (browsers
	// ignore it there), but avoid surprising localhost development sessions.
	host := request.Host
	if hostName, _, err := net.SplitHostPort(host); err == nil {
		host = hostName
	} else {
		host = strings.Trim(host, "[]")
	}
	host = strings.ToLower(strings.TrimSpace(host))
	return host != "" && host != "localhost" && net.ParseIP(host) == nil
}

func parseBoolEnv(name string, fallback bool) bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return parseBoolEnvValue(raw, fallback)
}

func parseBoolEnvValue(raw string, fallback bool) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}
