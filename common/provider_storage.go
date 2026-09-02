package common

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	providerStorageMaxDepth  = 16
	providerStorageMaxItems  = 256
	providerStorageMaxString = 4096
)

// NormalizeProviderURLForRuntime validates a provider media URL while keeping
// its complete query string. Query parameters may be the only authorization
// the authenticated media proxy has after a restart, so this function is for
// private runtime storage, never for a public response or log projection.
func NormalizeProviderURLForRuntime(rawURL string, maxBytes int) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || maxBytes <= 0 || len([]byte(trimmed)) > maxBytes {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Opaque != "" {
		return ""
	}
	parsed.User = nil
	parsed.Fragment = ""
	parsed.ForceQuery = false
	normalized := parsed.String()
	if len([]byte(normalized)) > maxBytes || ValidateHTTPURL(normalized) != nil {
		return ""
	}
	return normalized
}

// SanitizeProviderURLForStorage keeps a provider media URL useful as a
// resource locator without turning the database row into a replayable bearer
// credential. Invalid and oversized values are rejected with an empty result.
func SanitizeProviderURLForStorage(rawURL string, maxBytes int) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || maxBytes <= 0 || len([]byte(trimmed)) > maxBytes {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Opaque != "" {
		return ""
	}
	parsed.User = nil
	parsed.Fragment = ""
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return ""
	}
	removeProviderCredentialQuery(query)
	parsed.RawQuery = query.Encode()
	parsed.ForceQuery = false
	sanitized := parsed.String()
	if len([]byte(sanitized)) > maxBytes || ValidateHTTPURL(sanitized) != nil {
		return ""
	}
	return sanitized
}

func removeProviderCredentialQuery(query url.Values) {
	present := make(map[string]bool, len(query))
	for key := range query {
		present[normalizeProviderStorageKey(key)] = true
	}
	hasCloudFrontSignature := present["signature"] && (present["policy"] || present["keypairid"])
	hasAzureSignature := present["sig"] && (present["sv"] || present["se"] || present["sp"] || present["sr"])
	for key := range query {
		normalized := normalizeProviderStorageKey(key)
		remove := isProviderCredentialKey(key) ||
			strings.HasPrefix(normalized, "xamz") ||
			strings.HasPrefix(normalized, "xgoog") ||
			strings.HasPrefix(normalized, "xoss") ||
			strings.HasPrefix(normalized, "qsign") ||
			normalized == "qak" || normalized == "qkeytime" ||
			normalized == "googleaccessid" || normalized == "ossaccesskeyid"
		if hasCloudFrontSignature && (normalized == "policy" || normalized == "expires" || normalized == "keypairid") {
			remove = true
		}
		if hasAzureSignature {
			switch normalized {
			case "sv", "se", "sp", "sr", "st", "spr", "sip", "si", "srt", "ss":
				remove = true
			}
		}
		if remove {
			query.Del(key)
		}
	}
}

// SanitizeProviderDiagnosticForStorage removes credential-bearing diagnostic
// content and bounds untrusted provider text before persistence.
func SanitizeProviderDiagnosticForStorage(value string, maxBytes int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || maxBytes <= 0 {
		return ""
	}
	return truncateProviderStorageString(MaskSensitiveInfo(trimmed), maxBytes)
}

// SanitizeProviderJSONForStorage recursively removes credential fields and
// sanitizes URL values while preserving ordinary provider metadata. Malformed
// or oversized input is omitted rather than persisted verbatim.
func SanitizeProviderJSONForStorage(raw string, maxBytes int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || maxBytes <= 0 || len([]byte(trimmed)) > maxBytes {
		return ""
	}
	var value any
	if err := Unmarshal([]byte(trimmed), &value); err != nil {
		return ""
	}
	value = sanitizeProviderStorageValue(value, "", 0)
	encoded, err := Marshal(value)
	if err != nil || len(encoded) > maxBytes {
		return ""
	}
	return string(encoded)
}

func sanitizeProviderStorageValue(value any, key string, depth int) any {
	if depth > providerStorageMaxDepth {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		count := 0
		for childKey, childValue := range typed {
			if isProviderCredentialKey(childKey) || count >= providerStorageMaxItems {
				continue
			}
			out[childKey] = sanitizeProviderStorageValue(childValue, childKey, depth+1)
			count++
		}
		return out
	case []any:
		limit := len(typed)
		if limit > providerStorageMaxItems {
			limit = providerStorageMaxItems
		}
		out := make([]any, 0, limit)
		for i := 0; i < limit; i++ {
			out = append(out, sanitizeProviderStorageValue(typed[i], key, depth+1))
		}
		return out
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
			return "[redacted-data-url]"
		}
		if sanitized := SanitizeProviderURLForStorage(trimmed, providerStorageMaxString); sanitized != "" {
			return sanitized
		}
		if isProviderURLField(key) && trimmed != "" {
			return ""
		}
		return SanitizeProviderDiagnosticForStorage(typed, providerStorageMaxString)
	default:
		return value
	}
}

func isProviderURLField(key string) bool {
	normalized := normalizeProviderStorageKey(key)
	return normalized == "url" || normalized == "uri" || normalized == "location" ||
		strings.HasSuffix(normalized, "imageurl") || strings.HasSuffix(normalized, "imageurls") ||
		strings.HasSuffix(normalized, "videourl") || strings.HasSuffix(normalized, "videourls") ||
		strings.HasSuffix(normalized, "mediaurl") || strings.HasSuffix(normalized, "mediaurls")
}

func isProviderCredentialKey(key string) bool {
	normalized := normalizeProviderStorageKey(key)
	if strings.HasPrefix(normalized, "xamz") || strings.HasPrefix(normalized, "xgoog") ||
		strings.HasPrefix(normalized, "xoss") || strings.HasPrefix(normalized, "qsign") {
		return true
	}
	switch normalized {
	case "key", "apikey", "xapikey", "accesskey", "accesskeyid", "accesstoken",
		"authtoken", "refreshtoken", "idtoken", "sessiontoken", "authorization",
		"auth", "bearer", "credential", "credentials", "clientsecret", "privatekey",
		"secretkey", "signature", "sig", "sign", "xsignature", "secret", "token",
		"password", "passwd", "cookie", "setcookie", "webhooksecret", "googleaccessid",
		"ossaccesskeyid", "awsaccesskeyid", "subscriptionkey", "qak", "qkeytime":
		return true
	}
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "signature")
}

func normalizeProviderStorageKey(key string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(key)))
}

func truncateProviderStorageString(value string, maxBytes int) string {
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value
	}
	const suffix = "..."
	if maxBytes <= len(suffix) {
		return suffix[:maxBytes]
	}
	value = value[:maxBytes-len(suffix)]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + suffix
}
