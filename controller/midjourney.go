package controller

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

// midjourneyPollSummary is the result recorded on a midjourney_poll system task
// row, summarizing one polling pass.
type midjourneyPollSummary struct {
	UnfinishedTasks int `json:"unfinished_tasks"`
	ChannelsScanned int `json:"channels_scanned"`
	NullTasksFailed int `json:"null_tasks_failed"`
	PollErrors      int `json:"poll_errors"`
	// PendingRefundCandidates/Completed/Pending cover terminal Midjourney rows
	// whose quota marker survived a status CAS but whose refund was interrupted
	// before the next polling pass. They are intentionally separate from
	// provider polling counts because a task may already be terminal/100%.
	PendingRefundCandidates int `json:"pending_refund_candidates"`
	PendingRefundCompleted  int `json:"pending_refund_completed"`
	PendingRefundPending    int `json:"pending_refund_pending"`
	SubmitIntentCandidates  int `json:"submit_intent_candidates"`
	SubmitIntentCompleted   int `json:"submit_intent_completed"`
	SubmitIntentPending     int `json:"submit_intent_pending"`
	// AmbiguousTasksFailed counts local rows that share an upstream Midjourney
	// id within the same channel.  Provider ids are only safe to correlate in
	// that channel namespace; when two rows claim the same id there is no
	// deterministic row to update or refund, so both rows are failed rather
	// than letting a map overwrite one of them.
	AmbiguousTasksFailed int `json:"ambiguous_tasks_failed"`
	TimedOutTasksFailed  int `json:"timed_out_tasks_failed"`
}

const (
	maxMidjourneyVideoURLsJSONBytes = 1 << 20
	midjourneyTaskTimeout           = time.Hour
	midjourneyTaskTimeoutReason     = "上游任务超时（超过1小时）"
)

// midjourneyPollingTaskKey scopes a provider id to the channel that owns it.
// Midjourney deployments commonly sit behind multiple independent channels,
// and those channels are free to issue the same id.  A bare mj_id key would
// therefore let the last row loaded from the database receive responses from
// every channel that happens to use that id.
func midjourneyPollingTaskKey(channelID int, mjID string) string {
	return fmt.Sprintf("%d\x00%s", channelID, strings.TrimSpace(mjID))
}

func lookupMidjourneyPollingTask(taskM map[string]*model.Midjourney, channelID int, mjID string) *model.Midjourney {
	if taskM == nil {
		return nil
	}
	return taskM[midjourneyPollingTaskKey(channelID, mjID)]
}

func midjourneyTaskExceededTimeout(task *model.Midjourney, nowMillis int64) bool {
	return task != nil && task.Progress != "100%" &&
		nowMillis-task.SubmitTime > midjourneyTaskTimeout.Milliseconds()
}

// failMidjourneyTaskAndRefund closes an unsafe/inaccessible task without
// touching a row that has already reached a terminal state.  The status CAS is
// the ownership fence for the refund: only the poller that wins it may reverse
// the task's quota, so a concurrent poll cannot double-credit the account.
func failMidjourneyTaskAndRefund(ctx context.Context, task *model.Midjourney, reason string) bool {
	if task == nil || task.Id <= 0 || task.Status == "SUCCESS" || task.Status == "FAILURE" {
		return false
	}
	previousStatus := task.Status
	task.Status = "FAILURE"
	task.Progress = "100%"
	task.FailReason = reason
	if task.FinishTime == 0 {
		task.FinishTime = time.Now().UnixNano() / int64(time.Millisecond)
	}
	won, err := task.UpdateWithStatus(previousStatus)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("fail ambiguous Midjourney task %s error_meta=%s", task.MjId, common.SensitiveLogMeta(err.Error())))
		return false
	}
	if !won {
		return false
	}
	if task.Quota > 0 && !service.RefundMidjourneyQuota(ctx, task, reason) {
		logger.LogWarn(ctx, fmt.Sprintf("ambiguous Midjourney task refund deferred task=%s", task.MjId))
	}
	return true
}

// runMidjourneyTaskUpdateOnce performs one Midjourney polling pass synchronously.
// It honors ctx cancellation (the system-task runner cancels it when the lease
// is lost) and, when report is non-nil, reports progress as (processedChannels,
// totalChannels) so the system task surfaces a percentage.
func runMidjourneyTaskUpdateOnce(ctx context.Context, report func(processed, total int)) midjourneyPollSummary {
	summary := midjourneyPollSummary{}
	if ctx == nil {
		ctx = context.Background()
	}
	// First replay accepted submit intents. Recovery recreates a minimal
	// unfinished task row before applying the billing marker, so a process crash
	// after the provider response cannot leave a charged task that is impossible
	// to poll or refund.
	var intentErr error
	summary.SubmitIntentCandidates,
		summary.SubmitIntentCompleted,
		summary.SubmitIntentPending,
		intentErr = service.ReconcilePendingMidjourneySubmitIntentsWithError(ctx, model.TaskBillingReconcileBatchLimit)
	if intentErr != nil {
		summary.PollErrors++
		logger.LogWarn(ctx, fmt.Sprintf("reconcile accepted Midjourney submit intents failed: %v", intentErr))
	}
	// A process can die after the terminal status CAS and before the immediate
	// refund call. Replay those task-row markers before loading unfinished
	// provider tasks; otherwise a 100% failed row would never wake this worker.
	var refundErr error
	summary.PendingRefundCandidates,
		summary.PendingRefundCompleted,
		summary.PendingRefundPending,
		refundErr = service.ReconcilePendingMidjourneyRefundsWithError(ctx, model.TaskBillingReconcileBatchLimit)
	if refundErr != nil {
		summary.PollErrors++
		logger.LogWarn(ctx, fmt.Sprintf("reconcile pending Midjourney refunds failed: %v", refundErr))
	}

	tasks, tasksErr := model.GetAllUnFinishTasksWithError()
	if tasksErr != nil {
		summary.PollErrors++
		logger.LogError(ctx, fmt.Sprintf("load unfinished Midjourney tasks failed: %v", tasksErr))
		return summary
	}
	if len(tasks) == 0 {
		return summary
	}
	summary.UnfinishedTasks = len(tasks)

	logger.LogInfo(ctx, fmt.Sprintf("检测到未完成的任务数有: %v", len(tasks)))
	taskChannelM := make(map[int][]string)
	taskM := make(map[string]*model.Midjourney)
	tasksByKey := make(map[string][]*model.Midjourney)
	nullTaskIds := make([]int, 0)
	for _, task := range tasks {
		if task == nil {
			// A nil row should never be returned by GORM, but keep the poller
			// defensive when a custom repository/cache supplies one.  There is no
			// durable primary key to fence or refund, so simply skip it and let the
			// next pass re-read the authoritative queue.
			continue
		}
		if task.MjId == "" {
			// A missing provider id is unsafe to correlate.  Process the row through
			// the same per-task CAS/refund path as every other terminal failure;
			// an unconditional bulk UPDATE would strand any quota marker and could
			// race a concurrent settlement.
			nullTaskIds = append(nullTaskIds, task.Id)
			continue
		}
		key := midjourneyPollingTaskKey(task.ChannelId, task.MjId)
		tasksByKey[key] = append(tasksByKey[key], task)
	}
	if len(nullTaskIds) > 0 {
		summary.NullTasksFailed = len(nullTaskIds)
		for _, taskID := range nullTaskIds {
			var task *model.Midjourney
			for _, candidate := range tasks {
				if candidate != nil && candidate.Id == taskID {
					task = candidate
					break
				}
			}
			if task == nil {
				continue
			}
			if failMidjourneyTaskAndRefund(ctx, task, "任务缺少上游 mj_id，请联系管理员") {
				logger.LogInfo(ctx, fmt.Sprintf("Fix null mj_id task success: %d", taskID))
			}
		}
	}
	// Build the polling maps only from unambiguous rows.  A duplicate id in the
	// same channel cannot be safely attributed to either local account; fail and
	// refund every unfinished duplicate instead of allowing the map assignment
	// to silently pick whichever row happened to be loaded last.  IDs that are
	// equal across different channels intentionally produce different keys.
	for key, matchingTasks := range tasksByKey {
		if len(matchingTasks) != 1 {
			for _, task := range matchingTasks {
				if failMidjourneyTaskAndRefund(ctx, task, "同一渠道存在重复的上游 mj_id，无法安全对账") {
					summary.AmbiguousTasksFailed++
				}
			}
			continue
		}
		task := matchingTasks[0]
		taskM[key] = task
		taskChannelM[task.ChannelId] = append(taskChannelM[task.ChannelId], task.MjId)
	}
	if len(taskChannelM) == 0 {
		return summary
	}

	totalChannels := len(taskChannelM)
	processedChannels := 0
	for channelId, taskIds := range taskChannelM {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if report != nil {
			report(processedChannels, totalChannels)
		}
		processedChannels++
		summary.ChannelsScanned++
		logger.LogInfo(ctx, fmt.Sprintf("渠道 #%d 未完成的任务有: %d", channelId, len(taskIds)))
		if len(taskIds) == 0 {
			continue
		}
		midjourneyChannel, err := model.CacheGetChannel(channelId)
		if err != nil {
			summary.PollErrors++
			logger.LogError(ctx, fmt.Sprintf("CacheGetChannel: %v", err))
			// A transient cache/database failure says nothing about the provider
			// task.  Only an authoritative missing-channel result is terminal; keep
			// all other rows pending so the next poll can retry instead of issuing an
			// early refund while the upstream job may still be running.
			if model.IsChannelNotFound(err) {
				for _, mjID := range taskIds {
					if task := lookupMidjourneyPollingTask(taskM, channelId, mjID); task != nil {
						failMidjourneyTaskAndRefund(ctx, task, fmt.Sprintf("获取渠道信息失败，请联系管理员，渠道ID：%d", channelId))
					}
				}
			}
			continue
		}
		if midjourneyChannel == nil {
			summary.PollErrors++
			logger.LogError(ctx, fmt.Sprintf("CacheGetChannel returned nil for channel %d", channelId))
			continue
		}
		if midjourneyChannel.BaseURL == nil || strings.TrimSpace(*midjourneyChannel.BaseURL) == "" {
			summary.PollErrors++
			for _, mjID := range taskIds {
				if task := lookupMidjourneyPollingTask(taskM, channelId, mjID); task != nil {
					failMidjourneyTaskAndRefund(ctx, task, fmt.Sprintf("渠道缺少上游地址，请联系管理员，渠道ID：%d", channelId))
				}
			}
			continue
		}
		requestUrl := fmt.Sprintf("%s/mj/task/list-by-condition", *midjourneyChannel.BaseURL)

		body, err := common.Marshal(map[string]any{
			"ids": taskIds,
		})
		if err != nil {
			summary.PollErrors++
			logger.LogError(ctx, fmt.Sprintf("Get Task marshal body error: %v", err))
			continue
		}
		timeout := time.Second * 15
		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(requestCtx, "POST", requestUrl, bytes.NewBuffer(body))
		if err != nil {
			summary.PollErrors++
			cancel()
			logger.LogError(ctx, fmt.Sprintf("Get Task error: %v", err))
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("mj-api-secret", midjourneyChannel.Key)
		resp, err := service.GetHttpClient().Do(req)
		if err != nil {
			summary.PollErrors++
			logger.LogError(ctx, fmt.Sprintf("Get Task Do req error: %v", err))
			cancel()
			continue
		}
		if resp == nil || resp.Body == nil {
			summary.PollErrors++
			logger.LogError(ctx, "Get Task returned empty response")
			cancel()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			summary.PollErrors++
			logger.LogError(ctx, fmt.Sprintf("Get Task status code: %d", resp.StatusCode))
			resp.Body.Close()
			cancel()
			continue
		}
		responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
		if err != nil {
			summary.PollErrors++
			logger.LogError(ctx, fmt.Sprintf("Get Mjp Task parse body error: %v", err))
			resp.Body.Close()
			cancel()
			continue
		}
		var responseItems []dto.MidjourneyDto
		err = common.Unmarshal(responseBody, &responseItems)
		if err != nil {
			summary.PollErrors++
			logger.LogError(ctx, fmt.Sprintf("Get Mjp Task parse body error2: %v, body_meta=%s", err, common.SensitiveLogBody(responseBody)))
			resp.Body.Close()
			cancel()
			continue
		}
		resp.Body.Close()
		req.Body.Close()
		cancel()

		for _, responseItem := range responseItems {
			task := lookupMidjourneyPollingTask(taskM, channelId, responseItem.MjId)
			if task == nil {
				logger.LogWarn(ctx, fmt.Sprintf("Midjourney task response ignored: unknown mj_id=%s", responseItem.MjId))
				continue
			}
			// Normalize before media validation so a trustworthy terminal failure
			// can still close and refund the task without persisting unsafe media.
			if normalizedStatus := model.NormalizeMidjourneyStatus(responseItem.Status, responseItem.FailReason); normalizedStatus != "" {
				responseItem.Status = string(normalizedStatus)
			}
			// A provider terminal observation is authoritative even when it arrives
			// after the local timeout window. For a response that is still
			// non-terminal, synthesize the failure before media validation so unsafe
			// provider media cannot keep an expired task in the polling queue.
			if midjourneyTaskExceededTimeout(task, time.Now().UnixMilli()) &&
				!model.IsTerminalTaskStatus(model.TaskStatus(responseItem.Status)) {
				responseItem.FailReason = midjourneyTaskTimeoutReason
				responseItem.Status = string(model.TaskStatusFailure)
			}
			// Treat the provider's media fields as one atomic payload. Persisting
			// lifecycle metadata from a non-terminal or successful response whose
			// media is unsafe would leave a partially-applied row. A terminal failure
			// is the exception: retain the existing media snapshot while allowing the
			// failure CAS and durable refund to proceed.
			invalidMediaField := ""
			videoURLsJSON := ""
			if err := service.ValidateMidjourneyMediaURL(responseItem.ImageUrl); err != nil {
				invalidMediaField = "imageUrl"
			} else if err := service.ValidateMidjourneyMediaURL(responseItem.VideoUrl); err != nil {
				invalidMediaField = "videoUrl"
			} else if len(responseItem.VideoUrls) > service.MaxMidjourneyVideoURLCount {
				invalidMediaField = "videoUrls.count"
			} else {
				for index := range responseItem.VideoUrls {
					if err := service.ValidateMidjourneyMediaURL(responseItem.VideoUrls[index].Url); err != nil {
						invalidMediaField = fmt.Sprintf("videoUrls[%d]", index)
						break
					}
				}
			}
			if invalidMediaField == "" {
				responseItem.ImageUrl = strings.TrimSpace(responseItem.ImageUrl)
				responseItem.VideoUrl = strings.TrimSpace(responseItem.VideoUrl)
				for index := range responseItem.VideoUrls {
					responseItem.VideoUrls[index].Url = strings.TrimSpace(responseItem.VideoUrls[index].Url)
				}
				if len(responseItem.VideoUrls) > 0 {
					encoded, err := common.Marshal(responseItem.VideoUrls)
					if err != nil || len(encoded) > maxMidjourneyVideoURLsJSONBytes {
						invalidMediaField = "videoUrls.json"
					} else {
						videoURLsJSON = string(encoded)
					}
				}
			}
			if invalidMediaField != "" {
				summary.PollErrors++
				if responseItem.Status != string(model.TaskStatusFailure) ||
					model.IsMidjourneyStatusStale(task.Status, responseItem.Status) {
					logger.LogWarn(ctx, fmt.Sprintf("Midjourney task response ignored: invalid media field=%s mj_id=%s", invalidMediaField, responseItem.MjId))
					continue
				}
				logger.LogWarn(ctx, fmt.Sprintf("Midjourney task media ignored while applying terminal failure: invalid field=%s mj_id=%s", invalidMediaField, responseItem.MjId))
				responseItem.ImageUrl = task.ImageUrl
				responseItem.VideoUrl = task.VideoUrl
				responseItem.VideoUrls = nil
				videoURLsJSON = task.VideoUrls
			}

			if !checkMjTaskNeedUpdate(task, responseItem) {
				continue
			}
			preStatus := task.Status
			task.Code = 1
			task.Progress = responseItem.Progress
			task.PromptEn = responseItem.PromptEn
			task.State = responseItem.State
			task.SubmitTime = responseItem.SubmitTime
			task.StartTime = responseItem.StartTime
			task.FinishTime = responseItem.FinishTime
			task.ImageUrl = responseItem.ImageUrl
			if responseItem.Status != "" {
				task.Status = responseItem.Status
			} else if responseItem.FailReason != "" {
				task.Status = string(model.TaskStatusFailure)
			}
			task.FailReason = responseItem.FailReason
			if responseItem.Properties != nil {
				propertiesStr, _ := common.Marshal(responseItem.Properties)
				task.Properties = string(propertiesStr)
			}
			if responseItem.Buttons != nil {
				buttonStr, _ := common.Marshal(responseItem.Buttons)
				task.Buttons = string(buttonStr)
			}
			// 映射 VideoUrl
			task.VideoUrl = responseItem.VideoUrl

			// Media was validated and encoded before any task field was mutated.
			task.VideoUrls = videoURLsJSON

			shouldReturnQuota := false
			if task.Status == string(model.TaskStatusFailure) {
				logger.LogInfo(ctx, task.MjId+" 构建失败，"+task.FailReason)
				task.Progress = "100%"
				if task.Quota != 0 {
					shouldReturnQuota = true
				}
			}
			if task.Status == string(model.TaskStatusSuccess) {
				// Some provider versions report SUCCESS before updating progress;
				// terminal success must still leave the local row out of the polling
				// queue.
				task.Progress = "100%"
			}
			won, err := task.UpdateWithStatus(preStatus)
			if err != nil {
				logger.LogError(ctx, "UpdateMidjourneyTask task error: "+err.Error())
			} else if won && shouldReturnQuota {
				service.RefundMidjourneyQuota(ctx, task, "构图失败")
			}
		}
	}
	if ctx.Err() == nil {
		nowMillis := time.Now().UnixMilli()
		for _, task := range tasks {
			if !midjourneyTaskExceededTimeout(task, nowMillis) {
				continue
			}
			status := model.NormalizeMidjourneyStatus(task.Status, task.FailReason)
			if model.IsTerminalTaskStatus(status) {
				continue
			}
			if failMidjourneyTaskAndRefund(ctx, task, midjourneyTaskTimeoutReason) {
				summary.TimedOutTasksFailed++
			}
		}
	}
	if report != nil && (ctx == nil || ctx.Err() == nil) {
		report(totalChannels, totalChannels)
	}
	return summary
}

func checkMjTaskNeedUpdate(oldTask *model.Midjourney, newTask dto.MidjourneyDto) bool {
	if oldTask == nil {
		return true
	}
	// Provider responses are not ordered.  Apply the same monotonic/terminal
	// fence used by the model CAS before comparing payload fields; otherwise a
	// delayed FAILURE can look like a metadata change and trigger a refund after
	// SUCCESS has already been persisted.
	incomingStatus := newTask.Status
	if normalizedStatus := model.NormalizeMidjourneyStatus(newTask.Status, newTask.FailReason); normalizedStatus != "" {
		incomingStatus = string(normalizedStatus)
	}
	if model.IsMidjourneyStatusStale(oldTask.Status, incomingStatus) {
		return false
	}
	if oldTask.Code != 1 {
		return true
	}
	if oldTask.Progress != newTask.Progress {
		return true
	}
	if oldTask.PromptEn != newTask.PromptEn {
		return true
	}
	if oldTask.State != newTask.State {
		return true
	}
	if oldTask.SubmitTime != newTask.SubmitTime {
		return true
	}
	if oldTask.StartTime != newTask.StartTime {
		return true
	}
	if oldTask.FinishTime != newTask.FinishTime {
		return true
	}
	if oldTask.ImageUrl != newTask.ImageUrl {
		return true
	}
	if oldTask.Status != newTask.Status {
		return true
	}
	if oldTask.FailReason != newTask.FailReason {
		return true
	}
	if oldTask.FinishTime != newTask.FinishTime {
		return true
	}
	if oldTask.Progress != "100%" && newTask.FailReason != "" {
		return true
	}
	// 检查 VideoUrl 是否需要更新
	if oldTask.VideoUrl != newTask.VideoUrl {
		return true
	}
	// 检查 VideoUrls 是否需要更新
	if newTask.VideoUrls != nil && len(newTask.VideoUrls) > 0 {
		newVideoUrlsStr, _ := common.Marshal(newTask.VideoUrls)
		if oldTask.VideoUrls != string(newVideoUrlsStr) {
			return true
		}
	} else if oldTask.VideoUrls != "" {
		// 如果新数据没有 VideoUrls 但旧数据有，需要更新（清空）
		return true
	}

	return false
}

func GetAllMidjourney(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)

	// 解析其他查询参数
	queryParams := model.TaskQueryParams{
		ChannelID:      c.Query("channel_id"),
		MjID:           c.Query("mj_id"),
		StartTimestamp: c.Query("start_timestamp"),
		EndTimestamp:   c.Query("end_timestamp"),
	}

	items := model.GetAllTasks(pageInfo.GetStartIdx(), pageInfo.GetPageSize(), queryParams)
	total, err := model.CountAllTasks(queryParams)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	if setting.GetMidjourneyConfig().ForwardURLEnabled {
		serverAddress := system_setting.GetServerAddress()
		for i, originTask := range items {
			midjourney := redactMidjourneyModelForResponse(originTask)
			if midjourney != nil && midjourney.ImageUrl != "" {
				midjourney.ImageUrl = service.BuildMidjourneyImageProxyURL(serverAddress, midjourney.MjId)
			}
			items[i] = midjourney
		}
	} else {
		for i, originTask := range items {
			items[i] = redactMidjourneyModelForResponse(originTask)
		}
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

func GetUserMidjourney(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)

	userId := c.GetInt("id")

	queryParams := model.TaskQueryParams{
		MjID:           c.Query("mj_id"),
		StartTimestamp: c.Query("start_timestamp"),
		EndTimestamp:   c.Query("end_timestamp"),
	}

	items := model.GetAllUserTask(userId, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), queryParams)
	total, err := model.CountAllUserTask(userId, queryParams)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	serverAddress := system_setting.GetServerAddress()
	forwardImages := setting.GetMidjourneyConfig().ForwardURLEnabled
	for i, originTask := range items {
		midjourney := redactMidjourneyModelForResponse(originTask)
		if midjourney != nil && forwardImages && midjourney.ImageUrl != "" {
			midjourney.ImageUrl = service.BuildMidjourneyImageProxyURL(serverAddress, midjourney.MjId)
		}
		projectMidjourneyVideoProxyURLs(midjourney, originTask, serverAddress)
		items[i] = midjourney
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(items)
	common.ApiSuccess(c, pageInfo)
}

// redactMidjourneyModelForResponse protects legacy list endpoints without
// changing the task row used by the authenticated media proxies. It returns a
// shallow copy, so callers can safely project a response without mutating the
// object loaded from the database (or the original URL needed by the proxy).
func redactMidjourneyModelForResponse(task *model.Midjourney) *model.Midjourney {
	if task == nil {
		return nil
	}
	redacted := *task
	redacted.ImageUrl = service.RedactMidjourneyMediaURL(task.ImageUrl)
	redacted.VideoUrl = service.RedactMidjourneyMediaURL(task.VideoUrl)
	redacted.FailReason = service.RedactTaskFailureReason(task.FailReason)
	redacted.Description = service.RedactTaskFailureReason(task.Description)
	redacted.Buttons = redactMidjourneyJSON(task.Buttons)
	redacted.Properties = redactMidjourneyJSON(task.Properties)

	if strings.TrimSpace(task.VideoUrls) == "" {
		redacted.VideoUrls = ""
		return &redacted
	}
	if len(task.VideoUrls) > maxMidjourneyVideoURLsJSONBytes {
		redacted.VideoUrls = ""
		return &redacted
	}
	var videoURLs []dto.ImgUrls
	if err := common.Unmarshal([]byte(task.VideoUrls), &videoURLs); err != nil {
		redacted.VideoUrls = ""
		return &redacted
	}
	if len(videoURLs) > service.MaxMidjourneyVideoURLCount {
		videoURLs = videoURLs[:service.MaxMidjourneyVideoURLCount]
	}
	for i := range videoURLs {
		videoURLs[i].Url = service.RedactMidjourneyMediaURL(videoURLs[i].Url)
	}
	encoded, err := common.Marshal(videoURLs)
	if err != nil {
		redacted.VideoUrls = ""
		return &redacted
	}
	redacted.VideoUrls = string(encoded)
	return &redacted
}

func projectMidjourneyVideoProxyURLs(projected, source *model.Midjourney, serverAddress string) {
	if projected == nil || source == nil {
		return
	}
	projected.VideoUrl = ""
	if videoURL := strings.TrimSpace(source.VideoUrl); videoURL != "" && service.ValidateMidjourneyMediaURL(videoURL) == nil {
		projected.VideoUrl = service.BuildMidjourneyVideoProxyURL(serverAddress, source.MjId)
	}
	projected.VideoUrls = ""
	videoURLs, err := service.ParseMidjourneyVideoURLs(source.VideoUrls)
	if err != nil || len(videoURLs) == 0 {
		return
	}
	for index := range videoURLs {
		videoURLs[index].Url = service.BuildMidjourneyIndexedVideoProxyURL(serverAddress, source.MjId, index)
	}
	encoded, err := common.Marshal(videoURLs)
	if err == nil {
		projected.VideoUrls = string(encoded)
	}
}

func redactMidjourneyJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return string(service.RedactMidjourneyResponseBody([]byte(raw)))
}
