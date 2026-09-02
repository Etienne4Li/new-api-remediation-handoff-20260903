package common

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

type HasPrompt interface {
	GetPrompt() string
}

type HasImage interface {
	HasImage() bool
}

func GetFullRequestURL(baseURL string, requestURL string, channelType int) string {
	fullRequestURL := fmt.Sprintf("%s%s", baseURL, requestURL)

	if strings.HasPrefix(baseURL, "https://gateway.ai.cloudflare.com") {
		switch channelType {
		case constant.ChannelTypeOpenAI:
			fullRequestURL = fmt.Sprintf("%s%s", baseURL, strings.TrimPrefix(requestURL, "/v1"))
		case constant.ChannelTypeAzure:
			fullRequestURL = fmt.Sprintf("%s%s", baseURL, strings.TrimPrefix(requestURL, "/openai/deployments"))
		}
	}
	return fullRequestURL
}

func SanitizeURLForLog(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		// A malformed URL can still contain a credential or signed query. Do
		// not fall back to echoing the original value into a log.
		return "<invalid-url>"
	}
	// URL userinfo is never useful for diagnostics and may contain a password
	// or API credential. Strip it even when the URL has no query parameters.
	hadUserinfo := parsedURL.User != nil
	parsedURL.User = nil

	query := parsedURL.Query()
	changed := hadUserinfo
	for key := range query {
		if isSensitiveURLQueryKey(key) {
			query.Set(key, "***masked***")
			changed = true
		}
	}
	if !changed {
		return rawURL
	}

	parsedURL.RawQuery = query.Encode()
	return parsedURL.String()
}

func isSensitiveURLQueryKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch normalized {
	case "key",
		"api_key",
		"api-key",
		"apikey",
		"x-api-key",
		"access_token",
		"refresh_token",
		"id_token",
		"token",
		"authorization",
		"auth",
		"client_secret",
		"secret",
		"password",
		"passwd",
		"signature",
		"sig",
		"awsaccesskeyid",
		"x-amz-credential",
		"x-amz-security-token",
		"x-amz-signature":
		return true
	}
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "signature")
}

func GetAPIVersion(c *gin.Context) string {
	query := c.Request.URL.Query()
	apiVersion := query.Get("api-version")
	if apiVersion == "" {
		apiVersion = c.GetString("api_version")
	}
	return apiVersion
}

func createTaskError(err error, code string, statusCode int, localError bool) *dto.TaskError {
	if err == nil {
		err = fmt.Errorf("unknown task error")
	}
	return &dto.TaskError{
		Code:       code,
		Message:    common.MaskSensitiveInfo(err.Error()),
		StatusCode: statusCode,
		LocalError: localError,
		Error:      err,
	}
}

func taskRequestDecodeError(err error, fallbackCode string) *dto.TaskError {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "duration") || strings.Contains(message, "seconds") {
		return createTaskError(err, "invalid_seconds", http.StatusBadRequest, true)
	}
	return createTaskError(err, fallbackCode, http.StatusBadRequest, true)
}

func storeTaskRequest(c *gin.Context, info *RelayInfo, action string, requestObj TaskSubmitReq) {
	info.Action = action
	c.Set("task_request", requestObj)
}
func GetTaskRequest(c *gin.Context) (TaskSubmitReq, error) {
	v, exists := c.Get("task_request")
	if !exists {
		return TaskSubmitReq{}, fmt.Errorf("request not found in context")
	}
	req, ok := v.(TaskSubmitReq)
	if !ok {
		return TaskSubmitReq{}, fmt.Errorf("invalid task request type")
	}
	return req, nil
}

func validatePrompt(prompt string) *dto.TaskError {
	if strings.TrimSpace(prompt) == "" {
		return createTaskError(fmt.Errorf("prompt is required"), "invalid_request", http.StatusBadRequest, true)
	}
	return nil
}

// MaxTaskDurationSeconds caps user-supplied video duration. Duration is used
// as a billing multiplier (OtherRatio "seconds"); an unbounded value could
// overflow quota calculation into a negative charge.
const MaxTaskDurationSeconds = 3600

// ValidateTaskDurationBounds checks every client-facing duration encoding
// before an adaptor can turn it into a provider request or billing multiplier.
// Both fields are checked independently because different providers prefer
// either numeric `duration` or string `seconds`.
func ValidateTaskDurationBounds(req TaskSubmitReq) *dto.TaskError {
	// Validate both representations independently. Some adaptors prefer the
	// string `seconds` field while others prefer numeric `duration`; checking
	// only whichever field happens to be selected here lets a conflicting
	// oversized value bypass the provider-specific conversion.
	if req.Duration < 0 || req.Duration > MaxTaskDurationSeconds {
		return createTaskError(fmt.Errorf("seconds must be between 1 and %d", MaxTaskDurationSeconds), "invalid_seconds", http.StatusBadRequest, true)
	}
	if raw := strings.TrimSpace(req.Seconds); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 || seconds > MaxTaskDurationSeconds {
			return createTaskError(fmt.Errorf("seconds must be between 1 and %d", MaxTaskDurationSeconds), "invalid_seconds", http.StatusBadRequest, true)
		}
	}
	return nil
}

// Keep the package-local name for existing callers/tests while exposing the
// shared check to adaptors with special validation paths (for example Sora
// remix, which does not use ValidateMultipartDirect).
func validateTaskDurationBounds(req TaskSubmitReq) *dto.TaskError {
	return ValidateTaskDurationBounds(req)
}

func validateMultipartTaskRequest(c *gin.Context, info *RelayInfo, action string) (TaskSubmitReq, error) {
	var req TaskSubmitReq
	if _, err := c.MultipartForm(); err != nil {
		return req, err
	}

	formData := c.Request.PostForm
	req = TaskSubmitReq{
		Prompt:   formData.Get("prompt"),
		Model:    formData.Get("model"),
		Mode:     formData.Get("mode"),
		Image:    formData.Get("image"),
		Size:     formData.Get("size"),
		Metadata: make(map[string]interface{}),
	}

	if durationStr := formData.Get("seconds"); durationStr != "" {
		if duration, err := strconv.Atoi(durationStr); err == nil {
			req.Duration = duration
		}
	}

	if images := formData["images"]; len(images) > 0 {
		req.Images = images
	}

	for key, values := range formData {
		if len(values) > 0 && !isKnownTaskField(key) {
			if intVal, err := strconv.Atoi(values[0]); err == nil {
				req.Metadata[key] = intVal
			} else if floatVal, err := strconv.ParseFloat(values[0], 64); err == nil {
				req.Metadata[key] = floatVal
			} else {
				req.Metadata[key] = values[0]
			}
		}
	}
	return req, nil
}

func ValidateMultipartDirect(c *gin.Context, info *RelayInfo) *dto.TaskError {
	var prompt string
	var model string
	var seconds int
	var size string
	var hasInputReference bool

	var req TaskSubmitReq
	if err := common.UnmarshalBodyReusable(c, &req); err != nil {
		return taskRequestDecodeError(err, "invalid_json")
	}

	prompt = req.Prompt
	model = req.Model
	size = req.Size
	seconds, _ = strconv.Atoi(req.Seconds)
	if seconds == 0 {
		seconds = req.Duration
	}
	if req.InputReference != "" {
		req.Images = []string{req.InputReference}
	} else if len(req.Images) == 0 && strings.TrimSpace(req.Image) != "" {
		// 兼容单图上传
		req.Images = []string{strings.TrimSpace(req.Image)}
	}

	if strings.TrimSpace(req.Model) == "" {
		return createTaskError(fmt.Errorf("model field is required"), "missing_model", http.StatusBadRequest, true)
	}

	if req.HasImage() {
		hasInputReference = true
	}

	if taskErr := validatePrompt(prompt); taskErr != nil {
		return taskErr
	}

	if taskErr := validateTaskDurationBounds(req); taskErr != nil {
		return taskErr
	}

	action := constant.TaskActionTextGenerate
	if hasInputReference {
		action = constant.TaskActionGenerate
	}
	if strings.HasPrefix(model, "sora-2") {

		if size == "" {
			size = "720x1280"
		}

		if seconds <= 0 {
			seconds = 4
		}

		if model == "sora-2" && !lo.Contains([]string{"720x1280", "1280x720"}, size) {
			return createTaskError(fmt.Errorf("sora-2 size is invalid"), "invalid_size", http.StatusBadRequest, true)
		}
		if model == "sora-2-pro" && !lo.Contains([]string{"720x1280", "1280x720", "1792x1024", "1024x1792"}, size) {
			return createTaskError(fmt.Errorf("sora-2 size is invalid"), "invalid_size", http.StatusBadRequest, true)
		}
		// OtherRatios 已移到 Sora adaptor 的 EstimateBilling 中设置
	}

	storeTaskRequest(c, info, action, req)

	return nil
}

func isKnownTaskField(field string) bool {
	knownFields := map[string]bool{
		"prompt":          true,
		"model":           true,
		"mode":            true,
		"image":           true,
		"images":          true,
		"size":            true,
		"duration":        true,
		"input_reference": true, // Sora 特有字段
	}
	return knownFields[field]
}

func ValidateBasicTaskRequest(c *gin.Context, info *RelayInfo, action string) *dto.TaskError {
	var err error
	contentType := c.GetHeader("Content-Type")
	var req TaskSubmitReq
	if strings.HasPrefix(contentType, "multipart/form-data") {
		req, err = validateMultipartTaskRequest(c, info, action)
		if err != nil {
			return createTaskError(err, "invalid_multipart_form", http.StatusBadRequest, true)
		}
	}
	// 为了metadata字段的兼容性，统一UnmarshalBodyReusable
	if err := common.UnmarshalBodyReusable(c, &req); err != nil {
		return taskRequestDecodeError(err, "invalid_request")
	}

	if taskErr := validatePrompt(req.Prompt); taskErr != nil {
		return taskErr
	}

	if taskErr := validateTaskDurationBounds(req); taskErr != nil {
		return taskErr
	}

	if len(req.Images) == 0 && strings.TrimSpace(req.Image) != "" {
		// 兼容单图上传
		req.Images = []string{req.Image}
	}

	storeTaskRequest(c, info, action, req)
	return nil
}
