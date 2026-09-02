package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

const defaultMidjourneyMediaMaxBytes int64 = 64 << 20
const defaultMidjourneyMediaTimeout = 60 * time.Second

var errMidjourneyImageBodyTooLarge = errors.New("midjourney image body exceeds maximum size")
var errMidjourneyVideoBodyTooLarge = errors.New("midjourney video body exceeds maximum size")

// newMidjourneyMediaRequest gives every upstream media fetch both the caller's
// cancellation signal and a finite deadline. Without this boundary a stalled
// provider connection can keep an authenticated request and its buffers alive
// indefinitely, even though the surrounding API request has already ended.
func newMidjourneyMediaRequest(parent context.Context, mediaURL string, timeout time.Duration) (*http.Request, context.CancelFunc, error) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		timeout = defaultMidjourneyMediaTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return req, cancel, nil
}

func newMidjourneyImageRequest(parent context.Context, imageURL string, timeout time.Duration) (*http.Request, context.CancelFunc, error) {
	return newMidjourneyMediaRequest(parent, imageURL, timeout)
}

func midjourneyMediaMaxBytes() int64 {
	maxBytes := common.GetMaxFileDownloadBytes()
	if maxBytes <= 0 {
		return defaultMidjourneyMediaMaxBytes
	}
	return maxBytes
}

// readMidjourneyMediaBody joins the bytes consumed for MIME sniffing with the
// remaining response body while enforcing a hard limit. Buffering is bounded
// so an upstream with an unknown Content-Length cannot keep this handler
// reading until the process runs out of memory. The caller only writes a
// response after this function succeeds, avoiding a 200 response with a
// silently truncated body.
func readMidjourneyMediaBody(body io.Reader, contentLength int64, probe []byte, bodyTooLarge error) ([]byte, error) {
	limit := midjourneyMediaMaxBytes()
	if body == nil || limit <= 0 || int64(len(probe)) > limit {
		return nil, bodyTooLarge
	}
	if contentLength > limit || (contentLength >= 0 && contentLength < int64(len(probe))) {
		return nil, bodyTooLarge
	}
	remaining := limit - int64(len(probe))
	// remaining is bounded by limit, which is positive, so adding one cannot
	// overflow for the configured int-sized MAX_FILE_DOWNLOAD_MB value.
	tail, err := io.ReadAll(io.LimitReader(body, remaining+1))
	if err != nil {
		return nil, err
	}
	if int64(len(tail)) > remaining {
		return nil, bodyTooLarge
	}
	result := make([]byte, 0, len(probe)+len(tail))
	result = append(result, probe...)
	result = append(result, tail...)
	return result, nil
}

func readMidjourneyImageBody(body io.Reader, contentLength int64, probe []byte) ([]byte, error) {
	return readMidjourneyMediaBody(body, contentLength, probe, errMidjourneyImageBodyTooLarge)
}

// midjourneyChannelProxy treats an unavailable channel snapshot as an
// unproxied fetch. The image URL is still validated and fetched through the
// SSRF-protected client in that case. Keep the nil check even though
// CacheGetChannel normally guarantees a non-nil channel on success: this HTTP
// boundary must not panic if a future cache implementation breaks that
// contract.
func midjourneyChannelProxy(channel *model.Channel, lookupErr error) string {
	if lookupErr != nil || channel == nil {
		return ""
	}
	return channel.GetSetting().Proxy
}

func RelayMidjourneyImage(c *gin.Context) {
	// TokenAuth populates the owning user id. Keep this defensive check here as
	// well because the handler may be called directly in tests or by a future
	// route that accidentally omits the middleware.
	userID := c.GetInt("id")
	if userID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
		return
	}
	routeTaskID := strings.TrimSpace(c.Param("id"))
	if routeTaskID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "midjourney_task_not_found"})
		return
	}
	// mj_id is not globally secret or necessarily globally unique. Always
	// include user_id in the lookup so a token can only fetch its own task.
	midjourneyTask := midjourneyMediaTaskForRoute(userID, c.GetInt("role"), routeTaskID)
	if midjourneyTask == nil {
		// Deliberately use the same response for unknown and cross-user ids;
		// callers must not be able to probe task ownership.
		c.JSON(http.StatusNotFound, gin.H{
			"error": "midjourney_task_not_found",
		})
		return
	}
	imageURL := strings.TrimSpace(midjourneyTask.ImageUrl)
	if imageURL == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "midjourney_task_not_found"})
		return
	}
	var httpClient *http.Client
	channel, channelErr := model.CacheGetChannel(midjourneyTask.ChannelId)
	proxy := midjourneyChannelProxy(channel, channelErr)
	if proxy != "" {
		var err error
		if httpClient, err = service.GetHttpClientWithProxy(proxy); err != nil {
			c.JSON(400, gin.H{
				"error": "proxy_url_invalid",
			})
			return
		}
	}
	if httpClient == nil {
		httpClient = service.GetSSRFProtectedHTTPClient()
	}
	var validateErr error
	if proxy == "" {
		validateErr = service.ValidateSSRFProtectedFetchURL(imageURL)
	} else {
		// 渠道代理路径的连接由代理侧建立，无法做拨号时逐 IP 校验，
		// 因此保留请求前的一次性 SSRF 校验。
		fetchSetting := system_setting.GetFetchSetting()
		validateErr = common.ValidateURLWithFetchSetting(imageURL, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain)
	}
	if validateErr != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"error": fmt.Sprintf("request blocked: %v", validateErr),
		})
		return
	}
	if httpClient == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "image_client_unavailable"})
		return
	}
	req, cancel, err := newMidjourneyImageRequest(c.Request.Context(), imageURL, defaultMidjourneyMediaTimeout)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "http_get_image_failed",
		})
		return
	}
	defer cancel()
	resp, err := httpClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "http_get_image_failed",
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Do not reflect arbitrary upstream response bodies into this response.
		// Besides leaking provider details, an HTML body could be rendered by a
		// caller that ignores the JSON content type.
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "http_get_image_failed",
		})
		return
	}

	// Trust neither the upstream MIME declaration nor an arbitrary image/*
	// value. Probe the bytes and only serve the small set of raster formats
	// that cannot be interpreted as active HTML/SVG by a browser.
	const probeSize = 512
	probe := make([]byte, probeSize)
	readCount, readErr := io.ReadFull(resp.Body, probe)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "http_get_image_failed"})
		return
	}
	probe = probe[:readCount]
	detectedType := normalizeMidjourneyMediaType(http.DetectContentType(probe))
	declaredType := normalizeMidjourneyMediaType(resp.Header.Get("Content-Type"))
	if !isSafeMidjourneyImageType(detectedType) || (declaredType != "" && !isSafeMidjourneyImageType(declaredType)) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported_image_type"})
		return
	}
	if declaredType != "" && !compatibleMidjourneyImageTypes(declaredType, detectedType) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported_image_type"})
		return
	}

	imageBytes, err := readMidjourneyImageBody(resp.Body, resp.ContentLength, probe)
	if err != nil {
		if errors.Is(err, errMidjourneyImageBodyTooLarge) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "image_too_large"})
		} else {
			c.JSON(http.StatusBadGateway, gin.H{"error": "http_get_image_failed"})
		}
		return
	}

	// The global CORS middleware may have populated response headers before
	// this handler ran. This is a private user asset, so do not let CORS or
	// upstream cookie/cache policies turn it into a public cross-origin blob.
	for _, header := range []string{
		"Access-Control-Allow-Credentials",
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Origin",
		"Access-Control-Expose-Headers",
		"Access-Control-Max-Age",
		"Access-Control-Request-Headers",
		"Access-Control-Request-Method",
		"Set-Cookie",
	} {
		c.Writer.Header().Del(header)
	}
	c.Writer.Header().Set("Content-Type", detectedType)
	c.Writer.Header().Set("Cache-Control", "private, no-store, max-age=0")
	c.Writer.Header().Set("Pragma", "no-cache")
	c.Writer.Header().Set("X-Content-Type-Options", "nosniff")
	c.Writer.Header().Set("Referrer-Policy", "no-referrer")
	c.Writer.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Writer.Header().Set("Content-Disposition", `inline; filename="midjourney-image"`)
	c.Writer.Header().Set("Content-Length", strconv.Itoa(len(imageBytes)))
	if _, err = c.Writer.Write(imageBytes); err != nil {
		logger.LogError(c, "failed to stream image error_meta="+common.SensitiveLogMeta(err.Error()))
	}
	return
}

func RelayMidjourneyVideo(c *gin.Context) {
	userID := c.GetInt("id")
	if userID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
		return
	}
	routeTaskID := strings.TrimSpace(c.Param("id"))
	if routeTaskID == "" {
		midjourneyVideoNotFound(c)
		return
	}

	midjourneyTask := midjourneyMediaTaskForRoute(userID, c.GetInt("role"), routeTaskID)
	if midjourneyTask == nil {
		midjourneyVideoNotFound(c)
		return
	}

	videoURL := strings.TrimSpace(midjourneyTask.VideoUrl)
	if rawIndex := strings.TrimSpace(c.Param("index")); rawIndex != "" {
		index, err := strconv.Atoi(rawIndex)
		if err != nil || index < 0 {
			midjourneyVideoNotFound(c)
			return
		}
		videoURLs, err := service.ParseMidjourneyVideoURLs(midjourneyTask.VideoUrls)
		if err != nil || index >= len(videoURLs) {
			midjourneyVideoNotFound(c)
			return
		}
		videoURL = videoURLs[index].Url
	}
	if videoURL == "" {
		midjourneyVideoNotFound(c)
		return
	}

	var httpClient *http.Client
	channel, channelErr := model.CacheGetChannel(midjourneyTask.ChannelId)
	proxy := midjourneyChannelProxy(channel, channelErr)
	if proxy != "" {
		var err error
		if httpClient, err = service.GetHttpClientWithProxy(proxy); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "proxy_url_invalid"})
			return
		}
	}
	if httpClient == nil {
		httpClient = service.GetSSRFProtectedHTTPClient()
	}
	var validateErr error
	if proxy == "" {
		validateErr = service.ValidateSSRFProtectedFetchURL(videoURL)
	} else {
		fetchSetting := system_setting.GetFetchSetting()
		validateErr = common.ValidateURLWithFetchSetting(videoURL, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain)
	}
	if validateErr != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "request_blocked"})
		return
	}
	if httpClient == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "video_client_unavailable"})
		return
	}

	req, cancel, err := newMidjourneyMediaRequest(c.Request.Context(), videoURL, defaultMidjourneyMediaTimeout)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "http_get_video_failed"})
		return
	}
	defer cancel()
	resp, err := httpClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "http_get_video_failed"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "http_get_video_failed"})
		return
	}
	if resp.ContentLength > midjourneyMediaMaxBytes() {
		c.JSON(http.StatusBadGateway, gin.H{"error": "video_too_large"})
		return
	}

	const probeSize = 512
	probe := make([]byte, probeSize)
	readCount, readErr := io.ReadFull(resp.Body, probe)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "http_get_video_failed"})
		return
	}
	probe = probe[:readCount]
	contentType, err := service.ResolveVideoMIME(resp.Header.Get("Content-Type"), probe)
	if err != nil {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported_video_type"})
		return
	}

	videoBytes, err := readMidjourneyMediaBody(resp.Body, resp.ContentLength, probe, errMidjourneyVideoBodyTooLarge)
	if err != nil {
		if errors.Is(err, errMidjourneyVideoBodyTooLarge) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "video_too_large"})
		} else {
			c.JSON(http.StatusBadGateway, gin.H{"error": "http_get_video_failed"})
		}
		return
	}

	for _, header := range []string{
		"Access-Control-Allow-Credentials",
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Origin",
		"Access-Control-Expose-Headers",
		"Access-Control-Max-Age",
		"Access-Control-Request-Headers",
		"Access-Control-Request-Method",
		"Content-Encoding",
		"Content-Location",
		"Content-Range",
		"Expires",
		"Location",
		"Refresh",
		"Set-Cookie",
		"Set-Cookie2",
	} {
		c.Writer.Header().Del(header)
	}
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.Header().Set("Content-Length", strconv.Itoa(len(videoBytes)))
	c.Writer.Header().Set("Cache-Control", "private, no-store, max-age=0")
	c.Writer.Header().Set("Pragma", "no-cache")
	c.Writer.Header().Set("X-Content-Type-Options", "nosniff")
	c.Writer.Header().Set("Referrer-Policy", "no-referrer")
	c.Writer.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Writer.Header().Set("Content-Disposition", `inline; filename="midjourney-video"`)
	c.Writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if _, err = c.Writer.Write(videoBytes); err != nil {
		logger.LogError(c, "failed to stream video error_meta="+common.SensitiveLogMeta(err.Error()))
	}
}

func midjourneyVideoNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"error": "midjourney_video_not_found"})
}

func midjourneyMediaTaskForRoute(userID, userRole int, routeTaskID string) *model.Midjourney {
	taskID, err := service.DecodeMidjourneyProxyTaskID(routeTaskID)
	if err != nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	lookup := func(id string) *model.Midjourney {
		if userRole >= common.RoleAdminUser {
			return model.GetByOnlyMJId(id)
		}
		return model.GetByMJId(userID, id)
	}
	return lookup(taskID)
}

var safeMidjourneyImageTypes = map[string]struct{}{
	"image/gif":  {},
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

func normalizeMidjourneyMediaType(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "image/jpg" {
		return "image/jpeg"
	}
	return mediaType
}

func isSafeMidjourneyImageType(mediaType string) bool {
	_, ok := safeMidjourneyImageTypes[mediaType]
	return ok
}

func compatibleMidjourneyImageTypes(declared, detected string) bool {
	return declared == detected
}

func RelayMidjourneyNotify(c *gin.Context) *dto.MidjourneyResponse {
	var midjRequest dto.MidjourneyDto
	err := common.UnmarshalBodyReusable(c, &midjRequest)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "bind_request_body_failed",
			Properties:  nil,
			Result:      "",
		}
	}
	if err := validateMidjourneyNotifyMedia(&midjRequest); err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "invalid_media_url",
			Properties:  nil,
			Result:      "",
		}
	}
	midjourneyTask := model.GetByOnlyMJId(midjRequest.MjId)
	if midjourneyTask == nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "midjourney_task_not_found",
			Properties:  nil,
			Result:      "",
		}
	}
	midjourneyTask.Progress = midjRequest.Progress
	midjourneyTask.PromptEn = midjRequest.PromptEn
	midjourneyTask.State = midjRequest.State
	midjourneyTask.SubmitTime = midjRequest.SubmitTime
	midjourneyTask.StartTime = midjRequest.StartTime
	midjourneyTask.FinishTime = midjRequest.FinishTime
	midjourneyTask.ImageUrl = strings.TrimSpace(midjRequest.ImageUrl)
	midjourneyTask.VideoUrl = strings.TrimSpace(midjRequest.VideoUrl)
	videoUrlsStr, err := common.Marshal(midjRequest.VideoUrls)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "marshal_video_urls_failed",
			Properties:  nil,
			Result:      "",
		}
	}
	midjourneyTask.VideoUrls = string(videoUrlsStr)
	midjourneyTask.Status = midjRequest.Status
	midjourneyTask.FailReason = midjRequest.FailReason
	err = midjourneyTask.Update()
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "update_midjourney_task_failed",
		}
	}

	return nil
}

// validateMidjourneyNotifyMedia validates provider callback media before it is
// persisted. The callback is intentionally separate from the authenticated
// image proxy, so accepting arbitrary strings here would let a malformed or
// compromised provider response become a future server-side fetch target.
// SSRF policy is still enforced immediately before fetching; this helper only
// enforces the URL syntax/size contract and keeps the database row bounded.
func validateMidjourneyNotifyMedia(request *dto.MidjourneyDto) error {
	if request == nil {
		return errors.New("midjourney notify request is nil")
	}
	for _, media := range map[string]string{
		"imageUrl": request.ImageUrl,
		"videoUrl": request.VideoUrl,
	} {
		if err := service.ValidateMidjourneyMediaURL(media); err != nil {
			return err
		}
	}
	if len(request.VideoUrls) > service.MaxMidjourneyVideoURLCount {
		return fmt.Errorf("too many video URLs")
	}
	for index := range request.VideoUrls {
		if err := service.ValidateMidjourneyMediaURL(request.VideoUrls[index].Url); err != nil {
			return fmt.Errorf("videoUrls[%d]: %w", index, err)
		}
		request.VideoUrls[index].Url = strings.TrimSpace(request.VideoUrls[index].Url)
	}
	request.ImageUrl = strings.TrimSpace(request.ImageUrl)
	request.VideoUrl = strings.TrimSpace(request.VideoUrl)
	return nil
}

func coverMidjourneyTaskDto(c *gin.Context, originTask *model.Midjourney) (midjourneyTask dto.MidjourneyDto) {
	if originTask == nil {
		return midjourneyTask
	}
	midjourneyTask.MjId = originTask.MjId
	midjourneyTask.Progress = originTask.Progress
	midjourneyTask.PromptEn = originTask.PromptEn
	midjourneyTask.State = originTask.State
	midjourneyTask.SubmitTime = originTask.SubmitTime
	midjourneyTask.StartTime = originTask.StartTime
	midjourneyTask.FinishTime = originTask.FinishTime
	midjourneyTask.ImageUrl = ""
	midjourneyConfig := setting.GetMidjourneyConfig()
	if originTask.ImageUrl != "" && midjourneyConfig.ForwardURLEnabled {
		midjourneyTask.ImageUrl = service.BuildMidjourneyImageProxyURL(system_setting.GetServerAddress(), originTask.MjId)
		if originTask.Status != "SUCCESS" {
			midjourneyTask.ImageUrl += "?rand=" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
	} else {
		// Provider image URLs may contain signed query parameters. The image
		// proxy remains the preferred path, but when forwarding is disabled the
		// public DTO still must not disclose those credentials.
		midjourneyTask.ImageUrl = service.RedactMidjourneyMediaURL(originTask.ImageUrl)
	}
	serverAddress := system_setting.GetServerAddress()
	if videoURL := strings.TrimSpace(originTask.VideoUrl); videoURL != "" && service.ValidateMidjourneyMediaURL(videoURL) == nil {
		midjourneyTask.VideoUrl = service.BuildMidjourneyVideoProxyURL(serverAddress, originTask.MjId)
	}
	midjourneyTask.Status = originTask.Status
	midjourneyTask.FailReason = service.RedactTaskFailureReason(originTask.FailReason)
	midjourneyTask.Action = originTask.Action
	midjourneyTask.Description = service.RedactTaskFailureReason(originTask.Description)
	midjourneyTask.Prompt = originTask.Prompt
	if originTask.Buttons != "" {
		var buttons []dto.ActionButton
		// Buttons are provider-controlled JSON and may contain arbitrary values
		// (including URLs or credentials). Redact before decoding so the relay
		// task endpoint never reflects the raw callback payload.
		buttonsBody := service.RedactMidjourneyResponseBody([]byte(originTask.Buttons))
		err := common.Unmarshal(buttonsBody, &buttons)
		if err == nil {
			midjourneyTask.Buttons = buttons
		}
	}
	if videoURLs, err := service.ParseMidjourneyVideoURLs(originTask.VideoUrls); err == nil {
		for index := range videoURLs {
			videoURLs[index].Url = service.BuildMidjourneyIndexedVideoProxyURL(serverAddress, originTask.MjId, index)
		}
		midjourneyTask.VideoUrls = videoURLs
	}
	if originTask.Properties != "" {
		var properties dto.Properties
		err := common.Unmarshal([]byte(originTask.Properties), &properties)
		if err == nil {
			midjourneyTask.Properties = &properties
		}
	}
	return
}

func RelaySwapFace(c *gin.Context, info *relaycommon.RelayInfo) *dto.MidjourneyResponse {
	var swapFaceRequest dto.SwapFaceRequest
	err := common.UnmarshalBodyReusable(c, &swapFaceRequest)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "bind_request_body_failed")
	}

	info.InitChannelMeta(c)

	if swapFaceRequest.SourceBase64 == "" || swapFaceRequest.TargetBase64 == "" {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "sour_base64_and_target_base64_is_required")
	}
	modelName := service.CovertMjpActionToModelName(constant.MjActionSwapFace)

	priceData, err := helper.ModelPriceHelperPerCall(c, info)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: common.MaskSensitiveInfo(err.Error()),
		}
	}

	userQuota, err := model.GetUserQuota(info.UserId, false)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: common.MaskSensitiveInfo(err.Error()),
		}
	}

	if userQuota-priceData.Quota < 0 {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "quota_not_enough",
		}
	}
	intentCreated, intentErr := service.PrepareMidjourneySubmitIntent(
		info,
		constant.MjActionSwapFace,
		c.GetInt("channel_id"),
		priceData.Quota,
	)
	if intentErr != nil {
		common.SysLog("error creating Midjourney swap-face submit intent error_meta=" + common.SensitiveLogMeta(intentErr.Error()))
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "request_already_in_flight")
	}
	requestURL := getMjRequestPath(c.Request.URL.String())
	baseURL := c.GetString("base_url")
	fullRequestURL := fmt.Sprintf("%s%s", baseURL, requestURL)
	mjResp, _, err := service.DoMidjourneyHttpRequest(c, time.Second*60, fullRequestURL)
	if err != nil {
		return &mjResp.Response
	}
	midjResponse := &mjResp.Response
	midjourneyTask := &model.Midjourney{
		UserId:      info.UserId,
		Code:        midjResponse.Code,
		Action:      constant.MjActionSwapFace,
		MjId:        midjResponse.Result,
		Prompt:      "InsightFace",
		PromptEn:    "",
		Description: midjResponse.Description,
		State:       "",
		SubmitTime:  info.StartTime.UnixNano() / int64(time.Millisecond),
		StartTime:   time.Now().UnixNano() / int64(time.Millisecond),
		FinishTime:  0,
		ImageUrl:    "",
		Status:      "",
		Progress:    "0%",
		FailReason:  "",
		ChannelId:   c.GetInt("channel_id"),
	}
	if intentCreated {
		accepted := mjResp.StatusCode == http.StatusOK && midjResponse.Code == 1 && midjResponse.Result != ""
		if accepted {
			if err := service.AcceptMidjourneySubmitIntent(info, midjourneyTask, priceData.Quota); err != nil {
				common.SysLog("error accepting Midjourney swap-face intent error_meta=" + common.SensitiveLogMeta(err.Error()))
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_intent_failed")
			}
		} else if mjResp.StatusCode == http.StatusOK {
			if err := service.RejectMidjourneySubmitIntent(info, midjResponse.Description); err != nil {
				common.SysLog("error rejecting Midjourney swap-face intent error_meta=" + common.SensitiveLogMeta(err.Error()))
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_intent_failed")
			}
		}
	}
	billingPrepared, billingErr := service.PrepareMidjourneyTaskBilling(
		info,
		midjourneyTask,
		priceData.Quota,
		mjResp.StatusCode == http.StatusOK && midjResponse.Code == 1,
	)
	if billingErr != nil {
		common.SysLog("error consuming Midjourney quota error_meta=" + common.SensitiveLogMeta(billingErr.Error()))
	}
	// PrepareMidjourneyTaskBilling installs the durable settlement intent before
	// this write. Use the request fence whenever that identity is present so an
	// ambiguous INSERT can be retried without creating a second local task.
	if strings.TrimSpace(midjourneyTask.BillingRequestId) != "" {
		err = midjourneyTask.InsertWithBillingFence()
	} else {
		err = midjourneyTask.Insert()
	}
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "insert_midjourney_task_failed")
	}
	billingApplied, billingErr := service.SettleMidjourneyTaskBilling(info, midjourneyTask, billingPrepared)
	if billingErr != nil {
		common.SysLog("error settling Midjourney quota error_meta=" + common.SensitiveLogMeta(billingErr.Error()))
	}
	if billingApplied {
		billingChannelId := midjourneyTask.GetBillingChannelId()
		tokenName := c.GetString("token_name")
		logContent := fmt.Sprintf("模型固定价格 %.2f，分组倍率 %.2f，操作 %s", priceData.ModelPrice, priceData.GroupRatioInfo.GroupRatio, constant.MjActionSwapFace)
		other := service.GenerateMjOtherInfo(info, priceData)
		model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
			ChannelId: billingChannelId,
			ModelName: modelName,
			TokenName: tokenName,
			Quota:     midjourneyTask.Quota,
			Content:   logContent,
			TokenId:   midjourneyTask.TokenId,
			Group:     info.UsingGroup,
			Other:     other,
		})
		// Durable settlement already journals the aggregate usage counters in its
		// companion operation. Keep the historical direct updates only for
		// unfenced legacy callers; applying them after a durable replay would
		// double-count usage.
		if strings.TrimSpace(midjourneyTask.BillingRequestId) == "" {
			model.UpdateUserUsedQuotaAndRequestCount(info.UserId, midjourneyTask.Quota)
			model.UpdateChannelUsedQuota(billingChannelId, midjourneyTask.Quota)
		}
	}
	c.Writer.WriteHeader(mjResp.StatusCode)
	respBody, err := common.Marshal(midjResponse)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "unmarshal_response_body_failed")
	}
	respBody = redactMidjourneyResponseBody(respBody, "")
	_, err = io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "copy_response_body_failed")
	}
	return nil
}

func RelayMidjourneyTaskImageSeed(c *gin.Context) *dto.MidjourneyResponse {
	taskId := c.Param("id")
	userId := c.GetInt("id")
	originTask := model.GetByMJId(userId, taskId)
	if originTask == nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_no_found")
	}
	channel, err := model.GetChannelById(originTask.ChannelId, true)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "get_channel_info_failed")
	}
	if channel.Status != common.ChannelStatusEnabled {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "该任务所属渠道已被禁用")
	}
	c.Set("channel_id", originTask.ChannelId)
	c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", channel.Key))

	requestURL := getMjRequestPath(c.Request.URL.String())
	fullRequestURL := fmt.Sprintf("%s%s", channel.GetBaseURL(), requestURL)
	midjResponseWithStatus, _, err := service.DoMidjourneyHttpRequest(c, time.Second*30, fullRequestURL)
	if err != nil {
		return &midjResponseWithStatus.Response
	}
	midjResponse := &midjResponseWithStatus.Response
	c.Writer.WriteHeader(midjResponseWithStatus.StatusCode)
	respBody, err := common.Marshal(midjResponse)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "unmarshal_response_body_failed")
	}
	respBody = redactMidjourneyResponseBody(respBody, taskId)
	service.IOCopyBytesGracefully(c, nil, respBody)
	return nil
}

func RelayMidjourneyTask(c *gin.Context, relayMode int) *dto.MidjourneyResponse {
	userId := c.GetInt("id")
	var err error
	var respBody []byte
	switch relayMode {
	case relayconstant.RelayModeMidjourneyTaskFetch:
		taskId := c.Param("id")
		originTask := model.GetByMJId(userId, taskId)
		if originTask == nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "task_no_found",
			}
		}
		midjourneyTask := coverMidjourneyTaskDto(c, originTask)
		respBody, err = common.Marshal(midjourneyTask)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "unmarshal_response_body_failed",
			}
		}
	case relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		var condition = struct {
			IDs []string `json:"ids"`
		}{}
		// Reuse the bounded body storage used by the other relay handlers. A
		// condition request is later converted into a database IN query, so also
		// reject an unreasonably large number of IDs or values that cannot match
		// the varchar task-id column.
		err = common.UnmarshalBodyReusable(c, &condition)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "do_request_failed",
			}
		}
		const (
			maxMidjourneyConditionIDs = 1000
			maxMidjourneyIDLength     = 191
		)
		if len(condition.IDs) > maxMidjourneyConditionIDs {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "too_many_task_ids",
			}
		}
		for i := range condition.IDs {
			condition.IDs[i] = strings.TrimSpace(condition.IDs[i])
			if condition.IDs[i] == "" || len(condition.IDs[i]) > maxMidjourneyIDLength {
				return &dto.MidjourneyResponse{
					Code:        4,
					Description: "invalid_task_id",
				}
			}
		}
		var tasks []dto.MidjourneyDto
		if len(condition.IDs) != 0 {
			originTasks := model.GetByMJIds(userId, condition.IDs)
			for _, originTask := range originTasks {
				midjourneyTask := coverMidjourneyTaskDto(c, originTask)
				tasks = append(tasks, midjourneyTask)
			}
		}
		if tasks == nil {
			tasks = make([]dto.MidjourneyDto, 0)
		}
		respBody, err = common.Marshal(tasks)
		if err != nil {
			return &dto.MidjourneyResponse{
				Code:        4,
				Description: "unmarshal_response_body_failed",
			}
		}
	}

	c.Writer.Header().Set("Content-Type", "application/json")

	_, err = io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "copy_response_body_failed",
		}
	}
	return nil
}

func RelayMidjourneySubmit(c *gin.Context, relayInfo *relaycommon.RelayInfo) *dto.MidjourneyResponse {
	consumeQuota := true
	var midjRequest dto.MidjourneyRequest
	err := common.UnmarshalBodyReusable(c, &midjRequest)
	if err != nil {
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "bind_request_body_failed")
	}

	relayInfo.InitChannelMeta(c)

	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyAction { // midjourney plus，需要从customId中获取任务信息
		mjErr := service.CoverPlusActionToNormalAction(&midjRequest)
		if mjErr != nil {
			return mjErr
		}
		relayInfo.RelayMode = relayconstant.RelayModeMidjourneyChange
	}
	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyVideo {
		midjRequest.Action = constant.MjActionVideo
	}

	if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyImagine { //绘画任务，此类任务可重复
		if midjRequest.Prompt == "" {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "prompt_is_required")
		}
		midjRequest.Action = constant.MjActionImagine
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyDescribe { //按图生文任务，此类任务可重复
		midjRequest.Action = constant.MjActionDescribe
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyEdits { //编辑任务，此类任务可重复
		midjRequest.Action = constant.MjActionEdits
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyShorten { //缩短任务，此类任务可重复，plus only
		midjRequest.Action = constant.MjActionShorten
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyBlend { //绘画任务，此类任务可重复
		midjRequest.Action = constant.MjActionBlend
	} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyUpload { //绘画任务，此类任务可重复
		midjRequest.Action = constant.MjActionUpload
	} else if midjRequest.TaskId != "" { //放大、变换任务，此类任务，如果重复且已有结果，远端api会直接返回最终结果
		mjId := ""
		if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyChange {
			if midjRequest.TaskId == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_id_is_required")
			} else if midjRequest.Action == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "action_is_required")
			} else if midjRequest.Index == 0 {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "index_is_required")
			}
			//action = midjRequest.Action
			mjId = midjRequest.TaskId
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneySimpleChange {
			if midjRequest.Content == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "content_is_required")
			}
			params := service.ConvertSimpleChangeParams(midjRequest.Content)
			if params == nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "content_parse_failed")
			}
			mjId = params.TaskId
			midjRequest.Action = params.Action
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyModal {
			//if midjRequest.MaskBase64 == "" {
			//	return service.MidjourneyErrorWrapper(constant.MjRequestError, "mask_base64_is_required")
			//}
			mjId = midjRequest.TaskId
			midjRequest.Action = constant.MjActionModal
		} else if relayInfo.RelayMode == relayconstant.RelayModeMidjourneyVideo {
			midjRequest.Action = constant.MjActionVideo
			if midjRequest.TaskId == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_id_is_required")
			} else if midjRequest.Action == "" {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "action_is_required")
			}
			mjId = midjRequest.TaskId
		}

		originTask := model.GetByMJId(relayInfo.UserId, mjId)
		if originTask == nil {
			return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_not_found")
		} else { //原任务的Status=SUCCESS，则可以做放大UPSCALE、变换VARIATION等动作，此时必须使用原来的请求地址才能正确处理
			if setting.GetMidjourneyConfig().ActionCheckSuccessEnabled {
				if originTask.Status != "SUCCESS" && relayInfo.RelayMode != relayconstant.RelayModeMidjourneyModal {
					return service.MidjourneyErrorWrapper(constant.MjRequestError, "task_status_not_success")
				}
			}
			channel, err := model.GetChannelById(originTask.ChannelId, true)
			if err != nil {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "get_channel_info_failed")
			}
			if channel.Status != common.ChannelStatusEnabled {
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "该任务所属渠道已被禁用")
			}
			c.Set("base_url", channel.GetBaseURL())
			c.Set("channel_id", originTask.ChannelId)
			c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", channel.Key))
			logger.LogDebug(c, "Midjourney action uses origin channel: id=%s, base_url=%s", strconv.Itoa(originTask.ChannelId), relaycommon.SanitizeURLForLog(channel.GetBaseURL()))
		}
		midjRequest.Prompt = originTask.Prompt

		//if channelType == common.ChannelTypeMidjourneyPlus {
		//	// plus
		//} else {
		//	// 普通版渠道
		//
		//}
	}

	if midjRequest.Action == constant.MjActionInPaint || midjRequest.Action == constant.MjActionCustomZoom {
		consumeQuota = false
	}

	//baseURL := common.ChannelBaseURLs[channelType]
	requestURL := getMjRequestPath(c.Request.URL.String())

	baseURL := c.GetString("base_url")

	//midjRequest.NotifyHook = "http://127.0.0.1:3000/mj/notify"

	fullRequestURL := fmt.Sprintf("%s%s", baseURL, requestURL)

	modelName := service.CovertMjpActionToModelName(midjRequest.Action)

	priceData, err := helper.ModelPriceHelperPerCall(c, relayInfo)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: common.MaskSensitiveInfo(err.Error()),
		}
	}

	userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: common.MaskSensitiveInfo(err.Error()),
		}
	}

	if consumeQuota && userQuota-priceData.Quota < 0 {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "quota_not_enough",
		}
	}
	// Journal the pre-provider boundary without touching any ledger. If this
	// request ID is already in flight, do not issue a second upstream submit:
	// Midjourney does not guarantee idempotency and a duplicate could create a
	// second billable job under the same local request.
	intentCreated, intentErr := service.PrepareMidjourneySubmitIntent(
		relayInfo,
		midjRequest.Action,
		c.GetInt("channel_id"),
		func() int {
			if consumeQuota {
				return priceData.Quota
			}
			return 0
		}(),
	)
	if intentErr != nil {
		common.SysLog("error creating Midjourney submit intent error_meta=" + common.SensitiveLogMeta(intentErr.Error()))
		return service.MidjourneyErrorWrapper(constant.MjRequestError, "request_already_in_flight")
	}

	midjResponseWithStatus, responseBody, err := service.DoMidjourneyHttpRequest(c, time.Second*60, fullRequestURL)
	if err != nil {
		// No authoritative provider result exists. Keep the intent pending for
		// manual reconciliation; never mark it accepted or charge automatically.
		return &midjResponseWithStatus.Response
	}
	midjResponse := &midjResponseWithStatus.Response

	// 文档：https://github.com/novicezk/midjourney-proxy/blob/main/docs/api.md
	//1-提交成功
	// 21-任务已存在（处理中或者有结果了） {"code":21,"description":"任务已存在","result":"0741798445574458","properties":{"status":"SUCCESS","imageUrl":"https://xxxx"}}
	// 22-排队中 {"code":22,"description":"排队中，前面还有1个任务","result":"0741798445574458","properties":{"numberOfQueues":1,"discordInstanceId":"1118138338562560102"}}
	// 23-队列已满，请稍后再试 {"code":23,"description":"队列已满，请稍后尝试","result":"14001929738841620","properties":{"discordInstanceId":"1118138338562560102"}}
	// 24-prompt包含敏感词 {"code":24,"description":"可能包含敏感词","properties":{"promptEn":"nude body","bannedWord":"nude"}}
	// other: 提交错误，description为错误描述
	midjourneyTask := &model.Midjourney{
		UserId:      relayInfo.UserId,
		Code:        midjResponse.Code,
		Action:      midjRequest.Action,
		MjId:        midjResponse.Result,
		Prompt:      midjRequest.Prompt,
		PromptEn:    "",
		Description: midjResponse.Description,
		State:       "",
		SubmitTime:  time.Now().UnixNano() / int64(time.Millisecond),
		StartTime:   0,
		FinishTime:  0,
		ImageUrl:    "",
		Status:      "",
		Progress:    "0%",
		FailReason:  "",
		ChannelId:   c.GetInt("channel_id"),
	}
	if midjResponse.Code == 3 {
		//无实例账号自动禁用渠道（No available account instance）
		channel, err := model.GetChannelById(midjourneyTask.ChannelId, true)
		if err != nil || channel == nil {
			if err != nil {
				common.SysLog("get_channel_null error_meta=" + common.SensitiveLogMeta(err.Error()))
			} else {
				common.SysLog("get_channel_null: channel lookup returned nil")
			}
		} else if channel.GetAutoBan() && common.GetGeneralRuntimeConfig().AutomaticDisableChannelEnabled {
			model.UpdateChannelStatus(midjourneyTask.ChannelId, "", 2, "No available account instance")
		}
	}
	if midjResponse.Code != 1 && midjResponse.Code != 21 && midjResponse.Code != 22 {
		//非1-提交成功,21-任务已存在和22-排队中，则记录错误原因
		midjourneyTask.FailReason = midjResponse.Description
		consumeQuota = false
	}

	if midjResponse.Code == 21 { //21-任务已存在（处理中或者有结果了）
		// 将 properties 转换为一个 map
		properties, ok := midjResponse.Properties.(map[string]interface{})
		if ok {
			imageUrl, ok1 := properties["imageUrl"].(string)
			status, ok2 := properties["status"].(string)
			if ok1 && ok2 {
				midjourneyTask.ImageUrl = imageUrl
				midjourneyTask.Status = status
				if status == "SUCCESS" {
					midjourneyTask.Progress = "100%"
					midjourneyTask.StartTime = time.Now().UnixNano() / int64(time.Millisecond)
					midjourneyTask.FinishTime = time.Now().UnixNano() / int64(time.Millisecond)
					midjResponse.Code = 1
				}
			}
		}
		//修改返回值
		if midjRequest.Action != constant.MjActionInPaint && midjRequest.Action != constant.MjActionCustomZoom {
			newBody := strings.Replace(string(responseBody), `"code":21`, `"code":1`, -1)
			responseBody = []byte(newBody)
		}
	}
	if midjResponse.Code == 1 && midjRequest.Action == "UPLOAD" {
		midjourneyTask.Progress = "100%"
		midjourneyTask.Status = "SUCCESS"
	}
	if intentCreated {
		accepted := midjResponseWithStatus.StatusCode == http.StatusOK &&
			consumeQuota && midjResponse.Code != 0 && midjResponse.Result != "" &&
			(midjResponse.Code == 1 || midjResponse.Code == 21 || midjResponse.Code == 22)
		if accepted {
			if err := service.AcceptMidjourneySubmitIntent(relayInfo, midjourneyTask, priceData.Quota); err != nil {
				common.SysLog("error accepting Midjourney submit intent error_meta=" + common.SensitiveLogMeta(err.Error()))
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_intent_failed")
			}
		} else if midjResponseWithStatus.StatusCode == http.StatusOK {
			if err := service.RejectMidjourneySubmitIntent(relayInfo, midjourneyTask.FailReason); err != nil {
				common.SysLog("error rejecting Midjourney submit intent error_meta=" + common.SensitiveLogMeta(err.Error()))
				return service.MidjourneyErrorWrapper(constant.MjRequestError, "midjourney_billing_intent_failed")
			}
		}
	}
	billingPrepared, billingErr := service.PrepareMidjourneyTaskBilling(
		relayInfo,
		midjourneyTask,
		priceData.Quota,
		consumeQuota && midjResponseWithStatus.StatusCode == http.StatusOK,
	)
	if billingErr != nil {
		common.SysLog("error consuming Midjourney quota error_meta=" + common.SensitiveLogMeta(billingErr.Error()))
	}
	if strings.TrimSpace(midjourneyTask.BillingRequestId) != "" {
		err = midjourneyTask.InsertWithBillingFence()
	} else {
		err = midjourneyTask.Insert()
	}
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "insert_midjourney_task_failed",
		}
	}
	billingApplied, billingErr := service.SettleMidjourneyTaskBilling(relayInfo, midjourneyTask, billingPrepared)
	if billingErr != nil {
		common.SysLog("error settling Midjourney quota error_meta=" + common.SensitiveLogMeta(billingErr.Error()))
	}
	if billingApplied {
		billingChannelId := midjourneyTask.GetBillingChannelId()
		tokenName := c.GetString("token_name")
		logContent := fmt.Sprintf("模型固定价格 %.2f，分组倍率 %.2f，操作 %s，ID %s", priceData.ModelPrice, priceData.GroupRatioInfo.GroupRatio, midjRequest.Action, midjResponse.Result)
		other := service.GenerateMjOtherInfo(relayInfo, priceData)
		model.RecordConsumeLog(c, relayInfo.UserId, model.RecordConsumeLogParams{
			ChannelId: billingChannelId,
			ModelName: modelName,
			TokenName: tokenName,
			Quota:     midjourneyTask.Quota,
			Content:   logContent,
			TokenId:   midjourneyTask.TokenId,
			Group:     relayInfo.UsingGroup,
			Other:     other,
		})
		if strings.TrimSpace(midjourneyTask.BillingRequestId) == "" {
			model.UpdateUserUsedQuotaAndRequestCount(relayInfo.UserId, midjourneyTask.Quota)
			model.UpdateChannelUsedQuota(billingChannelId, midjourneyTask.Quota)
		}
	}

	if midjResponse.Code == 22 { //22-排队中，说明任务已存在
		//修改返回值
		newBody := strings.Replace(string(responseBody), `"code":22`, `"code":1`, -1)
		responseBody = []byte(newBody)
	}
	responseBody = redactMidjourneyResponseBody(responseBody, midjourneyTask.MjId)
	//resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	bodyReader := io.NopCloser(bytes.NewBuffer(responseBody))

	//for k, v := range resp.Header {
	//	c.Writer.Header().Set(k, v[0])
	//}
	c.Writer.WriteHeader(midjResponseWithStatus.StatusCode)

	_, err = io.Copy(c.Writer, bodyReader)
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "copy_response_body_failed",
		}
	}
	err = bodyReader.Close()
	if err != nil {
		return &dto.MidjourneyResponse{
			Code:        4,
			Description: "close_response_body_failed",
		}
	}
	return nil
}

type taskChangeParams struct {
	ID     string
	Action string
	Index  int
}

func getMjRequestPath(path string) string {
	requestURL := path
	if strings.Contains(requestURL, "/mj-") {
		urls := strings.Split(requestURL, "/mj/")
		if len(urls) < 2 {
			return requestURL
		}
		requestURL = "/mj/" + urls[1]
	}
	return requestURL
}
