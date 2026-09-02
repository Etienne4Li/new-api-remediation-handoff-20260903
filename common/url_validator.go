package common

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/constant"
)

// MaxHTTPURLLength bounds URLs that are handed to an upstream or used as a
// local fetch target.  Apart from limiting request/log allocation, this keeps
// signed media URLs from turning into an effectively unbounded persistence
// field.  Callers with a stricter contract (for example notifications) may
// apply a smaller limit.
const MaxHTTPURLLength = 16 << 10

// ValidateHTTPURL validates an absolute URL that will be handed to an
// upstream service as remote media. It deliberately checks only the URL
// contract: callers that fetch the URL locally must additionally use the
// SSRF-aware fetch client.
func ValidateHTTPURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("URL is empty")
	}
	if len([]byte(trimmed)) > MaxHTTPURLLength {
		return fmt.Errorf("URL is too long")
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return fmt.Errorf("URL contains control characters")
		}
	}

	parsedURL, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}
	if !strings.EqualFold(parsedURL.Scheme, "http") && !strings.EqualFold(parsedURL.Scheme, "https") {
		return fmt.Errorf("unsupported URL scheme: %s (only http/https allowed)", parsedURL.Scheme)
	}
	if parsedURL.Opaque != "" || parsedURL.Host == "" || parsedURL.Hostname() == "" {
		return fmt.Errorf("URL host is required")
	}
	if parsedURL.User != nil {
		return fmt.Errorf("URL userinfo is not allowed")
	}
	if parsedURL.Fragment != "" {
		return fmt.Errorf("URL fragment is not allowed")
	}
	// url.Parse accepts an explicit but empty trailing port (for example
	// "https://example.test:") and reports no value through Port().  Reject
	// that form, as well as malformed bracketed hosts, before the URL reaches a
	// dialer or proxy.
	host := parsedURL.Host
	if strings.HasPrefix(host, "[") {
		closeBracket := strings.LastIndex(host, "]")
		if closeBracket < 0 {
			return fmt.Errorf("invalid URL host")
		}
		if len(host) > closeBracket+1 && host[closeBracket+1] != ':' {
			return fmt.Errorf("invalid URL host")
		}
		if len(host) == closeBracket+1 {
			// no explicit port
		} else if len(host) == closeBracket+2 {
			return fmt.Errorf("invalid URL port")
		}
	} else if strings.Count(host, ":") == 1 && strings.HasSuffix(host, ":") {
		return fmt.Errorf("invalid URL port")
	}
	if port := parsedURL.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("invalid URL port: %s", port)
		}
	}
	return nil
}

// ValidateCredentialFreeURL rejects URL components that can carry replayable
// credentials. It intentionally does not require an HTTP scheme because some
// channel base URL values are project-defined endpoint identifiers rather than
// literal URLs.
func ValidateCredentialFreeURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("URL is empty")
	}
	parsedURL, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}
	if parsedURL.User != nil {
		return fmt.Errorf("URL userinfo is not allowed")
	}
	if parsedURL.Fragment != "" {
		return fmt.Errorf("URL fragment is not allowed")
	}
	query, err := url.ParseQuery(parsedURL.RawQuery)
	if err != nil {
		return fmt.Errorf("invalid URL query: %w", err)
	}
	for key := range query {
		if sensitiveLogQueryKey(key) {
			return fmt.Errorf("URL credential query parameter %q is not allowed", key)
		}
	}
	return nil
}

// ValidateRedirectURL validates that a redirect URL is safe to use.
// It checks that:
//   - The URL is properly formatted
//   - The scheme is either http or https
//   - The domain is in the trusted domains list (exact match or subdomain)
//
// Returns nil if the URL is valid and trusted, otherwise returns an error
// describing why the validation failed.
func ValidateRedirectURL(rawURL string) error {
	// Parse the URL
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL format: %s", err.Error())
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("invalid URL scheme: only http and https are allowed")
	}

	domain := strings.ToLower(parsedURL.Hostname())

	for _, trustedDomain := range constant.TrustedRedirectDomains {
		if domain == trustedDomain || strings.HasSuffix(domain, "."+trustedDomain) {
			return nil
		}
	}

	return fmt.Errorf("domain %s is not in the trusted domains list", domain)
}
