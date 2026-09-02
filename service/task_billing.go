package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// LogTaskConsumption 记录任务消费日志和统计信息（仅记录，不涉及实际扣费）。
// 实际扣费已由 BillingSession（PreConsumeBilling + SettleBilling）完成。
func LogTaskConsumption(c *gin.Context, info *relaycommon.RelayInfo) {
	tokenName := c.GetString("token_name")
	logContent := fmt.Sprintf("操作 %s", info.Action)
	// 支持任务仅按次计费
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		logContent = fmt.Sprintf("%s，按次计费", logContent)
	} else {
		if otherRatios := info.PriceData.OtherRatios(); len(otherRatios) > 0 {
			var contents []string
			for key, ra := range otherRatios {
				if 1.0 != ra {
					contents = append(contents, fmt.Sprintf("%s: %.2f", key, ra))
				}
			}
			if len(contents) > 0 {
				logContent = fmt.Sprintf("%s, 计算参数：%s", logContent, strings.Join(contents, ", "))
			}
		}
	}
	other := make(map[string]interface{})
	other["is_task"] = true
	other["request_path"] = c.Request.URL.Path
	other["model_price"] = info.PriceData.ModelPrice
	if info.PriceData.ModelRatio > 0 {
		other["model_ratio"] = info.PriceData.ModelRatio
	}
	other["group_ratio"] = info.PriceData.GroupRatioInfo.GroupRatio
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		other["user_group_ratio"] = info.PriceData.GroupRatioInfo.GroupSpecialRatio
	}
	if info.IsModelMapped {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = info.UpstreamModelName
	}
	attachQuotaSaturation(c, info, other)
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId: info.ChannelId,
		ModelName: info.OriginModelName,
		TokenName: tokenName,
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	})
	model.UpdateUserUsedQuotaAndRequestCount(info.UserId, info.PriceData.Quota)
	model.UpdateChannelUsedQuota(info.ChannelId, info.PriceData.Quota)
}

// ---------------------------------------------------------------------------
// 异步任务计费辅助函数
// ---------------------------------------------------------------------------

// resolveTokenKey 通过 TokenId 运行时获取令牌 Key（用于 Redis 缓存操作）。
// 如果令牌已被删除或查询失败，返回空字符串。
func resolveTokenKey(ctx context.Context, tokenId int, taskID string) string {
	token, err := model.GetTokenById(tokenId)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("获取令牌 key 失败 (tokenId=%d, task=%s): %s", tokenId, taskID, err.Error()))
		return ""
	}
	return token.Key
}

// taskAdjustFunding 调整任务的资金来源（钱包或订阅），delta > 0 表示扣费，delta < 0 表示退还。
func taskAdjustFunding(task *model.Task, delta int) error {
	if task == nil {
		return errors.New("task is nil")
	}
	switch strings.TrimSpace(task.PrivateData.BillingSource) {
	case "", BillingSourceWallet:
		if delta > 0 {
			return model.DecreaseUserQuota(task.UserId, delta, false)
		}
		return model.IncreaseUserQuota(task.UserId, -delta, false)
	case BillingSourceSubscription:
		if task.PrivateData.SubscriptionId <= 0 {
			return errors.New("task subscription id is missing")
		}
		return model.PostConsumeUserSubscriptionDelta(task.PrivateData.SubscriptionId, int64(delta))
	default:
		return fmt.Errorf("unsupported task billing source %q", task.PrivateData.BillingSource)
	}
}

// taskAdjustTokenQuota 调整任务的令牌额度，delta > 0 表示扣费，delta < 0 表示退还。
// 需要通过 resolveTokenKey 运行时获取 key（不从 PrivateData 中读取）。
func taskAdjustTokenQuota(ctx context.Context, task *model.Task, delta int) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	// Playground requests intentionally bypass token accounting.  The durable
	// modern paths already encode this in their operation spec; keep the
	// compatibility helper consistent so legacy refunds/adjustments cannot
	// mutate a real token for a playground task.
	if task.BillingPlayground || task.PrivateData.TokenId <= 0 || delta == 0 {
		return nil
	}
	tokenKey := resolveTokenKey(ctx, task.PrivateData.TokenId, task.TaskID)
	if tokenKey == "" {
		return fmt.Errorf("token %d is unavailable", task.PrivateData.TokenId)
	}
	var err error
	if delta > 0 {
		err = model.DecreaseTokenQuota(task.PrivateData.TokenId, tokenKey, delta)
	} else {
		err = model.IncreaseTokenQuota(task.PrivateData.TokenId, tokenKey, -delta)
	}
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("调整令牌额度失败 (delta=%d, task=%s): %s", delta, task.TaskID, err.Error()))
		return err
	}
	return nil
}

// taskBillingOther 从 task 的 BillingContext 构建日志 Other 字段。
func taskBillingOther(task *model.Task) map[string]interface{} {
	other := make(map[string]interface{})
	if bc := task.PrivateData.BillingContext; bc != nil {
		other["model_price"] = bc.ModelPrice
		if bc.ModelRatio > 0 {
			other["model_ratio"] = bc.ModelRatio
		}
		other["group_ratio"] = bc.GroupRatio
		if priceData := taskBillingContextPriceData(bc); priceData != nil {
			for k, v := range priceData.OtherRatios() {
				other[k] = v
			}
		}
	}
	props := task.Properties
	if props.UpstreamModelName != "" && props.UpstreamModelName != props.OriginModelName {
		other["is_model_mapped"] = true
		other["upstream_model_name"] = props.UpstreamModelName
	}
	return other
}

func taskBillingContextPriceData(bc *model.TaskBillingContext) *types.PriceData {
	if bc == nil || len(bc.OtherRatios) == 0 {
		return nil
	}
	priceData := &types.PriceData{}
	if !priceData.ReplaceOtherRatios(bc.OtherRatios) {
		return nil
	}
	return priceData
}

// taskModelName 从 BillingContext 或 Properties 中获取模型名称。
func taskModelName(task *model.Task) string {
	if bc := task.PrivateData.BillingContext; bc != nil && bc.OriginModelName != "" {
		return bc.OriginModelName
	}
	return task.Properties.OriginModelName
}

// ensureTaskSubmitSettlementComplete establishes the financial baseline that a
// terminal refund reverses. Legacy rows without a durable request identity keep
// the historical empty-state behavior; journaled tasks must prove that their
// submit settlement completed before any refund can use task.Quota.
func ensureTaskSubmitSettlementComplete(ctx context.Context, task *model.Task) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if task.BillingSettlementState == model.TaskBillingSettlementComplete {
		return nil
	}
	if task.BillingSettlementState == model.TaskBillingSettlementManual {
		return errors.New("task submit billing settlement requires manual reconciliation")
	}

	requestID := strings.TrimSpace(task.BillingRequestId)
	if task.BillingSettlementState == "" && requestID == "" {
		return nil
	}
	if task.BillingSettlementState == "" && task.BillingPreConsumedQuota == 0 && task.BillingSettlementQuota == 0 {
		return errors.New("task submit billing settlement snapshot is missing")
	}
	if err := FinalizePendingTaskBilling(ctx, task); err != nil {
		return err
	}
	if task.BillingSettlementState != model.TaskBillingSettlementComplete {
		return errors.New("task submit billing settlement is not complete")
	}
	return nil
}

// RefundTaskQuota 统一的任务失败退款逻辑。
// 当异步任务失败时，退还资金与令牌额度，并回减用户和渠道用量。
//
// 现代任务（带 BillingRequestId）通过 BillingOperation 统一事务化并可
// 安全重放；没有稳定请求标识的旧任务继续使用下方严格的逐账本兼容路径。
func RefundTaskQuota(ctx context.Context, task *model.Task, reason string) (refunded bool) {
	if task == nil {
		logger.LogError(ctx, "退款失败：task 为空")
		return false
	}
	// Quota is a durable financial marker, not an arbitrary signed delta. A
	// negative/corrupt value would invert the refund into a fresh charge; an
	// oversized value can overflow legacy helpers. Leave the row untouched for
	// operator reconciliation instead of trying to repair it heuristically.
	if task.Quota < 0 || task.Quota > common.MaxQuota {
		logger.LogError(ctx, fmt.Sprintf("退款失败：task quota 超出范围 task=%s quota=%d", task.TaskID, task.Quota))
		return false
	}
	// Rows from the refund rollout that are missing BillingRequestId still have
	// a persisted primary key and quota marker. Use a deterministic row-scoped
	// BillingOperation so financial and usage deltas are replay-safe across
	// process restarts. Historical rows before the rollout keep their explicit
	// no-refund policy and are handled by claimAndRefundTaskQuota.
	if strings.TrimSpace(task.BillingRequestId) == "" && task.ID > 0 && model.DB != nil &&
		task.SubmitTime >= model.TaskRefundLegacyCutoff {
		return refundTaskQuotaLegacyDurable(ctx, task, reason)
	}
	// Modern async tasks persist the request identity and immutable billing
	// snapshot.  Their refund must therefore go through the same durable
	// operation journal used by submit/settlement.  This branch is deliberately
	// before the historical lease/manual state machine below: a committed
	// BillingOperation is safe to replay even if the task marker update or the
	// process itself fails immediately afterwards.
	if task != nil && strings.TrimSpace(task.BillingRequestId) != "" {
		return refundTaskQuotaDurable(ctx, task, reason)
	}

	strictReconciliation := task != nil && task.BillingReconcileState == model.TaskBillingReconcileProcessing
	// A strict attempt is fenced as manual by default. The only automatic
	// retryable branch below is one where this invocation explicitly undoes the
	// already-applied token write after funding rejects; in that branch we have
	// observed a clean net rollback before releasing the fence.
	strictRetryable := false
	if strictReconciliation {
		defer func() {
			if refunded {
				return
			}
			var err error
			if strictRetryable {
				err = task.MarkBillingReconcileRetryable()
				if err != nil {
					logger.LogError(ctx, fmt.Sprintf("释放任务退款重试状态失败 task %s: %s", task.TaskID, err.Error()))
				}
				return
			}
			err = task.MarkBillingReconcileManual()
			if err != nil {
				// If this write also fails, the existing processing state remains a
				// conservative fence: automatic reconciliation never reclaims it.
				logger.LogError(ctx, fmt.Sprintf("封存任务退款为人工对账失败 task %s: %s", task.TaskID, err.Error()))
			}
		}()
	}

	quota := task.Quota
	if quota == 0 {
		if strictReconciliation {
			// A claimed row with an already-cleared marker can occur after a
			// concurrent repair. Persist the terminal state rather than leaving a
			// processing fence behind forever.
			if err := task.UpdateQuota(); err != nil {
				logger.LogError(ctx, fmt.Sprintf("清除已完成任务退款状态失败 task %s: %s", task.TaskID, err.Error()))
				return false
			}
		}
		return true
	}
	// A failed task may be observed before the submit handler has committed its
	// final settlement.  This function is also called directly by the terminal
	// reconciliation worker (which already owns the refund lease), so the guard
	// must live here rather than only in claimAndRefundTaskQuota.  Applying the
	// refund against the pre-consume baseline would over-credit the wallet/token
	// when the pending settlement is retried later.
	if err := ensureTaskSubmitSettlementComplete(ctx, task); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("defer task refund until submit settlement completes task %s: %v", task.TaskID, err))
		return false
	}

	// 1. 先退还令牌额度。令牌退款失败时不能先动资金，否则会产生
	// "钱包已退、令牌未退" 的不可重试状态。
	if err := taskAdjustTokenQuota(ctx, task, -quota); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("退还令牌额度失败，保留 task quota task %s: %s", task.TaskID, err.Error()))
		return false
	}

	// 2. 退还资金来源（钱包或订阅）。如果资金退款失败，补偿已经完成的
	// 令牌退款，恢复到调用前状态，确保后续重试不会重复退令牌。
	if err := taskAdjustFunding(task, -quota); err != nil {
		if compensationErr := taskAdjustTokenQuota(ctx, task, quota); compensationErr != nil {
			logger.LogError(ctx, fmt.Sprintf("资金退款失败且令牌退款补偿失败 task %s: funding=%s token=%s", task.TaskID, err.Error(), compensationErr.Error()))
		} else {
			// Both the original token refund and its inverse completed, so this
			// invocation left the ledgers at their pre-attempt values. It is safe
			// to release the lease for a later retry (provided the state write
			// itself succeeds).
			strictRetryable = true
		}
		logger.LogWarn(ctx, fmt.Sprintf("退还资金来源失败 task %s: %s", task.TaskID, err.Error()))
		return false
	}

	// 3. 回减预扣时累计的用户和渠道用量，请求次数保持不变
	model.UpdateUserUsedQuota(task.UserId, -quota)
	model.UpdateChannelUsedQuota(task.ChannelId, -quota)

	// 4. 记录日志
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["reason"] = reason
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
	})

	// 5. 资金退款完成后再清除持久化标记。
	// 回写失败必须显式告警，避免漏掉潜在的重复退款风险。
	task.Quota = 0
	if err := task.UpdateQuota(); err != nil {
		logger.LogError(ctx, fmt.Sprintf("退款成功但清除 task quota 失败 task %s: %s", task.TaskID, err.Error()))
		return false
	}
	return true
}

// LegacyTaskRefundComponent identifies durable refunds synthesized for task
// rows created after the refund rollout but before BillingRequestId was
// persisted. Keep this separate from the generic synchronous `refund`
// component: subscription task rows do not have a SubscriptionPreConsumeRecord
// and therefore must be replayed as a plain ledger operation.
const LegacyTaskRefundComponent = model.BillingOperationLegacyTaskRefundComponent

// legacyTaskRefundComponent is retained as an internal alias to keep the
// existing helper/tests concise. The old deployment used the generic `refund`
// component; the reconciler contains an explicit compatibility branch for
// those legacy-refund-* request IDs.
const legacyTaskRefundComponent = LegacyTaskRefundComponent

// legacyTaskRefundRequestID derives a stable operation identity for rows
// created before BillingRequestId was introduced. Including the task primary
// key and immutable billing fields prevents two legacy rows from sharing a
// refund fence, while hashing keeps the persisted request id bounded.
func legacyTaskRefundRequestID(task *model.Task, quota int) string {
	if task == nil {
		return ""
	}
	raw := fmt.Sprintf("legacy-task-refund|id=%d|task=%s|user=%d|token=%d|subscription=%d|source=%s|channel=%d|quota=%d",
		task.ID,
		task.TaskID,
		task.UserId,
		task.PrivateData.TokenId,
		task.PrivateData.SubscriptionId,
		strings.TrimSpace(task.PrivateData.BillingSource),
		task.ChannelId,
		quota,
	)
	return "legacy-refund-" + model.BillingOperationKey(raw, legacyTaskRefundComponent)
}

// refundTaskQuotaLegacyDurable is the replay-safe path for post-rollout task
// rows that lack BillingRequestId. Financial and informational deltas are
// committed by one idempotent operation; the task quota marker is cleared only
// afterwards, so a crash between those writes can be repaired by retrying the
// same operation key.
func refundTaskQuotaLegacyDurable(ctx context.Context, task *model.Task, reason string) bool {
	if task == nil || task.ID <= 0 || model.DB == nil || task.Quota <= 0 {
		return task != nil && task.Quota == 0
	}
	if task.BillingReconcileState == model.TaskBillingReconcileManual {
		return false
	}
	if task.Status != model.TaskStatusFailure {
		return false
	}
	// Direct callers may skip claimAndRefundTaskQuota; acquire the same lease so
	// overlapping workers cannot both attempt the marker transition.
	now := time.Now().Unix()
	if task.BillingReconcileState != model.TaskBillingReconcileProcessing || task.BillingReconcileUntil <= now {
		claimed, err := task.ClaimBillingReconciliation(now, now+int64(taskBillingReconcileLease/time.Second))
		if err != nil || !claimed {
			return false
		}
	}
	quota := task.Quota
	requestID := legacyTaskRefundRequestID(task, quota)
	if requestID == "" || task.UserId <= 0 {
		_ = task.MarkBillingReconcileManual()
		return false
	}
	spec := model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             legacyTaskRefundComponent,
		UserID:                task.UserId,
		TokenID:               task.PrivateData.TokenId,
		SubscriptionID:        task.PrivateData.SubscriptionId,
		TokenDelta:            -int64(quota),
		UserUsedQuotaDelta:    -int64(quota),
		ChannelID:             task.ChannelId,
		ChannelUsedQuotaDelta: -int64(quota),
		TokenUnlimited:        task.BillingTokenUnlimited,
	}
	if task.BillingPlayground {
		spec.TokenDelta = 0
	}
	switch strings.TrimSpace(task.PrivateData.BillingSource) {
	case "", BillingSourceWallet:
		spec.WalletDelta = int64(quota)
	case BillingSourceSubscription:
		if task.PrivateData.SubscriptionId <= 0 {
			_ = task.MarkBillingReconcileManual()
			return false
		}
		spec.SubscriptionDelta = -int64(quota)
	default:
		_ = task.MarkBillingReconcileManual()
		return false
	}
	if spec.TokenDelta != 0 && spec.TokenID <= 0 {
		_ = task.MarkBillingReconcileManual()
		return false
	}
	if token, err := model.GetTokenById(spec.TokenID); err == nil && token != nil {
		spec.TokenKey = token.Key
	}
	// This is a task-scoped compatibility operation, not a synchronous
	// subscription reservation refund. EnsureBillingRefundOperation deliberately
	// requires a SubscriptionPreConsumeRecord for non-zero SubscriptionDelta;
	// legacy task rows do not carry one, so persist the generic marker directly
	// and replay it with ApplyBillingOperation in the refund reconciler.
	if err := model.EnsureBillingOperation(spec); err != nil {
		_ = task.MarkBillingReconcileManual()
		logger.LogWarn(ctx, fmt.Sprintf("legacy task refund intent failed task %s: %v", task.TaskID, err))
		return false
	}
	if err := model.ApplyBillingOperation(spec); err != nil {
		// Persist a manual fence on any failed legacy attempt. Although the
		// operation itself is atomic, old rows lack an immutable request snapshot;
		// requiring an operator to requeue after verifying the ledger avoids
		// guessing across historical data corruption or ambiguous DB responses.
		_ = task.MarkBillingReconcileManual()
		logger.LogWarn(ctx, fmt.Sprintf("legacy task durable refund failed task %s: %v", task.TaskID, err))
		return false
	}
	task.Quota = 0
	if err := task.UpdateQuota(); err != nil {
		_ = task.MarkBillingReconcileRetryable()
		logger.LogError(ctx, fmt.Sprintf("legacy task refund committed but quota marker update failed task %s: %v", task.TaskID, err))
		return false
	}
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["reason"] = reason
	other["billing_legacy_reconciled"] = true
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
	return true
}

// taskRefundBillingSpec reconstructs the complete refund from the immutable
// task snapshot.  Financial ledgers and informational usage aggregates are
// intentionally part of one idempotent operation: a retry after a successful
// wallet/token refund cannot apply the counters a second time, and a missing
// channel row never prevents spendable credit from being returned.
func taskRefundBillingSpec(task *model.Task, quota int) (model.BillingOperationSpec, error) {
	if task == nil {
		return model.BillingOperationSpec{}, fmt.Errorf("task is nil")
	}
	requestID := strings.TrimSpace(task.BillingRequestId)
	if requestID == "" {
		return model.BillingOperationSpec{}, fmt.Errorf("task billing request id is missing")
	}
	if quota <= 0 || quota > common.MaxQuota {
		return model.BillingOperationSpec{}, fmt.Errorf("task refund quota is out of range: %d", quota)
	}
	if task.UserId <= 0 {
		return model.BillingOperationSpec{}, fmt.Errorf("task user id is missing")
	}

	spec := model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             "task_refund",
		UserID:                task.UserId,
		TokenID:               task.PrivateData.TokenId,
		SubscriptionID:        task.PrivateData.SubscriptionId,
		WalletDelta:           0,
		TokenDelta:            -int64(quota),
		SubscriptionDelta:     0,
		UserUsedQuotaDelta:    -int64(quota),
		ChannelID:             task.ChannelId,
		ChannelUsedQuotaDelta: -int64(quota),
		TokenUnlimited:        task.BillingTokenUnlimited,
	}
	if task.BillingPlayground {
		spec.TokenDelta = 0
	}
	switch strings.TrimSpace(task.PrivateData.BillingSource) {
	case "", BillingSourceWallet:
		spec.WalletDelta = int64(quota)
	case BillingSourceSubscription:
		if task.PrivateData.SubscriptionId <= 0 {
			return model.BillingOperationSpec{}, fmt.Errorf("task subscription id is missing")
		}
		spec.SubscriptionDelta = -int64(quota)
	default:
		return model.BillingOperationSpec{}, fmt.Errorf("unsupported task billing source %q", task.PrivateData.BillingSource)
	}
	// ChannelId is optional for imported/partially-created tasks.  User usage
	// remains durable; the channel helper treats a deleted channel as an
	// informational no-op while retaining the intended delta in the journal.
	if task.ChannelId <= 0 {
		spec.ChannelID = 0
		spec.ChannelUsedQuotaDelta = 0
	}
	if spec.TokenDelta != 0 && spec.TokenID <= 0 {
		return model.BillingOperationSpec{}, fmt.Errorf("task token id is missing")
	}
	return spec, nil
}

// markModernRefundRetryable releases a claimed modern refund after any
// retryable error.  BillingOperation makes both ledger and counter writes
// idempotent, so unlike the legacy component-wise path it is safe to retry
// after an ambiguous database response.  If the caller did not own a claim,
// the conditional update simply does nothing and the original pending marker
// remains available to the scheduler.
func markModernRefundRetryable(task *model.Task, err error) {
	if task == nil || task.ID <= 0 || task.BillingReconcileState != model.TaskBillingReconcileProcessing {
		return
	}
	if markErr := task.MarkBillingReconcileRetryable(); markErr != nil {
		common.SysLog(fmt.Sprintf("failed to release modern task refund retry task=%s error=%v original=%v", task.TaskID, markErr, err))
	}
}

// refundTaskQuotaDurable is the modern async-task refund path.  The operation
// journal commits funding, token, and aggregate usage atomically.  We clear
// the task quota marker before writing the best-effort human log; this ordering
// guarantees that a retry after a marker-write failure never duplicates a
// financial refund or a log entry.  A missing log is informational and can be
// reconstructed from the task row and BillingOperation during audit.
func refundTaskQuotaDurable(ctx context.Context, task *model.Task, reason string) bool {
	if task == nil {
		return false
	}
	if task.BillingReconcileState == model.TaskBillingReconcileComplete {
		if task.Quota <= 0 {
			return true
		}
		logger.LogError(ctx, fmt.Sprintf("modern task refund marker is complete but quota remains task=%s quota=%d", task.TaskID, task.Quota))
		return false
	}
	if task.BillingReconcileState == model.TaskBillingReconcileManual {
		logger.LogWarn(ctx, fmt.Sprintf("skip modern task refund fenced for manual reconciliation task %s", task.TaskID))
		return false
	}
	if task.ID <= 0 {
		// Never commit a durable refund for an unsaved task: without a primary
		// key there is no marker that a later worker can clear, so a transient
		// caller retry could strand an already-credited ledger.
		if task.Quota > 0 {
			logger.LogError(ctx, fmt.Sprintf("modern task refund requires persisted task id task=%s", task.TaskID))
			return false
		}
		return true
	}
	// Direct callers (for example realtime fetches or operator tooling) may
	// invoke RefundTaskQuota without the outer claim helper. Acquire the same
	// DB lease here unless this object already carries an active claim. A stale
	// in-memory object can have quota=0 after a failed marker UPDATE; reload it
	// once before deciding that another worker owns the refund.
	for claimAttempt := 0; claimAttempt < 2; claimAttempt++ {
		now := time.Now().Unix()
		if task.BillingReconcileState == model.TaskBillingReconcileProcessing && task.BillingReconcileUntil > now {
			break
		}
		claimed, err := task.ClaimBillingReconciliation(now, now+int64(taskBillingReconcileLease/time.Second))
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("claim modern task refund failed task %s: %v", task.TaskID, err))
			return false
		}
		if claimed {
			break
		}
		var current model.Task
		if err := model.DB.First(&current, task.ID).Error; err != nil {
			return false
		}
		*task = current
		if current.BillingReconcileState == model.TaskBillingReconcileComplete && current.Quota <= 0 {
			return true
		}
		if current.Quota <= 0 {
			if err := task.UpdateQuota(); err != nil {
				markModernRefundRetryable(task, err)
				return false
			}
			return true
		}
		if current.BillingReconcileState == model.TaskBillingReconcileManual {
			return false
		}
		// If the row is still pending/retryable, the next loop iteration retries
		// the claim with the freshly loaded quota/state. Processing with a live
		// lease belongs to another worker and is not reclaimed here.
		if current.BillingReconcileState == model.TaskBillingReconcileProcessing && current.BillingReconcileUntil > now {
			return false
		}
	}
	if task.BillingReconcileState != model.TaskBillingReconcileProcessing {
		return false
	}
	if task.Quota <= 0 {
		if err := task.UpdateQuota(); err != nil {
			markModernRefundRetryable(task, err)
			logger.LogWarn(ctx, fmt.Sprintf("clear already-refunded modern task marker failed task %s: %v", task.TaskID, err))
			return false
		}
		return true
	}

	// A terminal failure may race the post-submit settlement.  Finish that
	// operation first so the refund reverses the final committed amount rather
	// than the pre-consume baseline.
	if err := ensureTaskSubmitSettlementComplete(ctx, task); err != nil {
		if task.BillingSettlementState == model.TaskBillingSettlementManual {
			// A manual submit settlement has no trustworthy committed baseline.
			// Retrying the refund cannot change that fact and would make this row
			// churn through the automatic queue forever. Fence the refund under the
			// claim we already own so an operator must repair both states explicitly.
			if markErr := task.MarkBillingReconcileManual(); markErr != nil {
				logger.LogError(ctx, fmt.Sprintf("fence task refund for manual submit settlement failed task %s: %v", task.TaskID, markErr))
			}
			logger.LogWarn(ctx, fmt.Sprintf("task refund requires manual submit settlement reconciliation task %s: %v", task.TaskID, err))
			return false
		}
		markModernRefundRetryable(task, err)
		logger.LogWarn(ctx, fmt.Sprintf("defer modern task refund until submit settlement completes task %s: %v", task.TaskID, err))
		return false
	}

	quota := task.Quota
	spec, err := taskRefundBillingSpec(task, quota)
	if err != nil {
		markModernRefundRetryable(task, err)
		logger.LogError(ctx, fmt.Sprintf("modern task refund specification invalid task %s: %v", task.TaskID, err))
		return false
	}
	if spec.TokenDelta != 0 && spec.TokenID > 0 {
		// TokenKey is only a Redis cache hint; the database operation remains
		// keyed by TokenID. Resolve it at retry time so a successful durable
		// refund also repairs an already-hydrated token cache.
		if token, tokenErr := model.GetTokenById(spec.TokenID); tokenErr == nil && token != nil {
			spec.TokenKey = token.Key
		}
	}
	if err := model.ApplyBillingOperation(spec); err != nil {
		markModernRefundRetryable(task, err)
		logger.LogWarn(ctx, fmt.Sprintf("modern task durable refund failed task %s: %v", task.TaskID, err))
		return false
	}

	// The operation is now committed.  Updating the durable task marker is the
	// only remaining state transition; a failed update is safe to retry because
	// the operation key fences all ledger/counter mutations.
	task.Quota = 0
	if err := task.UpdateQuota(); err != nil {
		markModernRefundRetryable(task, err)
		logger.LogError(ctx, fmt.Sprintf("modern task refund committed but quota marker update failed task %s: %v", task.TaskID, err))
		return false
	}

	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["reason"] = reason
	other["billing_request_id"] = task.BillingRequestId
	other["billing_refund_reconciled"] = true
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
	return true
}

const legacyTaskAdjustmentComponent = "legacy_task_recalculate"

// legacyTaskAdjustmentRequestID derives a bounded, deterministic identity for
// a legacy task adjustment.  Including the observed baseline and target makes
// each monotonic terminal transition a distinct operation while replaying the
// same stale response resolves to the same marker.
func legacyTaskAdjustmentRequestID(task *model.Task, preConsumedQuota, actualQuota int) string {
	if task == nil {
		return ""
	}
	raw := fmt.Sprintf("legacy-task-adjust|id=%d|task=%s|user=%d|token=%d|subscription=%d|source=%s|pre=%d|actual=%d",
		task.ID,
		task.TaskID,
		task.UserId,
		task.PrivateData.TokenId,
		task.PrivateData.SubscriptionId,
		strings.TrimSpace(task.PrivateData.BillingSource),
		preConsumedQuota,
		actualQuota,
	)
	// BillingOperation request IDs are persisted in a bounded varchar. Hashing
	// the complete identity also avoids putting provider/task identifiers into
	// an index or allowing an unexpectedly long legacy value to bypass that
	// bound.
	return "legacy-" + model.BillingOperationKey(raw, legacyTaskAdjustmentComponent)
}

func legacyTaskAdjustmentSpec(task *model.Task, preConsumedQuota, actualQuota int) (model.BillingOperationSpec, error) {
	if task == nil {
		return model.BillingOperationSpec{}, fmt.Errorf("task is nil")
	}
	if preConsumedQuota < 0 || actualQuota <= 0 ||
		preConsumedQuota > common.MaxQuota || actualQuota > common.MaxQuota {
		return model.BillingOperationSpec{}, fmt.Errorf("task billing quota is out of range")
	}
	delta := int64(actualQuota) - int64(preConsumedQuota)
	if delta == 0 {
		return model.BillingOperationSpec{}, fmt.Errorf("task billing delta is zero")
	}
	requestID := legacyTaskAdjustmentRequestID(task, preConsumedQuota, actualQuota)
	if requestID == "" {
		return model.BillingOperationSpec{}, fmt.Errorf("task billing request id is missing")
	}
	spec := model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             legacyTaskAdjustmentComponent,
		UserID:                task.UserId,
		TokenID:               task.PrivateData.TokenId,
		SubscriptionID:        task.PrivateData.SubscriptionId,
		TokenDelta:            delta,
		UserUsedQuotaDelta:    delta,
		ChannelID:             task.ChannelId,
		ChannelUsedQuotaDelta: delta,
		TokenUnlimited:        task.BillingTokenUnlimited,
	}
	if task.BillingPlayground {
		spec.TokenDelta = 0
	}
	source := strings.TrimSpace(task.PrivateData.BillingSource)
	switch source {
	case "", BillingSourceWallet:
		spec.WalletDelta = -delta
		spec.RequireWalletBalance = delta > 0
	case BillingSourceSubscription:
		if task.PrivateData.SubscriptionId <= 0 {
			return model.BillingOperationSpec{}, fmt.Errorf("task subscription id is missing")
		}
		spec.SubscriptionDelta = delta
	default:
		return model.BillingOperationSpec{}, fmt.Errorf("unsupported task billing source %q", source)
	}
	if spec.TokenDelta > 0 && !spec.TokenUnlimited {
		spec.RequireTokenBalance = true
	}
	if task.ChannelId <= 0 {
		spec.ChannelID = 0
		spec.ChannelUsedQuotaDelta = 0
	}
	return spec, nil
}

// recordLegacyTaskAdjustmentLog writes the informational delta only after the
// durable operation has committed.  Replays that observe an already-applied
// marker intentionally skip the log so a stale poller cannot duplicate it.
func recordLegacyTaskAdjustmentLog(task *model.Task, preConsumedQuota, actualQuota, quotaDelta int, reason string, clamps ...*common.QuotaClamp) {
	if task == nil || quotaDelta == 0 {
		return
	}
	logType := model.LogTypeConsume
	logQuota := quotaDelta
	if quotaDelta < 0 {
		logType = model.LogTypeRefund
		logQuota = -quotaDelta
	}
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["pre_consumed_quota"] = preConsumedQuota
	other["actual_quota"] = actualQuota
	other["billing_legacy_reconciled"] = true
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
}

// RecalculateTaskQuota 通用的异步差额结算。
// actualQuota 是任务完成后的实际应扣额度，与预扣额度 (task.Quota) 做差额结算。
// reason 用于日志记录（例如 "token重算" 或 "adaptor调整"）。
// clamps 可选：若计算 actualQuota 时发生额度饱和，将其记入日志 admin_info（仅管理员可见）。
func RecalculateTaskQuota(ctx context.Context, task *model.Task, actualQuota int, reason string, clamps ...*common.QuotaClamp) {
	if task == nil || actualQuota <= 0 || actualQuota > common.MaxQuota {
		return
	}
	if task.Quota < 0 || task.Quota > common.MaxQuota {
		return
	}
	preConsumedQuota := task.Quota
	quotaDelta := actualQuota - preConsumedQuota

	if quotaDelta == 0 {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 预扣费准确（%s，%s）",
			task.TaskID, logger.LogQuota(actualQuota), reason))
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("任务 %s 差额结算：delta=%s（实际：%s，预扣：%s，%s）",
		task.TaskID,
		logger.LogQuota(quotaDelta),
		logger.LogQuota(actualQuota),
		logger.LogQuota(preConsumedQuota),
		reason,
	))

	// Persisted legacy rows did not carry BillingRequestId, but they still have
	// a primary key and can use a synthetic, row-scoped operation fence. The
	// model helper applies the ledger deltas and advances task.Quota in one
	// transaction, so a stale poller cannot repeat the charge or overdraft a
	// wallet after another request changed its balance.
	spec, specErr := legacyTaskAdjustmentSpec(task, preConsumedQuota, actualQuota)
	if specErr != nil {
		logger.LogError(ctx, fmt.Sprintf("legacy task billing specification invalid task %s: %v", task.TaskID, specErr))
		return
	}
	if task.ID > 0 {
		applied, err := model.ApplyBillingOperationAndTaskQuota(
			spec,
			task.ID,
			task.Status,
			preConsumedQuota,
			actualQuota,
		)
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("legacy task durable adjustment failed task %s: %v", task.TaskID, err))
			return
		}
		task.Quota = actualQuota
		if applied {
			recordLegacyTaskAdjustmentLog(task, preConsumedQuota, actualQuota, int(quotaDelta), reason, clamps...)
		}
		return
	}

	// Unsaved tasks are retained for backwards-compatible unit/test callers.
	// They have no durable task marker, but the synthetic operation still fences
	// the actual ledgers and keeps retries from applying the same delta twice.
	requestID := spec.RequestID
	alreadyApplied := false
	if operation, err := model.GetBillingOperation(requestID, legacyTaskAdjustmentComponent); err == nil && operation != nil {
		alreadyApplied = operation.Status == model.BillingOperationApplied
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		logger.LogError(ctx, fmt.Sprintf("load legacy task adjustment marker failed task %s: %v", task.TaskID, err))
		return
	}
	if err := model.ApplyBillingOperation(spec); err != nil {
		logger.LogError(ctx, fmt.Sprintf("legacy task adjustment failed task %s: %v", task.TaskID, err))
		return
	}
	task.Quota = actualQuota
	if !alreadyApplied {
		recordLegacyTaskAdjustmentLog(task, preConsumedQuota, actualQuota, int(quotaDelta), reason, clamps...)
	}
	return

}

// RecalculateTaskQuotaByTokens 根据实际 token 消耗重新计费（异步差额结算）。
// 当任务成功且返回了 totalTokens 时，根据模型倍率和分组倍率重新计算实际扣费额度，
// 与预扣费的差额进行补扣或退还。支持钱包和订阅计费来源。
func RecalculateTaskQuotaByTokens(ctx context.Context, task *model.Task, totalTokens int) {
	actualQuota, reason, clamp, ok := calculateTaskQuotaByTokens(task, totalTokens)
	if !ok {
		return
	}
	RecalculateTaskQuota(ctx, task, actualQuota, reason, clamp)
}

// calculateTaskQuotaByTokens computes the terminal amount without mutating any
// ledger.  Keeping this pure lets the durable task finalizer persist the target
// before applying its idempotent BillingOperation.
func calculateTaskQuotaByTokens(task *model.Task, totalTokens int) (actualQuota int, reason string, clamp *common.QuotaClamp, ok bool) {
	if task == nil || totalTokens <= 0 {
		return 0, "", nil, false
	}

	modelName := taskModelName(task)
	modelRatio := 0.0
	finalGroupRatio := 0.0
	// Modern tasks persist the exact effective model/group ratios used when the
	// provider request was submitted. A later pricing or user-group change must
	// not rewrite that task's terminal charge. Only legacy snapshots without an
	// origin model fall back to live settings.
	if bc := task.PrivateData.BillingContext; bc != nil && strings.TrimSpace(bc.OriginModelName) != "" {
		modelRatio = bc.ModelRatio
		finalGroupRatio = bc.GroupRatio
	} else {
		var hasRatioSetting bool
		modelRatio, hasRatioSetting, _ = ratio_setting.GetModelRatio(modelName)
		if !hasRatioSetting {
			return 0, "", nil, false
		}

		group := task.Group
		if group == "" {
			user, err := model.GetUserById(task.UserId, false)
			if err == nil && user != nil {
				group = user.Group
			}
		}
		if group == "" {
			return 0, "", nil, false
		}

		finalGroupRatio = ratio_setting.GetGroupRatio(group)
		if userGroupRatio, hasUserGroupRatio := ratio_setting.GetGroupGroupRatio(group, group); hasUserGroupRatio {
			finalGroupRatio = userGroupRatio
		}
	}
	if modelRatio <= 0 || finalGroupRatio <= 0 {
		return 0, "", nil, false
	}

	otherMultiplier := 1.0
	if priceData := taskBillingContextPriceData(task.PrivateData.BillingContext); priceData != nil {
		otherMultiplier = priceData.OtherRatioMultiplier()
	}

	actualQuota, clamp = common.QuotaFromFloatChecked(float64(totalTokens) * modelRatio * finalGroupRatio * otherMultiplier)
	reason = fmt.Sprintf("token重算：tokens=%d, modelRatio=%.2f, groupRatio=%.2f, otherMultiplier=%.4f", totalTokens, modelRatio, finalGroupRatio, otherMultiplier)
	return actualQuota, reason, clamp, actualQuota > 0
}
