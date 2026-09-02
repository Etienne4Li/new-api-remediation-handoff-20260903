package hailuo

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
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
)

// https://platform.minimaxi.com/docs/api-reference/video-generation-intro
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

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) (taskErr *taskdto.TaskError) {
	if taskErr := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate); taskErr != nil {
		return taskErr
	}
	// Metadata is merged into VideoRequest after the common validator. Validate
	// the merged payload before pre-consume so duration/model overrides cannot
	// alter the provider request after billing has been reserved.
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	if _, err := a.convertToRequestPayload(&req, info); err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	return nil
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return fmt.Sprintf("%s%s", a.baseURL, TextToVideoEndpoint), nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	v, exists := c.Get("task_request")
	if !exists {
		return nil, fmt.Errorf("request not found in context")
	}
	req, ok := v.(relaycommon.TaskSubmitReq)
	if !ok {
		return nil, fmt.Errorf("invalid request type in context")
	}

	body, err := a.convertToRequestPayload(&req, info)
	if err != nil {
		return nil, errors.Wrap(err, "convert request payload failed")
	}

	data, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}

	return bytes.NewReader(data), nil
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
	_ = resp.Body.Close()

	var hResp VideoResponse
	if err := common.Unmarshal(responseBody, &hResp); err != nil {
		taskErr = service.TaskErrorWrapper(errors.Wrapf(err, "body_meta: %s", common.SensitiveLogBody(responseBody)), "unmarshal_response_body_failed", http.StatusInternalServerError)
		return
	}

	if hResp.BaseResp.StatusCode != StatusSuccess {
		taskErr = service.TaskErrorWrapper(
			fmt.Errorf("hailuo upstream task error: code=%d message_meta=%s", hResp.BaseResp.StatusCode, common.SensitiveLogMeta(hResp.BaseResp.StatusMsg)),
			strconv.Itoa(hResp.BaseResp.StatusCode),
			http.StatusBadRequest,
		)
		return
	}

	ov := dto.NewOpenAIVideo()
	ov.ID = info.PublicTaskID
	ov.TaskID = info.PublicTaskID
	ov.CreatedAt = time.Now().Unix()
	ov.Model = info.OriginModelName

	c.JSON(http.StatusOK, ov)
	return hResp.TaskID, responseBody, nil
}

func (a *TaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	return a.FetchTaskWithContext(context.Background(), baseUrl, key, body, proxy)
}

func (a *TaskAdaptor) FetchTaskWithContext(ctx context.Context, baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	taskID, ok := body["task_id"].(string)
	taskID = strings.TrimSpace(taskID)
	if !ok || taskID == "" {
		return nil, fmt.Errorf("invalid task_id")
	}

	uri, err := url.JoinPath(strings.TrimSpace(baseUrl), strings.TrimPrefix(QueryTaskEndpoint, "/"))
	if err != nil {
		return nil, fmt.Errorf("build query task URL failed: %w", err)
	}
	parsedURI, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("parse query task URL failed: %w", err)
	}
	query := parsedURI.Query()
	query.Set("task_id", taskID)
	parsedURI.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURI.String(), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
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

func (a *TaskAdaptor) convertToRequestPayload(req *relaycommon.TaskSubmitReq, info *relaycommon.RelayInfo) (*VideoRequest, error) {
	if req == nil {
		return nil, errors.New("request is nil")
	}
	if info == nil {
		return nil, errors.New("relay info is nil")
	}
	expectedModel := ""
	if info.ChannelMeta != nil {
		expectedModel = strings.TrimSpace(info.UpstreamModelName)
	}
	if expectedModel == "" {
		expectedModel = strings.TrimSpace(req.Model)
	}
	if expectedModel == "" {
		return nil, errors.New("model is required")
	}
	for key := range req.Metadata {
		if strings.EqualFold(strings.TrimSpace(key), "model") || strings.EqualFold(strings.TrimSpace(key), "model_name") {
			return nil, errors.New("can't change model with metadata")
		}
	}
	modelConfig := GetModelConfig(expectedModel)
	duration := DefaultDuration
	if req.Duration > 0 {
		duration = req.Duration
	} else if req.Duration < 0 {
		return nil, fmt.Errorf("duration must be between 1 and %d", relaycommon.MaxTaskDurationSeconds)
	} else if raw := strings.TrimSpace(req.Seconds); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid seconds: %w", err)
		}
		if seconds < 0 || seconds > relaycommon.MaxTaskDurationSeconds {
			return nil, fmt.Errorf("seconds must be between 1 and %d", relaycommon.MaxTaskDurationSeconds)
		}
		if seconds > 0 {
			duration = seconds
		}
	}
	// Even when duration is supplied, validate a conflicting seconds field so
	// an oversized representation cannot be hidden by precedence rules.
	if raw := strings.TrimSpace(req.Seconds); raw != "" && req.Duration != 0 {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 || seconds > relaycommon.MaxTaskDurationSeconds {
			return nil, fmt.Errorf("seconds must be between 1 and %d", relaycommon.MaxTaskDurationSeconds)
		}
	}
	if duration <= 0 || duration > relaycommon.MaxTaskDurationSeconds {
		return nil, fmt.Errorf("duration must be between 1 and %d", relaycommon.MaxTaskDurationSeconds)
	}
	resolution := modelConfig.DefaultResolution
	if req.Size != "" {
		resolution = a.parseResolutionFromSize(req.Size, modelConfig)
	}

	videoRequest := &VideoRequest{
		Model:      expectedModel,
		Prompt:     req.Prompt,
		Duration:   &duration,
		Resolution: resolution,
	}
	if err := taskcommon.UnmarshalMetadata(req.Metadata, videoRequest); err != nil {
		return nil, errors.Wrap(err, "unmarshal metadata to video request failed")
	}
	if videoRequest.Duration == nil || *videoRequest.Duration <= 0 || *videoRequest.Duration > relaycommon.MaxTaskDurationSeconds {
		return nil, fmt.Errorf("duration must be between 1 and %d", relaycommon.MaxTaskDurationSeconds)
	}
	if videoRequest.Model != expectedModel {
		return nil, errors.New("can't change model with metadata")
	}

	return videoRequest, nil
}

func (a *TaskAdaptor) parseResolutionFromSize(size string, modelConfig ModelConfig) string {
	switch {
	case strings.Contains(size, "1080"):
		return Resolution1080P
	case strings.Contains(size, "768"):
		return Resolution768P
	case strings.Contains(size, "720"):
		return Resolution720P
	case strings.Contains(size, "512"):
		return Resolution512P
	default:
		return modelConfig.DefaultResolution
	}
}

func (a *TaskAdaptor) ParseTaskResult(respBody []byte) (*relaycommon.TaskInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), service.TaskPollingRequestTimeout)
	defer cancel()
	return a.ParseTaskResultWithContext(ctx, respBody, "")
}

func (a *TaskAdaptor) ParseTaskResultWithContext(ctx context.Context, respBody []byte, proxy string) (*relaycommon.TaskInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resTask := QueryTaskResponse{}
	if err := common.Unmarshal(respBody, &resTask); err != nil {
		return nil, errors.Wrap(err, "unmarshal task result failed")
	}

	taskResult := relaycommon.TaskInfo{TaskID: resTask.TaskID}

	if resTask.BaseResp.StatusCode == StatusSuccess {
		taskResult.Code = 0
	} else {
		taskResult.Code = resTask.BaseResp.StatusCode
		taskResult.Reason = resTask.BaseResp.StatusMsg
		taskResult.Status = model.TaskStatusFailure
		taskResult.Progress = "100%"
		// The envelope error is authoritative. A non-zero code paired with a
		// nested `Status=Success` must never settle the task.
		if taskResult.Reason == "" {
			taskResult.Reason = "hailuo task failed"
		}
		return &taskResult, nil
	}

	switch resTask.Status {
	case TaskStatusPreparing, TaskStatusQueueing, TaskStatusProcessing:
		taskResult.Status = model.TaskStatusInProgress
		taskResult.Progress = "30%"
		if resTask.Status == TaskStatusProcessing {
			taskResult.Progress = "50%"
		}
	case TaskStatusSuccess:
		taskResult.Status = model.TaskStatusSuccess
		taskResult.Progress = "100%"
		videoURL, err := a.resolveVideoURL(ctx, resTask.FileID, proxy)
		if err != nil {
			return nil, err
		}
		taskResult.Url = videoURL
	case TaskStatusFailed:
		taskResult.Status = model.TaskStatusFailure
		taskResult.Progress = "100%"
		if taskResult.Reason == "" {
			taskResult.Reason = "task failed"
		}
	default:
		taskResult.Status = model.TaskStatusInProgress
		taskResult.Progress = "30%"
	}

	return &taskResult, nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(originTask *model.Task) ([]byte, error) {
	var hailuoResp QueryTaskResponse
	if err := common.Unmarshal(originTask.Data, &hailuoResp); err != nil {
		return nil, errors.Wrap(err, "unmarshal hailuo task data failed")
	}

	openAIVideo := originTask.ToOpenAIVideo()
	if hailuoResp.BaseResp.StatusCode != StatusSuccess {
		openAIVideo.Error = &dto.OpenAIVideoError{
			Message: hailuoResp.BaseResp.StatusMsg,
			Code:    strconv.Itoa(hailuoResp.BaseResp.StatusCode),
		}
	}

	jsonData, err := common.Marshal(openAIVideo)
	if err != nil {
		return nil, errors.Wrap(err, "marshal openai video failed")
	}

	return jsonData, nil
}

func (a *TaskAdaptor) resolveVideoURL(ctx context.Context, fileID, proxy string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" || len(fileID) > 4096 {
		return "", errors.New("invalid file_id")
	}
	if strings.TrimSpace(a.apiKey) == "" || strings.TrimSpace(a.baseURL) == "" {
		return "", errors.New("hailuo credentials are not initialized")
	}

	retrieveURL, err := url.JoinPath(strings.TrimSpace(a.baseURL), "v1/files/retrieve")
	if err != nil {
		return "", fmt.Errorf("build retrieve file URL failed: %w", err)
	}
	parsedURL, err := url.Parse(retrieveURL)
	if err != nil {
		return "", fmt.Errorf("parse retrieve file URL failed: %w", err)
	}
	query := parsedURL.Query()
	query.Set("file_id", fileID)
	parsedURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return "", fmt.Errorf("new proxy http client failed: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("retrieve file returned status %d", resp.StatusCode)
	}

	var retrieveResp RetrieveFileResponse
	if err := common.Unmarshal(responseBody, &retrieveResp); err != nil {
		return "", err
	}

	if retrieveResp.BaseResp.StatusCode != StatusSuccess {
		return "", fmt.Errorf("retrieve file failed: code=%d message_meta=%s", retrieveResp.BaseResp.StatusCode, common.SensitiveLogMeta(retrieveResp.BaseResp.StatusMsg))
	}

	downloadURL := strings.TrimSpace(retrieveResp.File.DownloadURL)
	if err := common.ValidateHTTPURL(downloadURL); err != nil {
		return "", fmt.Errorf("invalid retrieve file download URL: %w", err)
	}
	return downloadURL, nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func containsInt(slice []int, item int) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
