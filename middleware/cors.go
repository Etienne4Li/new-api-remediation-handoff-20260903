package middleware

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

const corsAllowedOriginsEnv = "CORS_ALLOWED_ORIGINS"

// CORS returns the cross-origin policy for API and relay routes. Origins are
// deliberately deny-by-default: an operator must explicitly list every
// browser application origin instead of relying on the invalid
// "* + Allow-Credentials" combination. Same-origin requests do not need CORS
// headers and continue to work when the list is empty.
func CORS() gin.HandlerFunc {
	config := cors.DefaultConfig()
	allowedOrigins := loadAllowedCORSOrigins()
	config.AllowOriginWithContextFunc = func(_ *gin.Context, origin string) bool {
		normalized, err := common.NormalizeOrigin(origin)
		if err != nil {
			return false
		}
		_, allowed := allowedOrigins[normalized]
		return allowed
	}
	// Exact origins make credentialed browser requests safe. Deployments that
	// only expose bearer-token relay APIs can opt out of credentials explicitly.
	config.AllowCredentials = common.GetEnvOrDefaultBool("CORS_ALLOW_CREDENTIALS", true)
	config.AllowMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{
		"Origin",
		"Accept",
		"Content-Type",
		"Authorization",
		"Cache-Control",
		"Pragma",
		"X-Requested-With",
		"X-Auth-Session",
		"X-Security-Proof",
		"X-API-Key",
		"X-Goog-API-Key",
		"Anthropic-Version",
		"Anthropic-Beta",
		"OpenAI-Beta",
		"OpenAI-Organization",
		"OpenAI-Project",
		"MJ-API-Secret",
		"Idempotency-Key",
	}
	config.ExposeHeaders = []string{"Content-Type", "Content-Length", "X-Request-ID", "Auth-Version"}
	config.MaxAge = 12 * time.Hour
	return cors.New(config)
}

func loadAllowedCORSOrigins() map[string]struct{} {
	allowed := make(map[string]struct{})
	raw := strings.TrimSpace(os.Getenv(corsAllowedOriginsEnv))
	if raw == "" {
		// A separately hosted frontend is a common deployment shape. It is
		// trusted only when explicitly configured through FRONTEND_BASE_URL;
		// SESSION_COOKIE_TRUSTED_URL intentionally has different semantics and
		// is not silently reused as a CORS allowlist.
		raw = strings.TrimSpace(os.Getenv("FRONTEND_BASE_URL"))
	}
	if raw == "" {
		return allowed
	}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			log.Printf("WARNING: ignoring empty value in %s", corsAllowedOriginsEnv)
			continue
		}
		normalized, err := common.NormalizeOrigin(value)
		if err != nil {
			log.Printf("WARNING: ignoring invalid CORS origin %q: %v", value, err)
			continue
		}
		allowed[normalized] = struct{}{}
	}
	return allowed
}

func Version() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-New-Api-Version", common.Version)
		c.Next()
	}
}
