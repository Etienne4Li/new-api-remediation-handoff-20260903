package service

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	// User notification destinations are persisted and later fetched by a
	// background worker. Keep their size bounded so a settings request cannot
	// turn into an unbounded DB/Redis value or an oversized outbound request.
	MaxNotificationURLLength    = 2048
	MaxNotificationSecretLength = 256
	MaxNotificationTokenLength  = 256
	MaxNotificationEmailLength  = 320
)

// NormalizeNotificationURL validates a user-controlled notification target's
// syntax and returns a trimmed value suitable for persistence. Network/SSRF
// policy is deliberately checked again immediately before sending, because
// DNS and fetch settings can change after a setting is saved.
func NormalizeNotificationURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("notification URL is empty")
	}
	if len([]byte(rawURL)) > MaxNotificationURLLength {
		return "", fmt.Errorf("notification URL is too long")
	}
	for _, r := range rawURL {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("notification URL contains control characters")
		}
	}
	// Fragments are never part of an HTTP request target and can hide data
	// from server-side auditing; reject them instead of letting ParseRequestURI
	// treat '#' as ordinary path text.
	if strings.ContainsRune(rawURL, '#') {
		return "", fmt.Errorf("notification URL fragment is not allowed")
	}
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid notification URL: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("notification URL must use http or https")
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("notification URL must include a host")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("notification URL userinfo is not allowed")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("notification URL fragment is not allowed")
	}
	// Templates are supported in Bark paths/queries, but never in the origin;
	// otherwise a notification payload could rewrite the destination host.
	if strings.ContainsAny(parsed.Scheme+parsed.Host, "{}") {
		return "", fmt.Errorf("notification URL template is not allowed in origin")
	}
	// ParseRequestURI accepts numeric ports outside the TCP range (and an empty
	// trailing port), so enforce the network boundary explicitly here.
	portText := ""
	hasExplicitPort := false
	if strings.HasPrefix(parsed.Host, "[") {
		closeBracket := strings.LastIndex(parsed.Host, "]")
		if closeBracket < 0 {
			return "", fmt.Errorf("notification URL has an invalid host")
		}
		if len(parsed.Host) > closeBracket+1 {
			if parsed.Host[closeBracket+1] != ':' {
				return "", fmt.Errorf("notification URL has an invalid host")
			}
			hasExplicitPort = true
			portText = parsed.Host[closeBracket+2:]
		}
	} else if strings.Count(parsed.Host, ":") == 1 {
		hasExplicitPort = true
		portText = parsed.Host[strings.LastIndexByte(parsed.Host, ':')+1:]
	}
	if hasExplicitPort && portText == "" {
		return "", fmt.Errorf("notification URL has an invalid port")
	}
	if portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("notification URL has an invalid port")
		}
	}
	return rawURL, nil
}

// NormalizeNotificationCredential bounds and canonicalizes secrets/tokens
// stored alongside notification URLs. It intentionally does not impose an
// alphabet so existing webhook providers remain compatible.
func NormalizeNotificationCredential(value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	if len([]byte(value)) > maxLength {
		return "", fmt.Errorf("notification credential is too long")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("notification credential contains control characters")
		}
	}
	return value, nil
}
