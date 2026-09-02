package relay

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

const (
	maxMidjourneyResponseDepth  = 16
	maxMidjourneyResponseItems  = 256
	maxMidjourneyResponseString = 4096
	maxMidjourneyResponseBytes  = 1 << 20
)

// redactMidjourneyResponseBody protects the direct response path used by
// legacy Midjourney operations. Provider responses are not persisted here,
// but they are returned to API clients and commonly contain signed media URLs
// or diagnostic text with credentials.
func redactMidjourneyResponseBody(body []byte, taskID string) []byte {
	if len(body) == 0 {
		return nil
	}
	var value any
	if err := common.Unmarshal(body, &value); err != nil {
		return []byte("{}")
	}
	redacted := redactMidjourneyResponseValue(value, "", strings.TrimSpace(taskID), 0)
	encoded, err := common.Marshal(redacted)
	if err != nil || len(encoded) > maxMidjourneyResponseBytes {
		return []byte(`{"_redacted":true}`)
	}
	return encoded
}

func redactMidjourneyResponseValue(value any, key, taskID string, depth int) any {
	if depth > maxMidjourneyResponseDepth {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		count := 0
		for childKey, childValue := range typed {
			if isMidjourneyResponseSensitiveKey(childKey) {
				continue
			}
			if count >= maxMidjourneyResponseItems {
				break
			}
			out[childKey] = redactMidjourneyResponseValue(childValue, childKey, taskID, depth+1)
			count++
		}
		return out
	case []any:
		limit := len(typed)
		if limit > maxMidjourneyResponseItems {
			limit = maxMidjourneyResponseItems
		}
		out := make([]any, 0, limit)
		for i := 0; i < limit; i++ {
			out = append(out, redactMidjourneyResponseValue(typed[i], key, taskID, depth+1))
		}
		return out
	case string:
		return redactMidjourneyResponseString(typed, key, taskID)
	default:
		return value
	}
}

func redactMidjourneyResponseString(value, key, taskID string) string {
	normalized := normalizeMidjourneyResponseKey(key)
	if normalized == "imageurl" && taskID != "" && setting.GetMidjourneyConfig().ForwardURLEnabled &&
		service.ValidateMidjourneyMediaURL(value) == nil {
		return service.BuildMidjourneyImageProxyURL(system_setting.GetServerAddress(), taskID)
	}
	if isMidjourneyResponseURLKey(normalized) {
		return redactMidjourneyURLValue(value)
	}
	if isMidjourneyResponseDiagnosticKey(normalized) {
		return service.RedactTaskFailureReason(value)
	}
	// A provider may place a media URL in a generic field such as `result` or
	// `value`. Detect absolute HTTP(S) values independently of the field name
	// so a signed URL cannot bypass the media-key allowlist.
	if common.ValidateHTTPURL(value) == nil || strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "data:") {
		return redactMidjourneyURLValue(value)
	}
	// URL and credential material can also be embedded in a provider's free
	// form diagnostic field (for example `error_message` or a generic `value`).
	// Mask those strings without rewriting ordinary prompt/status text.
	lowerValue := strings.ToLower(value)
	if strings.Contains(lowerValue, "://") || strings.Contains(lowerValue, "authorization:") ||
		strings.Contains(lowerValue, "bearer ") || strings.Contains(lowerValue, "api_key") ||
		strings.Contains(lowerValue, "access_token") || strings.Contains(lowerValue, "signature=") {
		return service.RedactTaskFailureReason(value)
	}
	if len([]byte(value)) > maxMidjourneyResponseString {
		return value[:maxMidjourneyResponseString] + "..."
	}
	return value
}

func redactMidjourneyURLValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return "[redacted-data-url]"
	}
	return service.RedactMidjourneyMediaURL(trimmed)
}

func normalizeMidjourneyResponseKey(key string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(key)))
}

func isMidjourneyResponseSensitiveKey(key string) bool {
	switch normalizeMidjourneyResponseKey(key) {
	case "apikey", "xapikey", "accesskey", "accesstoken", "authtoken", "refreshtoken", "sessiontoken", "authorization", "auth", "bearer", "credential", "credentials", "clientsecret", "privatekey", "secretkey", "signature", "secret", "token", "password", "passwd", "cookie", "setcookie", "webhooksecret", "notifyhook", "callback", "callbackurl":
		return true
	default:
		return false
	}
}

func isMidjourneyResponseURLKey(key string) bool {
	normalized := normalizeMidjourneyResponseKey(key)
	return normalized == "imageurl" || normalized == "imageurls" || normalized == "videourl" ||
		normalized == "videourls" || normalized == "url" || normalized == "uri" || normalized == "location" ||
		normalized == "mediaurls" || strings.HasSuffix(normalized, "imageurl") || strings.HasSuffix(normalized, "imageurls") ||
		strings.HasSuffix(normalized, "videourl") || strings.HasSuffix(normalized, "videourls") ||
		strings.HasSuffix(normalized, "mediaurl") || strings.HasSuffix(normalized, "mediaurls")
}

func isMidjourneyResponseDiagnosticKey(key string) bool {
	normalized := normalizeMidjourneyResponseKey(key)
	switch normalized {
	case "description", "message", "error", "errormessage", "errmsg", "fail", "failreason", "reason", "detail", "details":
		return true
	}
	return strings.Contains(normalized, "error") || strings.Contains(normalized, "fail") ||
		strings.Contains(normalized, "reason") || strings.Contains(normalized, "message")
}
