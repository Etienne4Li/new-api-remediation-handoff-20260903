package sora

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/tidwall/sjson"
)

// ============================
// Request / Response structures
// ============================

type ContentItem struct {
	Type     string    `json:"type"`                // "text" or "image_url"
	Text     string    `json:"text,omitempty"`      // for text type
	ImageURL *ImageURL `json:"image_url,omitempty"` // for image_url type
}

type ImageURL struct {
	URL string `json:"url"`
}

type responseTask struct {
	ID                 string `json:"id"`
	TaskID             string `json:"task_id,omitempty"` //兼容旧接口
	Object             string `json:"object"`
	Model              string `json:"model"`
	Status             string `json:"status"`
	Progress           int    `json:"progress"`
	CreatedAt          int64  `json:"created_at"`
	CompletedAt        int64  `json:"completed_at,omitempty"`
	ExpiresAt          int64  `json:"expires_at,omitempty"`
	Seconds            string `json:"seconds,omitempty"`
	Size               string `json:"size,omitempty"`
	RemixedFromVideoID string `json:"remixed_from_video_id,omitempty"`
	Error              *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// ============================
// Adaptor implementation
// ============================

type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
	apiKey      string
	baseURL     string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
	a.apiKey = info.ApiKey
}

func validateRemixRequest(c *gin.Context) *dto.TaskError {
	var req relaycommon.TaskSubmitReq
	if err := common.UnmarshalBodyReusable(c, &req); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return service.TaskErrorWrapperLocal(fmt.Errorf("field prompt is required"), "invalid_request", http.StatusBadRequest)
	}
	if taskErr := relaycommon.ValidateTaskDurationBounds(req); taskErr != nil {
		return taskErr
	}
	// 存储原始请求到 context，与 ValidateMultipartDirect 路径保持一致
	c.Set("task_request", req)
	return nil
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) (taskErr *dto.TaskError) {
	if info.Action == constant.TaskActionRemix {
		return validateRemixRequest(c)
	}
	return relaycommon.ValidateMultipartDirect(c, info)
}

// EstimateBilling 根据用户请求的 seconds 和 size 计算 OtherRatios。
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	// remix 路径的 OtherRatios 已在 ResolveOriginTask 中设置
	if info.Action == constant.TaskActionRemix {
		return nil
	}

	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}

	seconds, err := strconv.Atoi(strings.TrimSpace(req.Seconds))
	if err != nil || seconds <= 0 {
		seconds = req.Duration
	}
	if seconds <= 0 {
		seconds = 4
	}
	// Keep this billing path defensive when called outside the normal request
	// validator (e.g. retries or direct tests). The same bound is enforced by
	// ValidateTaskDurationBounds before submission.
	if seconds > relaycommon.MaxTaskDurationSeconds {
		seconds = relaycommon.MaxTaskDurationSeconds
	}

	size := req.Size
	if size == "" {
		size = "720x1280"
	}

	ratios := map[string]float64{
		"seconds": float64(seconds),
		"size":    1,
	}
	if size == "1792x1024" || size == "1024x1792" {
		ratios["size"] = 1.666667
	}
	return ratios
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.Action == constant.TaskActionRemix {
		return fmt.Sprintf("%s/v1/videos/%s/remix", a.baseURL, info.OriginTaskID), nil
	}
	return fmt.Sprintf("%s/v1/videos", a.baseURL), nil
}

// BuildRequestHeader sets required headers.
func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, errors.Wrap(err, "get_request_body_failed")
	}
	cachedBody, err := storage.Bytes()
	if err != nil {
		return nil, errors.Wrap(err, "read_body_bytes_failed")
	}
	contentType := c.GetHeader("Content-Type")

	if strings.HasPrefix(contentType, "application/json") {
		var bodyMap map[string]interface{}
		if err := common.Unmarshal(cachedBody, &bodyMap); err == nil {
			stripSoraCallbackFields(bodyMap)
			bodyMap["model"] = info.UpstreamModelName
			if newBody, err := common.Marshal(bodyMap); err == nil {
				return bytes.NewReader(newBody), nil
			}
		}
		return bytes.NewReader(cachedBody), nil
	}

	if strings.Contains(contentType, "multipart/form-data") {
		formData, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return nil, fmt.Errorf("parse multipart form failed: %w", err)
		}
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		if err := writer.WriteField("model", info.UpstreamModelName); err != nil {
			return nil, fmt.Errorf("write model field failed: %w", err)
		}
		for key, values := range formData.Value {
			if key == "model" || isSoraCallbackField(key) {
				continue
			}
			for _, v := range values {
				writer.WriteField(key, v)
			}
		}
		var totalFileBytes int64
		for fieldName, fileHeaders := range formData.File {
			// Sora currently accepts only the input_reference media field. Do
			// not blindly proxy arbitrary multipart files or their caller-supplied
			// headers into the upstream request.
			if fieldName != "input_reference" {
				return nil, fmt.Errorf("unsupported multipart file field %q", fieldName)
			}
			for _, fh := range fileHeaders {
				if fh == nil {
					return nil, errors.New("input_reference file is missing")
				}
				maxFileBytes := common.GetMaxFileDownloadBytes()
				if fh.Size < 0 || fh.Size > maxFileBytes || totalFileBytes > maxFileBytes-fh.Size {
					return nil, fmt.Errorf("input_reference file exceeds maximum allowed size of %d bytes", maxFileBytes)
				}
				f, err := fh.Open()
				if err != nil {
					return nil, fmt.Errorf("open input_reference file failed: %w", err)
				}
				fileBytes, readErr := common.ReadBodyLimited(f, fh.Size, maxFileBytes)
				_ = f.Close()
				if readErr != nil {
					if errors.Is(readErr, common.ErrRequestBodyTooLarge) {
						return nil, fmt.Errorf("input_reference file exceeds maximum allowed size of %d bytes", maxFileBytes)
					}
					return nil, fmt.Errorf("read input_reference file failed: %w", readErr)
				}
				ct, mimeErr := resolveSoraUploadMIME(fileBytes)
				if mimeErr != nil {
					return nil, mimeErr
				}
				totalFileBytes += int64(len(fileBytes))
				filename := "input_reference." + soraMIMEExtension(ct)
				h := make(textproto.MIMEHeader)
				disposition := mime.FormatMediaType("form-data", map[string]string{
					"name":     fieldName,
					"filename": filename,
				})
				if disposition == "" {
					return nil, errors.New("format multipart disposition failed")
				}
				h.Set("Content-Disposition", disposition)
				h.Set("Content-Type", ct)
				part, err := writer.CreatePart(h)
				if err != nil {
					return nil, fmt.Errorf("create input_reference part failed: %w", err)
				}
				if _, err := part.Write(fileBytes); err != nil {
					return nil, fmt.Errorf("write input_reference part failed: %w", err)
				}
			}
		}
		if err := writer.Close(); err != nil {
			return nil, fmt.Errorf("close multipart writer failed: %w", err)
		}
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return &buf, nil
	}

	return common.NewReplayableBodyReader(storage), nil
}

// isSoraCallbackField recognizes callback URL aliases regardless of case or
// whether the provider-facing name uses an underscore or hyphen separator.
func isSoraCallbackField(key string) bool {
	normalized := strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", "")
	return strings.EqualFold(normalized, "callbackurl")
}

// stripSoraCallbackFields removes provider callback controls from arbitrary
// JSON objects, including nested metadata, before the request is forwarded.
func stripSoraCallbackFields(value any) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if isSoraCallbackField(key) {
				delete(typed, key)
				continue
			}
			stripSoraCallbackFields(child)
		}
	case []interface{}:
		for _, child := range typed {
			stripSoraCallbackFields(child)
		}
	}
}

// resolveSoraUploadMIME derives a safe media type from the uploaded bytes.
// Sora input_reference accepts either an image or a video; declarations in the
// multipart header and filename are intentionally ignored.
func resolveSoraUploadMIME(data []byte) (string, error) {
	if mimeType, err := service.ResolveImageMIME(data); err == nil {
		return mimeType, nil
	}
	if mimeType := service.SniffVideoMIME(data); service.IsSafeVideoMIME(mimeType) {
		return mimeType, nil
	}
	return "", fmt.Errorf("unsupported input_reference media type")
}

func soraMIMEExtension(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "video/mp4":
		return "mp4"
	case "video/quicktime":
		return "mov"
	case "video/webm":
		return "webm"
	case "video/mpeg":
		return "mpeg"
	default:
		return "media"
	}
}

// DoRequest delegates to common helper.
func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

// DoResponse handles upstream response, returns taskID etc.
func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}
	_ = resp.Body.Close()

	// Parse Sora response
	var dResp responseTask
	if err := common.Unmarshal(responseBody, &dResp); err != nil {
		taskErr = service.TaskErrorWrapper(errors.Wrapf(err, "body_meta: %s", common.SensitiveLogBody(responseBody)), "unmarshal_response_body_failed", http.StatusInternalServerError)
		return
	}

	upstreamID := dResp.ID
	if upstreamID == "" {
		upstreamID = dResp.TaskID
	}
	if upstreamID == "" {
		taskErr = service.TaskErrorWrapper(fmt.Errorf("task_id is empty"), "invalid_response", http.StatusInternalServerError)
		return
	}

	// 使用公开 task_xxxx ID 返回给客户端
	dResp.ID = info.PublicTaskID
	dResp.TaskID = info.PublicTaskID
	c.JSON(http.StatusOK, dResp)
	return upstreamID, responseBody, nil
}

// FetchTask fetch task status
func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	return a.FetchTaskWithContext(context.Background(), baseUrl, key, body, proxy)
}

func (a *TaskAdaptor) FetchTaskWithContext(ctx context.Context, baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid task_id")
	}
	escapedTaskID, err := taskcommon.EscapeTaskIDPathSegment(taskID)
	if err != nil {
		return nil, fmt.Errorf("invalid task_id: %w", err)
	}

	uri := fmt.Sprintf("%s/v1/videos/%s", strings.TrimRight(baseUrl, "/"), escapedTaskID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) GetModelList() []string {
	return ModelList
}

func (a *TaskAdaptor) GetChannelName() string {
	return ChannelName
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	resTask := responseTask{}
	if err := common.Unmarshal(respBody, &resTask); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}

	taskResult := relaycommon.TaskInfo{
		Code: 0,
	}
	taskResult.TaskID = resTask.ID
	if taskResult.TaskID == "" {
		taskResult.TaskID = resTask.TaskID
	}

	switch resTask.Status {
	case "queued", "pending":
		taskResult.Status = model.TaskStatusQueued
	case "processing", "in_progress":
		taskResult.Status = model.TaskStatusInProgress
	case "completed":
		taskResult.Status = model.TaskStatusSuccess
		// Url intentionally left empty — the caller constructs the proxy URL using the public task ID
	case "failed", "cancelled":
		taskResult.Status = model.TaskStatusFailure
		if resTask.Error != nil {
			taskResult.Reason = resTask.Error.Message
		} else {
			taskResult.Reason = "task failed"
		}
	default:
	}
	if resTask.Progress > 0 && resTask.Progress < 100 {
		taskResult.Progress = fmt.Sprintf("%d%%", resTask.Progress)
	}

	return &taskResult, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	data := task.Data
	var err error
	if data, err = sjson.SetBytes(data, "id", task.TaskID); err != nil {
		return nil, errors.Wrap(err, "set id failed")
	}
	return data, nil
}
