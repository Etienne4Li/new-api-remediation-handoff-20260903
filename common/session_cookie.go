package common

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
)

// SessionCookieConfig is the request-time snapshot of cookie transport and
// browser-origin policy.  The exported legacy variables in constants.go are
// retained for source compatibility, but production code must obtain a copy
// through GetSessionCookieConfig.  In particular, the trusted-origin slice is
// deep-copied so a request can iterate it safely while an administrator (or
// process reloader) publishes a new configuration.
type SessionCookieConfig struct {
	Secure      bool
	TrustedURLs []string
}

var sessionCookieConfigMu sync.RWMutex

func cloneSessionCookieURLs(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

// GetSessionCookieConfig returns one coherent, detached cookie-policy
// snapshot.  Callers may retain the returned value for the lifetime of a
// request without observing a concurrent hot update.
func GetSessionCookieConfig() SessionCookieConfig {
	sessionCookieConfigMu.RLock()
	defer sessionCookieConfigMu.RUnlock()
	return SessionCookieConfig{
		Secure:      SessionCookieSecure,
		TrustedURLs: cloneSessionCookieURLs(SessionCookieTrustedURLs),
	}
}

// UpdateSessionCookieConfig atomically publishes a cookie-policy snapshot.
// The callback receives a private copy; the published trusted-origin slice is
// cloned again so caller-owned backing storage cannot mutate live state.
func UpdateSessionCookieConfig(update func(*SessionCookieConfig)) {
	if update == nil {
		return
	}
	sessionCookieConfigMu.Lock()
	defer sessionCookieConfigMu.Unlock()
	next := SessionCookieConfig{
		Secure:      SessionCookieSecure,
		TrustedURLs: cloneSessionCookieURLs(SessionCookieTrustedURLs),
	}
	update(&next)
	SessionCookieSecure = next.Secure
	SessionCookieTrustedURLs = cloneSessionCookieURLs(next.TrustedURLs)
}

// SetSessionCookieConfig is a convenience for initialization and tests.
func SetSessionCookieConfig(secure bool, trustedURLs []string) {
	sessionCookieConfigMu.Lock()
	SessionCookieSecure = secure
	SessionCookieTrustedURLs = cloneSessionCookieURLs(trustedURLs)
	sessionCookieConfigMu.Unlock()
}

// IsSessionCookieSecure returns the currently published Secure flag.
func IsSessionCookieSecure() bool {
	sessionCookieConfigMu.RLock()
	defer sessionCookieConfigMu.RUnlock()
	return SessionCookieSecure
}

// NormalizeOrigin validates and canonicalizes a browser origin. Only an exact
// scheme/host/effective-port match is meaningful; paths and wildcards are not
// accepted for authentication cookie endpoints.
func NormalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("origin is empty or invalid")
	}
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid origin: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("origin scheme must be http or https")
	}
	if parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" || (parsedURL.Path != "" && parsedURL.Path != "/") {
		return "", fmt.Errorf("origin must contain only scheme and host")
	}
	hostname := strings.ToLower(parsedURL.Hostname())
	if hostname == "" || strings.Contains(hostname, "*") {
		return "", fmt.Errorf("origin host is empty")
	}
	port := parsedURL.Port()
	normalizedHost := hostname
	if strings.Contains(hostname, ":") {
		normalizedHost = "[" + hostname + "]"
	}
	if port == "" || (parsedURL.Scheme == "http" && port == "80") || (parsedURL.Scheme == "https" && port == "443") {
		return parsedURL.Scheme + "://" + normalizedHost, nil
	}
	return parsedURL.Scheme + "://" + net.JoinHostPort(hostname, port), nil
}

func InitSessionCookieSettings() error {
	secureRaw := strings.TrimSpace(os.Getenv("SESSION_COOKIE_SECURE"))
	trustedURLsRaw := strings.TrimSpace(os.Getenv("SESSION_COOKIE_TRUSTED_URL"))

	// Build and validate the complete value locally, then publish once. This
	// avoids exposing a partially parsed allowlist to concurrent requests.
	setInsecure := func() { SetSessionCookieConfig(false, nil) }

	if secureRaw == "" || strings.EqualFold(secureRaw, "false") {
		if trustedURLsRaw != "" {
			setInsecure()
			return fmt.Errorf("SESSION_COOKIE_TRUSTED_URL requires SESSION_COOKIE_SECURE=true")
		}
		setInsecure()
		return nil
	}

	if !strings.EqualFold(secureRaw, "true") {
		setInsecure()
		return fmt.Errorf("SESSION_COOKIE_SECURE must be true or false")
	}

	if trustedURLsRaw == "" {
		setInsecure()
		return fmt.Errorf("SESSION_COOKIE_SECURE=true requires SESSION_COOKIE_TRUSTED_URL")
	}

	trustedURLs := strings.Split(trustedURLsRaw, ",")
	trustedURLsNormalized := make([]string, 0, len(trustedURLs))
	for _, trustedURL := range trustedURLs {
		trustedURL = strings.TrimSpace(trustedURL)
		if trustedURL == "" {
			setInsecure()
			return fmt.Errorf("SESSION_COOKIE_TRUSTED_URL contains an empty URL")
		}
		normalizedOrigin, err := NormalizeOrigin(trustedURL)
		if err != nil {
			setInsecure()
			return fmt.Errorf("invalid SESSION_COOKIE_TRUSTED_URL: %w", err)
		}
		if !strings.HasPrefix(normalizedOrigin, "https://") {
			setInsecure()
			return fmt.Errorf("SESSION_COOKIE_TRUSTED_URL must contain only https URLs with hosts")
		}
		trustedURLsNormalized = append(trustedURLsNormalized, normalizedOrigin)
	}

	SetSessionCookieConfig(true, trustedURLsNormalized)
	return nil
}
