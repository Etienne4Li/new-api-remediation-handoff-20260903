package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type TaskSubmitResult struct {
	UpstreamTaskID string
	TaskData       []byte
	Platform       constant.TaskPlatform
	Quota          int
	//PerCallPrice   types.PriceData
}

// ResolveOriginTask 处理基于已有任务的提交（remix / continuation）：
// 查找原始任务、从中提取模型名称、将渠道锁定到原始任务的渠道
// （通过 info.LockedChannel，重试时复用同一渠道并轮换 key），
// 以及提取 OtherRatios（时长、分辨率）。
// 该函数在控制器的重试循环之前调用一次，其结果通过 info 字段和上下文持久化。
func ResolveOriginTask(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	// 检测 remix action
	path := c.Request.URL.Path
	if strings.Contains(path, "/v1/videos/") && strings.HasSuffix(path, "/remix") {
		info.Action = constant.TaskActionRemix
	}

	// 提取 remix 任务的 video_id
	if info.Action == constant.TaskActionRemix {
		videoID := c.Param("video_id")
		if strings.TrimSpace(videoID) == "" {
			return service.TaskErrorWrapperLocal(fmt.Errorf("video_id is required"), "invalid_request", http.StatusBadRequest)
		}
		info.OriginTaskID = videoID
	}

	if info.OriginTaskID == "" {
		return nil
	}

	// 查找原始任务
	originTask, exist, err := model.GetByTaskId(info.UserId, info.OriginTaskID)
	if err != nil {
		return service.TaskErrorWrapper(err, "get_origin_task_failed", http.StatusInternalServerError)
	}
	if !exist {
		return service.TaskErrorWrapperLocal(errors.New("task_origin_not_exist"), "task_not_exist", http.StatusBadRequest)
	}

	// 从原始任务推导模型名称
	if info.OriginModelName == "" {
		if originTask.Properties.OriginModelName != "" {
			info.OriginModelName = originTask.Properties.OriginModelName
		} else if originTask.Properties.UpstreamModelName != "" {
			info.OriginModelName = originTask.Properties.UpstreamModelName
		} else {
			var taskData map[string]interface{}
			_ = common.Unmarshal(originTask.Data, &taskData)
			if m, ok := taskData["model"].(string); ok && m != "" {
				info.OriginModelName = m
			}
		}
	}

	// 锁定到原始任务的渠道（重试时复用同一渠道，轮换 key）
	ch, err := model.GetChannelById(originTask.ChannelId, true)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "channel_not_found", http.StatusBadRequest)
	}
	if ch.Status != common.ChannelStatusEnabled {
		return service.TaskErrorWrapperLocal(errors.New("the channel of the origin task is disabled"), "task_channel_disable", http.StatusBadRequest)
	}
	info.LockedChannel = ch

	if originTask.ChannelId != info.ChannelId {
		key, _, newAPIError := ch.GetNextEnabledKey()
		if newAPIError != nil {
			return service.TaskErrorWrapper(newAPIError, "channel_no_available_key", newAPIError.StatusCode)
		}
		common.SetContextKey(c, constant.ContextKeyChannelKey, key)
		common.SetContextKey(c, constant.ContextKeyChannelType, ch.Type)
		common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, ch.GetBaseURL())
		common.SetContextKey(c, constant.ContextKeyChannelId, originTask.ChannelId)

		info.ChannelBaseUrl = ch.GetBaseURL()
		info.ChannelId = originTask.ChannelId
		info.ChannelType = ch.Type
		info.ApiKey = key
	}

	// 提取 remix 参数（时长、分辨率 → OtherRatios）
	if info.Action == constant.TaskActionRemix {
		if originTask.PrivateData.BillingContext != nil {
			// 新的 remix 逻辑：直接从原始任务的 BillingContext 中提取 OtherRatios（如果存在）
			for s, f := range originTask.PrivateData.BillingContext.OtherRatios {
				info.PriceData.AddOtherRatio(s, f)
			}
		} else {
			// 旧的 remix 逻辑：直接从 task data 解析 seconds 和 size（如果存在）
			var taskData map[string]interface{}
			_ = common.Unmarshal(originTask.Data, &taskData)
			secondsStr, _ := taskData["seconds"].(string)
			seconds, _ := strconv.Atoi(secondsStr)
			if seconds <= 0 {
				seconds = 4
			}
			// 历史任务数据可能包含未经校验的时长，作为计费乘数前必须钳制
			if seconds > relaycommon.MaxTaskDurationSeconds {
				seconds = relaycommon.MaxTaskDurationSeconds
			}
			sizeStr, _ := taskData["size"].(string)
			info.PriceData.AddOtherRatio("seconds", float64(seconds))
			info.PriceData.AddOtherRatio("size", 1)
			if sizeStr == "1792x1024" || sizeStr == "1024x1792" {
				info.PriceData.AddOtherRatio("size", 1.666667)
			}
		}
	}

	return nil
}

// RelayTaskSubmit 完成 task 提交的全部流程（每次尝试调用一次）：
// 刷新渠道元数据 → 确定 platform/adaptor → 验证请求 →
// 估算计费(EstimateBilling) → 计算价格 → 预扣费（仅首次）→
// 构建/发送/解析上游请求 → 提交后计费调整(AdjustBillingOnSubmit)。
// 控制器负责 defer Refund 和成功后 Settle。
func RelayTaskSubmit(c *gin.Context, info *relaycommon.RelayInfo) (*TaskSubmitResult, *dto.TaskError) {
	info.InitChannelMeta(c)

	// 1. 确定 platform → 创建适配器 → 验证请求
	platform := constant.TaskPlatform(c.GetString("platform"))
	if platform == "" {
		platform = GetTaskPlatform(c)
	}
	adaptor := GetTaskAdaptor(platform)
	if adaptor == nil {
		return nil, service.TaskErrorWrapperLocal(fmt.Errorf("invalid api platform: %s", platform), "invalid_api_platform", http.StatusBadRequest)
	}
	adaptor.Init(info)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
		return nil, taskErr
	}

	// 2. 确定模型名称
	modelName := info.OriginModelName
	if modelName == "" {
		modelName = service.CoverTaskActionToModelName(platform, info.Action)
	}

	// 2.5 应用渠道的模型映射（与同步任务对齐）
	info.OriginModelName = modelName
	info.UpstreamModelName = modelName
	if err := helper.ModelMappedHelper(c, info, nil); err != nil {
		return nil, service.TaskErrorWrapperLocal(err, "model_mapping_failed", http.StatusBadRequest)
	}

	// 3. 预生成公开 task ID（仅首次）
	if info.PublicTaskID == "" {
		info.PublicTaskID = model.GenerateTaskID()
	}

	// 4. 价格计算：基础模型价格
	info.OriginModelName = modelName
	priceData, err := helper.ModelPriceHelperPerCall(c, info)
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "model_price_error", http.StatusBadRequest)
	}
	info.SetPriceDataSnapshot(priceData)

	// 5. 计费估算：让适配器根据用户请求提供 OtherRatios（时长、分辨率等）
	//    必须在 ModelPriceHelperPerCall 之后调用（它会重建 PriceData）。
	//    ResolveOriginTask 可能已在 remix 路径中预设了 OtherRatios，此处合并。
	if estimatedRatios := adaptor.EstimateBilling(c, info); len(estimatedRatios) > 0 {
		for k, v := range estimatedRatios {
			info.PriceData.AddOtherRatio(k, v)
		}
	}

	// 6. 将 OtherRatios 应用到基础额度（饱和转换，防止溢出成负数）
	if !common.StringsContains(constant.TaskPricePatches, modelName) {
		quotaWithRatios := info.PriceData.ApplyOtherRatiosToFloat(float64(info.PriceData.Quota))
		quota, clamp := common.QuotaFromFloatChecked(quotaWithRatios)
		info.PriceData.Quota = quota
		noteTaskQuotaClamp(info, clamp)
	}

	// 7. 预扣费（仅首次 — 重试时 info.Billing 已存在，跳过）
	if info.Billing == nil && !info.PriceData.FreeModel {
		info.ForcePreConsume = true
		if apiErr := service.PreConsumeBilling(c, info.PriceData.Quota, info); apiErr != nil {
			return nil, service.TaskErrorFromAPIError(apiErr)
		}
	}

	// 8. 构建请求体
	requestBody, err := adaptor.BuildRequestBody(c, info)
	if err != nil {
		// Request-body construction only consumes client-controlled input. A
		// malformed media reference, metadata value, or unsupported option is a
		// local validation failure and must not be retried against other channels
		// as if the provider had failed.
		return nil, service.TaskErrorWrapperLocal(err, "build_request_failed", http.StatusBadRequest)
	}

	// 9. 发送请求
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if resp != nil && resp.StatusCode != http.StatusOK {
		responseBody, readErr := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
		service.CloseResponseBodyGracefully(resp)
		if readErr != nil {
			return nil, service.TaskErrorWrapper(readErr, "fail_to_fetch_task", resp.StatusCode)
		}
		return nil, service.TaskErrorWrapper(fmt.Errorf("upstream task response body_meta=%s", common.SensitiveLogBody(responseBody)), "fail_to_fetch_task", resp.StatusCode)
	}

	// 10. 返回 OtherRatios 给下游（header 必须在 DoResponse 写 body 之前设置）
	otherRatios := info.PriceData.OtherRatios()
	if otherRatios == nil {
		otherRatios = map[string]float64{}
	}
	ratiosJSON, _ := common.Marshal(otherRatios)
	c.Header("X-New-Api-Other-Ratios", string(ratiosJSON))

	// 11. 解析响应
	upstreamTaskID, taskData, taskErr := adaptor.DoResponse(c, resp, info)
	if taskErr != nil {
		return nil, taskErr
	}

	// 11. 提交后计费调整：让适配器根据上游实际返回调整 OtherRatios
	finalQuota := info.PriceData.Quota
	if adjustedRatios := adaptor.AdjustBillingOnSubmit(info, taskData); len(adjustedRatios) > 0 {
		if adjustedQuota, ok := recalcQuotaFromRatios(info, adjustedRatios); ok {
			// 基于调整后的 ratios 重新计算 quota
			finalQuota = adjustedQuota
			info.PriceData.ReplaceOtherRatios(adjustedRatios)
			info.PriceData.Quota = finalQuota
		}
	}

	return &TaskSubmitResult{
		UpstreamTaskID: upstreamTaskID,
		TaskData:       taskData,
		Platform:       platform,
		Quota:          finalQuota,
	}, nil
}

// recalcQuotaFromRatios 根据 adjustedRatios 重新计算 quota。
// 公式: baseQuota × ∏(ratio) — 其中 baseQuota 是不含 OtherRatios 的基础额度。
func recalcQuotaFromRatios(info *relaycommon.RelayInfo, ratios map[string]float64) (int, bool) {
	// 从 PriceData 获取不含 OtherRatios 的基础价格
	baseQuota := info.PriceData.RemoveOtherRatiosFromFloat(float64(info.PriceData.Quota))
	priceData := info.PriceData
	if !priceData.ReplaceOtherRatios(ratios) {
		return 0, false
	}
	// 应用新的 ratios
	result := priceData.ApplyOtherRatiosToFloat(baseQuota)
	quota, clamp := common.QuotaFromFloatChecked(result)
	noteTaskQuotaClamp(info, clamp)
	return quota, true
}

// noteTaskQuotaClamp records the first quota saturation event onto the task's
// RelayInfo so LogTaskConsumption can surface it on the submit log's
// admin_info. First non-nil clamp wins.
func noteTaskQuotaClamp(info *relaycommon.RelayInfo, clamp *common.QuotaClamp) {
	if clamp == nil || info == nil {
		return
	}
	if info.QuotaClamp == nil {
		info.QuotaClamp = clamp
	}
}

var fetchRespBuilders = map[int]func(c *gin.Context) (respBody []byte, taskResp *dto.TaskError){
	relayconstant.RelayModeSunoFetchByID:  sunoFetchByIDRespBodyBuilder,
	relayconstant.RelayModeSunoFetch:      sunoFetchRespBodyBuilder,
	relayconstant.RelayModeVideoFetchByID: videoFetchByIDRespBodyBuilder,
}

func RelayTaskFetch(c *gin.Context, relayMode int) (taskResp *dto.TaskError) {
	respBuilder, ok := fetchRespBuilders[relayMode]
	if !ok {
		taskResp = service.TaskErrorWrapperLocal(errors.New("invalid_relay_mode"), "invalid_relay_mode", http.StatusBadRequest)
		return taskResp
	}

	respBody, taskErr := respBuilder(c)
	if taskErr != nil {
		return taskErr
	}
	if len(respBody) == 0 {
		respBody = []byte("{\"code\":\"success\",\"data\":null}")
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	_, err := io.Copy(c.Writer, bytes.NewBuffer(respBody))
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError)
		return
	}
	return
}

func sunoFetchRespBodyBuilder(c *gin.Context) (respBody []byte, taskResp *dto.TaskError) {
	userId := c.GetInt("id")
	var condition = struct {
		IDs    []any  `json:"ids"`
		Action string `json:"action"`
	}{}
	err := c.BindJSON(&condition)
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "invalid_request", http.StatusBadRequest)
		return
	}
	var tasks []any
	if len(condition.IDs) > 0 {
		taskModels, err := model.GetByTaskIds(userId, condition.IDs)
		if err != nil {
			taskResp = service.TaskErrorWrapper(err, "get_tasks_failed", http.StatusInternalServerError)
			return
		}
		for _, task := range taskModels {
			tasks = append(tasks, TaskModel2Dto(task))
		}
	} else {
		tasks = make([]any, 0)
	}
	respBody, err = common.Marshal(dto.TaskResponse[[]any]{
		Code: "success",
		Data: tasks,
	})
	return
}

func sunoFetchByIDRespBodyBuilder(c *gin.Context) (respBody []byte, taskResp *dto.TaskError) {
	taskId := c.Param("id")
	userId := c.GetInt("id")

	originTask, exist, err := model.GetByTaskId(userId, taskId)
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "get_task_failed", http.StatusInternalServerError)
		return
	}
	if !exist {
		taskResp = service.TaskErrorWrapperLocal(errors.New("task_not_exist"), "task_not_exist", http.StatusBadRequest)
		return
	}

	respBody, err = common.Marshal(dto.TaskResponse[any]{
		Code: "success",
		Data: TaskModel2Dto(originTask),
	})
	return
}

func videoFetchByIDRespBodyBuilder(c *gin.Context) (respBody []byte, taskResp *dto.TaskError) {
	taskId := c.Param("task_id")
	if taskId == "" {
		taskId = c.GetString("task_id")
	}
	userId := c.GetInt("id")

	originTask, exist, err := model.GetByTaskId(userId, taskId)
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "get_task_failed", http.StatusInternalServerError)
		return
	}
	if !exist {
		taskResp = service.TaskErrorWrapperLocal(errors.New("task_not_exist"), "task_not_exist", http.StatusBadRequest)
		return
	}

	isOpenAIVideoAPI := strings.HasPrefix(c.Request.RequestURI, "/v1/videos/")

	// Gemini/Vertex 支持实时查询：用户 fetch 时直接从上游拉取最新状态。
	// Propagate the HTTP request context so a disconnected client can cancel
	// both the OAuth exchange and the provider status request immediately.
	requestCtx := context.Background()
	if c != nil && c.Request != nil {
		requestCtx = c.Request.Context()
	}
	if realtimeResp := tryRealtimeFetchWithContext(requestCtx, originTask, isOpenAIVideoAPI); len(realtimeResp) > 0 {
		respBody = realtimeResp
		return
	}

	// OpenAI Video API 格式: 走各 adaptor 的 ConvertToOpenAIVideo
	if isOpenAIVideoAPI {
		adaptor := GetTaskAdaptor(originTask.Platform)
		if adaptor == nil {
			taskResp = service.TaskErrorWrapperLocal(fmt.Errorf("invalid channel id: %d", originTask.ChannelId), "invalid_channel_id", http.StatusBadRequest)
			return
		}
		if converter, ok := adaptor.(channel.OpenAIVideoConverter); ok {
			openAIVideoData, err := converter.ConvertToOpenAIVideo(originTask)
			if err != nil {
				taskResp = service.TaskErrorWrapper(err, "convert_to_openai_video_failed", http.StatusInternalServerError)
				return
			}
			// Task.Data is deliberately redacted before it is persisted or exposed.
			// Several legacy adaptors still read that projection and copy the
			// provider media URL into metadata.url.  Never return that URL directly:
			// signed URLs may already be invalid after redaction, and returning one
			// would disclose a provider credential to the API client.  Normalize the
			// converter output at this single response boundary and make the local,
			// authenticated proxy the only public media location.
			openAIVideoData, err = normalizeOpenAIVideoResponse(originTask, openAIVideoData)
			if err != nil {
				taskResp = service.TaskErrorWrapper(err, "normalize_openai_video_failed", http.StatusInternalServerError)
				return
			}
			respBody = openAIVideoData
			return
		}
		taskResp = service.TaskErrorWrapperLocal(fmt.Errorf("not_implemented:%s", originTask.Platform), "not_implemented", http.StatusNotImplemented)
		return
	}

	// 通用 TaskDto 格式
	taskDTO := TaskModel2Dto(originTask)
	if originTask.Status == model.TaskStatusSuccess {
		// Keep the legacy task response useful after provider URL redaction. The
		// direct/signed URL is private implementation state; callers can fetch
		// the media through the authenticated local content endpoint.
		taskDTO.ResultURL = taskcommon.BuildProxyURL(originTask.TaskID)
	}
	respBody, err = common.Marshal(dto.TaskResponse[any]{
		Code: "success",
		Data: taskDTO,
	})
	if err != nil {
		taskResp = service.TaskErrorWrapper(err, "marshal_response_failed", http.StatusInternalServerError)
	}
	return
}

// normalizeOpenAIVideoResponse applies the outward-facing safety boundary to
// an adaptor-produced OpenAI video object.  Converters are intentionally kept
// provider-specific, but the response must have one invariant: a completed
// task never exposes a provider URL (which may contain a bearer/signature
// query) when the local proxy can serve the media with the caller's auth.
//
// Redaction is applied again here for historical task rows and for adaptors
// that return a copy of task.Data directly (notably Sora).  The helper uses the
// shared JSON wrapper rather than encoding/json so it follows repository-wide
// serialization rules.
func normalizeOpenAIVideoResponse(task *model.Task, response []byte) ([]byte, error) {
	if task == nil {
		return nil, errors.New("task is nil")
	}
	if len(response) == 0 {
		return nil, errors.New("openai video response is empty")
	}

	safeResponse := service.RedactTaskResponseBody(response)
	if len(safeResponse) == 0 {
		return nil, errors.New("openai video response is empty after redaction")
	}
	var payload map[string]any
	if err := common.Unmarshal(safeResponse, &payload); err != nil {
		return nil, fmt.Errorf("decode openai video response: %w", err)
	}
	if payload == nil {
		return nil, errors.New("openai video response is not an object")
	}

	if task.Status == model.TaskStatusSuccess {
		proxyURL := taskcommon.BuildProxyURL(task.TaskID)
		if strings.TrimSpace(proxyURL) == "" {
			return nil, errors.New("video proxy URL is empty")
		}
		metadata, ok := payload["metadata"].(map[string]any)
		if !ok || metadata == nil {
			metadata = make(map[string]any)
		}
		metadata["url"] = proxyURL
		payload["metadata"] = metadata
	}

	encoded, err := common.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode openai video response: %w", err)
	}
	return encoded, nil
}

// tryRealtimeFetch 尝试从上游实时拉取 Gemini/Vertex 任务状态。
// 仅当渠道类型为 Gemini 或 Vertex 时触发；其他渠道或出错时返回 nil。
// 当非 OpenAI Video API 时，还会构建自定义格式的响应体。
func tryRealtimeFetch(task *model.Task, isOpenAIVideoAPI bool) []byte {
	return tryRealtimeFetchWithContext(context.Background(), task, isOpenAIVideoAPI)
}

func tryRealtimeFetchWithContext(ctx context.Context, task *model.Task, isOpenAIVideoAPI bool) []byte {
	if task == nil || strings.TrimSpace(task.GetUpstreamTaskID()) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	channelModel, err := model.GetChannelById(task.ChannelId, true)
	if err != nil || channelModel == nil {
		return nil
	}
	if channelModel.Type != constant.ChannelTypeVertexAi && channelModel.Type != constant.ChannelTypeGemini {
		return nil
	}

	baseURL := constant.ChannelBaseURLs[channelModel.Type]
	if channelModel.GetBaseURL() != "" {
		baseURL = channelModel.GetBaseURL()
	}
	proxy := channelModel.GetSetting().Proxy
	adaptor := GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(channelModel.Type)))
	if adaptor == nil {
		return nil
	}

	key := strings.TrimSpace(task.PrivateData.Key)
	if key == "" {
		key = channelModel.Key
	}
	if key == "" {
		return nil
	}
	fetchCtx, fetchCancel := context.WithTimeout(ctx, service.TaskPollingRequestTimeout)
	resp, err := service.FetchTaskWithContext(fetchCtx, adaptor, baseURL, key, map[string]any{
		"task_id": task.GetUpstreamTaskID(),
		"action":  task.Action,
	}, proxy)
	if err != nil || resp == nil || resp.Body == nil {
		fetchCancel()
		return nil
	}
	defer fetchCancel()
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// A realtime fetch error is not a task state transition.  Leave the
		// durable row untouched so the background poller can retry it.
		return nil
	}
	body, err := service.ReadTaskPollingResponse(resp.Body, resp.ContentLength)
	if err != nil {
		return nil
	}

	ti, err := service.ParseTaskResultWithContext(fetchCtx, adaptor, body, proxy)
	if err != nil || ti == nil {
		return nil
	}
	if err := service.ValidateTaskPollingResponseIdentity(task, ti.TaskID); err != nil {
		// A realtime fetch is allowed to fail closed. Returning nil makes the
		// caller render the durable local task state, while the background poller
		// can retry the provider request; crucially, no status or billing fields
		// are touched for a response belonging to another task.
		common.SysLog(fmt.Sprintf("realtime task response identity mismatch task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
		return nil
	}

	snap := task.Snapshot()
	// Provider responses may arrive out of order. Apply the same monotonic
	// status fence as the background poller before preparing billing or mutating
	// the task. A non-empty reason without a status is an explicit FAILURE;
	// empty/UNKNOWN statuses otherwise have no authority and fall back to the
	// already-persisted task state.
	incomingStatus := model.TaskStatus(strings.TrimSpace(ti.Status))
	if incomingStatus == "" && strings.TrimSpace(ti.Reason) != "" {
		incomingStatus = model.TaskStatusFailure
	}
	if model.IsTaskStatusStale(task.Status, incomingStatus) {
		// Realtime and background responses may be observed out of order. Keep
		// returning the durable local state for the custom task format; OpenAI's
		// converter will render that state in its normal path below.
		if isOpenAIVideoAPI {
			return nil
		}
		format := detectVideoFormat(body)
		out := map[string]any{
			"error": nil, "format": format, "metadata": nil,
			"status": mapTaskStatusToSimple(task.Status), "task_id": task.TaskID,
			"url": publicTaskVideoURL(task),
		}
		respBody, _ := common.Marshal(dto.TaskResponse[any]{Code: "success", Data: out})
		return respBody
	}
	if incomingStatus != "" {
		ti.Status = string(incomingStatus)
	}
	terminalAdjustmentPrepared := false
	var terminalAdjustmentErr error
	if incomingStatus == model.TaskStatusSuccess && strings.TrimSpace(task.BillingRequestId) != "" {
		var prepareErr error
		terminalAdjustmentPrepared, prepareErr = service.PrepareTaskBillingAdjustment(task, adaptor, ti)
		if prepareErr != nil {
			terminalAdjustmentErr = fmt.Errorf("prepare realtime terminal billing adjustment failed task=%s: %w", task.TaskID, prepareErr)
			service.StageTaskBillingAdjustmentManual(task, terminalAdjustmentErr)
			common.SysLog(fmt.Sprintf("prepare realtime terminal billing adjustment failed task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(prepareErr.Error())))
		}
	}

	// 将上游最新状态更新到 task
	if ti.Status != "" {
		task.Status = model.TaskStatus(ti.Status)
	}
	if ti.Progress != "" {
		task.Progress = ti.Progress
	}
	if ti.Reason != "" {
		task.FailReason = ti.Reason
	}
	if (task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure) && task.FinishTime == 0 {
		task.FinishTime = time.Now().Unix()
	}
	if strings.HasPrefix(ti.Url, "data:") {
		// data: URI — never expose or persist the inline media. Use the local
		// proxy endpoint when no result URL has already been committed; it will
		// re-fetch/validate the provider payload on demand.
		if strings.TrimSpace(task.PrivateData.ResultURL) == "" && task.Status == model.TaskStatusSuccess {
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		}
	} else if ti.Url != "" {
		if normalized := service.NormalizeTaskResultURL(ti.Url); normalized != "" {
			task.PrivateData.ResultURL = normalized
		} else if strings.TrimSpace(task.PrivateData.ResultURL) == "" {
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		}
	} else if ti.RemoteUrl != "" {
		// Gemini returns a signed/storage URI in RemoteUrl rather than Url.
		// Persist it so the subsequent video proxy can resolve the completed
		// task after this realtime fetch path.
		if normalized := service.NormalizeTaskResultURL(ti.RemoteUrl); normalized != "" {
			task.PrivateData.ResultURL = normalized
		} else if strings.TrimSpace(task.PrivateData.ResultURL) == "" {
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		}
	} else if task.Status == model.TaskStatusSuccess && strings.TrimSpace(task.PrivateData.ResultURL) == "" {
		// No URL from adaptor — construct proxy URL using public task ID
		task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
	}
	// A terminal failure must enter the durable refund queue in the same write
	// as the status transition.  This is especially important for realtime
	// Gemini/Vertex fetch, which may be the first observer of the terminal state.
	if task.Status == model.TaskStatusFailure && task.Quota > 0 &&
		(task.SubmitTime <= 0 || task.SubmitTime >= model.TaskRefundLegacyCutoff) {
		task.BillingReconcileState = model.TaskBillingReconcilePending
	}

	statusTransitionWon := false
	statusUpdateErr := error(nil)
	statusCASAttempted := false
	if !snap.Equal(task.Snapshot()) {
		statusCASAttempted = true
		statusTransitionWon, statusUpdateErr = task.UpdateWithStatus(snap.Status)
	}
	if statusCASAttempted && !statusTransitionWon && statusUpdateErr == nil {
		// A concurrent poller may have won the CAS (or the database may report a
		// same-value no-op). Reload before constructing the response so callers do
		// not observe a stale terminal status, and never use this stale object for
		// billing side effects.
		var current model.Task
		if err := model.DB.First(&current, task.ID).Error; err != nil {
			return nil
		}
		*task = current
		terminalAdjustmentErr = nil
	}
	// A response that races another poller may parse cleanly but lose the CAS.
	// Do not run any billing side effects from that stale in-memory object. The
	// winning worker (or the reconciliation queue) owns settlement/refund.
	persistedTerminal := statusUpdateErr == nil && (statusTransitionWon || snap.Status == task.Status)
	terminalStatusTransition := snap.Status != task.Status
	billingOwner := (terminalStatusTransition && statusTransitionWon) ||
		(!statusCASAttempted && snap.Status == task.Status)
	if statusUpdateErr != nil {
		billingOwner = false
	}

	// The task submit handler persists a pending settlement marker before
	// returning the upstream task id.  Complete that durable operation before
	// applying any terminal adjustment; retries across processes use the same
	// BillingOperation key and therefore cannot double-charge.
	if persistedTerminal && billingOwner && task.Status == model.TaskStatusSuccess &&
		(task.BillingSettlementState == model.TaskBillingSettlementPending ||
			task.BillingSettlementState == model.TaskBillingSettlementProcessing) {
		if err := service.FinalizePendingTaskBilling(context.Background(), task); err != nil {
			// Keep returning the provider status.  The durable marker remains
			// pending and the background worker will retry it.
			common.SysLog(fmt.Sprintf("realtime task settlement deferred task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
		}
	}
	if persistedTerminal && billingOwner && task.Status == model.TaskStatusFailure && task.Quota > 0 {
		// If the submit-time settlement is still pending, commit it before
		// refunding the terminal task.  This preserves the invariant that the
		// refund amount (the final task quota) matches the amount actually charged.
		settlementReady := strings.TrimSpace(task.BillingRequestId) == "" || task.BillingSettlementState == model.TaskBillingSettlementComplete
		if strings.TrimSpace(task.BillingRequestId) != "" && task.BillingSettlementState != model.TaskBillingSettlementComplete {
			if err := service.FinalizePendingTaskBilling(context.Background(), task); err != nil {
				settlementReady = false
				common.SysLog(fmt.Sprintf("realtime failed task settlement deferred task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
			} else {
				settlementReady = task.BillingSettlementState == model.TaskBillingSettlementComplete
			}
		}
		if settlementReady {
			service.ReconcileFailedTaskBilling(context.Background(), task, task.FailReason)
		}
	}
	if persistedTerminal && billingOwner && task.Status == model.TaskStatusSuccess &&
		terminalAdjustmentErr == nil &&
		(terminalAdjustmentPrepared || strings.TrimSpace(task.BillingRequestId) != "") {
		if billingAdaptor, ok := adaptor.(service.TaskPollingAdaptor); ok {
			service.SettleTaskBillingOnComplete(context.Background(), billingAdaptor, task, ti)
		}
	}

	// OpenAI Video API 由调用者的 ConvertToOpenAIVideo 分支处理
	if isOpenAIVideoAPI {
		return nil
	}

	// 非 OpenAI Video API: 构建自定义格式响应
	format := detectVideoFormat(body)
	out := map[string]any{
		"error":    nil,
		"format":   format,
		"metadata": nil,
		"status":   mapTaskStatusToSimple(task.Status),
		"task_id":  task.TaskID,
		"url":      publicTaskVideoURL(task),
	}
	respBody, _ := common.Marshal(dto.TaskResponse[any]{
		Code: "success",
		Data: out,
	})
	return respBody
}

// detectVideoFormat 从 Gemini/Vertex 原始响应中探测视频格式
func detectVideoFormat(rawBody []byte) string {
	var raw map[string]any
	if err := common.Unmarshal(rawBody, &raw); err != nil {
		return "mp4"
	}
	respObj, ok := raw["response"].(map[string]any)
	if !ok {
		return "mp4"
	}
	vids, ok := respObj["videos"].([]any)
	if !ok || len(vids) == 0 {
		return "mp4"
	}
	v0, ok := vids[0].(map[string]any)
	if !ok {
		return "mp4"
	}
	mt, ok := v0["mimeType"].(string)
	if !ok || mt == "" || strings.Contains(mt, "mp4") {
		return "mp4"
	}
	return mt
}

// mapTaskStatusToSimple 将内部 TaskStatus 映射为简化状态字符串
func mapTaskStatusToSimple(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusSuccess:
		return "succeeded"
	case model.TaskStatusFailure:
		return "failed"
	case model.TaskStatusQueued, model.TaskStatusSubmitted:
		return "queued"
	default:
		return "processing"
	}
}

// publicTaskVideoURL is the only URL form that task status responses should
// expose.  Completed media is served through the authenticated proxy; a
// non-terminal legacy row may still carry a diagnostic URL, but it must pass
// through the same redaction boundary as the generic task DTO.
func publicTaskVideoURL(task *model.Task) string {
	if task == nil {
		return ""
	}
	if task.Status == model.TaskStatusSuccess {
		return taskcommon.BuildProxyURL(task.TaskID)
	}
	return service.RedactTaskResultURL(task.GetResultURL())
}

func TaskModel2Dto(task *model.Task) *dto.TaskDto {
	if task == nil {
		return nil
	}
	return &dto.TaskDto{
		ID:         task.ID,
		CreatedAt:  task.CreatedAt,
		UpdatedAt:  task.UpdatedAt,
		TaskID:     task.TaskID,
		Platform:   string(task.Platform),
		UserId:     task.UserId,
		Group:      task.Group,
		ChannelId:  task.ChannelId,
		Quota:      task.Quota,
		Action:     task.Action,
		Status:     string(task.Status),
		FailReason: service.RedactTaskFailureReason(task.FailReason),
		ResultURL:  service.RedactTaskResultURL(task.GetResultURL()),
		SubmitTime: task.SubmitTime,
		StartTime:  task.StartTime,
		FinishTime: task.FinishTime,
		Progress:   task.Progress,
		Properties: task.Properties,
		Username:   task.Username,
		// Task.Data may have been written by an older worker before the polling
		// redaction boundary existed. Re-apply the same bounded sanitizer at the
		// DTO edge so historical rows cannot leak credentials or oversized provider
		// payloads through either user or admin fetch endpoints.
		Data: service.RedactTaskResponseBody(task.Data),
	}
}
