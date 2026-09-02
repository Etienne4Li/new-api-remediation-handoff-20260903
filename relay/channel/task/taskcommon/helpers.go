package taskcommon

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

// UnmarshalMetadata converts a map[string]any metadata to a typed struct via JSON round-trip.
// This replaces the repeated pattern: json.Marshal(metadata) → json.Unmarshal(bytes, &target).
func UnmarshalMetadata(metadata map[string]any, target any) error {
	if metadata == nil {
		return nil
	}
	// Prevent metadata from overriding model fields to avoid billing bypass.
	// Work on a shallow copy: deleting from the request's map in-place mutates
	// the value retained in Gin context and can make retries observe a
	// different request than the first attempt.
	safeMetadata := make(map[string]any, len(metadata))
	for key, value := range metadata {
		// encoding/json matches struct fields case-insensitively. Filter the
		// protected provider-selection fields with EqualFold as well, otherwise
		// `Model`/`MODEL_NAME` can still overwrite a selected upstream model.
		// Callback fields are deliberately excluded: task callbacks are
		// provider-initiated outbound requests and must never be user-controlled.
		switch {
		case strings.EqualFold(key, "model"),
			strings.EqualFold(key, "model_name"),
			strings.EqualFold(key, "req_key"),
			strings.EqualFold(key, "callback_url"),
			strings.EqualFold(key, "callbackurl"):
			continue
		default:
			safeMetadata[key] = value
		}
	}
	metaBytes, err := common.Marshal(safeMetadata)
	if err != nil {
		return fmt.Errorf("marshal metadata failed: %w", err)
	}
	if err := common.Unmarshal(metaBytes, target); err != nil {
		return fmt.Errorf("unmarshal metadata failed: %w", err)
	}
	return nil
}

// DefaultString returns val if non-empty, otherwise fallback.
func DefaultString(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}

// DefaultInt returns val if non-zero, otherwise fallback.
func DefaultInt(val, fallback int) int {
	if val == 0 {
		return fallback
	}
	return val
}

// NormalizeTaskResultURL validates a provider-returned media URL before it is
// placed in TaskInfo. Inline data URLs are handled by the provider adaptor
// that owns their byte/MIME validation; this helper is intentionally limited
// to absolute HTTP(S) URLs. An empty value means the provider has not supplied
// a URL yet and is returned unchanged so callers can keep polling.
func NormalizeTaskResultURL(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if err := common.ValidateHTTPURL(trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// EscapeTaskIDPathSegment validates and escapes an opaque provider task ID
// before it is interpolated into a status/content endpoint path. Provider
// IDs are untrusted response data; without this boundary characters such as
// '?', '#', or '/' can change the request target or escape the intended path.
func EscapeTaskIDPathSegment(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("task ID is empty")
	}
	if len([]byte(trimmed)) > common.MaxHTTPURLLength {
		return "", fmt.Errorf("task ID is too long")
	}
	if trimmed == "." || trimmed == ".." {
		return "", fmt.Errorf("task ID is invalid")
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("task ID contains control characters")
		}
	}
	return url.PathEscape(trimmed), nil
}

// EncodeLocalTaskID encodes an upstream operation name to a URL-safe base64 string.
// Used by Gemini/Vertex to store upstream names as task IDs.
func EncodeLocalTaskID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

// DecodeLocalTaskID decodes a base64-encoded upstream operation name.
func DecodeLocalTaskID(id string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// BuildProxyURL constructs the video proxy URL using the public task ID.
// e.g., "https://your-server.com/v1/videos/task_xxxx/content"
func BuildProxyURL(taskID string) string {
	taskID = url.PathEscape(strings.TrimSpace(taskID))
	base := strings.TrimRight(strings.TrimSpace(system_setting.GetServerAddress()), "/")
	if base == "" {
		// A relative URL remains usable behind a reverse proxy and avoids
		// manufacturing an invalid absolute URL when ServerAddress has not been
		// configured yet.
		return fmt.Sprintf("/v1/videos/%s/content", taskID)
	}
	return fmt.Sprintf("%s/v1/videos/%s/content", base, taskID)
}

// Status-to-progress mapping constants for polling updates.
const (
	ProgressSubmitted  = "10%"
	ProgressQueued     = "20%"
	ProgressInProgress = "30%"
	ProgressComplete   = "100%"
)

// ---------------------------------------------------------------------------
// BaseBilling — embeddable no-op implementations for TaskAdaptor billing methods.
// Adaptors that do not need custom billing can embed this struct directly.
// ---------------------------------------------------------------------------

type BaseBilling struct{}

// EstimateBilling returns nil (no extra ratios; use base model price).
func (BaseBilling) EstimateBilling(_ *gin.Context, _ *relaycommon.RelayInfo) map[string]float64 {
	return nil
}

// AdjustBillingOnSubmit returns nil (no submit-time adjustment).
func (BaseBilling) AdjustBillingOnSubmit(_ *relaycommon.RelayInfo, _ []byte) map[string]float64 {
	return nil
}

// AdjustBillingOnComplete returns 0 (keep pre-charged amount).
func (BaseBilling) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}
