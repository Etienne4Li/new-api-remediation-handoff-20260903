package vidu

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"

	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/pkg/errors"
)

// ============================
// Request / Response structures
// ============================

type requestPayload struct {
	Model             string   `json:"model"`
	Images            []string `json:"images"`
	Prompt            string   `json:"prompt,omitempty"`
	Duration          int      `json:"duration,omitempty"`
	Seed              int      `json:"seed,omitempty"`
	Resolution        string   `json:"resolution,omitempty"`
	MovementAmplitude string   `json:"movement_amplitude,omitempty"`
	Bgm               bool     `json:"bgm,omitempty"`
	Payload           string   `json:"payload,omitempty"`
	CallbackUrl       string   `json:"callback_url,omitempty"`
}

type responsePayload struct {
	TaskId            string   `json:"task_id"`
	State             string   `json:"state"`
	Model             string   `json:"model"`
	Images            []string `json:"images"`
	Prompt            string   `json:"prompt"`
	Duration          int      `json:"duration"`
	Seed              int      `json:"seed"`
	Resolution        string   `json:"resolution"`
	Bgm               bool     `json:"bgm"`
	MovementAmplitude string   `json:"movement_amplitude"`
	Payload           string   `json:"payload"`
	CreatedAt         string   `json:"created_at"`
}

type taskResultResponse struct {
	State     string     `json:"state"`
	ErrCode   string     `json:"err_code"`
	Credits   int        `json:"credits"`
	Payload   string     `json:"payload"`
	Creations []creation `json:"creations"`
}

type creation struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	CoverURL string `json:"cover_url"`
}

// ============================
// Adaptor implementation
// ============================

type TaskAdaptor struct {
	taskcommon.BaseBilling
	ChannelType int
	baseURL     string
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType
	a.baseURL = info.ChannelBaseUrl
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	if err := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate); err != nil {
		return err
	}
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return service.TaskErrorWrapper(err, "get_task_request_failed", http.StatusBadRequest)
	}
	// Metadata is merged into the provider payload after the shared request
	// validator. Validate that merged representation here, before pre-consume,
	// so metadata.duration cannot bypass the billing/resource boundary.
	if _, err := a.convertToRequestPayload(&req, info); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	action := constant.TaskActionTextGenerate
	if meatAction, ok := req.Metadata["action"]; ok {
		action, _ = meatAction.(string)
	} else if req.HasImage() {
		action = constant.TaskActionGenerate
		if info.ChannelType == constant.ChannelTypeVidu {
			// vidu 增加 首尾帧生视频和参考图生视频
			if len(req.Images) == 2 {
				action = constant.TaskActionFirstTailGenerate
			} else if len(req.Images) > 2 {
				action = constant.TaskActionReferenceGenerate
			}
		}
	}
	info.Action = action
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	v, exists := c.Get("task_request")
	if !exists {
		return nil, fmt.Errorf("request not found in context")
	}
	req := v.(relaycommon.TaskSubmitReq)

	body, err := a.convertToRequestPayload(&req, info)
	if err != nil {
		return nil, err
	}

	if info.Action == constant.TaskActionReferenceGenerate {
		if strings.Contains(body.Model, "viduq2") {
			// 参考图生视频只能用 viduq2 模型, 不能带有pro或turbo后缀 https://platform.vidu.cn/docs/reference-to-video
			body.Model = "viduq2"
		}
	}

	data, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	var path string
	switch info.Action {
	case constant.TaskActionGenerate:
		path = "/img2video"
	case constant.TaskActionFirstTailGenerate:
		path = "/start-end2video"
	case constant.TaskActionReferenceGenerate:
		path = "/reference2video"
	default:
		path = "/text2video"
	}
	return fmt.Sprintf("%s/ent/v2%s", a.baseURL, path), nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+info.ApiKey)
	return nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *taskdto.TaskError) {
	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}

	var vResp responsePayload
	err = common.Unmarshal(responseBody, &vResp)
	if err != nil {
		taskErr = service.TaskErrorWrapper(errors.Wrap(err, fmt.Sprintf("body_meta: %s", common.SensitiveLogBody(responseBody))), "unmarshal_response_failed", http.StatusInternalServerError)
		return
	}

	if vResp.State == "failed" {
		taskErr = service.TaskErrorWrapperLocal(fmt.Errorf("task failed"), "task_failed", http.StatusBadRequest)
		return
	}

	ov := dto.NewOpenAIVideo()
	ov.ID = info.PublicTaskID
	ov.TaskID = info.PublicTaskID
	ov.CreatedAt = time.Now().Unix()
	ov.Model = info.OriginModelName
	c.JSON(http.StatusOK, ov)
	return vResp.TaskId, responseBody, nil
}

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

	url := fmt.Sprintf("%s/ent/v2/tasks/%s/creations", strings.TrimRight(baseUrl, "/"), escapedTaskID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+key)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func (a *TaskAdaptor) GetModelList() []string {
	return []string{"viduq2", "viduq1", "vidu2.0", "vidu1.5"}
}

func (a *TaskAdaptor) GetChannelName() string {
	return "vidu"
}

// ============================
// helpers
// ============================

func (a *TaskAdaptor) convertToRequestPayload(req *relaycommon.TaskSubmitReq, info *relaycommon.RelayInfo) (*requestPayload, error) {
	if req == nil {
		return nil, errors.New("request is nil")
	}
	if info == nil {
		return nil, errors.New("relay info is nil")
	}
	for key := range req.Metadata {
		if strings.EqualFold(strings.TrimSpace(key), "model") || strings.EqualFold(strings.TrimSpace(key), "model_name") {
			return nil, errors.New("can't change model with metadata")
		}
	}
	duration := req.Duration
	if strings.TrimSpace(req.Seconds) != "" {
		seconds, err := strconv.Atoi(strings.TrimSpace(req.Seconds))
		if err != nil {
			return nil, fmt.Errorf("invalid seconds: %w", err)
		}
		if seconds < 0 || seconds > relaycommon.MaxTaskDurationSeconds {
			return nil, fmt.Errorf("seconds must be between 1 and %d", relaycommon.MaxTaskDurationSeconds)
		}
		if duration == 0 {
			duration = seconds
		}
	}
	if duration < 0 || duration > relaycommon.MaxTaskDurationSeconds {
		return nil, fmt.Errorf("duration must be between 1 and %d", relaycommon.MaxTaskDurationSeconds)
	}
	if duration == 0 {
		duration = 5
	}
	r := requestPayload{
		Model:             taskcommon.DefaultString(info.UpstreamModelName, "viduq1"),
		Images:            req.Images,
		Prompt:            req.Prompt,
		Duration:          duration,
		Resolution:        taskcommon.DefaultString(req.Size, "1080p"),
		MovementAmplitude: "auto",
		Bgm:               false,
	}
	if err := taskcommon.UnmarshalMetadata(req.Metadata, &r); err != nil {
		return nil, errors.Wrap(err, "unmarshal metadata failed")
	}
	if r.Duration <= 0 || r.Duration > relaycommon.MaxTaskDurationSeconds {
		return nil, fmt.Errorf("duration must be between 1 and %d", relaycommon.MaxTaskDurationSeconds)
	}
	// The channel-selected model is authoritative. UnmarshalMetadata filters
	// model aliases, but retain an explicit invariant for future decoder changes.
	expectedModel := taskcommon.DefaultString(info.UpstreamModelName, "viduq1")
	if r.Model != expectedModel {
		return nil, errors.New("can't change model with metadata")
	}
	if err := validateViduImageReferences(r.Images); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.CallbackUrl) != "" {
		return nil, errors.New("callback_url is not supported")
	}
	return &r, nil
}

func validateViduImageReferences(images []string) error {
	for index := range images {
		trimmed := strings.TrimSpace(images[index])
		if trimmed == "" {
			return fmt.Errorf("images[%d] is empty", index)
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
			images[index] = trimmed
			continue
		}
		parsed, err := url.Parse(trimmed)
		if err == nil && parsed.Scheme == "" && parsed.Host == "" {
			// Vidu also accepts raw base64 image data.
			images[index] = trimmed
			continue
		}
		if err := common.ValidateHTTPURL(trimmed); err != nil {
			return fmt.Errorf("images[%d]: %w", index, err)
		}
		images[index] = trimmed
	}
	return nil
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	taskInfo := &relaycommon.TaskInfo{}

	var taskResp taskResultResponse
	err := common.Unmarshal(respBody, &taskResp)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal response body")
	}

	state := taskResp.State
	switch state {
	case "created", "queueing":
		taskInfo.Status = model.TaskStatusSubmitted
	case "processing":
		taskInfo.Status = model.TaskStatusInProgress
	case "success":
		taskInfo.Status = model.TaskStatusSuccess
		if len(taskResp.Creations) > 0 {
			if normalized, err := taskcommon.NormalizeTaskResultURL(taskResp.Creations[0].URL); err != nil {
				return nil, fmt.Errorf("invalid vidu video URL: %w", err)
			} else {
				taskInfo.Url = normalized
			}
		}
	case "failed":
		taskInfo.Status = model.TaskStatusFailure
		if taskResp.ErrCode != "" {
			taskInfo.Reason = taskResp.ErrCode
		}
	default:
		return nil, fmt.Errorf("unknown task state: %s", state)
	}

	return taskInfo, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(originTask *model.Task) ([]byte, error) {
	var viduResp taskResultResponse
	if err := common.Unmarshal(originTask.Data, &viduResp); err != nil {
		return nil, errors.Wrap(err, "unmarshal vidu task data failed")
	}

	openAIVideo := dto.NewOpenAIVideo()
	openAIVideo.ID = originTask.TaskID
	openAIVideo.Status = originTask.Status.ToVideoStatus()
	openAIVideo.SetProgressStr(originTask.Progress)
	openAIVideo.CreatedAt = originTask.CreatedAt
	openAIVideo.CompletedAt = originTask.UpdatedAt

	if len(viduResp.Creations) > 0 && viduResp.Creations[0].URL != "" {
		openAIVideo.SetMetadata("url", viduResp.Creations[0].URL)
	}

	if viduResp.State == "failed" && viduResp.ErrCode != "" {
		openAIVideo.Error = &dto.OpenAIVideoError{
			Message: viduResp.ErrCode,
			Code:    viduResp.ErrCode,
		}
	}

	return common.Marshal(openAIVideo)
}
