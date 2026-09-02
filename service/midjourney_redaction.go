package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Midjourney media URLs come from an upstream provider and are persisted in
// the legacy task row.  Keep a finite boundary at ingress so a malformed
// callback cannot turn the row into an unbounded URL or JSON blob.
const (
	MaxMidjourneyMediaURLLength = 4096
	MaxMidjourneyVideoURLCount  = 256
)

// ValidateMidjourneyMediaURL checks the URL contract for an upstream image or
// video reference.  It deliberately does not perform a local SSRF check: the
// provider owns these URLs, while the authenticated image proxy performs the
// SSRF-aware check immediately before it dials a URL.
func ValidateMidjourneyMediaURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil
	}
	if len([]byte(trimmed)) > MaxMidjourneyMediaURLLength {
		return fmt.Errorf("midjourney media URL is too long")
	}
	if err := common.ValidateHTTPURL(trimmed); err != nil {
		return fmt.Errorf("invalid midjourney media URL: %w", err)
	}
	return nil
}

// RedactMidjourneyMediaURL is the outward-facing projection for legacy
// Midjourney media.  Invalid/non-HTTP values are omitted, while valid URLs
// lose userinfo, query credentials/signatures, and fragments through the
// shared task URL boundary.
func RedactMidjourneyMediaURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || len([]byte(trimmed)) > MaxMidjourneyMediaURLLength {
		return ""
	}
	// Redaction is intentionally more tolerant than the ingress validator: old
	// rows may contain URL userinfo or a fragment. Strip those credential-like
	// components first, then validate the public projection itself.
	sanitized, ok := sanitizeTaskURL(trimmed)
	if !ok || common.ValidateHTTPURL(sanitized) != nil {
		return ""
	}
	return sanitized
}

// RedactMidjourneyResponseBody applies the bounded task-response sanitizer to
// a legacy submit/seed response before it is reflected to an API client.
// Midjourney providers frequently put signed image/video URLs under
// properties, so sanitizing only the persisted task DTO is insufficient.
func RedactMidjourneyResponseBody(body []byte) []byte {
	redacted := RedactTaskResponseBody(body)
	if len(redacted) == 0 {
		return redacted
	}
	var value any
	if err := common.Unmarshal(redacted, &value); err != nil {
		return []byte("{}")
	}
	value = redactMidjourneyUnsafeURLValues(value, "", 0)
	encoded, err := common.Marshal(value)
	if err != nil || len(encoded) > 1<<20 {
		return []byte(`{"_redacted":true}`)
	}
	return encoded
}

func redactMidjourneyUnsafeURLValues(value any, key string, depth int) any {
	if depth > 16 {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = redactMidjourneyUnsafeURLValues(childValue, childKey, depth+1)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, childValue := range typed {
			out = append(out, redactMidjourneyUnsafeURLValues(childValue, key, depth+1))
		}
		return out
	case string:
		trimmed := strings.TrimSpace(typed)
		normalized := normalizeVideoResponseKey(key)
		if trimmed == "[redacted-data-url]" {
			return trimmed
		}
		isURLField := normalized == "url" || normalized == "uri" || normalized == "location" ||
			strings.HasSuffix(normalized, "imageurl") || strings.HasSuffix(normalized, "imageurls") ||
			strings.HasSuffix(normalized, "videourl") || strings.HasSuffix(normalized, "videourls") ||
			strings.HasSuffix(normalized, "mediaurl") || strings.HasSuffix(normalized, "mediaurls")
		if isURLField {
			if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
				return "[redacted-data-url]"
			}
			if trimmed == "" || common.ValidateHTTPURL(trimmed) != nil {
				return ""
			}
		}
		// `result` is often a plain provider task id. Only clear it when it
		// unmistakably carries a non-HTTP URL/scheme; valid URLs were already
		// normalized by RedactTaskResponseBody.
		if normalized == "result" {
			lower := strings.ToLower(trimmed)
			if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "javascript:") ||
				strings.HasPrefix(lower, "vbscript:") || strings.HasPrefix(lower, "file:") ||
				strings.Contains(trimmed, "://") && common.ValidateHTTPURL(trimmed) != nil {
				return ""
			}
		}
		return typed
	default:
		return value
	}
}
