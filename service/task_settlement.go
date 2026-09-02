package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"gorm.io/gorm"
)

const (
	taskBillingSettlementLease = 2 * time.Minute
	taskBillingSettlementRetry = 15 * time.Second
	taskBillingAdjustmentLease = 2 * time.Minute
	taskBillingAdjustmentRetry = 15 * time.Second
	// These component names are persisted in BillingOperation and therefore
	// must remain stable across deployments.
	taskBillingSettlementComponent      = "settle"
	taskBillingSettlementUsageComponent = "settle_usage"
)

// taskBillingFinancialComponents is the complete set of durable settlement
// components that can be left pending across a process/database interruption.
// The normal async path uses settle, but the component-specific aliases are
// still emitted by compatibility funding implementations and older workers.
// Keeping the allowlist in one place prevents the scheduler probe and the
// replay worker from silently drifting apart. legacy_task_recalculate is
// intentionally excluded: its operation key is a one-way hash of a legacy
// Task row, so a marker-only replay could update ledgers without advancing the
// corresponding Task.Quota. That component is replayed only by the task-aware
// RecalculateTaskQuota seam (or sent to manual reconciliation).
var taskBillingFinancialComponents = []string{
	taskBillingSettlementComponent,
	legacyBillingSettlementComponent,
	model.BillingOperationWalletSettleComponent,
	model.BillingOperationSubscriptionSettleComponent,
	model.BillingOperationTerminalSettleComponent,
}

// taskBillingUsageComponents contains usage markers whose counters are safe to
// replay only after a corresponding financial marker has been applied. The
// legacy_settle_usage component is intentionally excluded: it is a direct
// compatibility entry for historical rows that have no financial companion and
// must not be turned into a permanently "pending" scheduler alarm.
var taskBillingUsageComponents = []string{
	taskBillingSettlementUsageComponent,
	syncBillingUsageComponent,
	model.BillingOperationTerminalSettleUsageComponent,
}

// TaskBillingOperationComponents returns a copy of every marker component that
// should wake the async billing reconciler. A copy keeps callers from mutating
// the process-wide allowlist and accidentally disabling recovery in another
// goroutine.
func TaskBillingOperationComponents() []string {
	components := make([]string, 0, len(taskBillingFinancialComponents)+len(taskBillingUsageComponents))
	components = append(components, taskBillingFinancialComponents...)
	components = append(components, taskBillingUsageComponents...)
	return components
}

// containsTaskBillingComponent is deliberately kept next to the allowlists.
// The reconciler queries by the complete alias set and then performs two
// passes over the returned rows; using the same predicates for both operations
// avoids a subtle failure mode where a newly-added alias is fetched but never
// marked as processed (and therefore remains pending forever).
func containsTaskBillingComponent(components []string, component string) bool {
	component = strings.TrimSpace(component)
	if component == "" {
		return false
	}
	for _, candidate := range components {
		if component == candidate {
			return true
		}
	}
	return false
}

func isTaskBillingFinancialComponent(component string) bool {
	return containsTaskBillingComponent(taskBillingFinancialComponents, component)
}

func isTaskBillingUsageComponent(component string) bool {
	return containsTaskBillingComponent(taskBillingUsageComponents, component)
}

// ErrTaskBillingSettlementInProgress tells callers that another worker owns
// the lease.  It is intentionally distinct from a failed settlement: terminal
// refund paths must not proceed while a concurrent settlement may still be
// committing the final delta.
var ErrTaskBillingSettlementInProgress = errors.New("task billing settlement is in progress")

// ErrTaskBillingSettlementSnapshotMissing identifies a modern task whose
// request-scoped billing snapshot cannot explain its persisted quota. A zero
// settlement amount is valid, but it is not a licence to infer a charge from
// the mutable Task.Quota field. Callers must fence this row for manual
// reconciliation instead of guessing at the amount.
var ErrTaskBillingSettlementSnapshotMissing = errors.New("task billing settlement snapshot is missing")

// taskSettlementQuotaSnapshot returns the immutable amount that was captured
// when an async request was accepted. Rows without BillingRequestId predate
// the durable snapshot and retain the historical Task.Quota fallback. Once a
// request id exists, BillingSettlementQuota is authoritative even when zero.
// The one ambiguous shape is an all-zero request snapshot paired with a
// positive mutable Task.Quota.  A durable settlement marker (or a completed
// settlement state) proves that the zero was intentional; without that proof
// the row is rejected fail-closed rather than guessed.
func taskSettlementQuotaSnapshot(task *model.Task) (int, error) {
	if task == nil {
		return 0, errors.New("task is nil")
	}
	settlementQuota := task.BillingSettlementQuota
	requestID := strings.TrimSpace(task.BillingRequestId)
	if requestID == "" && settlementQuota == 0 && task.Quota > 0 {
		settlementQuota = task.Quota
	}
	if settlementQuota < 0 || settlementQuota > common.MaxQuota {
		return 0, errors.New("task billing settlement quota is out of range")
	}
	if task.BillingPreConsumedQuota < 0 || task.BillingPreConsumedQuota > common.MaxQuota {
		return 0, errors.New("task billing pre-consumed quota is out of range")
	}
	// A positive pre-consume snapshot proves that zero is an intentional final
	// amount (the provider may report no billable usage and the reservation is
	// then fully returned). For an all-zero snapshot, an already-completed
	// state or durable settlement marker is the minimum proof that zero was
	// captured intentionally; otherwise the mutable quota is an ambiguous
	// legacy value and must be fenced.
	if requestID != "" && task.BillingPreConsumedQuota == 0 && settlementQuota == 0 && task.Quota > 0 &&
		task.BillingSettlementState != model.TaskBillingSettlementComplete && !taskHasDurableSettlementMarker(task) {
		return 0, fmt.Errorf("%w: request-scoped task has quota=%d with zero settlement snapshot", ErrTaskBillingSettlementSnapshotMissing, task.Quota)
	}
	return settlementQuota, nil
}

func taskHasDurableSettlementMarker(task *model.Task) bool {
	if task == nil || strings.TrimSpace(task.BillingRequestId) == "" || model.DB == nil {
		return false
	}
	operation, err := model.GetBillingOperation(task.BillingRequestId, taskBillingSettlementComponent)
	return err == nil && operation != nil
}

// validateGenericAsyncSettlementOwner prevents marker-only recovery from
// charging a provider-accepted request that has no pollable local Task row.
// The generic settlement marker contains ledger identities, but not the
// provider task identity or response metadata needed to query/refund the job.
// Midjourney uses the legacy_settle component plus its own durable submit
// intent, so only the generic settle component requires a Task owner here.
func validateGenericAsyncSettlementOwner(row model.BillingOperation) error {
	if strings.TrimSpace(row.Component) != taskBillingSettlementComponent {
		return nil
	}
	if model.DB == nil {
		return errors.New("database is not initialized")
	}
	requestID := strings.TrimSpace(row.RequestId)
	if requestID == "" {
		return errors.New("async settlement request id is missing")
	}
	var tasks []model.Task
	if err := model.DB.Where("billing_request_id = ?", requestID).
		Order("id").Limit(2).Find(&tasks).Error; err != nil {
		return fmt.Errorf("load async settlement task owner: %w", err)
	}
	if len(tasks) == 0 {
		return errors.New("async settlement has no durable task owner; manual reconciliation required")
	}
	if len(tasks) != 1 {
		return fmt.Errorf("%w: async settlement has multiple task owners", model.ErrBillingOperationConflict)
	}
	task := &tasks[0]
	if task.UserId != row.UserId || row.UserId <= 0 {
		return fmt.Errorf("%w: async settlement task owner mismatch", model.ErrBillingOperationConflict)
	}
	if task.BillingSettlementState == model.TaskBillingSettlementManual {
		return errors.New("async settlement task is fenced for manual reconciliation")
	}
	expected, err := taskSettlementSpec(task)
	if err != nil {
		return fmt.Errorf("rebuild async settlement from task owner: %w", err)
	}
	if strings.TrimSpace(expected.RequestID) != requestID ||
		strings.TrimSpace(expected.Component) != strings.TrimSpace(row.Component) ||
		expected.UserID != row.UserId ||
		expected.TokenID != row.TokenId ||
		expected.SubscriptionID != row.SubscriptionId ||
		expected.WalletDelta != row.WalletDelta ||
		expected.TokenDelta != row.TokenDelta ||
		expected.SubscriptionDelta != row.SubscriptionDelta ||
		expected.UserUsedQuotaDelta != row.UserUsedQuotaDelta ||
		expected.UserRequestCountDelta != row.UserRequestCountDelta ||
		expected.ChannelID != row.ChannelId ||
		expected.ChannelUsedQuotaDelta != row.ChannelUsedQuotaDelta ||
		expected.RequireWalletBalance != row.RequireWalletBalance ||
		expected.RequireTokenBalance != row.RequireTokenBalance ||
		expected.TokenUnlimited != row.TokenUnlimited {
		return fmt.Errorf("%w: async settlement marker does not match task snapshot", model.ErrBillingOperationConflict)
	}
	return nil
}

func deferPendingBillingOperation(row model.BillingOperation, cause error) error {
	if err := model.DeferPendingBillingOperation(row.Id, row.UpdatedAt); err != nil {
		return errors.Join(cause, fmt.Errorf("defer pending billing operation: %w", err))
	}
	return cause
}

// markTaskSettlementSnapshotManual records the fail-closed state for a task
// whose immutable billing inputs are contradictory. The transition is one
// conditional UPDATE covering empty/pending/processing states so a competing
// worker cannot claim a row between an empty->pending upgrade and the manual
// fence.
func markTaskSettlementSnapshotManual(task *model.Task, reason error) {
	if task == nil || task.ID <= 0 || model.DB == nil {
		return
	}
	message := "manual reconciliation required"
	if reason != nil && strings.TrimSpace(reason.Error()) != "" {
		message = strings.TrimSpace(reason.Error())
		if len(message) > 2000 {
			message = message[:2000]
		}
	}
	result := model.DB.Model(&model.Task{}).
		Where("id = ?", task.ID).
		Where("(billing_settlement_state = '' OR billing_settlement_state IS NULL OR billing_settlement_state IN ?)", []model.TaskBillingSettlementState{
			model.TaskBillingSettlementPending,
			model.TaskBillingSettlementProcessing,
		}).
		Updates(map[string]interface{}{
			"billing_settlement_state": model.TaskBillingSettlementManual,
			"billing_settlement_until": 0,
			"billing_settlement_error": message,
		})
	if result.Error == nil && result.RowsAffected == 1 {
		task.BillingSettlementState = model.TaskBillingSettlementManual
		task.BillingSettlementUntil = 0
		task.BillingSettlementError = message
	}
}

// EnsureAsyncTaskBillingOperations installs the durable settlement and usage
// markers before an accepted async task is persisted. The two markers are
// created atomically; if the process exits after the provider accepted the
// request but before the task INSERT/settlement, they retain the intent without
// charging an unqueryable task. The reconciliation worker applies them only
// after one Task row with the exact immutable billing snapshot owns the request.
//
// Marker creation is intentionally separate from ApplyBillingOperation. It
// records intent only and never changes a wallet, token, subscription, or
// usage counter. Existing rows are validated for an exact immutable match.
func EnsureAsyncTaskBillingOperations(relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	if relayInfo == nil {
		return errors.New("relay info is nil")
	}
	// Async task persistence and polling both derive their channel namespace
	// from ChannelMeta. A nil embedded pointer has no safe promoted ChannelId
	// value; accepting it and silently writing channel_id=0 would let a later
	// task row carry a different channel identity from its usage marker. The
	// zero-id case remains valid when metadata is present (for internal/free
	// tasks that intentionally have no channel ledger).
	if relayInfo.ChannelMeta == nil {
		return errors.New("async task channel metadata is missing")
	}
	requestID := strings.TrimSpace(relayInfo.RequestId)
	if requestID == "" {
		return errors.New("task billing request id is missing")
	}
	if actualQuota < 0 || actualQuota > common.MaxQuota {
		return fmt.Errorf("task billing settlement quota is out of range: %d", actualQuota)
	}

	specs := make([]model.BillingOperationSpec, 0, 2)
	if relayInfo.Billing != nil {
		session, ok := relayInfo.Billing.(*BillingSession)
		if !ok || session == nil {
			return errors.New("async task billing session is not durable")
		}
		spec, err := session.BuildSettlementOperationSpec(actualQuota)
		if err != nil {
			return err
		}
		// Keep the component constant explicit in case the session helper is
		// reused by a future non-task settlement path.
		spec.Component = taskBillingSettlementComponent
		specs = append(specs, spec)
	} else if relayInfo.PriceData.FreeModel {
		// Free tasks have no financial session, but their usage marker still
		// requires an explicit, durable settlement companion.  Without this
		// zero-delta operation the fail-closed reconciler would (correctly for
		// paid data) leave the usage marker orphaned forever after a crash.
		specs = append(specs, model.BillingOperationSpec{
			RequestID: requestID,
			Component: taskBillingSettlementComponent,
			UserID:    relayInfo.UserId,
		})
	} else if !relayInfo.PriceData.FreeModel {
		// A paid async request without a reconstructible BillingSession cannot
		// produce a financial settlement companion. Do not persist an orphan
		// usage marker that the reconciler must leave pending forever.
		return errors.New("async task billing session is missing")
	}

	channelID := relayInfo.GetChannelID()
	usageSpec, err := taskBillingUsageOperationSpec(requestID, relayInfo.UserId, channelID, actualQuota)
	if err != nil {
		return err
	}
	usageSpec.Component = taskBillingSettlementUsageComponent
	specs = append(specs, usageSpec)
	return model.EnsureBillingOperations(specs...)
}

func taskBillingUsageOperationSpec(requestID string, userID, channelID, quota int) (model.BillingOperationSpec, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return model.BillingOperationSpec{}, errors.New("task billing request id is missing")
	}
	if userID <= 0 {
		return model.BillingOperationSpec{}, errors.New("task user id is missing")
	}
	if quota < 0 || quota > common.MaxQuota {
		return model.BillingOperationSpec{}, errors.New("task billing settlement quota is out of range")
	}
	if channelID < 0 {
		return model.BillingOperationSpec{}, errors.New("task channel id is invalid")
	}
	spec := model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             taskBillingSettlementUsageComponent,
		UserID:                userID,
		UserUsedQuotaDelta:    int64(quota),
		UserRequestCountDelta: 1,
		ChannelID:             channelID,
		ChannelUsedQuotaDelta: int64(quota),
	}
	if channelID <= 0 {
		spec.ChannelID = 0
		spec.ChannelUsedQuotaDelta = 0
	}
	return spec, nil
}

// taskSettlementSpec reconstructs the exact post-submit ledger operation from
// the immutable fields persisted on Task.  The operation component is the same
// "settle" component used by BillingSession, so a crash after the original
// transaction committed is a harmless no-op on retry.
func taskSettlementSpec(task *model.Task) (model.BillingOperationSpec, error) {
	if task == nil {
		return model.BillingOperationSpec{}, errors.New("task is nil")
	}
	requestID := strings.TrimSpace(task.BillingRequestId)
	if requestID == "" {
		return model.BillingOperationSpec{}, errors.New("task billing request id is missing")
	}
	if task.UserId <= 0 {
		return model.BillingOperationSpec{}, errors.New("task user id is missing")
	}
	settlementQuota, err := taskSettlementQuotaSnapshot(task)
	if err != nil {
		return model.BillingOperationSpec{}, err
	}
	if task.BillingPreConsumedQuota < 0 || task.BillingPreConsumedQuota > common.MaxQuota {
		return model.BillingOperationSpec{}, errors.New("task billing quota is out of range")
	}
	preConsumed := int64(task.BillingPreConsumedQuota)
	actual := int64(settlementQuota)
	delta := actual - preConsumed

	spec := model.BillingOperationSpec{
		RequestID:         requestID,
		Component:         taskBillingSettlementComponent,
		UserID:            task.UserId,
		TokenID:           task.PrivateData.TokenId,
		TokenUnlimited:    task.BillingTokenUnlimited,
		WalletDelta:       0,
		TokenDelta:        delta,
		SubscriptionDelta: 0,
	}
	if task.BillingPlayground {
		spec.TokenDelta = 0
	}
	switch strings.TrimSpace(task.PrivateData.BillingSource) {
	case "", BillingSourceWallet:
		spec.WalletDelta = -delta
	case BillingSourceSubscription:
		if task.PrivateData.SubscriptionId <= 0 {
			return model.BillingOperationSpec{}, errors.New("task subscription id is missing")
		}
		spec.SubscriptionID = task.PrivateData.SubscriptionId
		spec.SubscriptionDelta = delta
	default:
		return model.BillingOperationSpec{}, fmt.Errorf("unsupported task billing source %q", task.PrivateData.BillingSource)
	}
	if spec.TokenDelta != 0 && task.PrivateData.TokenId <= 0 {
		return model.BillingOperationSpec{}, errors.New("task token id is missing")
	}
	return spec, nil
}

// settleTaskBillingDurable applies the final post-submit delta.  It does not
// mutate Task itself; callers must mark the durable state complete only after
// this function returns nil.
func settleTaskBillingDurable(task *model.Task) error {
	spec, err := taskSettlementSpec(task)
	if err != nil {
		return err
	}
	if model.DB == nil {
		return errors.New("database is not initialized")
	}
	if spec.TokenDelta != 0 && spec.TokenID > 0 {
		// TokenKey is used solely for cache repair.  The database operation is
		// keyed by TokenID and remains authoritative when a token was rotated or
		// its cache entry was evicted.
		if token, err := model.GetTokenById(spec.TokenID); err == nil && token != nil {
			spec.TokenKey = token.Key
		}
	}
	return model.ApplyBillingOperation(spec)
}

// RecordTaskConsumptionOnce records the submit-time informational consume log
// and aggregate counters behind a durable at-most-once fence.  Financial state
// is committed separately by BillingOperation; the marker prevents retries
// from incrementing usage twice after a worker restart.
func RecordTaskConsumptionOnce(ctx context.Context, task *model.Task) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if task.ID <= 0 {
		return errors.New("task id is required for billing usage")
	}
	requestID := strings.TrimSpace(task.BillingRequestId)
	component := taskBillingSettlementUsageComponent
	if requestID == "" {
		// Rows created before BillingRequestId was introduced (including free
		// tasks replayed by an older worker) still need their informational
		// counters recorded. Derive a stable row-scoped operation key instead of
		// falling back to an un-fenced increment; the persisted task ID is the
		// minimum identity needed for a safe retry.
		requestID = fmt.Sprintf("legacy-task-row:%d", task.ID)
		component = "legacy_settle_usage"
	}
	settlementQuota, err := taskSettlementQuotaSnapshot(task)
	if err != nil {
		// A modern row with an ambiguous zero snapshot must never be allowed to
		// proceed through the usage journal. Fence it for manual review when the
		// row is durable; callers still receive the original typed error.
		if errors.Is(err, ErrTaskBillingSettlementSnapshotMissing) {
			markTaskSettlementSnapshotManual(task, err)
		}
		return err
	}
	// The usage marker used to be claimed before writing counters. If the
	// process died (or the database rejected a counter update) after that
	// claim, the retry saw a permanent "recorded" flag and silently lost
	// usage. Journal the counters behind their own idempotent BillingOperation
	// first; a replay is a no-op even when the task marker was not yet written.
	if err := applyTaskUsageOperation(requestID, component, task.UserId, task.ChannelId, settlementQuota); err != nil {
		return err
	}
	claimed, err := task.ClaimBillingUsage()
	if err != nil {
		// The operation is already durable. Returning the marker error keeps the
		// task in pending state so a later pass can retry only this cheap fence;
		// the journal prevents the usage counters from being applied twice.
		return err
	}
	if !claimed {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	logContent := fmt.Sprintf("操作 %s", task.Action)
	if task.PrivateData.BillingContext != nil && task.PrivateData.BillingContext.PerCallBilling {
		// Keep the same human-readable hint used by the request-path logger.
		// BillingContext remains authoritative for machine-readable pricing data.
		logContent += "，按次计费"
	}
	other := taskBillingOther(task)
	other["is_task"] = true
	other["task_id"] = task.TaskID
	other["billing_settlement_reconciled"] = true
	if task.BillingRequestId != "" {
		other["billing_request_id"] = task.BillingRequestId
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeConsume,
		Content:   logContent,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     settlementQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
	// Aggregate counters are committed by the idempotent journal above. Keep
	// this function's log write best-effort for backwards compatibility; a
	// future log outbox can replay it without touching financial state.
	return nil
}

// RecordTaskBillingUsageOnce journals informational usage for an accepted
// async request when no Task row could be persisted (for example a database
// failure immediately after the provider accepted the task). The request ID
// is the same immutable ID used by BillingSession's settlement operation, so
// a later successful task insert/retry can call RecordTaskConsumptionOnce
// without incrementing counters a second time.
func RecordTaskBillingUsageOnce(requestID string, userID, channelID, quota int) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errors.New("task billing request id is missing")
	}
	if userID <= 0 {
		return errors.New("task user id is missing")
	}
	if quota < 0 || quota > common.MaxQuota {
		return errors.New("task billing settlement quota is out of range")
	}
	return applyTaskUsageOperation(requestID, taskBillingSettlementUsageComponent, userID, channelID, quota)
}

func applyTaskUsageOperation(requestID, component string, userID, channelID, quota int) error {
	usageSpec, err := taskBillingUsageOperationSpec(requestID, userID, channelID, quota)
	if err != nil {
		return err
	}
	usageSpec.Component = component
	// Usage is an informational companion and the model deliberately refuses
	// to apply it before a financial settlement marker.  Most callers already
	// settle first, but the accepted-async recovery path can invoke this helper
	// while the exact settlement marker is still pending (for example a free
	// task, or a process that crashed after marker creation).  Replay that
	// persisted spec instead of fabricating a zero/guessed financial delta: the
	// marker is the only source that knows whether wallet, token, or subscription
	// ledgers were involved.  If no companion exists, fall through to the model
	// invariant so the transaction fails closed without leaving an orphan usage
	// marker.
	if err := applyExistingTaskBillingCompanion(requestID, component, userID, usageSpec); err != nil {
		return err
	}
	return model.ApplyBillingOperation(usageSpec)
}

// applyExistingTaskBillingCompanion makes a usage write compatible with the
// model's settlement-before-usage invariant without weakening that invariant.
// It only replays an already-durable financial marker; it never synthesizes a
// marker from the usage amount because that amount cannot identify the funding
// source or the pre-consume baseline.  A missing companion is intentionally
// left to ApplyBillingOperation, whose atomic check returns a conflict and
// rolls back any attempted usage marker creation.
func applyExistingTaskBillingCompanion(requestID, usageComponent string, userID int, usageSpec model.BillingOperationSpec) error {
	companions := taskBillingUsageCompanions(usageComponent)
	if len(companions) == 0 {
		// legacy_settle_usage is the pre-journal compatibility component and is
		// explicitly allowed to stand alone.
		return nil
	}
	if model.DB == nil {
		return errors.New("database is not initialized")
	}
	var rows []model.BillingOperation
	if err := model.DB.Where("request_id = ? AND component IN ?", requestID, companions).
		Order("id asc").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		// Do not create an orphan usage marker here. Let the model perform its
		// normal atomic check below, preserving the typed conflict for callers.
		return nil
	}
	if len(rows) != 1 {
		return fmt.Errorf("%w: usage operation requires exactly one financial companion", model.ErrBillingOperationConflict)
	}
	companion := rows[0]
	if companion.UserId != userID {
		return fmt.Errorf("%w: usage companion user mismatch", model.ErrBillingOperationConflict)
	}
	if companion.Status != model.BillingOperationPending {
		// Applied rows are already settled. Unknown statuses are intentionally
		// passed through to ApplyBillingOperation, which reports the durable
		// conflict using its normal validation path.
		return nil
	}
	// Validate/create the usage marker before applying the companion. This
	// catches a reused request key or changed quota without charging the
	// financial ledger first. EnsureBillingOperation is marker-only and is
	// therefore safe across a crash between these two calls.
	if err := model.EnsureBillingOperation(usageSpec); err != nil {
		return err
	}
	return applyPendingBillingOperationRow(companion)
}

// FinalizePendingTaskBilling claims, applies and completes one pending task.
// It is shared by the HTTP submit path, the background worker and realtime
// fetch.  The operation journal makes the apply step idempotent across all
// three entry points.
func FinalizePendingTaskBilling(ctx context.Context, task *model.Task) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if _, err := taskSettlementQuotaSnapshot(task); err != nil {
		if errors.Is(err, ErrTaskBillingSettlementSnapshotMissing) {
			markTaskSettlementSnapshotManual(task, err)
		}
		return err
	}
	if task.BillingSettlementState == model.TaskBillingSettlementComplete {
		return nil
	}
	if task.BillingSettlementState == "" {
		// A modern paid task must never silently bypass settlement merely because
		// an early deployment omitted the state column. Reconstruct a pending
		// marker from its immutable request snapshot; genuinely legacy/free rows
		// remain compatible with the old empty-state convention.
		// A zero pre-consume amount is valid for trusted wallet requests: the
		// request bypasses the reservation, but the provider can still report a
		// positive final charge.  Use the complete immutable snapshot when
		// deciding whether this is a genuinely free/legacy row; otherwise a
		// rollout that lost only the state column would silently skip settlement.
		if strings.TrimSpace(task.BillingRequestId) == "" ||
			(task.BillingPreConsumedQuota == 0 && task.BillingSettlementQuota == 0) {
			return nil
		}
		if model.DB == nil {
			return errors.New("database is not initialized")
		}
		result := model.DB.Model(&model.Task{}).
			Where("id = ? AND (billing_settlement_state = '' OR billing_settlement_state IS NULL)", task.ID).
			Updates(map[string]interface{}{"billing_settlement_state": model.TaskBillingSettlementPending, "billing_settlement_until": 0})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var current model.Task
			if err := model.DB.First(&current, task.ID).Error; err != nil {
				return err
			}
			*task = current
			if task.BillingSettlementState == model.TaskBillingSettlementComplete {
				return nil
			}
			if task.BillingSettlementState == "" {
				return errors.New("task billing settlement state could not be initialized")
			}
		}
		task.BillingSettlementState = model.TaskBillingSettlementPending
	}
	if task.BillingSettlementState == model.TaskBillingSettlementManual {
		return errors.New("task billing settlement requires manual reconciliation")
	}
	now := time.Now().Unix()
	claimed, err := task.ClaimBillingSettlement(now, now+int64(taskBillingSettlementLease/time.Second))
	if err != nil {
		return err
	}
	if !claimed {
		// Another worker may have completed it.  Reloading avoids reporting a
		// spurious error to a realtime fetch while preserving the lease fence.
		var current model.Task
		if model.DB == nil {
			return errors.New("database is not initialized")
		}
		if reloadErr := model.DB.First(&current, task.ID).Error; reloadErr != nil {
			return fmt.Errorf("reload task after settlement claim contention: %w", reloadErr)
		}
		if current.BillingSettlementState == model.TaskBillingSettlementComplete {
			*task = current
			return nil
		}
		return ErrTaskBillingSettlementInProgress
	}

	if err := settleTaskBillingDurable(task); err != nil {
		// BillingOperation is atomic and idempotent, so retrying after a DB
		// error is safe.  A short backoff avoids hot-looping during an outage.
		next := time.Now().Unix() + int64(taskBillingSettlementRetry/time.Second)
		if markErr := task.MarkBillingSettlementPending(next, err); markErr != nil {
			logger.LogError(ctx, fmt.Sprintf("failed to release task settlement lease task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(markErr.Error())))
		}
		return err
	}
	if err := RecordTaskConsumptionOnce(ctx, task); err != nil {
		next := time.Now().Unix() + int64(taskBillingSettlementRetry/time.Second)
		if markErr := task.MarkBillingSettlementPending(next, err); markErr != nil {
			return fmt.Errorf("task settlement usage recording failed: %w (mark pending: %v)", err, markErr)
		}
		return err
	}
	if err := task.MarkBillingSettlementComplete(task.BillingSettlementQuota); err != nil {
		// The financial operation and usage marker are already durable.  Keep the
		// row pending so a later pass can safely retry the final state transition.
		return err
	}
	return nil
}

// ReconcilePendingTaskSettlements processes a bounded batch.  The returned
// pending count is used in system-task telemetry and does not imply a refund;
// it means the durable operation will be retried after its backoff.
func ReconcilePendingTaskSettlements(ctx context.Context, limit int) (candidates, completed, pending int) {
	candidates, completed, pending, _ = reconcilePendingTaskSettlementsWithError(ctx, limit)
	return candidates, completed, pending
}

func reconcilePendingTaskSettlementsWithError(ctx context.Context, limit int) (candidates, completed, pending int, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tasks, err := model.GetTasksWithPendingBillingSettlementWithError(time.Now().Unix(), limit)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("load pending task settlements failed: %v", err))
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
		if err := FinalizePendingTaskBilling(ctx, task); err != nil {
			pending++
			runErrors = append(runErrors, err)
			continue
		}
		if task.BillingSettlementState == model.TaskBillingSettlementComplete {
			completed++
		} else {
			pending++
		}
	}
	return candidates, completed, pending, errors.Join(runErrors...)
}

// ReconcilePendingTaskBillingOperations replays marker-only async settlement
// operations. Generic Task settlements require a durable task owner before any
// ledger mutation: their markers do not contain enough provider identity to
// reconstruct an accepted task after a failed local INSERT. Provider-specific
// flows with their own accepted-task outbox (currently Midjourney) retain their
// separate recovery path.
//
// Settlement markers are always processed before settle_usage markers. A usage
// marker with a still-pending settlement companion is left pending so an
// outage cannot make aggregate usage claim a charge that was never committed.
func ReconcilePendingTaskBillingOperations(ctx context.Context, limit int) (candidates, completed, pending int) {
	candidates, completed, pending, _ = reconcilePendingTaskBillingOperationsWithError(ctx, limit)
	return candidates, completed, pending
}

func reconcilePendingTaskBillingOperationsWithError(ctx context.Context, limit int) (candidates, completed, pending int, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	batchLimit := limit
	if batchLimit <= 0 {
		batchLimit = 100
	} else if batchLimit > 1000 {
		batchLimit = 1000
	}
	financialComponents := append([]string(nil), taskBillingFinancialComponents...)
	usageComponents := append([]string(nil), taskBillingUsageComponents...)
	rows, err := model.GetPendingBillingOperations(financialComponents, batchLimit)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("load pending async billing operations failed: %v", err))
		return 0, 0, 1, err
	}
	// Query usage separately only after reserving the batch for financial work.
	// Otherwise old orphan usage markers can occupy every ID-ordered page and
	// permanently hide later settlement rows that would unblock valid usage.
	if remaining := batchLimit - len(rows); remaining > 0 {
		usageRows, usageErr := model.GetPendingBillingOperations(usageComponents, remaining)
		if usageErr != nil {
			logger.LogError(ctx, fmt.Sprintf("load pending async usage operations failed: %v", usageErr))
			return 0, 0, 1, usageErr
		}
		rows = append(rows, usageRows...)
	}
	candidates = len(rows)
	// Track each row independently.  Settlement and usage rows are traversed in
	// two passes, so deriving the remaining count from completed+pending can
	// double-count rows when cancellation lands between passes (or when a row
	// is skipped in the first pass).  A per-row flag makes the telemetry a
	// partition of the candidate batch: every row is accounted for exactly once.
	processed := make([]bool, len(rows))
	var runErrors []error
	markUnprocessedPending := func() {
		for i := range rows {
			if !processed[i] {
				processed[i] = true
				pending++
			}
		}
	}
	// Process financial settlement rows first, regardless of their numeric ID;
	// marker creation and database insertion happen in separate transactions.
	for i, row := range rows {
		if ctx.Err() != nil {
			markUnprocessedPending()
			runErrors = append(runErrors, ctx.Err())
			break
		}
		if !isTaskBillingFinancialComponent(row.Component) {
			continue
		}
		processed[i] = true
		if err := validateGenericAsyncSettlementOwner(row); err != nil {
			pending++
			runErrors = append(runErrors, deferPendingBillingOperation(row, err))
			logger.LogWarn(ctx, fmt.Sprintf("defer async settlement without durable task owner request_meta=%s: %v", common.SensitiveLogMeta(row.RequestId), err))
			continue
		}
		if err := applyPendingBillingOperationRow(row); err != nil {
			pending++
			runErrors = append(runErrors, deferPendingBillingOperation(row, err))
			logger.LogWarn(ctx, fmt.Sprintf("replay async settlement operation failed request_meta=%s: %v", common.SensitiveLogMeta(row.RequestId), err))
		} else {
			completed++
		}
	}
	for i, row := range rows {
		if ctx.Err() != nil {
			markUnprocessedPending()
			runErrors = append(runErrors, ctx.Err())
			break
		}
		if !isTaskBillingUsageComponent(row.Component) {
			continue
		}
		processed[i] = true
		companions := taskBillingUsageCompanions(row.Component)
		// A usage marker is only informational after its financial settlement
		// companion has been durably applied.  A missing companion means the
		// marker transaction was interrupted (or the data is legacy/manual), not
		// that the charge succeeded.  Leaving it pending lets reconciliation
		// repair the financial side first instead of inflating used_quota.
		companionApplied := 0
		var appliedCompanion *model.BillingOperation
		var lookupErrors []error
		var identityErr error
		for _, component := range companions {
			settlement, err := model.GetBillingOperation(row.RequestId, component)
			if err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					lookupErrors = append(lookupErrors, err)
				}
				continue
			}
			if row.UserId <= 0 || settlement.UserId != row.UserId {
				identityErr = fmt.Errorf("async usage operation owner does not match settlement companion")
				continue
			}
			if settlement.Status == model.BillingOperationApplied {
				companionApplied++
				matched := *settlement
				appliedCompanion = &matched
			}
		}
		if identityErr != nil || len(lookupErrors) > 0 || companionApplied != 1 {
			pending++
			var rowErr error
			if identityErr != nil {
				rowErr = identityErr
				logger.LogWarn(ctx, fmt.Sprintf("reject async usage owner mismatch request_meta=%s: %v", common.SensitiveLogMeta(row.RequestId), identityErr))
			} else if len(lookupErrors) > 0 {
				rowErr = errors.Join(lookupErrors...)
				logger.LogWarn(ctx, fmt.Sprintf("load async settlement companion failed request_meta=%s: %v", common.SensitiveLogMeta(row.RequestId), rowErr))
			} else {
				rowErr = errors.New("async usage operation requires exactly one applied settlement companion")
			}
			runErrors = append(runErrors, deferPendingBillingOperation(row, rowErr))
			continue
		}
		if appliedCompanion != nil {
			if err := validateGenericAsyncSettlementOwner(*appliedCompanion); err != nil {
				pending++
				runErrors = append(runErrors, deferPendingBillingOperation(row, err))
				logger.LogWarn(ctx, fmt.Sprintf("defer async usage without durable task owner request_meta=%s: %v", common.SensitiveLogMeta(row.RequestId), err))
				continue
			}
		}
		if err := applyPendingBillingOperationRow(row); err != nil {
			pending++
			runErrors = append(runErrors, deferPendingBillingOperation(row, err))
			logger.LogWarn(ctx, fmt.Sprintf("replay async usage operation failed request_meta=%s: %v", common.SensitiveLogMeta(row.RequestId), err))
		} else {
			completed++
		}
	}
	return candidates, completed, pending, errors.Join(runErrors...)
}

// taskBillingUsageCompanions returns the financial marker aliases that may
// authorize one usage marker. Keeping this relation explicit is important: a
// generic "any settlement for the request" lookup could let terminal usage be
// applied before the terminal adjustment (or let an unrelated compatibility
// marker authorize it).
func taskBillingUsageCompanions(component string) []string {
	switch component {
	case model.BillingOperationTerminalSettleUsageComponent:
		return []string{
			model.BillingOperationTerminalSettleComponent,
			model.BillingOperationLegacyTaskRecalculateComponent,
		}
	case taskBillingSettlementUsageComponent, syncBillingUsageComponent:
		return []string{
			taskBillingSettlementComponent,
			legacyBillingSettlementComponent,
			model.BillingOperationWalletSettleComponent,
			model.BillingOperationSubscriptionSettleComponent,
		}
	default:
		return nil
	}
}

func applyPendingBillingOperationRow(row model.BillingOperation) error {
	spec := model.BillingOperationSpec{
		RequestID:             row.RequestId,
		Component:             row.Component,
		UserID:                row.UserId,
		TokenID:               row.TokenId,
		SubscriptionID:        row.SubscriptionId,
		WalletDelta:           row.WalletDelta,
		TokenDelta:            row.TokenDelta,
		SubscriptionDelta:     row.SubscriptionDelta,
		UserUsedQuotaDelta:    row.UserUsedQuotaDelta,
		UserRequestCountDelta: row.UserRequestCountDelta,
		ChannelID:             row.ChannelId,
		ChannelUsedQuotaDelta: row.ChannelUsedQuotaDelta,
		RequireWalletBalance:  row.RequireWalletBalance,
		RequireTokenBalance:   row.RequireTokenBalance,
		TokenUnlimited:        row.TokenUnlimited,
	}
	if spec.TokenDelta != 0 && spec.TokenID > 0 {
		// TokenKey is an optional cache hint. Resolve it at replay time without
		// making the durable DB operation depend on a possibly rotated key.
		if token, err := model.GetTokenById(spec.TokenID); err == nil && token != nil {
			spec.TokenKey = token.Key
		}
	}
	return model.ApplyBillingOperation(spec)
}

// taskBillingAdjustmentSpec reconstructs the terminal delta from immutable
// task snapshots.  BillingSettlementQuota is the amount committed when the
// provider accepted the task; BillingFinalQuota is selected once, by the first
// terminal observer.  The operation component is intentionally distinct from
// "settle" so a retry cannot reinterpret the submit-time journal row.
func taskBillingAdjustmentSpec(task *model.Task) (model.BillingOperationSpec, error) {
	if task == nil {
		return model.BillingOperationSpec{}, errors.New("task is nil")
	}
	requestID := strings.TrimSpace(task.BillingRequestId)
	if requestID == "" {
		return model.BillingOperationSpec{}, errors.New("task billing request id is missing")
	}
	if _, err := taskSettlementQuotaSnapshot(task); err != nil {
		return model.BillingOperationSpec{}, err
	}
	if err := validateTaskBillingAdjustmentQuotas(task.BillingSettlementQuota, task.BillingFinalQuota); err != nil {
		return model.BillingOperationSpec{}, err
	}
	// Convert each bounded operand before subtracting.  Subtracting two int
	// values first can wrap on platforms where a corrupt/legacy row contains a
	// machine-sized value near MaxInt; the resulting small delta could then be
	// accepted as a legitimate refund.
	delta := int64(task.BillingFinalQuota) - int64(task.BillingSettlementQuota)
	spec := model.BillingOperationSpec{
		RequestID:         requestID,
		Component:         "terminal_settle",
		UserID:            task.UserId,
		TokenID:           task.PrivateData.TokenId,
		SubscriptionID:    task.PrivateData.SubscriptionId,
		TokenUnlimited:    task.BillingTokenUnlimited,
		TokenDelta:        delta,
		WalletDelta:       0,
		SubscriptionDelta: 0,
	}
	if task.BillingPlayground {
		spec.TokenDelta = 0
	}
	switch strings.TrimSpace(task.PrivateData.BillingSource) {
	case "", BillingSourceWallet:
		spec.WalletDelta = -delta
	case BillingSourceSubscription:
		if task.PrivateData.SubscriptionId <= 0 {
			return model.BillingOperationSpec{}, errors.New("task subscription id is missing")
		}
		spec.SubscriptionDelta = delta
	default:
		return model.BillingOperationSpec{}, fmt.Errorf("unsupported task billing source %q", task.PrivateData.BillingSource)
	}
	if spec.TokenDelta != 0 && task.PrivateData.TokenId <= 0 {
		return model.BillingOperationSpec{}, errors.New("task token id is missing")
	}
	if spec.WalletDelta != 0 && task.UserId <= 0 {
		return model.BillingOperationSpec{}, errors.New("task user id is missing")
	}
	return spec, nil
}

// validateTaskBillingAdjustmentQuotas is shared by every terminal-adjustment
// path.  Task billing snapshots are persisted as int columns, so checking only
// for negative values is insufficient: an out-of-range positive value can
// overflow the difference calculation and turn into an unintended credit.
func validateTaskBillingAdjustmentQuotas(settlementQuota, finalQuota int) error {
	if settlementQuota < 0 || finalQuota < 0 {
		return errors.New("task billing quota is negative")
	}
	if settlementQuota > common.MaxQuota || finalQuota > common.MaxQuota {
		return fmt.Errorf("task billing quota exceeds %d", common.MaxQuota)
	}
	return nil
}

// PrepareTaskBillingAdjustment selects and records the terminal target before
// any ledger mutation.  It is called while a terminal status CAS is being
// assembled, so a crash cannot leave a successful task without enough
// information for a later reconciliation pass.  Once a target is pending or
// processing it is immutable; a later provider response must not rewrite it.
func PrepareTaskBillingAdjustment(task *model.Task, adaptor TaskPollingAdaptor, taskResult *relaycommon.TaskInfo) (bool, error) {
	if task == nil || strings.TrimSpace(task.BillingRequestId) == "" {
		return false, nil
	}
	if task.BillingAdjustmentState == model.TaskBillingAdjustmentComplete || task.BillingAdjustmentState == model.TaskBillingAdjustmentManual {
		return true, nil
	}
	base, err := taskSettlementQuotaSnapshot(task)
	if err != nil {
		return false, err
	}
	target := base
	reason := "终态额度与提交预扣一致"
	if bc := task.PrivateData.BillingContext; bc == nil || !bc.PerCallBilling {
		if adaptor != nil {
			if actual := adaptor.AdjustBillingOnComplete(task, taskResult); actual > 0 {
				target = actual
				reason = "适配器终态计费调整"
			} else if taskResult != nil && taskResult.TotalTokens > 0 {
				if actual, tokenReason, _, ok := calculateTaskQuotaByTokens(task, taskResult.TotalTokens); ok {
					target = actual
					reason = tokenReason
				}
			}
		}
	}
	if target < 0 || target > common.MaxQuota {
		return false, errors.New("task billing final quota is out of range")
	}
	if task.BillingAdjustmentState != "" {
		if task.BillingFinalQuota != target {
			return false, fmt.Errorf("terminal billing target changed: previous=%d current=%d", task.BillingFinalQuota, target)
		}
		return true, nil
	}
	task.BillingFinalQuota = target
	task.BillingAdjustmentReason = reason
	task.BillingAdjustmentState = model.TaskBillingAdjustmentPending
	task.BillingAdjustmentUntil = 0
	task.BillingAdjustmentError = ""
	return true, nil
}

// StageTaskBillingAdjustmentManual places a fail-closed adjustment marker on
// the in-memory task. Callers persist it together with provider SUCCESS through
// UpdateWithStatus, so a calculation error can never leave a completed provider
// task eligible for timeout failure/refund.
func StageTaskBillingAdjustmentManual(task *model.Task, reason error) {
	if task == nil {
		return
	}
	message := "terminal billing adjustment requires manual reconciliation"
	if reason != nil && strings.TrimSpace(reason.Error()) != "" {
		message = strings.TrimSpace(reason.Error())
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	task.BillingAdjustmentState = model.TaskBillingAdjustmentManual
	task.BillingAdjustmentUntil = 0
	if task.BillingAdjustmentReason == "" {
		task.BillingAdjustmentReason = "终态差额结算需人工处理"
	}
	task.BillingAdjustmentError = message
}

// RecordTaskBillingAdjustmentUsageOnce records only the delta between the
// submit-time and terminal amounts. The aggregate delta is journaled before
// ClaimBillingAdjustmentUsage, making a replay safe from both lost and double
// counts; a later audit can reconstruct a missing informational log from the
// durable operation and task row.
func RecordTaskBillingAdjustmentUsageOnce(ctx context.Context, task *model.Task) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if task.ID <= 0 {
		return errors.New("task id is required for billing adjustment usage")
	}
	if _, err := taskSettlementQuotaSnapshot(task); err != nil {
		return err
	}
	if err := validateTaskBillingAdjustmentQuotas(task.BillingSettlementQuota, task.BillingFinalQuota); err != nil {
		return err
	}
	// Both operands are validated against MaxQuota before conversion, so this
	// subtraction cannot wrap and remains safe to pass to the usage journal.
	delta := int64(task.BillingFinalQuota) - int64(task.BillingSettlementQuota)
	requestID := strings.TrimSpace(task.BillingRequestId)
	if requestID == "" {
		return errors.New("task billing request id is missing")
	}
	// Journal the aggregate delta before setting the task marker. This closes
	// the marker-before-side-effect crash window while retaining exactly-once
	// semantics across worker restarts.
	usageSpec := model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             "terminal_settle_usage",
		UserID:                task.UserId,
		UserUsedQuotaDelta:    delta,
		ChannelID:             task.ChannelId,
		ChannelUsedQuotaDelta: delta,
	}
	if task.ChannelId <= 0 {
		usageSpec.ChannelID = 0
		usageSpec.ChannelUsedQuotaDelta = 0
	}
	if err := model.ApplyBillingOperation(usageSpec); err != nil {
		return err
	}
	claimed, err := task.ClaimBillingAdjustmentUsage()
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if delta == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	other := taskBillingOther(task)
	other["task_id"] = task.TaskID
	other["billing_terminal_adjustment"] = true
	other["pre_settlement_quota"] = task.BillingSettlementQuota
	other["final_quota"] = task.BillingFinalQuota
	logType := model.LogTypeConsume
	// delta is bounded by validateTaskBillingAdjustmentQuotas, so this
	// conversion is safe and cannot truncate a machine-sized corrupt value.
	logQuota := int(delta)
	if delta < 0 {
		logType = model.LogTypeRefund
		logQuota = int(-delta)
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   task.BillingAdjustmentReason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
	return nil
}

// FinalizePendingTaskBillingAdjustment claims, applies and completes a
// terminal adjustment.  It is safe to call from the background poller and the
// realtime fetch path concurrently: the DB lease fences the task marker and
// BillingOperation fences all non-idempotent ledger writes.
func FinalizePendingTaskBillingAdjustment(ctx context.Context, task *model.Task) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if _, err := taskSettlementQuotaSnapshot(task); err != nil {
		if errors.Is(err, ErrTaskBillingSettlementSnapshotMissing) {
			markTaskSettlementSnapshotManual(task, err)
			_ = task.MarkBillingAdjustmentManual(err)
		}
		return err
	}
	if task.BillingAdjustmentState == model.TaskBillingAdjustmentComplete {
		return nil
	}
	if task.BillingAdjustmentState == "" {
		if strings.TrimSpace(task.BillingRequestId) == "" {
			return nil
		}
		// Recover a crash between the terminal status CAS and target marker
		// persistence. The current task quota is the already committed submit
		// amount, so this conservative target produces a zero financial delta.
		if task.BillingFinalQuota == 0 {
			task.BillingFinalQuota = task.Quota
		}
		if task.BillingAdjustmentReason == "" {
			task.BillingAdjustmentReason = "异步任务终态差额结算（恢复）"
		}
		if model.DB == nil {
			return errors.New("database is not initialized")
		}
		if _, err := task.EnsureBillingAdjustmentTarget(task.BillingFinalQuota); err != nil {
			return err
		}
		if err := model.DB.First(task, task.ID).Error; err != nil {
			return fmt.Errorf("failed to reload terminal billing adjustment target: %w", err)
		}
	}
	if task.BillingAdjustmentState == model.TaskBillingAdjustmentManual {
		return errors.New("task billing adjustment requires manual reconciliation")
	}
	if model.DB == nil {
		return errors.New("database is not initialized")
	}
	now := time.Now().Unix()
	claimed, err := task.ClaimBillingAdjustment(now, now+int64(taskBillingAdjustmentLease/time.Second))
	if err != nil {
		return err
	}
	if !claimed {
		var current model.Task
		reloadErr := model.DB.First(&current, task.ID).Error
		if reloadErr == nil {
			// A legacy successful row can reach this function with an in-memory
			// target before the one-time marker was persisted (for example when
			// the provider returned an unchanged terminal response). Install that
			// target conditionally, then retry the claim. The empty-state predicate
			// prevents overwriting another worker's target.
			if current.BillingAdjustmentState == "" && strings.TrimSpace(task.BillingRequestId) != "" {
				current.BillingFinalQuota = task.BillingFinalQuota
				current.BillingAdjustmentReason = task.BillingAdjustmentReason
				if _, ensureErr := current.EnsureBillingAdjustmentTarget(current.BillingFinalQuota); ensureErr != nil {
					return ensureErr
				}
				if err := model.DB.First(&current, task.ID).Error; err != nil {
					return fmt.Errorf("reload terminal billing adjustment target: %w", err)
				}
				*task = current
				claimed, err = task.ClaimBillingAdjustment(now, now+int64(taskBillingAdjustmentLease/time.Second))
				if err != nil {
					return err
				}
			}
			*task = current
			if current.BillingAdjustmentState == model.TaskBillingAdjustmentComplete {
				return nil
			}
			if claimed {
				// Continue with the claimed, refreshed row below.
			} else {
				return nil
			}
		} else {
			return fmt.Errorf("reload task before billing adjustment claim: %w", reloadErr)
		}
	}

	// Always reload after claiming. The caller may hold a stale task pointer
	// whose quota/status was changed by the winning terminal CAS.
	var current model.Task
	if err := model.DB.First(&current, task.ID).Error; err != nil {
		return err
	}
	*task = current
	if task.Status != model.TaskStatusSuccess {
		return task.MarkBillingAdjustmentManual(errors.New("terminal billing adjustment requires SUCCESS task"))
	}

	// A terminal adjustment is defined relative to BillingSettlementQuota, so it
	// is unsafe until that baseline is known to be committed. This also covers a
	// modern row whose state column was lost during an older rollout: the submit
	// finalizer reconstructs and replays its durable operation before we continue.
	if task.BillingSettlementState != model.TaskBillingSettlementComplete {
		settlementErr := FinalizePendingTaskBilling(ctx, task)
		if err := model.DB.First(task, task.ID).Error; err != nil {
			return fmt.Errorf("reload task after submit billing settlement: %w", err)
		}
		if task.BillingSettlementState == model.TaskBillingSettlementManual {
			err := errors.New("submit billing settlement requires manual reconciliation")
			if settlementErr != nil {
				err = fmt.Errorf("%w: %v", err, settlementErr)
			}
			if markErr := task.MarkBillingAdjustmentManual(err); markErr != nil {
				return fmt.Errorf("%w (mark adjustment manual: %v)", err, markErr)
			}
			return err
		}
		if settlementErr != nil {
			next := time.Now().Unix() + int64(taskBillingAdjustmentRetry/time.Second)
			if markErr := task.MarkBillingAdjustmentPending(next, settlementErr); markErr != nil {
				return fmt.Errorf("submit billing settlement failed: %w (mark pending: %v)", settlementErr, markErr)
			}
			return settlementErr
		}
		if task.BillingSettlementState != model.TaskBillingSettlementComplete {
			err := errors.New("submit billing settlement is not complete")
			if markErr := task.MarkBillingAdjustmentPending(time.Now().Unix()+int64(taskBillingAdjustmentRetry/time.Second), err); markErr != nil {
				return fmt.Errorf("%w (mark pending: %v)", err, markErr)
			}
			return err
		}
	}

	spec, err := taskBillingAdjustmentSpec(task)
	if err != nil {
		if markErr := task.MarkBillingAdjustmentManual(err); markErr != nil {
			return fmt.Errorf("invalid terminal billing adjustment: %w (mark manual: %v)", err, markErr)
		}
		return err
	}
	// Apply the terminal financial marker even when the delta is zero.  The
	// marker is the immutable companion for terminal_settle_usage and records
	// the exact funding/token identity that was considered.  Skipping it for a
	// zero delta left an orphan usage marker after a crash and made a replay
	// unable to prove that the terminal adjustment had been evaluated.
	if spec.TokenDelta != 0 && spec.TokenID > 0 {
		if token, tokenErr := model.GetTokenById(spec.TokenID); tokenErr == nil && token != nil {
			spec.TokenKey = token.Key
		}
	}
	if err := model.ApplyBillingOperation(spec); err != nil {
		next := time.Now().Unix() + int64(taskBillingAdjustmentRetry/time.Second)
		if markErr := task.MarkBillingAdjustmentPending(next, err); markErr != nil {
			return fmt.Errorf("terminal billing adjustment failed: %w (mark pending: %v)", err, markErr)
		}
		return err
	}
	if err := RecordTaskBillingAdjustmentUsageOnce(ctx, task); err != nil {
		next := time.Now().Unix() + int64(taskBillingAdjustmentRetry/time.Second)
		if markErr := task.MarkBillingAdjustmentPending(next, err); markErr != nil {
			return fmt.Errorf("terminal usage recording failed: %w (mark pending: %v)", err, markErr)
		}
		return err
	}
	if err := task.MarkBillingAdjustmentComplete(task.BillingFinalQuota); err != nil {
		return err
	}
	return nil
}

// ReconcilePendingTaskBillingAdjustments processes a bounded batch of terminal
// adjustments for the scheduler. Pending count means the durable operation is
// deferred/retryable, not that a refund should be attempted.
func ReconcilePendingTaskBillingAdjustments(ctx context.Context, limit int) (candidates, completed, pending int) {
	candidates, completed, pending, _ = reconcilePendingTaskBillingAdjustmentsWithError(ctx, limit)
	return candidates, completed, pending
}

func reconcilePendingTaskBillingAdjustmentsWithError(ctx context.Context, limit int) (candidates, completed, pending int, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tasks, err := model.GetTasksWithPendingBillingAdjustmentWithError(time.Now().Unix(), limit)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("load pending task billing adjustments failed: %v", err))
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
		if err := FinalizePendingTaskBillingAdjustment(ctx, task); err != nil {
			pending++
			runErrors = append(runErrors, err)
			continue
		}
		if task.BillingAdjustmentState == model.TaskBillingAdjustmentComplete {
			completed++
		} else {
			pending++
		}
	}
	return candidates, completed, pending, errors.Join(runErrors...)
}

// SettleTaskBillingOnComplete is retained as the public hook used by realtime
// fetch callers. Modern tasks use the durable adjustment state; legacy rows
// without BillingRequestId are intentionally left to the historical helper.
func SettleTaskBillingOnComplete(ctx context.Context, adaptor TaskPollingAdaptor, task *model.Task, taskResult *relaycommon.TaskInfo) {
	if task == nil {
		return
	}
	if strings.TrimSpace(task.BillingRequestId) == "" {
		settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)
		return
	}
	if task.BillingAdjustmentState == "" {
		target, snapshotErr := taskSettlementQuotaSnapshot(task)
		if snapshotErr != nil {
			markTaskSettlementSnapshotManual(task, snapshotErr)
			logger.LogError(ctx, fmt.Sprintf("prepare terminal task billing adjustment failed task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(snapshotErr.Error())))
			return
		}
		if bc := task.PrivateData.BillingContext; bc != nil && !bc.PerCallBilling {
			if adaptor != nil {
				if actual := adaptor.AdjustBillingOnComplete(task, taskResult); actual > 0 {
					target = actual
				} else if taskResult != nil && taskResult.TotalTokens > 0 {
					if actual, _, _, ok := calculateTaskQuotaByTokens(task, taskResult.TotalTokens); ok {
						target = actual
					}
				}
			}
		}
		if task.BillingAdjustmentReason == "" {
			task.BillingAdjustmentReason = "异步任务终态差额结算"
		}
		if _, err := task.EnsureBillingAdjustmentTarget(target); err != nil {
			logger.LogError(ctx, fmt.Sprintf("prepare terminal task billing adjustment failed task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
			return
		}
		if model.DB == nil {
			logger.LogError(ctx, fmt.Sprintf("reload terminal task billing adjustment failed task=%s: database is not initialized", task.TaskID))
			return
		}
		if err := model.DB.First(task, task.ID).Error; err != nil {
			logger.LogError(ctx, fmt.Sprintf("reload terminal task billing adjustment failed task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
			return
		}
	}
	if err := FinalizePendingTaskBillingAdjustment(ctx, task); err != nil {
		logger.LogError(ctx, fmt.Sprintf("terminal task billing adjustment deferred task=%s error_meta=%s", task.TaskID, common.SensitiveLogMeta(err.Error())))
	}
}
