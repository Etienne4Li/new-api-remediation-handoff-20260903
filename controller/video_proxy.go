package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

const (
	// http.DetectContentType only examines the first 512 bytes. Keep the
	// probe at that size so a remote response is not buffered unnecessarily.
	videoProxyProbeSize = 512

	// Data URLs are decoded into memory, so use the same operational limit as
	// other remote file downloads and a conservative default during tests or
	// before configuration has been loaded.
	defaultVideoProxyMaxBytes int64 = 64 << 20

	// Task polling responses are JSON envelopes and may contain an inline
	// base64-encoded video (Vertex can return bytesBase64Encoded). Allow the
	// configured media limit plus a bounded envelope margin, but never read an
	// unbounded upstream response into memory.
	videoProxyTaskResponseOverhead int64 = 1 << 20
)

// These are the media types that the video endpoint is willing to emit. A
// type is accepted only after the bytes also pass the container sniff below;
// an attacker cannot turn an HTML response into a video by changing a header.
var videoProxyVideoHeaderBlocklist = []string{
	// These headers either carry credentials/state or let an upstream choose
	// browser execution/caching policy for a private NewAPI response.
	"Set-Cookie",
	"Set-Cookie2",
	"Content-Encoding",
	"Content-Location",
	"Content-Range",
	"Content-Security-Policy",
	"Content-Disposition",
	"Cache-Control",
	"Expires",
	"Pragma",
	"Trailer",
	"Transfer-Encoding",
	// Hop-by-hop headers must never be reflected from an upstream response.
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Upgrade",
	// Prevent an upstream from influencing the front proxy or browser
	// navigation/execution policy.
	"Location",
	"Refresh",
	"X-Accel-Redirect",
	"X-Accel-Buffering",
	"Cross-Origin-Embedder-Policy",
	"Cross-Origin-Opener-Policy",
	"Cross-Origin-Resource-Policy",
	"Origin-Agent-Cluster",
	"Timing-Allow-Origin",
	// CORS headers may already have been populated by the global relay
	// middleware. The endpoint returns bearer-like private media, so it must
	// not opt that media into cross-origin reads.
	"Access-Control-Allow-Credentials",
	"Access-Control-Allow-Headers",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Origin",
	"Access-Control-Expose-Headers",
	"Access-Control-Max-Age",
	"Access-Control-Request-Headers",
	"Access-Control-Request-Method",
}

// videoProxyError returns a standardized OpenAI-style error response.
func videoProxyError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
		},
	})
}

// sameVideoProxyOrigin reports whether two absolute HTTP(S) URLs share the
// same origin (scheme, hostname and effective port).  Provider media URLs are
// often signed storage URLs on a different host from the status endpoint; an
// API credential must never be sent to that host merely because the URL was
// returned by the provider.
func sameVideoProxyOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || left.Hostname() == "" || right.Hostname() == "" {
		return false
	}
	if left.User != nil || right.User != nil {
		return false
	}
	leftScheme := strings.ToLower(strings.TrimSpace(left.Scheme))
	rightScheme := strings.ToLower(strings.TrimSpace(right.Scheme))
	if (leftScheme != "http" && leftScheme != "https") || leftScheme != rightScheme {
		return false
	}
	if !strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return videoProxyEffectivePort(left) == videoProxyEffectivePort(right)
}

func videoProxyEffectivePort(value *url.URL) string {
	if value == nil {
		return ""
	}
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

// shouldAttachGeminiAPIKey limits the Gemini key to the API origin that
// produced the media URL.  Generated videos normally use a signed storage URL
// and therefore do not need the key at all; retaining it on a cross-origin
// request would disclose a long-lived credential to the storage provider.
func shouldAttachGeminiAPIKey(channelBaseURL, mediaURL string) bool {
	base, err := url.Parse(strings.TrimSpace(channelBaseURL))
	if err != nil {
		return false
	}
	media, err := url.Parse(strings.TrimSpace(mediaURL))
	if err != nil {
		return false
	}
	if base.User != nil || media.User != nil {
		return false
	}
	return sameVideoProxyOrigin(base, media)
}

// videoProxyClientWithRedirectGuard returns a shallow client copy whose
// redirect callback removes bearer-like request headers whenever an upstream
// redirect crosses origins.  The shared provider clients are cached and must
// not be mutated per request; cloning the client keeps the guard local while
// preserving its transport, timeout and existing SSRF redirect policy.
func videoProxyClientWithRedirectGuard(client *http.Client) *http.Client {
	if client == nil {
		return nil
	}
	guarded := *client
	previous := client.CheckRedirect
	guarded.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next != nil && len(via) > 0 {
			previousRequest := via[len(via)-1]
			if previousRequest != nil && !sameVideoProxyOrigin(previousRequest.URL, next.URL) {
				for _, header := range []string{
					"Authorization",
					"Proxy-Authorization",
					"Cookie",
					"Referer",
					"x-goog-api-key",
					"x-api-key",
				} {
					next.Header.Del(header)
				}
			}
		}
		if previous != nil {
			return previous(next, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &guarded
}

func VideoProxy(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}

	userID := c.GetInt("id")
	task, exists, err := model.GetByTaskId(userID, taskID)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to query task %s: error_meta=%s", taskID, common.SensitiveLogMeta(err.Error())))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to query task")
		return
	}
	if !exists || task == nil {
		videoProxyError(c, http.StatusNotFound, "invalid_request_error", "Task not found")
		return
	}

	if task.Status != model.TaskStatusSuccess {
		videoProxyError(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Task is not completed yet, current status: %s", task.Status))
		return
	}

	channel, err := model.CacheGetChannel(task.ChannelId)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to get channel for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(err.Error())))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to retrieve channel information")
		return
	}
	// Keep the HTTP boundary defensive even if a future cache implementation
	// accidentally violates CacheGetChannel's non-nil-on-success contract.
	// Without this check the next GetBaseURL/GetSetting call would panic and
	// expose an availability failure (or invoke the recovery middleware).
	if channel == nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Channel lookup returned nil for task %s", taskID))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to retrieve channel information")
		return
	}
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}

	var videoURL string
	var geminiAPIKey string
	proxy := channel.GetSetting().Proxy
	client := service.GetSSRFProtectedHTTPClient()
	if proxy != "" {
		// 渠道代理路径的连接由代理侧建立，无法做拨号时逐 IP 校验，
		// 因此后面对 videoURL 保留请求前的一次性 SSRF 校验。
		client, err = service.GetHttpClientWithProxy(proxy)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to create proxy client for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(err.Error())))
			videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create proxy client")
			return
		}
	}
	if client == nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video proxy client unavailable for task %s", taskID))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Video proxy service unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "", nil)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to create request: error_meta=%s", common.SensitiveLogMeta(err.Error())))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create proxy request")
		return
	}

	switch channel.Type {
	case constant.ChannelTypeGemini:
		// GetBaseURL returns an empty string when the channel uses its type
		// default. Header scoping and URL sanitization must use Gemini's actual
		// API origin rather than the generic OpenAI fallback above.
		baseURL = geminiVideoAPIBaseURL(channel)
		apiKey := task.PrivateData.Key
		if apiKey == "" {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Missing stored API key for Gemini task %s", taskID))
			videoProxyError(c, http.StatusInternalServerError, "server_error", "API key not stored for task")
			return
		}
		videoURL, err = getGeminiVideoURLWithContext(ctx, channel, task, apiKey)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to resolve Gemini video URL for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(err.Error())))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve Gemini video URL")
			return
		}
		geminiAPIKey = apiKey
	case constant.ChannelTypeVertexAi:
		videoURL, err = getVertexVideoURLWithContext(ctx, channel, task)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to resolve Vertex video URL for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(err.Error())))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to resolve Vertex video URL")
			return
		}
	case constant.ChannelTypeOpenAI, constant.ChannelTypeSora:
		escapedTaskID, escapeErr := taskcommon.EscapeTaskIDPathSegment(task.GetUpstreamTaskID())
		if escapeErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Invalid upstream task ID for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(escapeErr.Error())))
			videoProxyError(c, http.StatusInternalServerError, "server_error", "Invalid upstream task ID")
			return
		}
		videoURL = fmt.Sprintf("%s/v1/videos/%s/content", strings.TrimRight(baseURL, "/"), escapedTaskID)
		req.Header.Set("Authorization", "Bearer "+channel.Key)
	default:
		// Video URL is stored in PrivateData.ResultURL (fallback to FailReason for old data)
		videoURL = task.GetResultURL()
	}

	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video URL is empty for task %s", taskID))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		return
	}

	if isVideoProxyDataURL(videoURL) {
		if err := writeVideoDataURL(c, videoURL); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to decode video data URL for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(err.Error())))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		}
		return
	}

	var validateErr error
	if proxy == "" {
		validateErr = service.ValidateSSRFProtectedFetchURL(videoURL)
	} else {
		fetchSetting := system_setting.GetFetchSetting()
		validateErr = common.ValidateURLWithFetchSetting(videoURL, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain)
	}
	if validateErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video URL blocked for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(validateErr.Error())))
		videoProxyError(c, http.StatusForbidden, "server_error", "request blocked by fetch policy")
		return
	}

	req.URL, err = url.Parse(videoURL)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to parse video URL %q: error_meta=%s", common.SanitizeRequestURIForLog(videoURL), common.SensitiveLogMeta(err.Error())))
		videoProxyError(c, http.StatusInternalServerError, "server_error", "Failed to create proxy request")
		return
	}
	// Only the Gemini API origin may receive the API key.  Signed storage URLs
	// (the normal successful-video path) are cross-origin and must be fetched
	// without it.  The redirect guard below repeats this protection for any
	// upstream redirect chain.
	if geminiAPIKey != "" && shouldAttachGeminiAPIKey(baseURL, videoURL) {
		req.Header.Set("x-goog-api-key", geminiAPIKey)
	} else {
		req.Header.Del("x-goog-api-key")
	}

	resp, err := videoProxyClientWithRedirectGuard(client).Do(req)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to fetch video from %q: error_meta=%s", common.SanitizeRequestURIForLog(videoURL), common.SensitiveLogMeta(err.Error())))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Upstream returned status %d for %q", resp.StatusCode, common.SanitizeRequestURIForLog(videoURL)))
		videoProxyError(c, http.StatusBadGateway, "server_error",
			fmt.Sprintf("Upstream service returned status %d", resp.StatusCode))
		return
	}

	// Reject an advertised body that is too large before writing any response
	// bytes. Unknown-length streams are spooled through the same hard limit
	// below, so a peer cannot turn this endpoint into an unbounded reader.
	if resp.ContentLength > videoProxyMaxBytes() {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Video response for task %s exceeds maximum size", taskID))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Video content is too large")
		return
	}

	probe := make([]byte, videoProxyProbeSize)
	readCount, readErr := io.ReadFull(resp.Body, probe)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to inspect video content for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(readErr.Error())))
		videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		return
	}
	if readCount == 0 {
		videoProxyError(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Unsupported video content")
		return
	}
	probe = probe[:readCount]
	contentType, typeErr := resolveVideoProxyContentType(resp.Header.Get("Content-Type"), probe)
	if typeErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Rejected video content for task %s: %v", taskID, typeErr))
		videoProxyError(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Unsupported video content")
		return
	}

	spooled, bodyLength, spoolErr := spoolVideoProxyBody(resp.Body, probe, videoProxyMaxBytes())
	if spoolErr != nil {
		if errors.Is(spoolErr, errVideoProxyBodyTooLarge) {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Video response for task %s exceeds maximum size", taskID))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Video content is too large")
		} else {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to buffer video content for task %s: error_meta=%s", taskID, common.SensitiveLogMeta(spoolErr.Error())))
			videoProxyError(c, http.StatusBadGateway, "server_error", "Failed to fetch video content")
		}
		return
	}
	defer func() {
		name := spooled.Name()
		_ = spooled.Close()
		_ = os.Remove(name)
	}()

	setVideoProxyResponseHeaders(c, contentType, bodyLength)
	c.Writer.WriteHeader(resp.StatusCode)
	if _, err = io.Copy(c.Writer, spooled); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Failed to stream video content: error_meta=%s", common.SensitiveLogMeta(err.Error())))
	}
}

var errVideoProxyBodyTooLarge = errors.New("video proxy body exceeds maximum size")

// spoolVideoProxyBody consumes an upstream stream into a private temporary
// file while enforcing a hard byte limit. Buffering before sending headers is
// intentional: callers must never receive a 200 response whose body is later
// truncated merely because an unknown-length upstream exceeded the limit.
func spoolVideoProxyBody(body io.Reader, probe []byte, maxBytes int64) (*os.File, int64, error) {
	if body == nil || maxBytes <= 0 || int64(len(probe)) > maxBytes {
		return nil, 0, errVideoProxyBodyTooLarge
	}
	tmp, err := os.CreateTemp("", "newapi-video-proxy-*")
	if err != nil {
		return nil, 0, err
	}
	cleanup := func(e error) (*os.File, int64, error) {
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
		return nil, 0, e
	}
	if len(probe) > 0 {
		if _, err := tmp.Write(probe); err != nil {
			return cleanup(err)
		}
	}
	remaining := maxBytes - int64(len(probe))
	n, err := io.Copy(tmp, io.LimitReader(body, remaining+1))
	if err != nil {
		return cleanup(err)
	}
	total := int64(len(probe)) + n
	if total > maxBytes {
		return cleanup(errVideoProxyBodyTooLarge)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return cleanup(err)
	}
	return tmp, total, nil
}

func isVideoProxyDataURL(value string) bool {
	return len(value) >= len("data:") && strings.EqualFold(value[:len("data:")], "data:")
}

func writeVideoDataURL(c *gin.Context, dataURL string) error {
	mimeType, videoBytes, err := decodeVideoProxyDataURL(dataURL)
	if err != nil {
		return err
	}

	setVideoProxyResponseHeaders(c, mimeType, int64(len(videoBytes)))
	c.Writer.WriteHeader(http.StatusOK)
	_, err = c.Writer.Write(videoBytes)
	return err
}

// decodeVideoProxyDataURL validates and decodes a base64 data URL without
// trusting its declared MIME type. It is intentionally pure so the parser and
// size/type boundaries can be regression-tested without a database or router.
func decodeVideoProxyDataURL(dataURL string) (string, []byte, error) {
	dataURL = strings.TrimSpace(dataURL)
	parts := strings.SplitN(dataURL, ",", 2)
	if len(parts) != 2 {
		return "", nil, fmt.Errorf("invalid data url")
	}

	header := strings.TrimSpace(parts[0])
	payload := parts[1]
	if len(header) < len("data:") || !strings.EqualFold(header[:len("data:")], "data:") {
		return "", nil, fmt.Errorf("unsupported data url")
	}

	metadata := header[len("data:"):]
	metadataParts := strings.Split(metadata, ";")
	if len(metadataParts) == 0 {
		return "", nil, fmt.Errorf("unsupported data url")
	}
	declaredRaw := strings.TrimSpace(metadataParts[0])
	hasBase64 := false
	for _, parameter := range metadataParts[1:] {
		parameter = strings.TrimSpace(parameter)
		if strings.EqualFold(parameter, "base64") {
			if hasBase64 {
				return "", nil, fmt.Errorf("invalid data url encoding")
			}
			hasBase64 = true
			continue
		}
		// Video data URLs do not need arbitrary charset or application
		// parameters. Rejecting them keeps the MIME parser deterministic and
		// prevents parameter smuggling around the allowlist.
		return "", nil, fmt.Errorf("unsupported data url parameter")
	}
	if !hasBase64 {
		return "", nil, fmt.Errorf("unsupported data url encoding")
	}
	if payload == "" {
		return "", nil, fmt.Errorf("empty data url payload")
	}

	declaredType := ""
	if declaredRaw != "" {
		var err error
		declaredType, err = normalizeVideoProxyMIME(declaredRaw)
		if err != nil || !isSafeVideoProxyMIME(declaredType) {
			return "", nil, fmt.Errorf("unsupported video MIME type")
		}
	}
	if int64(len(payload)) > maxEncodedVideoProxyBytes(videoProxyMaxBytes()) {
		return "", nil, fmt.Errorf("video data url exceeds maximum size")
	}

	videoBytes, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		videoBytes, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return "", nil, fmt.Errorf("invalid data url payload: %w", err)
		}
	}
	if int64(len(videoBytes)) > videoProxyMaxBytes() {
		return "", nil, fmt.Errorf("video data url exceeds maximum size")
	}

	contentType, err := resolveVideoProxyContentType(declaredType, videoBytes)
	if err != nil {
		return "", nil, err
	}
	return contentType, videoBytes, nil
}

func videoProxyMaxBytes() int64 {
	if constant.MaxFileDownloadMB <= 0 {
		return defaultVideoProxyMaxBytes
	}
	// The configured value is an integer number of MiB. Guard the conversion
	// so a malformed/extreme environment value cannot wrap into a tiny limit.
	maxMB := int64(constant.MaxFileDownloadMB)
	if maxMB > (int64(^uint64(0)>>1) >> 20) {
		return defaultVideoProxyMaxBytes
	}
	return maxMB << 20
}

// videoProxyTaskResponseMaxBytes bounds the JSON response returned by a task
// status endpoint. A successful Vertex response may embed a base64 video,
// whose encoded representation is larger than the decoded media limit. Keep
// enough room for that representation and a small JSON envelope while still
// enforcing a finite upper bound.
func videoProxyTaskResponseMaxBytes() int64 {
	mediaLimit := videoProxyMaxBytes()
	encodedLimit := maxEncodedVideoProxyBytes(mediaLimit)
	maxInt64 := int64(^uint64(0) >> 1)
	if encodedLimit > maxInt64-videoProxyTaskResponseOverhead {
		return maxInt64
	}
	return encodedLimit + videoProxyTaskResponseOverhead
}

var errVideoProxyTaskResponseTooLarge = errors.New("video proxy task response exceeds maximum size")

// readVideoProxyTaskResponse reads a provider task response with a hard
// limit. Callers must close resp.Body; this helper intentionally does not
// expose an oversized payload to the JSON parser.
func readVideoProxyTaskResponse(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("empty task response")
	}
	maxBytes := videoProxyTaskResponseMaxBytes()
	// Reserve one byte for the sentinel read without allowing an extreme
	// calculated limit to wrap into a negative value.
	const maxInt64 = int64(^uint64(0) >> 1)
	if maxBytes >= maxInt64 {
		maxBytes = maxInt64 - 1
	}
	if resp.ContentLength > maxBytes {
		return nil, errVideoProxyTaskResponseTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read task response failed: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, errVideoProxyTaskResponseTooLarge
	}
	return body, nil
}

func maxEncodedVideoProxyBytes(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return 0
	}
	// Four base64 bytes encode at most three payload bytes. The small margin
	// accommodates an optional two-byte padding suffix.
	maxInt64 := int64(^uint64(0) >> 1)
	if maxBytes > (maxInt64-4)/4*3 {
		return maxInt64
	}
	return ((maxBytes + 2) / 3 * 4) + 4
}

func normalizeVideoProxyMIME(raw string) (string, error) {
	return service.NormalizeVideoMIME(raw)
}

func isSafeVideoProxyMIME(mediaType string) bool {
	return service.IsSafeVideoMIME(mediaType)
}

func compatibleVideoProxyMIME(declared, detected string) bool {
	// Keep this compatibility wrapper for controller tests and call sites;
	// canonical MIME policy lives in service.ResolveVideoMIME.
	if declared == detected {
		return true
	}
	if (declared == "video/mp4" || declared == "video/quicktime") &&
		(detected == "video/mp4" || detected == "video/quicktime") {
		return true
	}
	return declared == "video/mpegps" && detected == "video/mpeg"
}

func resolveVideoProxyContentType(declaredRaw string, probe []byte) (string, error) {
	return service.ResolveVideoMIME(declaredRaw, probe)
}

func sniffVideoProxyMIME(data []byte) string {
	return service.SniffVideoMIME(data)
}

func setVideoProxyResponseHeaders(c *gin.Context, contentType string, contentLength int64) {
	header := c.Writer.Header()
	for _, key := range videoProxyVideoHeaderBlocklist {
		header.Del(key)
	}
	header.Set("Content-Type", contentType)
	if contentLength > 0 {
		header.Set("Content-Length", strconv.FormatInt(contentLength, 10))
	} else {
		header.Del("Content-Length")
	}
	// Keep media private even when the upstream provider advertises a public
	// cache policy. A task URL is bearer-like and may contain authenticated
	// provider content.
	header.Set("Cache-Control", "private, no-store, max-age=0")
	header.Set("Pragma", "no-cache")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	header.Set("Content-Disposition", `inline; filename="video"`)
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
}
