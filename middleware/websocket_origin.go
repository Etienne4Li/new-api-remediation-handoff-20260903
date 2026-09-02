package middleware

import (
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// WebSocketOriginAllowed is the origin policy used by the realtime WebSocket
// upgrader.  A WebSocket handshake is not protected by the browser's CORS
// preflight, so accepting every Origin would allow a hostile page to reuse a
// user's browser-held API key and issue realtime requests cross-site.
//
// Non-browser clients commonly omit Origin; those requests remain allowed and
// are still protected by the normal bearer-token authentication middleware.
// Browser requests must use one exact, normalized origin from
// WEBSOCKET_ALLOWED_ORIGINS, CORS_ALLOWED_ORIGINS, FRONTEND_BASE_URL, the
// configured session trusted URLs, or the request's own origin.
func WebSocketOriginAllowed(request *http.Request) bool {
	if request == nil {
		return false
	}
	originValues := request.Header.Values("Origin")
	if len(originValues) == 0 {
		return true
	}
	// RFC 6454 permits one Origin value. Multiple values (including a comma
	// joined value) are ambiguous and must not be accepted.
	if len(originValues) != 1 || strings.Contains(originValues[0], ",") {
		return false
	}
	origin, err := common.NormalizeOrigin(originValues[0])
	if err != nil {
		return false
	}

	allowed := loadWebSocketAllowedOrigins(request)
	_, ok := allowed[origin]
	return ok
}

func loadWebSocketAllowedOrigins(request *http.Request) map[string]struct{} {
	allowed := make(map[string]struct{})
	// A dedicated setting takes precedence. Falling back to the CORS and
	// frontend settings keeps existing deployments from having to duplicate an
	// allowlist while still requiring explicit configuration for cross-origin
	// browser clients.
	raw := strings.TrimSpace(os.Getenv("WEBSOCKET_ALLOWED_ORIGINS"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(corsAllowedOriginsEnv))
	}
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("FRONTEND_BASE_URL"))
	}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if normalized, err := common.NormalizeOrigin(value); err == nil {
			allowed[normalized] = struct{}{}
		}
	}
	for _, trusted := range common.GetSessionCookieConfig().TrustedURLs {
		if normalized, err := common.NormalizeOrigin(trusted); err == nil {
			allowed[normalized] = struct{}{}
		}
	}

	// Same-origin WebSocket requests do not need a separately configured
	// frontend origin. Derive the scheme from the actual connection; an
	// edge proxy terminating TLS should list its external origin explicitly in
	// one of the settings above rather than relying on an untrusted forwarded
	// header.
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if request.Host != "" {
		if normalized, err := common.NormalizeOrigin(scheme + "://" + request.Host); err == nil {
			allowed[normalized] = struct{}{}
		}
	}
	return allowed
}
