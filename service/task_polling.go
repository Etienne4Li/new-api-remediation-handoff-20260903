package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"
)

const taskBillingReconcileLease = 2 * time.Minute

// Async provider responses are JSON metadata in normal operation, but Gemini
// and Vertex may embed a base64 video. Keep the generous media limit while
// still bounding an untrusted Content-Length/stream so a provider or proxy
// cannot exhaust the polling worker's memory.
const defaultTaskPollingResponseBytes int64 = 64 << 20

// Error responses are diagnostics only and must never consume the much larger
// successful-media budget. The bytes are fingerprinted rather than logged.
const maxTaskPollingDiagnosticBytes int64 = 64 << 10

var errTaskPollingResponseTooLarge = errors.New("async task provider response exceeds maximum size")
var errTaskPollingResponseBodyNil = errors.New("async task provider response body is nil")

// ErrTaskPollingIdentityMismatch indicates that a provider status response
// carried an explicit task identity different from the identity that was
// requested.  Applying such a response would associate another provider task
// with this local row and could settle/refund the wrong account, so callers
// must treat it as non-authoritative and leave the task unchanged.
var ErrTaskPollingIdentityMismatch = errors.New("provider task identity mismatch")

// ValidateTaskPollingResponseIdentity enforces the provider-response side of
// the immutable task correlation contract. It is shared by background and
// realtime polling so neither path can bypass the identity fence.
func ValidateTaskPollingResponseIdentity(task *model.Task, responseTaskID string) error {
	if task == nil {
		return ErrTaskPollingIdentityMismatch
	}
	actual := strings.TrimSpace(responseTaskID)
	if actual == "" {
		// Several provider status APIs omit the ID because it is encoded in the
		// request URL.  An omitted identity is tolerated; an explicit one is
		// checked strictly below.
		return nil
	}
	expected := strings.TrimSpace(task.GetUpstreamTaskID())
	if expected == "" || actual != expected {
		return ErrTaskPollingIdentityMismatch
	}
	return nil
}

func maxTaskPollingResponseBytes() int64 {
	// Keep this conversion in the shared helper so an extreme or negative
	// MAX_FILE_DOWNLOAD_MB cannot wrap when converted to bytes.  The fallback is
	// intentionally the same 64 MiB default used by the task polling path.
	limit := common.GetMaxFileDownloadBytes()
	if limit <= 0 {
		return defaultTaskPollingResponseBytes
	}
	return limit
}

func nilTaskPollingReader(body io.Reader) bool {
	if body == nil {
		return true
	}
	// An io.Reader interface can contain a typed nil pointer.  Calling Read on
	// such a value commonly panics inside io.LimitReader, so reject it at the
	// boundary just like a nil interface.
	v := reflect.ValueOf(body)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// ReadTaskPollingResponse is shared by the realtime fetch path so every task
// response gets the same hard memory bound.
func ReadTaskPollingResponse(body io.Reader, contentLength int64) ([]byte, error) {
	if nilTaskPollingReader(body) {
		return nil, errTaskPollingResponseBodyNil
	}
	limit := maxTaskPollingResponseBytes()
	// Leave room for the sentinel byte below even if a future configuration
	// helper ever returns MaxInt64.
	const maxInt64 = int64(^uint64(0) >> 1)
	if limit >= maxInt64 {
		limit = maxInt64 - 1
	}
	if contentLength >= 0 && contentLength > limit {
		return nil, errTaskPollingResponseTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errTaskPollingResponseTooLarge
	}
	return data, nil
}

func taskPollingDiagnosticBodyMeta(body io.Reader) string {
	if nilTaskPollingReader(body) {
		return "body=unavailable"
	}
	data, err := io.ReadAll(io.LimitReader(body, maxTaskPollingDiagnosticBytes+1))
	if err != nil {
		return "read_error=" + common.SensitiveLogMeta(err.Error())
	}
	truncated := int64(len(data)) > maxTaskPollingDiagnosticBytes
	if truncated {
		data = data[:maxTaskPollingDiagnosticBytes]
	}
	return fmt.Sprintf("body_meta=%s truncated=%t", common.SensitiveLogBody(data), truncated)
}

// TaskPollingAdaptor 定义轮询所需的最小适配器接口，避免 service -> relay 的循环依赖
type TaskPollingAdaptor interface {
	Init(info *relaycommon.RelayInfo)
	FetchTaskWithContext(ctx context.Context, baseURL string, key string, body map[string]any, proxy string) (*http.Response, error)
	ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error)
	// AdjustBillingOnComplete 在任务到达终态（成功/失败）时由轮询循环调用。
	// 返回正数触发差额结算（补扣/退还），返回 0 保持预扣费金额不变。
	AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int
}

// TaskPollingResultContextAdaptor is implemented by adaptors that need to
// resolve additional provider data while parsing a status response. The
// caller's polling deadline and channel proxy must cover that follow-up I/O as
// well as the initial status request.
type TaskPollingResultContextAdaptor interface {
	ParseTaskResultWithContext(ctx context.Context, body []byte, proxy string) (*relaycommon.TaskInfo, error)
}

// TaskPollingRequestTimeout bounds one provider status request.  It is kept
// separate from RelayTimeout: the latter is allowed to be zero for streaming
// relay calls, while a task status request must never wait forever for a
// provider/proxy that stopped sending bytes.  Adaptors that implement
// TaskPollingContextAdaptor receive the caller's deadline on the HTTP request.
const TaskPollingRequestTimeout = 30 * time.Second

// maxTaskPollingChannelConcurrency caps simultaneous provider status requests
// across video channels. Per-channel requests remain sequential and retain the
// provider-specific sleep below; this bound protects the shared HTTP/DB pools
// when a large task batch spans many channels.
const maxTaskPollingChannelConcurrency = 16

// FetchTaskWithContext dispatches a cancellable status request. Context support
// is part of TaskPollingAdaptor's contract: silently falling back to FetchTask
// would let one legacy provider call bypass both lease cancellation and the
// request timeout, potentially wedging the entire polling pass indefinitely.
func FetchTaskWithContext(ctx context.Context, adaptor TaskPollingAdaptor, baseURL string, key string, body map[string]any, proxy string) (*http.Response, error) {
	if adaptor == nil {
		return nil, errors.New("task adaptor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return adaptor.FetchTaskWithContext(ctx, baseURL, key, body, proxy)
}

func ParseTaskResultWithContext(ctx context.Context, adaptor TaskPollingAdaptor, body []byte, proxy string) (*relaycommon.TaskInfo, error) {
	if adaptor == nil {
		return nil, errors.New("task adaptor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if contextual, ok := adaptor.(TaskPollingResultContextAdaptor); ok {
		return contextual.ParseTaskResultWithContext(ctx, body, proxy)
	}
	return adaptor.ParseTaskResult(body)
}

// GetTaskAdaptorFunc 由 main 包注入，用于获取指定平台的任务适配器。
// 打破 service -> relay -> relay/channel -> service 的循环依赖。
var GetTaskAdaptorFunc func(platform constant.TaskPlatform) TaskPollingAdaptor

// sweepTimedOutTasks 在主轮询之前独立清理超时任务。
// 每次最多处理 100 条，剩余的下个周期继续处理。
// 使用 per-task CAS (UpdateWithStatus) 防止覆盖被正常轮询已推进的任务。
func sweepTimedOutTasksWithError(ctx context.Context) error {
	if constant.TaskTimeoutMinutes <= 0 {
		return nil
	}
	cutoff := time.Now().Unix() - int64(constant.TaskTimeoutMinutes)*60
	tasks, err := model.GetTimedOutUnfinishedTasksWithError(cutoff, 100)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}

	reason := fmt.Sprintf("任务超时（%d分钟）", constant.TaskTimeoutMinutes)
	legacyReason := "任务超时（旧系统遗留任务，不进行退款，请联系管理员）"
	now := time.Now().Unix()
	timedOutCount := 0

	for _, task := range tasks {
		isLegacy := task.SubmitTime > 0 && task.SubmitTime < model.TaskRefundLegacyCutoff

		oldStatus := task.Status
		observedQuota := task.Quota
		task.Status = model.TaskStatusFailure
		task.Progress = "100%"
		task.FinishTime = now
		if isLegacy {
			task.FailReason = legacyReason
			// 旧系统任务明确不退款，随终态 CAS 一并清掉 quota，
			// 避免留下可再次退款的计费状态。
			task.Quota = 0
		} else {
			task.FailReason = reason
			if task.Quota != 0 {
				task.BillingReconcileState = model.TaskBillingReconcilePending
			}
		}

		won, err := task.UpdateWithStatus(oldStatus, observedQuota)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("sweepTimedOutTasks CAS update error for task %s: %v", task.TaskID, err))
			continue
		}
		if !won {
			logger.LogInfo(ctx, fmt.Sprintf("sweepTimedOutTasks: task %s already transitioned, skip", task.TaskID))
			continue
		}
		timedOutCount++
		if !isLegacy && task.Quota != 0 {
			claimAndRefundTaskQuota(ctx, task, reason)
		}
	}

	if timedOutCount > 0 {
		logger.LogInfo(ctx, fmt.Sprintf("sweepTimedOutTasks: timed out %d tasks", timedOutCount))
	}
	return nil
}

// sweepTimedOutTasks is retained for tests and legacy callers. The polling
// entrypoint uses sweepTimedOutTasksWithError so a database outage is logged
// instead of being mistaken for an empty timeout queue.
func sweepTimedOutTasks(ctx context.Context) {
	if err := sweepTimedOutTasksWithError(ctx); err != nil {
		logger.LogError(ctx, fmt.Sprintf("load timed-out tasks failed: %v", err))
	}
}

// failTaskAndRefund transitions one unfinished task to FAILURE with a CAS and
// then reverses its durable billing marker. Polling error paths (for example,
// a deleted channel) must use the same lifecycle as an upstream-reported
// failure; a bulk status-only update strands any pre-consumed quota forever.
// The CAS result is the single ownership fence, so overlapping pollers can
// never refund the same task twice.
func failTaskAndRefund(ctx context.Context, task *model.Task, reason string) bool {
	if task == nil || task.Status == model.TaskStatusFailure || task.Status == model.TaskStatusSuccess {
		return false
	}

	previousStatus := task.Status
	observedQuota := task.Quota
	task.Status = model.TaskStatusFailure
	task.Progress = taskcommon.ProgressComplete
	task.FailReason = reason
	if task.FinishTime == 0 {
		task.FinishTime = time.Now().Unix()
	}

	// Historical tasks predate the durable refund lifecycle. Preserve the
	// existing rollout boundary by clearing their marker atomically with the
	// failure transition, without issuing a refund.
	isLegacy := task.SubmitTime > 0 && task.SubmitTime < model.TaskRefundLegacyCutoff
	if isLegacy {
		task.Quota = 0
	} else if task.Quota != 0 {
		task.BillingReconcileState = model.TaskBillingReconcilePending
	}

	won, err := task.UpdateWithStatus(previousStatus, observedQuota)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("mark task %s failure CAS update error: %v", task.TaskID, err))
		return false
	}
	if !won {
		logger.LogInfo(ctx, fmt.Sprintf("mark task %s failure CAS lost, skip billing", task.TaskID))
		return false
	}

	if isLegacy || task.Quota == 0 {
		return true
	}
	return claimAndRefundTaskQuota(ctx, task, reason)
}

// claimAndRefundTaskQuota acquires the same durable lease used by terminal
// reconciliation before invoking the non-idempotent funding/token updates.
// Keeping the claim at every terminal transition closes the race where a
// scheduler pass observes FAILURE between the status CAS and the immediate
// refund performed by that transition.
func claimAndRefundTaskQuota(ctx context.Context, task *model.Task, reason string) bool {
	if task == nil || task.Quota <= 0 {
		return RefundTaskQuota(ctx, task, reason)
	}
	// A terminal provider response can race the submit handler's post-submit
	// settlement.  Never refund task.Quota against a pre-consume baseline: if we
	// do, a later settlement retry may apply its delta after the refund and leave
	// the wallet/token ledgers over-refunded.  Finish the durable settlement (or
	// leave the task queued for the settlement worker) before claiming the
	// terminal refund lease.
	if err := ensureTaskSubmitSettlementComplete(ctx, task); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("defer task refund until submit settlement completes task %s: %v", task.TaskID, err))
		return false
	}
	// Preserve the rollout boundary for historical rows. They may reach a
	// terminal state through an upstream poll (rather than the timeout sweep),
	// but must not receive a retroactive automatic refund. Clear the marker so
	// the reconciliation worker cannot reinterpret the row later.
	if task.SubmitTime > 0 && task.SubmitTime < model.TaskRefundLegacyCutoff {
		task.Quota = 0
		if err := task.UpdateQuota(); err != nil {
			logger.LogError(ctx, fmt.Sprintf("clear legacy task quota failed task %s: %v", task.TaskID, err))
			return false
		}
		return true
	}
	now := time.Now().Unix()
	leaseUntil := now + int64(taskBillingReconcileLease/time.Second)
	claimed, err := task.ClaimBillingReconciliation(now, leaseUntil)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("claim task billing refund failed task %s: %v", task.TaskID, err))
		return false
	}
	if !claimed {
		logger.LogInfo(ctx, fmt.Sprintf("task %s billing refund claim lost, skip", task.TaskID))
		return false
	}
	return RefundTaskQuota(ctx, task, reason)
}

// ReconcileFailedTaskBilling is the safe entry point for non-polling callers
// (notably realtime Gemini/Vertex fetch) that observe a terminal failure first.
// It preserves the same CAS/lease fence used by the background poller.
func ReconcileFailedTaskBilling(ctx context.Context, task *model.Task, reason string) bool {
	return claimAndRefundTaskQuota(ctx, task, reason)
}

// TaskPollSummary is the result recorded on an async_task_poll system task row,
// summarizing one polling pass.
type TaskPollSummary struct {
	UnfinishedTasks  int `json:"unfinished_tasks"`
	PlatformsScanned int `json:"platforms_scanned"`
	NullTasksFailed  int `json:"null_tasks_failed"`
	// FailedStages counts top-level polling stages that reported an operational
	// error. Other platforms/channels still run, but the owning system task must
	// not report a false success when this is non-zero.
	FailedStages int `json:"failed_stages"`
	// AmbiguousTasksFailed counts rows that share an upstream ID on the same
	// provider channel. Such rows cannot be safely correlated to a response;
	// failing/refunding them is safer than charging or updating the wrong row.
	AmbiguousTasksFailed int `json:"ambiguous_tasks_failed"`
	// TerminalBillingCandidates is the number of modern failed tasks found with
	// a non-zero durable refund marker. TerminalBillingRefunded counts claims
	// that completed; TerminalBillingPending counts claimed attempts that did
	// not complete and were fenced for manual review.
	TerminalBillingCandidates int `json:"terminal_billing_candidates"`
	TerminalBillingRefunded   int `json:"terminal_billing_refunded"`
	TerminalBillingPending    int `json:"terminal_billing_pending"`
	// BillingSettlementCandidates/Completed/Pending cover the post-submit
	// durable settlement queue.  They are separate from terminal refunds so
	// operators can distinguish a failed task refund from an accepted task whose
	// final charge is still being committed.
	BillingSettlementCandidates int `json:"billing_settlement_candidates"`
	BillingSettlementCompleted  int `json:"billing_settlement_completed"`
	BillingSettlementPending    int `json:"billing_settlement_pending"`
	BillingAdjustmentCandidates int `json:"billing_adjustment_candidates"`
	BillingAdjustmentCompleted  int `json:"billing_adjustment_completed"`
	BillingAdjustmentPending    int `json:"billing_adjustment_pending"`
	BillingOperationCandidates  int `json:"billing_operation_candidates"`
	BillingOperationCompleted   int `json:"billing_operation_completed"`
	BillingOperationPending     int `json:"billing_operation_pending"`
	// BillingRefundCandidates/Completed/Pending cover synchronous and other
	// durable refund intents that outlive the process which created them. They
	// are intentionally separate from terminal task refunds above: an intent
	// can exist even when no async Task row was ever persisted.
	BillingRefundCandidates int `json:"billing_refund_candidates"`
	BillingRefundCompleted  int `json:"billing_refund_completed"`
	BillingRefundPending    int `json:"billing_refund_pending"`
}

// reconcileTerminalTaskBilling retries only refunds for modern tasks already
// in FAILURE with a non-zero quota marker. Successful tasks are deliberately
// excluded because their non-zero quota is a valid final charge and cannot be
// distinguished from a failed settlement without an additional durable
// settlement record. Each row is claimed with a DB compare-and-swap lease so
// overlapping workers cannot apply the same non-idempotent refund twice.
func reconcileTerminalTaskBilling(ctx context.Context, limit int) (candidates, refunded, pending int) {
	candidates, refunded, pending, _ = reconcileTerminalTaskBillingWithError(ctx, limit)
	return candidates, refunded, pending
}

func reconcileTerminalTaskBillingWithError(ctx context.Context, limit int) (candidates, refunded, pending int, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().Unix()
	tasks, err := model.GetTerminalTasksWithPendingQuotaWithError(model.TaskRefundLegacyCutoff, now, limit)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("load terminal task billing reconciliation queue failed: %v", err))
		return 0, 0, 1, err
	}
	candidates = len(tasks)
	var runErrors []error
	for i, task := range tasks {
		if ctx.Err() != nil {
			pending += len(tasks) - i
			runErrors = append(runErrors, ctx.Err())
			break
		}
		claimNow := time.Now().Unix()
		leaseUntil := claimNow + int64(taskBillingReconcileLease/time.Second)
		claimed, err := task.ClaimBillingReconciliation(claimNow, leaseUntil)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("claim terminal task billing reconciliation failed task %s: %v", task.TaskID, err))
			pending++
			runErrors = append(runErrors, err)
			continue
		}
		if !claimed {
			// Another poller won the CAS, or the row changed after the query.
			continue
		}
		if RefundTaskQuota(ctx, task, "终态任务退款重试") {
			refunded++
			continue
		}
		// RefundTaskQuota retains the quota marker and moves a strict attempt to
		// manual. Automatic replay is unsafe because a crash or ambiguous write
		// may have occurred after a non-idempotent ledger side effect.
		pending++
		runErrors = append(runErrors, errors.New("terminal task refund requires reconciliation"))
	}
	return candidates, refunded, pending, errors.Join(runErrors...)
}

// pollingTaskKey scopes an upstream task ID to the channel that owns it. Most
// providers only guarantee uniqueness within a merchant/channel, so using the
// bare upstream ID as a process-wide map key can cross-apply a result to a
// different local task.
func pollingTaskKey(channelID int, upstreamID string) string {
	return fmt.Sprintf("%d\x00%s", channelID, strings.TrimSpace(upstreamID))
}

// lookupPollingTask accepts both the channel-scoped map used by the main
// polling loop and the historical bare-ID maps used by older callers/tests.
// A legacy entry is accepted only when its channel matches. Unknown channel
// namespaces (zero/negative IDs) fail closed rather than acting as a wildcard,
// so a response fetched for a real channel cannot mutate an unscoped task.
func lookupPollingTask(taskM map[string]*model.Task, channelID int, upstreamID string) *model.Task {
	if channelID <= 0 {
		return nil
	}
	canonicalID := strings.TrimSpace(upstreamID)
	if canonicalID == "" {
		return nil
	}
	// Validate the value stored in the map as well as the key. A malformed or
	// caller-controlled map can otherwise point an apparently valid provider ID
	// at a different task and bypass the database identity fence in memory.
	isMatch := func(task *model.Task) bool {
		return task != nil &&
			// ChannelId==0 means that the namespace is unknown. Treating it as a
			// wildcard would let a legacy/malformed map entry receive a response
			// fetched for an unrelated channel and potentially settle its task.
			task.ChannelId == channelID &&
			strings.TrimSpace(task.GetUpstreamTaskID()) == canonicalID
	}
	if task := taskM[pollingTaskKey(channelID, canonicalID)]; isMatch(task) {
		return task
	}
	// Preserve compatibility with maps assembled before IDs were canonicalized
	// (for example a key containing surrounding whitespace).
	if upstreamID != canonicalID {
		if task := taskM[pollingTaskKey(channelID, upstreamID)]; isMatch(task) {
			return task
		}
	}
	if task := taskM[canonicalID]; isMatch(task) {
		return task
	}
	if upstreamID != canonicalID {
		if task := taskM[upstreamID]; isMatch(task) {
			return task
		}
	}
	return nil
}

// RunTaskPollingOnce performs one async-task (Suno/video) polling pass
// synchronously. It honors ctx cancellation (the system-task runner cancels it
// when the lease is lost) and, when report is non-nil, reports progress as
// (processedPlatforms, totalPlatforms). It returns immediately if the task
// adaptor factory has not been wired yet, to avoid a nil call during startup.
func RunTaskPollingOnce(ctx context.Context, report func(processed, total int)) (TaskPollSummary, error) {
	summary := TaskPollSummary{}
	if ctx == nil {
		ctx = context.Background()
	}
	failedStages := 0
	recordFailure := func(message string, err error) {
		if err == nil {
			return
		}
		failedStages++
		summary.FailedStages = failedStages
		logger.LogError(ctx, fmt.Sprintf("%s error_meta=%s", message, common.SensitiveLogMeta(err.Error())))
	}
	result := func() (TaskPollSummary, error) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return summary, ctxErr
		}
		if failedStages > 0 {
			return summary, fmt.Errorf("async task polling pass completed with %d failed stage(s); see server logs", failedStages)
		}
		return summary, nil
	}

	common.SysLog("任务进度轮询开始")
	if err := sweepTimedOutTasksWithError(ctx); err != nil {
		recordFailure("sweep timed-out tasks failed", err)
	}
	// Replay marker-only settlement/usage operations before task-row based
	// reconciliation. This covers provider-accepted requests whose local Task
	// INSERT failed or whose process exited before the row was visible.
	var reconciliationErr error
	// Refund intents are independent of provider polling and must be replayed
	// even when the adaptor factory is unavailable during startup. Run this
	// before task settlement so an explicit failed-request fence is handled as
	// soon as possible; model-level lifecycle checks still fail closed if a
	// conflicting settlement marker exists.
	summary.BillingRefundCandidates,
		summary.BillingRefundCompleted,
		summary.BillingRefundPending,
		reconciliationErr = reconcilePendingBillingRefundsWithError(ctx, model.TaskBillingSettlementBatchLimit)
	recordFailure("reconcile pending billing refunds failed", reconciliationErr)
	summary.BillingOperationCandidates,
		summary.BillingOperationCompleted,
		summary.BillingOperationPending,
		reconciliationErr = reconcilePendingTaskBillingOperationsWithError(ctx, model.TaskBillingSettlementBatchLimit)
	recordFailure("reconcile pending task billing operations failed", reconciliationErr)
	summary.BillingSettlementCandidates,
		summary.BillingSettlementCompleted,
		summary.BillingSettlementPending,
		reconciliationErr = reconcilePendingTaskSettlementsWithError(ctx, model.TaskBillingSettlementBatchLimit)
	recordFailure("reconcile pending task settlements failed", reconciliationErr)
	summary.BillingAdjustmentCandidates,
		summary.BillingAdjustmentCompleted,
		summary.BillingAdjustmentPending,
		reconciliationErr = reconcilePendingTaskBillingAdjustmentsWithError(ctx, model.TaskBillingAdjustmentBatchLimit)
	recordFailure("reconcile pending task billing adjustments failed", reconciliationErr)
	// Settle accepted tasks before retrying terminal refunds. A task can reach a
	// provider-reported FAILURE while its submit-time settlement is still
	// pending; refunding first would reverse the pre-consume amount and then
	// apply a settlement delta against the wrong ledger baseline.
	summary.TerminalBillingCandidates,
		summary.TerminalBillingRefunded,
		summary.TerminalBillingPending,
		reconciliationErr = reconcileTerminalTaskBillingWithError(ctx, model.TaskBillingReconcileBatchLimit)
	recordFailure("reconcile terminal task billing failed", reconciliationErr)
	// Billing reconciliation does not require a provider adaptor. Run it even
	// during startup when the factory has not been wired yet; active polling is
	// skipped until the adaptor is available.
	if GetTaskAdaptorFunc == nil {
		common.SysLog("任务进度轮询完成（adaptor 未就绪）")
		return result()
	}
	allTasks, err := model.GetAllUnFinishSyncTasksWithError(constant.TaskQueryLimit)
	if err != nil {
		recordFailure("load unfinished async tasks failed", err)
		return result()
	}
	summary.UnfinishedTasks = len(allTasks)
	platformTask := make(map[constant.TaskPlatform][]*model.Task)
	for _, t := range allTasks {
		platformTask[t.Platform] = append(platformTask[t.Platform], t)
	}

	totalPlatforms := len(platformTask)
	processedPlatforms := 0
	for platform, tasks := range platformTask {
		if ctx.Err() != nil {
			break
		}
		if report != nil {
			report(processedPlatforms, totalPlatforms)
		}
		processedPlatforms++
		if len(tasks) == 0 {
			continue
		}
		summary.PlatformsScanned++
		taskChannelM := make(map[int][]string)
		taskM := make(map[string]*model.Task)
		nullTasks := make([]*model.Task, 0)
		tasksByKey := make(map[string][]*model.Task)
		for _, task := range tasks {
			upstreamID := task.GetUpstreamTaskID()
			if upstreamID == "" {
				// 统计失败的未完成任务
				nullTasks = append(nullTasks, task)
				continue
			}
			key := pollingTaskKey(task.ChannelId, upstreamID)
			tasksByKey[key] = append(tasksByKey[key], task)
		}
		if len(nullTasks) > 0 {
			summary.NullTasksFailed += len(nullTasks)
			for _, task := range nullTasks {
				if failTaskAndRefund(ctx, task, "任务缺少上游 task_id，请联系管理员") {
					logger.LogInfo(ctx, fmt.Sprintf("Fix null task_id task success: %s", task.TaskID))
				}
			}
		}
		// An upstream ID is only safe to correlate within its channel. If the
		// same channel has duplicate local rows, there is no deterministic way
		// to decide which row a provider response belongs to; fail and refund
		// every ambiguous row instead of silently charging the wrong account.
		for key, matchingTasks := range tasksByKey {
			if len(matchingTasks) != 1 {
				for _, task := range matchingTasks {
					if failTaskAndRefund(ctx, task, "同一渠道存在重复的上游 task_id，无法安全对账") {
						summary.AmbiguousTasksFailed++
					}
				}
				continue
			}
			task := matchingTasks[0]
			upstreamID := task.GetUpstreamTaskID()
			taskM[key] = task
			taskChannelM[task.ChannelId] = append(taskChannelM[task.ChannelId], upstreamID)
		}
		if len(taskChannelM) == 0 {
			continue
		}

		if err := DispatchPlatformUpdate(ctx, platform, taskChannelM, taskM); err != nil {
			recordFailure(fmt.Sprintf("poll platform %s failed", platform), err)
		}
	}
	if report != nil && ctx.Err() == nil {
		report(totalPlatforms, totalPlatforms)
	}
	common.SysLog("任务进度轮询完成")
	return result()
}

// DispatchPlatformUpdate 按平台分发轮询更新
func DispatchPlatformUpdate(ctx context.Context, platform constant.TaskPlatform, taskChannelM map[int][]string, taskM map[string]*model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	switch platform {
	case constant.TaskPlatformMidjourney:
		// MJ 轮询由其自身处理，这里预留入口
		return nil
	case constant.TaskPlatformSuno:
		return UpdateSunoTasks(ctx, taskChannelM, taskM)
	default:
		return UpdateVideoTasks(ctx, platform, taskChannelM, taskM)
	}
}

// UpdateSunoTasks 按渠道更新所有 Suno 任务
func UpdateSunoTasks(ctx context.Context, taskChannelM map[int][]string, taskM map[string]*model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	channelIDs := make([]int, 0, len(taskChannelM))
	for channelID := range taskChannelM {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Ints(channelIDs)
	failedChannels := 0
	for _, channelId := range channelIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		taskIds := taskChannelM[channelId]
		err := updateSunoTasks(ctx, channelId, taskIds, taskM)
		if err != nil {
			failedChannels++
			logger.LogError(ctx, fmt.Sprintf("渠道 #%d 更新异步任务失败 error_meta=%s", channelId, common.SensitiveLogMeta(err.Error())))
		}
	}
	if failedChannels > 0 {
		return fmt.Errorf("%d Suno channel(s) failed to poll; see server logs", failedChannels)
	}
	return nil
}

func updateSunoTasks(ctx context.Context, channelId int, taskIds []string, taskM map[string]*model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	logger.LogInfo(ctx, fmt.Sprintf("渠道 #%d 未完成的任务有: %d", channelId, len(taskIds)))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(taskIds) == 0 {
		return nil
	}
	ch, err := model.CacheGetChannel(channelId)
	if err != nil {
		common.SysLog(fmt.Sprintf("CacheGetChannel failed error_meta=%s", common.SensitiveLogMeta(err.Error())))
		// Only a confirmed deletion is an authoritative terminal condition. A
		// database/cache outage says nothing about the provider task and must leave
		// it pending for the next poll rather than granting an early refund.
		if model.IsChannelNotFound(err) {
			for _, upstreamID := range taskIds {
				if t := lookupPollingTask(taskM, channelId, upstreamID); t != nil {
					failTaskAndRefund(ctx, t, fmt.Sprintf("获取渠道信息失败，请联系管理员，渠道ID：%d", channelId))
				}
			}
		}
		return err
	}
	if ch == nil {
		return fmt.Errorf("channel %d is empty", channelId)
	}
	if GetTaskAdaptorFunc == nil {
		return errors.New("task adaptor factory not initialized")
	}
	adaptor := GetTaskAdaptorFunc(constant.TaskPlatformSuno)
	if adaptor == nil {
		return errors.New("adaptor not found")
	}
	proxy := ch.GetSetting().Proxy
	baseURL := ch.GetBaseURL()
	if baseURL == "" {
		baseURL = constant.ChannelBaseURLs[ch.Type]
	}
	fetchCtx, fetchCancel := context.WithTimeout(ctx, TaskPollingRequestTimeout)
	resp, err := FetchTaskWithContext(fetchCtx, adaptor, baseURL, ch.Key, map[string]any{
		"ids": taskIds,
	}, proxy)
	if err != nil {
		fetchCancel()
		common.SysLog(fmt.Sprintf("Get Task request failed error_meta=%s", common.SensitiveLogMeta(err.Error())))
		return err
	}
	if resp == nil || resp.Body == nil {
		fetchCancel()
		return fmt.Errorf("Get Task returned empty response")
	}
	defer fetchCancel()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.LogError(ctx, fmt.Sprintf("Get Task status code: %d %s", resp.StatusCode, taskPollingDiagnosticBodyMeta(resp.Body)))
		return fmt.Errorf("Get Task status code: %d", resp.StatusCode)
	}
	responseBody, err := ReadTaskPollingResponse(resp.Body, resp.ContentLength)
	if err != nil {
		common.SysLog(fmt.Sprintf("Get Suno Task read body failed error_meta=%s", common.SensitiveLogMeta(err.Error())))
		return err
	}
	var responseItems taskdto.TaskResponse[[]taskdto.SunoDataResponse]
	err = common.Unmarshal(responseBody, &responseItems)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Get Suno Task parse body failed error_meta=%s body_meta=%s", common.SensitiveLogMeta(err.Error()), common.SensitiveLogBody(responseBody)))
		return err
	}
	if !responseItems.IsSuccess() {
		common.SysLog(fmt.Sprintf("渠道 #%d 未完成的任务有: %d, response_meta=%s", channelId, len(taskIds), common.SensitiveLogBody(responseBody)))
		return fmt.Errorf("suno task response unsuccessful: code=%s", responseItems.Code)
	}
	if err := fetchCtx.Err(); err != nil {
		return err
	}

	for _, responseItem := range responseItems.Data {
		if err := fetchCtx.Err(); err != nil {
			return err
		}
		task := lookupPollingTask(taskM, channelId, responseItem.TaskID)
		if task == nil {
			logger.LogWarn(ctx, fmt.Sprintf("Suno task response ignored: unknown task_id=%s", responseItem.TaskID))
			continue
		}
		// Provider responses may arrive out of order when a realtime fetch races
		// the background poller. Apply the shared monotonic fence before mutating
		// any task fields (or preparing a refund). A failure reason without a
		// status is an explicit failure signal and is checked as FAILURE; an empty
		// response with no reason is not authoritative and is ignored.
		incomingStatus := model.TaskStatus(strings.TrimSpace(responseItem.Status))
		if incomingStatus == "" && responseItem.FailReason != "" {
			incomingStatus = model.TaskStatusFailure
		}
		if model.IsTaskStatusStale(task.Status, incomingStatus) {
			logger.LogWarn(ctx, fmt.Sprintf("ignore stale/unusable Suno task status task=%s current=%s incoming=%s", task.TaskID, task.Status, incomingStatus))
			continue
		}
		// Suno returns the complete provider object, including audio/image URLs,
		// signed query strings and occasionally credential-like metadata.  Compare
		// and persist only the bounded redacted copy; comparing against raw data
		// would make every poll look like a change after the first redaction.
		safeData := RedactTaskResponseBody(responseItem.Data)
		redactedItem := responseItem
		redactedItem.Data = safeData
		if !taskNeedsUpdate(task, redactedItem) {
			continue
		}

		prevStatus := task.Status
		task.Status = lo.If(incomingStatus != "", incomingStatus).Else(task.Status)
		task.FailReason = lo.If(responseItem.FailReason != "", responseItem.FailReason).Else(task.FailReason)
		task.StartTime = lo.If(responseItem.StartTime != 0, responseItem.StartTime).Else(task.StartTime)
		task.FinishTime = lo.If(responseItem.FinishTime != 0, responseItem.FinishTime).Else(task.FinishTime)
		isFailure := responseItem.FailReason != "" || task.Status == model.TaskStatusFailure
		if isFailure {
			logger.LogInfo(ctx, fmt.Sprintf("Suno task %s failed fail_reason_meta=%s", task.TaskID, common.SensitiveLogMeta(task.FailReason)))
			task.Status = model.TaskStatusFailure
			task.Progress = "100%"
			if task.FinishTime == 0 {
				task.FinishTime = time.Now().Unix()
			}
			if task.SubmitTime <= 0 || task.SubmitTime >= model.TaskRefundLegacyCutoff {
				if task.Quota != 0 {
					task.BillingReconcileState = model.TaskBillingReconcilePending
				}
			}
		}
		if responseItem.Status == model.TaskStatusSuccess {
			task.Progress = "100%"
		}
		task.Data = safeData

		// 持久化走 CAS，防止重叠轮询/sweep/多实例/持久化失败重试导致重复退款或覆盖终态。
		won, err := task.UpdateWithStatus(prevStatus)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("UpdateSunoTask task %s failed error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
		} else if !won {
			logger.LogWarn(ctx, fmt.Sprintf("Task %s CAS lost or no-op update, skip billing", task.TaskID))
		} else if isFailure && prevStatus != model.TaskStatusFailure && task.Quota != 0 {
			claimAndRefundTaskQuota(ctx, task, task.FailReason)
		}
	}
	return nil
}

// taskNeedsUpdate 检查 Suno 任务是否需要更新
func taskNeedsUpdate(oldTask *model.Task, newTask taskdto.SunoDataResponse) bool {
	if oldTask == nil {
		return true
	}
	if oldTask.StartTime != newTask.StartTime {
		return true
	}
	if oldTask.FinishTime != newTask.FinishTime {
		return true
	}
	if string(oldTask.Status) != newTask.Status {
		return true
	}
	if oldTask.FailReason != newTask.FailReason {
		return true
	}

	if (oldTask.Status == model.TaskStatusFailure || oldTask.Status == model.TaskStatusSuccess) && oldTask.Progress != "100%" {
		return true
	}

	if !taskPollingDataEqual(oldTask.Data, newTask.Data) {
		return true
	}
	return false
}

// taskPollingDataEqual compares JSON payloads semantically. Sorting the raw
// bytes (the historical implementation) is not a canonicalization: two
// different objects can contain the same byte multiset (for example values
// 1/2 swapped), causing a real provider update to be silently skipped.
func taskPollingDataEqual(oldData, newData []byte) bool {
	var oldValue, newValue any
	if oldErr := common.Unmarshal(oldData, &oldValue); oldErr == nil {
		if newErr := common.Unmarshal(newData, &newValue); newErr == nil {
			oldCanonical, oldMarshalErr := common.Marshal(oldValue)
			newCanonical, newMarshalErr := common.Marshal(newValue)
			if oldMarshalErr == nil && newMarshalErr == nil {
				return bytes.Equal(oldCanonical, newCanonical)
			}
		}
	}
	return bytes.Equal(bytes.TrimSpace(oldData), bytes.TrimSpace(newData))
}

// UpdateVideoTasks 按渠道更新所有视频任务
func UpdateVideoTasks(ctx context.Context, platform constant.TaskPlatform, taskChannelM map[int][]string, taskM map[string]*model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	channelIDs := make([]int, 0, len(taskChannelM))
	for channelID := range taskChannelM {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Ints(channelIDs)

	type channelPollingJob struct {
		channelID int
		taskIDs   []string
	}
	jobs := make([]channelPollingJob, 0, len(channelIDs))
	for _, channelId := range channelIDs {
		taskIds := taskChannelM[channelId]
		if len(taskIds) == 0 {
			continue
		}
		jobs = append(jobs, channelPollingJob{
			channelID: channelId,
			taskIDs:   append([]string(nil), taskIds...),
		})
	}
	if len(jobs) == 0 {
		return nil
	}

	workerCount := min(maxTaskPollingChannelConcurrency, len(jobs))
	jobQueue := make(chan channelPollingJob)
	jobErrors := make(chan error, len(jobs))
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		gopool.Go(func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobQueue:
					if !ok {
						return
					}
					jobErr := func() (err error) {
						defer func() {
							if recovered := recover(); recovered != nil {
								logger.LogError(ctx, fmt.Sprintf("Channel #%d panicked while polling video async tasks panic_meta=%s", job.channelID, common.SensitiveLogMeta(fmt.Sprint(recovered))))
								err = fmt.Errorf("video channel #%d polling panicked", job.channelID)
							}
						}()
						return updateVideoTasks(ctx, platform, job.channelID, job.taskIDs, taskM)
					}()
					if jobErr != nil {
						logger.LogError(ctx, fmt.Sprintf("Channel #%d failed to update video async tasks error_meta=%s", job.channelID, common.SensitiveLogMeta(jobErr.Error())))
						jobErrors <- jobErr
					}
				}
			}
		})
	}
	dispatching := true
	for _, job := range jobs {
		if !dispatching {
			break
		}
		select {
		case jobQueue <- job:
		case <-ctx.Done():
			dispatching = false
		}
	}
	close(jobQueue)
	wg.Wait()
	close(jobErrors)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	failedChannels := 0
	for range jobErrors {
		failedChannels++
	}
	if failedChannels > 0 {
		return fmt.Errorf("%d video channel(s) failed to poll; see server logs", failedChannels)
	}
	return nil
}

func updateVideoTasks(ctx context.Context, platform constant.TaskPlatform, channelId int, taskIds []string, taskM map[string]*model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	logger.LogInfo(ctx, fmt.Sprintf("Channel #%d pending video tasks: %d", channelId, len(taskIds)))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(taskIds) == 0 {
		return nil
	}
	cacheGetChannel, err := model.CacheGetChannel(channelId)
	if err != nil {
		if model.IsChannelNotFound(err) {
			for _, upstreamID := range taskIds {
				if t := lookupPollingTask(taskM, channelId, upstreamID); t != nil {
					failTaskAndRefund(ctx, t, fmt.Sprintf("Failed to get channel info, channel ID: %d", channelId))
				}
			}
		}
		return fmt.Errorf("CacheGetChannel failed: %w", err)
	}
	adaptor := GetTaskAdaptorFunc(platform)
	if adaptor == nil {
		return fmt.Errorf("video adaptor not found")
	}
	info := &relaycommon.RelayInfo{}
	info.ChannelMeta = &relaycommon.ChannelMeta{
		ChannelBaseUrl: cacheGetChannel.GetBaseURL(),
	}
	info.ApiKey = cacheGetChannel.Key
	adaptor.Init(info)
	disablePollingSleep := cacheGetChannel.GetOtherSettings().DisableTaskPollingSleep
	failedTasks := 0
	for i, taskId := range taskIds {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := updateVideoSingleTask(ctx, adaptor, cacheGetChannel, taskId, taskM); err != nil {
			failedTasks++
			logger.LogError(ctx, fmt.Sprintf("Failed to update video task %s error_meta=%s", taskId, common.SensitiveLogMeta(err.Error())))
		}
		if disablePollingSleep || i == len(taskIds)-1 {
			continue
		}

		// sleep 1 second between tasks for this channel only.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	if failedTasks > 0 {
		return fmt.Errorf("%d video task(s) failed to poll in channel %d", failedTasks, channelId)
	}
	return nil
}

func updateVideoSingleTask(ctx context.Context, adaptor TaskPollingAdaptor, ch *model.Channel, taskId string, taskM map[string]*model.Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if ch == nil {
		return fmt.Errorf("channel is nil for task %s", taskId)
	}
	if adaptor == nil {
		return fmt.Errorf("task adaptor is nil for task %s", taskId)
	}
	now := time.Now().Unix()
	baseURL := constant.ChannelBaseURLs[ch.Type]
	if ch.GetBaseURL() != "" {
		baseURL = ch.GetBaseURL()
	}
	proxy := ch.GetSetting().Proxy

	task := lookupPollingTask(taskM, ch.Id, taskId)
	if task == nil {
		logger.LogError(ctx, fmt.Sprintf("Task %s not found in taskM", taskId))
		return fmt.Errorf("task %s not found", taskId)
	}
	key := ch.Key

	privateData := task.PrivateData
	if privateData.Key != "" {
		key = privateData.Key
	}
	fetchCtx, fetchCancel := context.WithTimeout(ctx, TaskPollingRequestTimeout)
	resp, err := FetchTaskWithContext(fetchCtx, adaptor, baseURL, key, map[string]any{
		"task_id": task.GetUpstreamTaskID(),
		"action":  task.Action,
	}, proxy)
	if err != nil {
		fetchCancel()
		return fmt.Errorf("fetchTask failed for task %s: %w", taskId, err)
	}
	if resp == nil || resp.Body == nil {
		fetchCancel()
		return fmt.Errorf("fetchTask returned empty response for task %s", taskId)
	}
	defer fetchCancel()
	defer resp.Body.Close()
	// HTTP status describes the polling request, not the provider task lifecycle.
	// Never parse an error envelope as terminal task state: authentication,
	// routing and rate-limit failures can occur while the paid task keeps running.
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		logger.LogError(ctx, fmt.Sprintf("fetchTask HTTP status %d for task %s %s", resp.StatusCode, taskId, taskPollingDiagnosticBodyMeta(resp.Body)))
		return fmt.Errorf("fetchTask HTTP status %d for task %s", resp.StatusCode, taskId)
	}
	responseBody, err := ReadTaskPollingResponse(resp.Body, resp.ContentLength)
	if err != nil {
		return fmt.Errorf("readAll failed for task %s: %w", taskId, err)
	}

	logger.LogDebug(ctx, "updateVideoSingleTask response_meta=%s", common.SensitiveLogBody(responseBody))

	snap := task.Snapshot()

	taskResult := &relaycommon.TaskInfo{}
	// try parse as New API response format
	var responseItems taskdto.TaskResponse[model.Task]
	if err = common.Unmarshal(responseBody, &responseItems); err == nil && responseItems.IsSuccess() {
		logger.LogDebug(ctx, "updateVideoSingleTask parsed as new api response format: status=%s progress=%s task_id=%s", responseItems.Data.Status, responseItems.Data.Progress, responseItems.Data.TaskID)
		t := responseItems.Data
		taskResult.TaskID = t.TaskID
		taskResult.Status = string(t.Status)
		taskResult.Url = t.GetResultURL()
		taskResult.Progress = t.Progress
		taskResult.Reason = t.FailReason
	} else {
		parsed, parseErr := ParseTaskResultWithContext(fetchCtx, adaptor, responseBody, proxy)
		if parseErr != nil {
			return fmt.Errorf("parseTaskResult failed for task %s: %w", taskId, parseErr)
		}
		if parsed == nil {
			return fmt.Errorf("parseTaskResult returned nil for task %s", taskId)
		}
		taskResult = parsed
	}
	if err := fetchCtx.Err(); err != nil {
		return err
	}

	logger.LogDebug(ctx, "updateVideoSingleTask task_result: status=%s progress=%s task_id=%s", taskResult.Status, taskResult.Progress, taskResult.TaskID)
	if err := ValidateTaskPollingResponseIdentity(task, taskResult.TaskID); err != nil {
		// Do not mutate task.Data/status or invoke any billing path when the
		// provider identifies a different task. The caller can retry the request
		// or route it to manual reconciliation without risking cross-task billing.
		logger.LogWarn(ctx, fmt.Sprintf("ignore task response identity mismatch task=%s response_task_id=%s", task.TaskID, common.SensitiveLogMeta(strings.TrimSpace(taskResult.TaskID))))
		return err
	}
	// Provider responses can arrive out of order (especially when a realtime
	// fetch races the background poller). Apply the shared monotonic fence before
	// mutating the task or preparing billing. Some adaptors expose a failure only
	// through the reason field; treat that combination as an explicit FAILURE,
	// while an empty response with no reason remains retryable/non-authoritative.
	incomingStatus := model.TaskStatus(strings.TrimSpace(taskResult.Status))
	if incomingStatus == "" && strings.TrimSpace(taskResult.Reason) != "" {
		incomingStatus = model.TaskStatusFailure
	}
	if model.IsTaskStatusStale(task.Status, incomingStatus) {
		if incomingStatus != "" {
			logger.LogWarn(ctx, fmt.Sprintf("ignore stale task status task=%s current=%s incoming=%s", task.TaskID, task.Status, incomingStatus))
			return nil
		}
		// An HTTP error body is not, by itself, an authoritative task state. A
		// 401/403 can be a rotated channel key, a 404 can be a provider routing
		// glitch, and a 4xx response may be retried by the provider. Inferring
		// FAILURE here would immediately refund a request that may still incur an
		// upstream charge. Keep the task pending unless the adaptor returned an
		// explicit terminal status; timeout reconciliation remains the fallback.
		errorResult := &dto.GeneralErrorResponse{}
		if err = common.Unmarshal(responseBody, &errorResult); err == nil {
			openaiError := errorResult.TryToOpenAIError()
			if openaiError != nil {
				return fmt.Errorf("task %s returned no status (HTTP %d, provider code=%s)", taskId, resp.StatusCode, openaiError.Code)
			}
		}
		// Unknown/malformed responses are retryable. The adaptor can only mark a
		// task terminal when it returned an explicit status.
		logger.LogWarn(ctx, fmt.Sprintf("Task %s returned empty status; keeping task pending response_meta=%s", taskId, common.SensitiveLogBody(responseBody)))
		return fmt.Errorf("task %s returned empty status", taskId)
	}
	taskResult.Status = string(incomingStatus)
	// Persist provider metadata only after the status fence has accepted the
	// observation. A stale response must not even mutate the shared in-memory
	// task object used by another worker in this polling pass.
	task.Data = RedactTaskResponseBody(responseBody)

	shouldRefund := false
	shouldSettle := false
	terminalAdjustmentPrepared := false
	var terminalAdjustmentErr error
	// Only the poller that wins the terminal status CAS may perform legacy
	// component-wise billing. A stale response can still be parsed successfully,
	// but applying its adjustment after another worker changed task.Quota would
	// double-refund or charge against an obsolete baseline.
	billingTransitionWon := false
	quota := task.Quota

	task.Status = model.TaskStatus(taskResult.Status)
	switch taskResult.Status {
	case model.TaskStatusSubmitted:
		task.Progress = taskcommon.ProgressSubmitted
	case model.TaskStatusQueued:
		task.Progress = taskcommon.ProgressQueued
	case model.TaskStatusInProgress:
		task.Progress = taskcommon.ProgressInProgress
		if task.StartTime == 0 {
			task.StartTime = now
		}
	case model.TaskStatusSuccess:
		task.Progress = taskcommon.ProgressComplete
		if task.FinishTime == 0 {
			task.FinishTime = now
		}
		resultURL := strings.TrimSpace(taskResult.Url)
		remoteURL := strings.TrimSpace(taskResult.RemoteUrl)
		switch {
		case strings.HasPrefix(strings.ToLower(resultURL), "data:"):
			// Inline media stays in the transient provider response. Never let it
			// replace a complete private URL already committed by another fetch.
			if strings.TrimSpace(task.PrivateData.ResultURL) == "" {
				task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
			}
		case resultURL != "":
			// Direct upstream URL (e.g. Kling, Ali, Doubao, etc.)
			if normalized := NormalizeTaskResultURL(resultURL); normalized != "" {
				task.PrivateData.ResultURL = normalized
			} else if strings.TrimSpace(task.PrivateData.ResultURL) == "" {
				task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
			}
		case remoteURL != "":
			// Gemini exposes its complete signed storage URL through RemoteUrl.
			// Persist it privately before Task.Data is redacted for public APIs.
			if normalized := NormalizeTaskResultURL(remoteURL); normalized != "" {
				task.PrivateData.ResultURL = normalized
			} else if strings.TrimSpace(task.PrivateData.ResultURL) == "" {
				task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
			}
		case strings.TrimSpace(task.PrivateData.ResultURL) == "":
			// No URL from adaptor — construct proxy URL using public task ID
			task.PrivateData.ResultURL = taskcommon.BuildProxyURL(task.TaskID)
		}
		shouldSettle = true
		if strings.TrimSpace(task.BillingRequestId) != "" {
			var prepareErr error
			terminalAdjustmentPrepared, prepareErr = PrepareTaskBillingAdjustment(task, adaptor, taskResult)
			if prepareErr != nil {
				// Provider SUCCESS is authoritative even when the terminal amount cannot
				// be calculated safely. Persist the success and a manual billing fence in
				// the same status CAS; returning before that CAS would leave the task
				// eligible for timeout failure and an incorrect refund.
				terminalAdjustmentErr = fmt.Errorf("prepare terminal billing adjustment failed for task %s: %w", taskId, prepareErr)
				StageTaskBillingAdjustmentManual(task, terminalAdjustmentErr)
			}
		}
	case model.TaskStatusFailure:
		// Task.PrivateData may contain the provider key and result URL. Keep the
		// failure log to stable identifiers and metadata so a debug-enabled
		// deployment cannot exfiltrate those fields.
		logger.LogInfo(ctx, fmt.Sprintf("Task %s failed: status=%s progress=%s reason_meta=%s",
			taskId, task.Status, task.Progress, common.SensitiveLogMeta(taskResult.Reason)))
		task.Status = model.TaskStatusFailure
		task.Progress = taskcommon.ProgressComplete
		if task.FinishTime == 0 {
			task.FinishTime = now
		}
		if task.SubmitTime <= 0 || task.SubmitTime >= model.TaskRefundLegacyCutoff {
			if task.Quota != 0 {
				task.BillingReconcileState = model.TaskBillingReconcilePending
			}
		}
		task.FailReason = taskResult.Reason
		logger.LogInfo(ctx, fmt.Sprintf("Task %s failed: reason_meta=%s", task.TaskID, common.SensitiveLogMeta(task.FailReason)))
		taskResult.Progress = taskcommon.ProgressComplete
		if quota != 0 {
			shouldRefund = true
		}
	default:
		return fmt.Errorf("unknown task status %s for task %s", taskResult.Status, task.TaskID)
	}
	if taskResult.Progress != "" {
		task.Progress = taskResult.Progress
	}
	if err := fetchCtx.Err(); err != nil {
		return err
	}

	isDone := task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure
	if isDone && snap.Status != task.Status {
		won, err := task.UpdateWithStatus(snap.Status)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("UpdateWithStatus failed for task %s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
			return fmt.Errorf("update task %s with terminal status: %w", task.TaskID, err)
		} else if !won {
			logger.LogWarn(ctx, fmt.Sprintf("Task %s CAS lost or no-op update, skip billing", task.TaskID))
			shouldRefund = false
			shouldSettle = false
			var persisted model.Task
			if err := model.DB.First(&persisted, task.ID).Error; err != nil {
				return fmt.Errorf("reload task %s after terminal CAS loss: %w", task.TaskID, err)
			}
			*task = persisted
			terminalAdjustmentErr = nil
		} else {
			billingTransitionWon = true
		}
	} else if !snap.Equal(task.Snapshot()) {
		won, err := task.UpdateWithStatus(snap.Status)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("Failed to update task %s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
			return fmt.Errorf("update task %s: %w", task.TaskID, err)
		}
		if !won {
			logger.LogWarn(ctx, fmt.Sprintf("Task %s CAS lost or no-op update, reload persisted state", task.TaskID))
			var persisted model.Task
			if err := model.DB.First(&persisted, task.ID).Error; err != nil {
				return fmt.Errorf("reload task %s after CAS loss: %w", task.TaskID, err)
			}
			*task = persisted
			terminalAdjustmentErr = nil
		}
	} else {
		// No changes, skip update
		logger.LogDebug(ctx, "No update needed for task %s", task.TaskID)
	}

	if !billingTransitionWon {
		shouldSettle = false
		shouldRefund = false
	}
	if terminalAdjustmentErr != nil {
		shouldSettle = false
	}
	if shouldSettle {
		if terminalAdjustmentPrepared || strings.TrimSpace(task.BillingRequestId) != "" {
			if err := FinalizePendingTaskBillingAdjustment(ctx, task); err != nil {
				logger.LogError(ctx, fmt.Sprintf("terminal billing adjustment deferred for task %s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
			}
		} else {
			settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)
		}
	}
	if shouldRefund {
		claimAndRefundTaskQuota(ctx, task, task.FailReason)
	}
	if terminalAdjustmentErr != nil {
		return terminalAdjustmentErr
	}

	return nil
}

// RedactTaskResponseBody returns a bounded, credential-safe representation of
// an untrusted provider task response.  Task.Data is exposed by both the user
// and administrator task APIs, so this boundary is intentionally shared by
// submit-time persistence, polling updates, and DTO serialization.  A nil
// input remains nil (for providers that return no metadata); malformed or
// oversized JSON becomes a small valid marker rather than being persisted or
// reflected verbatim.
func RedactTaskResponseBody(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	if len(body) > maxRedactedTaskInputBytes {
		return []byte(`{"_redacted":true}`)
	}
	// Task.Data is returned by the task APIs. Never fall back to the provider's
	// raw bytes when decoding fails: an error page may contain credentials,
	// signed URLs, or an arbitrarily large payload, and persisting it would also
	// leave malformed JSON in the task row. A small valid object is safe for the
	// retry path, which can fetch the provider response again if needed.
	var value any
	if err := common.Unmarshal(body, &value); err != nil {
		return []byte("{}")
	}
	redacted := redactTaskResponseValue(value, "", 0)
	encoded, err := common.Marshal(redacted)
	if err != nil || len(encoded) > maxRedactedTaskDataBytes {
		return []byte("{\"_redacted\":true}")
	}
	return encoded
}

// Keep the old private name for package-local callers and historical tests.
func redactVideoResponseBody(body []byte) []byte {
	return RedactTaskResponseBody(body)
}

// Provider task payloads are not trusted application data. Keep the persisted
// copy useful for status/debug metadata while removing binary media and common
// credential fields at every nesting level (not only response.videos).
const (
	maxRedactedTaskInputBytes = 16 << 20
	maxRedactedTaskDataBytes  = 1 << 20
	maxRedactedTaskDataDepth  = 16
	maxRedactedTaskDataItems  = 256
	maxRedactedTaskDataString = 4096
)

func redactTaskResponseValue(value any, key string, depth int) any {
	if depth > maxRedactedTaskDataDepth {
		return "[redacted]"
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		count := 0
		for k, item := range v {
			if isSensitiveVideoResponseKey(k) {
				continue
			}
			if count >= maxRedactedTaskDataItems {
				break
			}
			out[k] = redactTaskResponseValue(item, k, depth+1)
			count++
		}
		return out
	case []any:
		limit := len(v)
		if limit > maxRedactedTaskDataItems {
			limit = maxRedactedTaskDataItems
		}
		out := make([]any, 0, limit)
		for i := 0; i < limit; i++ {
			out = append(out, redactTaskResponseValue(v[i], key, depth+1))
		}
		return out
	case string:
		if isBase64VideoResponseKey(key) {
			// Binary fields are not needed for status polling. Dropping them at
			// the parent map is preferable, but retain a bounded marker for
			// callers that pass a scalar/array directly.
			return truncateBase64(v)
		}
		if isVideoResponseStringKey(key) {
			// Both inline media and signed media URLs are commonly placed under
			// `video`/`video_url` keys. Keep only a short diagnostic prefix: a
			// complete URL is not needed for polling state and may carry a long
			// bearer/signature query string.
			return sanitizeTaskMediaString(v)
		}
		// URLs can carry bearer-like signatures even when a provider uses a
		// generic field name (for example `result` or `location`). Keep only the
		// origin/path in persisted task metadata; the private proxy state remains
		// responsible for fetching the actual media.
		if sanitized, ok := sanitizeTaskURL(v); ok {
			return sanitized
		}
		if len(v) > maxRedactedTaskDataString {
			return truncateString(v, maxRedactedTaskDataString)
		}
		return v
	default:
		return value
	}
}

func normalizeVideoResponseKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.NewReplacer("_", "", "-", "", " ", "").Replace(key)
	return key
}

func isBase64VideoResponseKey(key string) bool {
	normalized := normalizeVideoResponseKey(key)
	return strings.Contains(normalized, "base64") || normalized == "binarydata"
}

func isSensitiveVideoResponseKey(key string) bool {
	normalized := normalizeVideoResponseKey(key)
	if isBase64VideoResponseKey(normalized) {
		return true
	}
	switch normalized {
	case "apikey", "xapikey", "accesskey", "accesstoken", "authtoken", "refreshtoken", "sessiontoken", "authorization", "auth", "bearer", "credential", "credentials", "clientsecret", "privatekey", "secretkey", "signature", "secret", "token", "password", "passwd", "cookie", "setcookie", "webhooksecret", "apikeyid":
		return true
	default:
		return false
	}
}

func isVideoResponseStringKey(key string) bool {
	normalized := normalizeVideoResponseKey(key)
	return normalized == "video" || normalized == "videos" || normalized == "audio" || normalized == "image" ||
		strings.HasSuffix(normalized, "videourl") || strings.HasSuffix(normalized, "audiourl") ||
		strings.HasSuffix(normalized, "imageurl") || strings.HasSuffix(normalized, "mediaurl") ||
		normalized == "url" || normalized == "uri" || normalized == "location" || normalized == "result"
}

func sanitizeTaskMediaString(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return "[redacted-data-url]"
	}
	if sanitized, ok := sanitizeTaskURL(trimmed); ok {
		return sanitized
	}
	return truncateBase64(value)
}

func sanitizeTaskURL(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return value, false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return value, false
	}
	// Userinfo is another bearer-credential carrier and is not needed to
	// identify a task media resource. Drop it before the value can reach a DTO
	// or log, even when the provider used a generic URL field name.
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return truncateString(parsed.String(), maxRedactedTaskDataString), true
}

// RedactTaskResultURL applies the same URL boundary to the legacy ResultURL
// field exposed by TaskDto.  ResultURL is kept separate from Task.Data for
// backwards compatibility, so sanitizing only the provider response body is
// insufficient for historical rows or providers that persist a signed URL in
// PrivateData.  Internal proxy code continues to use Task.GetResultURL()
// directly; this helper is only for outward-facing DTOs.
func RedactTaskResultURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len([]byte(trimmed)) > maxRedactedTaskDataString {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return "[redacted-data-url]"
	}
	if sanitized, ok := sanitizeTaskURL(trimmed); ok {
		return sanitized
	}
	if isLocalTaskVideoProxyURL(trimmed) {
		return trimmed
	}
	return ""
}

func isLocalTaskVideoProxyURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Opaque != "" || parsed.Host != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	const (
		prefix = "/v1/videos/"
		suffix = "/content"
	)
	escapedPath := parsed.EscapedPath()
	if !strings.HasPrefix(escapedPath, prefix) || !strings.HasSuffix(escapedPath, suffix) ||
		len(escapedPath) <= len(prefix)+len(suffix) {
		return false
	}
	escapedID := escapedPath[len(prefix) : len(escapedPath)-len(suffix)]
	if escapedID == "" || strings.Contains(escapedID, "/") {
		return false
	}
	taskID, err := url.PathUnescape(escapedID)
	if err != nil || strings.TrimSpace(taskID) == "" || taskID == "." || taskID == ".." ||
		strings.ContainsAny(taskID, "/\\\r\n") {
		return false
	}
	return true
}

const maxTaskResultURLBytes = 16 << 10

// NormalizeTaskResultURL bounds and validates a provider media URL before it
// is retained in Task.PrivateData. This is a syntax/size boundary only: any
// local fetch must still go through the SSRF-aware HTTP client immediately
// before dialing, because the fetch policy and DNS answers can change later.
// Invalid values return an empty string so callers can retain an existing URL
// or fall back to the authenticated local proxy endpoint.
func NormalizeTaskResultURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len([]byte(trimmed)) > maxTaskResultURLBytes {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		// Inline media is handled transiently by the adaptor and must never be
		// copied into the JSON task row.
		return ""
	}
	return common.NormalizeProviderURLForRuntime(trimmed, maxTaskResultURLBytes)
}

// RedactTaskFailureReason applies the same outward-facing safety boundary to
// provider failure text. Providers sometimes echo signed URLs, request
// headers, or unbounded diagnostic payloads in an error message; the raw value
// remains available internally for billing/reconciliation, but task DTOs must
// not expose it to API clients.
func RedactTaskFailureReason(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	const suffixLength = len("...")
	return truncateString(common.MaskSensitiveInfo(trimmed), maxRedactedTaskDataString-suffixLength)
}

func truncateString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

func truncateBase64(s string) string {
	const maxKeep = 256
	if len(s) <= maxKeep {
		return s
	}
	return s[:maxKeep] + "..."
}

// settleTaskBillingOnComplete 任务完成时的统一计费调整。
// 优先级：1. adaptor.AdjustBillingOnComplete 返回正数 → 使用 adaptor 计算的额度
//
//  2. taskResult.TotalTokens > 0 → 按 token 重算
//  3. 都不满足 → 保持预扣额度不变
func settleTaskBillingOnComplete(ctx context.Context, adaptor TaskPollingAdaptor, task *model.Task, taskResult *relaycommon.TaskInfo) {
	if task == nil || taskResult == nil {
		return
	}
	if task != nil && strings.TrimSpace(task.BillingRequestId) != "" {
		SettleTaskBillingOnComplete(ctx, adaptor, task, taskResult)
		return
	}
	// 0. 按次计费的任务不做差额结算
	if bc := task.PrivateData.BillingContext; bc != nil && bc.PerCallBilling {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 按次计费，跳过差额结算", task.TaskID))
		return
	}
	// 1. 优先让 adaptor 决定最终额度
	if adaptor != nil {
		if actualQuota := adaptor.AdjustBillingOnComplete(task, taskResult); actualQuota > 0 {
			RecalculateTaskQuota(ctx, task, actualQuota, "adaptor计费调整")
			return
		}
	}
	// 2. 回退到 token 重算
	if taskResult.TotalTokens > 0 {
		RecalculateTaskQuotaByTokens(ctx, task, taskResult.TotalTokens)
		return
	}
	// 3. 无调整，保持预扣额度
}
