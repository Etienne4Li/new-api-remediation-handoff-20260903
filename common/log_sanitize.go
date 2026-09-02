package common

import (
	"fmt"
	"net/url"
)

// SensitiveLogHash returns a short, one-way fingerprint suitable for
// correlating repeated security events without putting the secret itself in a
// log.  The domain separator prevents the digest from being confused with a
// hash generated for another purpose elsewhere in the application.
func SensitiveLogHash(value string) string {
	// Use the deployment's crypto secret as the HMAC key. A plain SHA digest
	// would let anyone with a log perform an offline dictionary attack against
	// short-lived signatures, reset codes, or other low-entropy credentials.
	return GenerateHMAC("newapi-sensitive-log-v1\x00" + value)[:16]
}

// SensitiveLogMeta describes a sensitive value using only its byte length and
// a short fingerprint.  It deliberately never includes a prefix/suffix of the
// original value: API keys and signed values are often identifiable from
// either end.
func SensitiveLogMeta(value string) string {
	if value == "" {
		return "empty"
	}
	return fmt.Sprintf("len=%d hash=%s", len([]byte(value)), SensitiveLogHash(value))
}

// SensitiveLogBody is the body equivalent of SensitiveLogMeta.  Keeping this
// helper at the boundary makes it harder for webhook handlers to accidentally
// reintroduce full payload logging while retaining useful correlation data.
func SensitiveLogBody(body []byte) string {
	return SensitiveLogMeta(string(body))
}

// SanitizeRequestURIForLog removes sensitive query values while preserving the
// route and non-sensitive parameters.  RequestURI is commonly copied into
// logs verbatim; this helper is intended for that exact boundary.  On a parse
// failure it returns a safe marker rather than echoing a potentially secret
// malformed URI.
func SanitizeRequestURIForLog(rawURI string) string {
	if rawURI == "" {
		return ""
	}
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return "<invalid-uri>"
	}
	query := parsed.Query()
	for key, values := range query {
		if !sensitiveLogQueryKey(key) {
			continue
		}
		for i, value := range values {
			values[i] = "hash:" + SensitiveLogHash(value)
		}
		query[key] = values
	}
	parsed.RawQuery = query.Encode()
	// RequestURI excludes userinfo and fragments.  Returning only path/query
	// also avoids accidentally logging credentials embedded in an absolute URL.
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsed.ForceQuery || parsed.RawQuery != "" {
		return path + "?" + parsed.RawQuery
	}
	return path
}

func sensitiveLogQueryKey(key string) bool {
	return isProviderCredentialKey(key)
}
