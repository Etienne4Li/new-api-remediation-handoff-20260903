package service

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func CovertMjpActionToModelName(mjAction string) string {
	modelName := "mj_" + strings.ToLower(mjAction)
	if mjAction == constant.MjActionSwapFace {
		modelName = "swap_face"
	}
	return modelName
}

const (
	// Midjourney uses the same durable component names as the generic async
	// task reconciler. Keeping the aliases identical means a marker left behind
	// by an accepted MJ request is picked up by the existing scheduler even
	// when the HTTP process dies before the legacy handler returns.
	midjourneySettlementComponent      = legacyBillingSettlementComponent
	midjourneySettlementUsageComponent = taskBillingSettlementUsageComponent
)

// midjourneyBillingSpecs builds the immutable financial and informational
// operations for one accepted, wallet-funded Midjourney request. Midjourney's
// historical endpoint does not run a BillingSession/pre-consume phase, so the
// full charge is represented as a legacy_settle operation (pre-consume=0).
// The task row and every retry must use this exact snapshot; deriving it from a
// later RelayInfo can otherwise charge a rotated token or a different channel.
func midjourneyBillingSpecs(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, quota int) (model.BillingOperationSpec, model.BillingOperationSpec, error) {
	if relayInfo == nil {
		return model.BillingOperationSpec{}, model.BillingOperationSpec{}, errors.New("relay info is nil")
	}
	requestID := strings.TrimSpace(relayInfo.RequestId)
	if requestID == "" {
		return model.BillingOperationSpec{}, model.BillingOperationSpec{}, errors.New("midjourney billing request id is missing")
	}
	if quota < 0 || quota > common.MaxQuota {
		return model.BillingOperationSpec{}, model.BillingOperationSpec{}, fmt.Errorf("midjourney billing quota is out of range: %d", quota)
	}
	if relayInfo.BillingSource == BillingSourceSubscription {
		return model.BillingOperationSpec{}, model.BillingOperationSpec{}, errors.New("legacy Midjourney billing does not support subscriptions")
	}
	userID := relayInfo.UserId
	tokenID := relayInfo.TokenId
	tokenKey := relayInfo.TokenKey
	channelID := relayInfo.GetChannelID()
	if task != nil {
		if task.UserId > 0 {
			userID = task.UserId
		}
		if task.TokenId > 0 {
			tokenID = task.TokenId
		}
		if task.BillingChannelId > 0 {
			channelID = task.BillingChannelId
		} else if task.ChannelId > 0 {
			channelID = task.ChannelId
		}
	}
	if userID <= 0 {
		return model.BillingOperationSpec{}, model.BillingOperationSpec{}, errors.New("midjourney billing user id is missing")
	}
	financial := model.BillingOperationSpec{
		RequestID:            requestID,
		Component:            midjourneySettlementComponent,
		UserID:               userID,
		TokenID:              tokenID,
		TokenKey:             tokenKey,
		WalletDelta:          -int64(quota),
		TokenDelta:           int64(quota),
		RequireWalletBalance: quota > 0,
		RequireTokenBalance:  quota > 0 && !relayInfo.TokenUnlimited && !relayInfo.IsPlayground,
		TokenUnlimited:       relayInfo.TokenUnlimited,
	}
	if relayInfo.IsPlayground {
		financial.TokenID = 0
		financial.TokenDelta = 0
		financial.RequireTokenBalance = false
	}
	if financial.TokenDelta != 0 && financial.TokenID <= 0 {
		return model.BillingOperationSpec{}, model.BillingOperationSpec{}, errors.New("midjourney billing token id is missing")
	}
	usage := model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             midjourneySettlementUsageComponent,
		UserID:                userID,
		UserUsedQuotaDelta:    int64(quota),
		UserRequestCountDelta: 1,
		ChannelID:             channelID,
		ChannelUsedQuotaDelta: int64(quota),
	}
	if channelID <= 0 {
		usage.ChannelID = 0
		usage.ChannelUsedQuotaDelta = 0
	}
	return financial, usage, nil
}

// EnsureMidjourneyTaskBillingOperations records the accepted request's
// settlement and usage intents before the task row is inserted. Marker
// creation is side-effect free; if the subsequent INSERT or settlement fails,
// the async billing reconciler can replay the same immutable operations.
func EnsureMidjourneyTaskBillingOperations(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, quota int) error {
	if model.DB == nil {
		return errors.New("billing operation database is not initialized")
	}
	financial, usage, err := midjourneyBillingSpecs(relayInfo, task, quota)
	if err != nil {
		return err
	}
	return model.EnsureBillingOperations(financial, usage)
}

// PrepareMidjourneySubmitIntent records a non-billing outbox row immediately
// before the provider request. It is the durable boundary for the otherwise
// unobservable "provider accepted, process died before marker creation"
// window. The function returns created=false for an exact pending duplicate;
// callers must not issue the upstream request again in that case because the
// provider API is not guaranteed to be idempotent.
func PrepareMidjourneySubmitIntent(relayInfo *relaycommon.RelayInfo, action string, executionChannelID, quota int) (bool, error) {
	if relayInfo == nil {
		return false, errors.New("relay info is nil")
	}
	requestID := strings.TrimSpace(relayInfo.RequestId)
	if requestID == "" || quota <= 0 {
		// No request identity or no billable amount means the historical path is
		// still sufficient; there is no ledger mutation to protect.
		return false, nil
	}
	if relayInfo.BillingSource == BillingSourceSubscription {
		return false, errors.New("legacy Midjourney billing does not support subscriptions")
	}
	billingChannelID := executionChannelID
	if relayInfo.ChannelMeta != nil && relayInfo.ChannelId > 0 {
		billingChannelID = relayInfo.ChannelId
	}
	intent := &model.MidjourneySubmitIntent{
		RequestId:        requestID,
		Status:           model.MidjourneySubmitIntentPending,
		UserId:           relayInfo.UserId,
		TokenId:          relayInfo.TokenId,
		ChannelId:        executionChannelID,
		BillingChannelId: billingChannelID,
		Quota:            quota,
		TokenUnlimited:   relayInfo.TokenUnlimited,
		Playground:       relayInfo.IsPlayground,
		Action:           action,
	}
	created, err := model.CreateMidjourneySubmitIntent(intent)
	if err != nil {
		return false, err
	}
	if !created {
		return false, model.ErrMidjourneySubmitIntentInFlight
	}
	return true, nil
}

// AcceptMidjourneySubmitIntent records the provider task identity before
// installing legacy_settle + settle_usage markers. It must be called only
// after an explicit billable provider response (HTTP success plus a valid
// provider task id). The model performs the two durable phases separately so a
// crash after the identity commit leaves accepted_pending for reconciliation;
// a retry with the same identity is idempotent and a different provider
// id/amount fails closed.
func AcceptMidjourneySubmitIntent(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, quota int) error {
	if relayInfo == nil || task == nil {
		return errors.New("relay info/task is nil")
	}
	requestID := strings.TrimSpace(relayInfo.RequestId)
	if requestID == "" {
		return nil
	}
	if quota <= 0 || strings.TrimSpace(task.MjId) == "" {
		return model.ErrMidjourneySubmitIntentInvalid
	}
	// Populate the same immutable billing fields that will be persisted on the
	// task row. midjourneyBillingSpecs then applies playground/token rules from
	// RelayInfo while honoring the request's chosen billing channel.
	task.BillingRequestId = requestID
	task.TokenId = relayInfo.TokenId
	task.BillingChannelId = task.ChannelId
	if relayInfo.ChannelMeta != nil && relayInfo.ChannelId > 0 {
		task.BillingChannelId = relayInfo.ChannelId
	}
	// Commit the provider identity before rebuilding/validating the billing
	// specs. That validation is normally trivial, but a process can die at any
	// instruction after this function is entered; accepted_pending then gives a
	// fresh worker the immutable intent snapshot needed to finish the charge.
	if err := model.MarkMidjourneySubmitIntentAcceptedPending(requestID, task.MjId); err != nil {
		return err
	}
	financial, usage, err := midjourneyBillingSpecs(relayInfo, task, quota)
	if err != nil {
		return err
	}
	return model.FinalizeMidjourneySubmitIntent(requestID, task.MjId, financial, usage)
}

// RejectMidjourneySubmitIntent records a confirmed non-billable provider
// response. It is intentionally not called for transport/parse errors, which
// leave the pre-provider intent pending for manual investigation.
func RejectMidjourneySubmitIntent(relayInfo *relaycommon.RelayInfo, reason string) error {
	if relayInfo == nil {
		return errors.New("relay info is nil")
	}
	requestID := strings.TrimSpace(relayInfo.RequestId)
	if requestID == "" {
		return nil
	}
	return model.RejectMidjourneySubmitIntent(requestID, reason)
}

func midjourneyBillingSpecsFromSubmitIntent(intent *model.MidjourneySubmitIntent) (model.BillingOperationSpec, model.BillingOperationSpec, error) {
	if intent == nil {
		return model.BillingOperationSpec{}, model.BillingOperationSpec{}, errors.New("Midjourney submit intent is nil")
	}
	info := &relaycommon.RelayInfo{
		RequestId:      intent.RequestId,
		UserId:         intent.UserId,
		TokenId:        intent.TokenId,
		TokenUnlimited: intent.TokenUnlimited,
		IsPlayground:   intent.Playground,
		BillingSource:  BillingSourceWallet,
		ChannelMeta:    &relaycommon.ChannelMeta{ChannelId: intent.BillingChannelId},
	}
	task := &model.Midjourney{
		UserId:           intent.UserId,
		TokenId:          intent.TokenId,
		ChannelId:        intent.ChannelId,
		BillingChannelId: intent.BillingChannelId,
	}
	return midjourneyBillingSpecs(info, task, intent.Quota)
}

// ReconcilePendingMidjourneySubmitIntents replays accepted-intent marker
// pairs. accepted_pending rows carry a durable provider task identity and are
// first finalized (status transition + marker creation) before any ledger is
// applied. Plain pending rows (provider outcome unknown) are never charged.
func ReconcilePendingMidjourneySubmitIntents(ctx context.Context, limit int) (candidates, completed, pending int) {
	candidates, completed, pending, _ = ReconcilePendingMidjourneySubmitIntentsWithError(ctx, limit)
	return candidates, completed, pending
}

// ReconcilePendingMidjourneySubmitIntentsWithError is the scheduler-facing
// variant that aggregates row errors while continuing with independent
// accepted intents.
func ReconcilePendingMidjourneySubmitIntentsWithError(ctx context.Context, limit int) (candidates, completed, pending int, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	intents, err := model.GetMidjourneySubmitIntentsNeedingReconcile(limit)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("load pending Midjourney submit intents failed: %v", err))
		return 0, 0, 1, err
	}
	candidates = len(intents)
	var runErrors []error
	for i, intent := range intents {
		if err := ctx.Err(); err != nil {
			pending += len(intents) - i
			runErrors = append(runErrors, err)
			break
		}
		financial, usage, specErr := midjourneyBillingSpecsFromSubmitIntent(&intent)
		if specErr == nil && intent.Status == model.MidjourneySubmitIntentAcceptedPending {
			if strings.TrimSpace(intent.ProviderTaskId) == "" {
				specErr = model.ErrMidjourneySubmitIntentConflict
			} else {
				specErr = model.FinalizeMidjourneySubmitIntent(
					intent.RequestId,
					intent.ProviderTaskId,
					financial,
					usage,
				)
			}
		}
		if specErr == nil {
			// Keep marker creation ahead of task restoration. InsertWithBillingFence
			// deliberately refuses a row without the immutable settlement marker;
			// accepted intents from an older/ambiguous run may need that marker pair
			// recreated here before the local task can be rebuilt.
			specErr = model.EnsureBillingOperations(financial, usage)
		}
		if specErr == nil {
			// Rebuild a minimal local task before applying any ledger delta. The
			// provider identity is durable in the intent, but without this row a
			// recovered charge could never be polled or refunded through the normal
			// Midjourney lifecycle. The model helper strictly validates an existing
			// row and uses the same request fence for an INSERT retry.
			_, specErr = model.EnsureMidjourneyTaskFromSubmitIntent(&intent)
		}
		if specErr == nil {
			specErr = model.ApplyBillingOperation(financial)
		}
		if specErr == nil {
			specErr = model.ApplyBillingOperation(usage)
		}
		if specErr == nil {
			specErr = model.MarkMidjourneySubmitIntentReconciled(intent.RequestId)
		}
		if specErr != nil {
			pending++
			if deferErr := model.DeferMidjourneySubmitIntentReconciliation(intent.Id, intent.UpdatedAt, specErr); deferErr != nil {
				specErr = errors.Join(specErr, fmt.Errorf("defer Midjourney submit intent: %w", deferErr))
			}
			runErrors = append(runErrors, specErr)
			logger.LogWarn(ctx, fmt.Sprintf("replay accepted Midjourney submit intent failed request_meta=%s: %v", common.SensitiveLogMeta(intent.RequestId), specErr))
			continue
		}
		completed++
	}
	return candidates, completed, pending, errors.Join(runErrors...)
}

func midjourneyDurableBilling(task *model.Midjourney) bool {
	return task != nil && strings.TrimSpace(task.BillingRequestId) != "" && model.DB != nil
}

// midjourneyBillingSpecFromRow reconstructs an immutable operation from the
// database marker.  A refund/settlement retry must use the marker's snapshot,
// never a fresh RelayInfo (which may point at a rotated token or a different
// execution channel).  TokenKey is only a cache-repair hint and is resolved
// opportunistically, just like the generic billing reconciler.
func midjourneyBillingSpecFromRow(row *model.BillingOperation) (model.BillingOperationSpec, error) {
	if row == nil {
		return model.BillingOperationSpec{}, errors.New("midjourney billing marker is nil")
	}
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
		if token, err := model.GetTokenById(spec.TokenID); err == nil && token != nil {
			spec.TokenKey = token.Key
		}
	}
	return spec, nil
}

// loadMidjourneySubmitBillingMarkers loads and validates the two immutable
// markers written before an accepted task is inserted.  A terminal refund is
// only safe after this baseline is known: refunding task.Quota while the
// submit settlement is still pending would credit the wallet/token against a
// pre-charge state, and a later settlement replay would charge it again.
//
// The pair is intentionally required for request-scoped tasks. Marker
// creation is atomic in PrepareMidjourneyTaskBilling, so a missing companion
// indicates a legacy/corrupt row that must remain pending for manual repair
// rather than being guessed from mutable task fields.
func loadMidjourneySubmitBillingMarkers(task *model.Midjourney) (*model.BillingOperation, *model.BillingOperation, error) {
	if task == nil {
		return nil, nil, errors.New("midjourney task is nil")
	}
	requestID := strings.TrimSpace(task.BillingRequestId)
	if requestID == "" {
		return nil, nil, errors.New("midjourney billing request id is missing")
	}
	financial, err := model.GetBillingOperation(requestID, midjourneySettlementComponent)
	if err != nil {
		return nil, nil, fmt.Errorf("load Midjourney settlement marker: %w", err)
	}
	usage, err := model.GetBillingOperation(requestID, midjourneySettlementUsageComponent)
	if err != nil {
		return nil, nil, fmt.Errorf("load Midjourney usage marker: %w", err)
	}
	if financial.RequestId != requestID || financial.Component != midjourneySettlementComponent ||
		usage.RequestId != requestID || usage.Component != midjourneySettlementUsageComponent {
		return nil, nil, model.ErrMidjourneyInsertFenceConflict
	}
	if financial.Status != model.BillingOperationPending && financial.Status != model.BillingOperationApplied {
		return nil, nil, fmt.Errorf("invalid Midjourney settlement marker status %q", financial.Status)
	}
	if usage.Status != model.BillingOperationPending && usage.Status != model.BillingOperationApplied {
		return nil, nil, fmt.Errorf("invalid Midjourney usage marker status %q", usage.Status)
	}
	if financial.UserId != task.UserId || usage.UserId != task.UserId || task.UserId <= 0 {
		return nil, nil, model.ErrMidjourneyInsertFenceConflict
	}
	if err := model.ValidateMidjourneyBillingMarker(financial, task); err != nil {
		return nil, nil, err
	}
	quota := int64(task.Quota)
	if quota <= 0 || quota > int64(common.MaxQuota) || financial.WalletDelta != -quota || financial.SubscriptionDelta != 0 {
		return nil, nil, fmt.Errorf("Midjourney settlement marker quota mismatch: task=%d wallet=%d", task.Quota, financial.WalletDelta)
	}
	// Midjourney submit settlement never carries informational counters on the
	// financial row; those belong exclusively to settle_usage. Reject a marker
	// that would otherwise double-apply usage during replay.
	if financial.UserUsedQuotaDelta != 0 || financial.UserRequestCountDelta != 0 ||
		financial.ChannelUsedQuotaDelta != 0 || financial.ChannelId != 0 {
		return nil, nil, model.ErrMidjourneyInsertFenceConflict
	}
	expectedChannel := task.GetBillingChannelId()
	if expectedChannel <= 0 {
		expectedChannel = 0
	}
	if usage.WalletDelta != 0 || usage.TokenDelta != 0 || usage.SubscriptionDelta != 0 ||
		usage.UserUsedQuotaDelta != quota || usage.UserRequestCountDelta != 1 ||
		usage.ChannelId != expectedChannel {
		return nil, nil, model.ErrMidjourneyInsertFenceConflict
	}
	if expectedChannel == 0 {
		if usage.ChannelUsedQuotaDelta != 0 {
			return nil, nil, model.ErrMidjourneyInsertFenceConflict
		}
	} else if usage.ChannelUsedQuotaDelta != quota {
		return nil, nil, model.ErrMidjourneyInsertFenceConflict
	}
	return financial, usage, nil
}

// ensureMidjourneySubmitSettlementComplete replays the request-scoped
// settlement pair before a terminal refund. It is safe to call repeatedly:
// ApplyBillingOperation observes already-applied markers and only advances a
// pending companion. The context is accepted for a uniform reconciler API;
// model operations are short DB transactions and do not perform provider I/O.
func ensureMidjourneySubmitSettlementComplete(ctx context.Context, task *model.Midjourney) error {
	if task == nil {
		return errors.New("midjourney task is nil")
	}
	if strings.TrimSpace(task.BillingRequestId) == "" {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	financial, usage, err := loadMidjourneySubmitBillingMarkers(task)
	if err != nil {
		return err
	}
	financialSpec, err := midjourneyBillingSpecFromRow(financial)
	if err != nil {
		return err
	}
	if err := model.ApplyBillingOperation(financialSpec); err != nil {
		return fmt.Errorf("apply Midjourney settlement marker: %w", err)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	usageSpec, err := midjourneyBillingSpecFromRow(usage)
	if err != nil {
		return err
	}
	if err := model.ApplyBillingOperation(usageSpec); err != nil {
		return fmt.Errorf("apply Midjourney usage marker: %w", err)
	}
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// PrepareMidjourneyTaskBilling sets the durable refund marker before the task is inserted.
func PrepareMidjourneyTaskBilling(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, quota int, shouldBill bool) (bool, error) {
	if task == nil {
		return false, errors.New("Midjourney task is nil")
	}
	task.Quota = 0
	task.TokenId = 0
	task.BillingChannelId = 0
	task.BillingRequestId = ""
	if !shouldBill {
		return false, nil
	}
	if relayInfo == nil {
		return false, errors.New("relay info is nil")
	}
	if quota < 0 {
		return false, errors.New("quota cannot be negative")
	}
	if relayInfo.BillingSource == BillingSourceSubscription {
		return false, errors.New("legacy Midjourney billing does not support subscriptions")
	}

	task.Quota = quota
	task.BillingChannelId = task.ChannelId
	if relayInfo.ChannelMeta != nil && relayInfo.ChannelId > 0 {
		task.BillingChannelId = relayInfo.ChannelId
	}
	requestID := strings.TrimSpace(relayInfo.RequestId)
	if requestID != "" && model.DB != nil {
		// Persist the token identity before the task row exists. Refund/replay
		// workers must never infer it from a later channel snapshot.
		task.TokenId = relayInfo.TokenId
		task.BillingRequestId = requestID
		if err := EnsureMidjourneyTaskBillingOperations(relayInfo, task, quota); err != nil {
			return false, err
		}
	}
	return true, nil
}

// SettleMidjourneyTaskBilling charges a persisted legacy task and records the applied stages.
func SettleMidjourneyTaskBilling(relayInfo *relaycommon.RelayInfo, task *model.Midjourney, prepared bool) (bool, error) {
	if !prepared {
		return false, nil
	}
	if relayInfo == nil {
		return false, errors.New("relay info is nil")
	}
	if task == nil || task.Id == 0 {
		return false, errors.New("Midjourney task must be persisted before billing")
	}
	if midjourneyDurableBilling(task) {
		financial, usage, err := midjourneyBillingSpecs(relayInfo, task, task.Quota)
		if err != nil {
			return false, err
		}
		// PrepareMidjourneyTaskBilling normally installs both rows. Re-ensuring
		// here repairs a task created by an older worker or a marker-only crash,
		// while preserving the immutable-spec conflict fence.
		if err := model.EnsureBillingOperations(financial, usage); err != nil {
			return false, err
		}
		if err := model.ApplyBillingOperation(financial); err != nil {
			return false, err
		}
		if err := model.ApplyBillingOperation(usage); err != nil {
			// The financial marker is already committed. Leave the usage marker
			// pending for the async reconciler; returning false prevents callers
			// from issuing a second best-effort aggregate update.
			return false, err
		}
		// If this request was created through the pre-provider intent outbox,
		// both markers are now confirmed applied. A failed timestamp update is
		// recoverable (the reconciler will replay the idempotent pair), so retain
		// the successful billing result while surfacing the diagnostic.
		if err := model.MarkMidjourneySubmitIntentReconciled(task.BillingRequestId); err != nil &&
			!errors.Is(err, model.ErrMidjourneySubmitIntentConflict) &&
			!errors.Is(err, gorm.ErrRecordNotFound) {
			return true, err
		}
		return true, nil
	}

	result, billingErr := postConsumeQuotaWithResult(relayInfo, task.Quota, 0, true)
	if !result.FundingApplied {
		task.Quota = 0
		task.TokenId = 0
		task.BillingChannelId = 0
		if updateErr := task.UpdateBillingState(); updateErr != nil {
			return false, errors.Join(billingErr, fmt.Errorf("clear Midjourney billing state: %w", updateErr))
		}
		return false, billingErr
	}

	task.TokenId = 0
	if result.TokenApplied {
		task.TokenId = relayInfo.TokenId
	}
	if updateErr := task.UpdateBillingState(); updateErr != nil {
		return true, errors.Join(billingErr, fmt.Errorf("update Midjourney billing state: %w", updateErr))
	}
	return true, billingErr
}

// RefundMidjourneyQuota reverses every accounting element recorded for a billed legacy task.
func RefundMidjourneyQuota(ctx context.Context, task *model.Midjourney, reason string) bool {
	if task == nil {
		logger.LogError(ctx, "Midjourney 退款失败：task 为空")
		return false
	}
	quota := task.Quota
	if quota < 0 {
		logger.LogError(ctx, fmt.Sprintf("Midjourney 退款失败：quota 为负数 task %s", task.MjId))
		return false
	}
	if quota == 0 {
		return true
	}
	// A refund must have a durable task marker to retry after an ambiguous
	// process/database failure.  Older code mutated token, wallet, usage and
	// the task row in separate calls; a crash between any two calls could charge
	// or credit twice.  Refuse an unsaved task rather than performing an
	// un-fenced ledger mutation that no later worker can reconcile.
	if task.Id <= 0 || model.DB == nil {
		logger.LogError(ctx, fmt.Sprintf("Midjourney 退款失败：task 未持久化 task=%s", task.MjId))
		return false
	}

	var (
		billingChannelID int
		requestID        string
		spec             model.BillingOperationSpec
	)
	if strings.TrimSpace(task.BillingRequestId) != "" {
		// A terminal provider failure can race the submit handler's settlement.
		// Finish the immutable submit markers first; otherwise refunding quota
		// against the pre-charge baseline would over-credit the account when a
		// later settlement retry runs.
		if err := ensureMidjourneySubmitSettlementComplete(ctx, task); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Midjourney 退款等待提交结算 task=%s error_meta=%s", task.MjId, common.SensitiveLogMeta(err.Error())))
			return false
		}
		financial, usage, err := loadMidjourneySubmitBillingMarkers(task)
		if err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Midjourney 退款加载提交标记失败 task=%s error_meta=%s", task.MjId, common.SensitiveLogMeta(err.Error())))
			return false
		}
		financialSpec, err := midjourneyBillingSpecFromRow(financial)
		if err != nil {
			return false
		}
		usageSpec, err := midjourneyBillingSpecFromRow(usage)
		if err != nil {
			return false
		}
		billingChannelID = usage.ChannelId
		requestID = strings.TrimSpace(task.BillingRequestId)
		// Reverse the exact marker snapshots. In particular, playground and
		// unlimited-token requests may intentionally have TokenDelta == 0.
		spec = model.BillingOperationSpec{
			RequestID:             requestID,
			Component:             model.BillingOperationLegacyTaskRefundComponent,
			UserID:                task.UserId,
			TokenID:               financialSpec.TokenID,
			WalletDelta:           -financialSpec.WalletDelta,
			TokenDelta:            -financialSpec.TokenDelta,
			SubscriptionDelta:     -financialSpec.SubscriptionDelta,
			UserUsedQuotaDelta:    -usageSpec.UserUsedQuotaDelta,
			ChannelID:             billingChannelID,
			ChannelUsedQuotaDelta: -usageSpec.ChannelUsedQuotaDelta,
			TokenUnlimited:        financialSpec.TokenUnlimited,
		}
		if spec.TokenDelta != 0 && spec.TokenID > 0 {
			if token, tokenErr := model.GetTokenById(spec.TokenID); tokenErr == nil && token != nil {
				spec.TokenKey = token.Key
			}
		}
	} else {
		billingChannelID = task.GetBillingChannelId()
		requestID = midjourneyRefundRequestID(task, quota, billingChannelID)
		if requestID == "" {
			logger.LogError(ctx, fmt.Sprintf("Midjourney 退款失败：账务身份缺失 task=%s", task.MjId))
			return false
		}
		spec = model.BillingOperationSpec{
			RequestID:             requestID,
			Component:             model.BillingOperationLegacyTaskRefundComponent,
			UserID:                task.UserId,
			TokenID:               task.TokenId,
			WalletDelta:           int64(quota),
			TokenDelta:            0,
			UserUsedQuotaDelta:    -int64(quota),
			ChannelID:             billingChannelID,
			ChannelUsedQuotaDelta: -int64(quota),
		}
		// A zero/legacy token marker means the original charge did not touch a
		// token. Otherwise reverse the exact token ledger identified by the task.
		if task.TokenId > 0 {
			spec.TokenDelta = -int64(quota)
			if token, err := model.GetTokenById(task.TokenId); err == nil && token != nil {
				spec.TokenKey = token.Key
			}
		}
		if billingChannelID <= 0 {
			spec.ChannelID = 0
			spec.ChannelUsedQuotaDelta = 0
		}
	}
	if requestID == "" || task.UserId <= 0 {
		logger.LogError(ctx, fmt.Sprintf("Midjourney 退款失败：账务身份缺失 task=%s", task.MjId))
		return false
	}

	// Install the marker in its own short transaction before touching any
	// ledger.  If the process exits after this point, the shared refund
	// reconciler can reconstruct the immutable spec and apply it exactly once.
	if err := model.EnsureBillingOperation(spec); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Midjourney 退款意图持久化失败 task=%s error_meta=%s", task.MjId, common.SensitiveLogMeta(err.Error())))
		return false
	}
	if err := model.ApplyBillingOperation(spec); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Midjourney 耐久退款失败 task=%s error_meta=%s", task.MjId, common.SensitiveLogMeta(err.Error())))
		return false
	}

	// Clear the task marker only after the idempotent operation commits. The CAS
	// is the at-most-once fence for the human log: two pollers may both replay
	// the same operation, but only one can change quota from the expected value
	// to zero and therefore only one emits a refund log.
	cleared, err := task.ClearBillingQuota(quota)
	if err != nil {
		// Keep the in-memory snapshot retryable as well. The operation is already
		// durable, so a later pass only repairs this marker and cannot double
		// credit any ledger.
		task.Quota = quota
		logger.LogError(ctx, fmt.Sprintf("Midjourney 退款已提交但清除 quota 失败 task=%s error_meta=%s", task.MjId, common.SensitiveLogMeta(err.Error())))
		return false
	}
	if !cleared {
		// Another worker won the quota CAS after applying the same idempotent
		// operation. Treat the refund as complete, but do not duplicate its log.
		return true
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: billingChannelID,
		ModelName: CovertMjpActionToModelName(task.Action),
		Quota:     quota,
		TokenId:   task.TokenId,
		Other: map[string]interface{}{
			"task_id": task.MjId,
			"reason":  reason,
		},
	})
	return true
}

// ReconcilePendingMidjourneyRefunds retries refunds for terminal Midjourney
// rows whose durable quota marker survived a process exit. This queue is
// separate from the provider polling queue: a task can already be at 100%
// and still need its accounting reversed when the process died between the
// terminal status CAS and RefundMidjourneyQuota.
func ReconcilePendingMidjourneyRefunds(ctx context.Context, limit int) (candidates, completed, pending int) {
	candidates, completed, pending, _ = ReconcilePendingMidjourneyRefundsWithError(ctx, limit)
	return candidates, completed, pending
}

// ReconcilePendingMidjourneyRefundsWithError is the scheduler-facing variant
// that retains an aggregate error. Every candidate is attempted so one
// malformed task does not starve unrelated refunds; unresolved rows remain in
// the database query for a later pass/manual repair.
func ReconcilePendingMidjourneyRefundsWithError(ctx context.Context, limit int) (candidates, completed, pending int, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tasks, err := model.GetMidjourneyTasksWithPendingRefundWithError(limit)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("load pending Midjourney refunds failed: %v", err))
		return 0, 0, 1, err
	}
	candidates = len(tasks)
	var runErrors []error
	for i, task := range tasks {
		if err := ctx.Err(); err != nil {
			pending += len(tasks) - i
			runErrors = append(runErrors, err)
			break
		}
		if RefundMidjourneyQuota(ctx, task, "终态任务退款重试") {
			completed++
			continue
		}
		pending++
		refundErr := fmt.Errorf("Midjourney refund remains pending task=%s", task.MjId)
		if deferErr := model.DeferMidjourneyRefundReconciliation(task.Id, task.RefundRetryAt); deferErr != nil {
			refundErr = errors.Join(refundErr, fmt.Errorf("defer Midjourney refund reconciliation: %w", deferErr))
		}
		runErrors = append(runErrors, refundErr)
	}
	return candidates, completed, pending, errors.Join(runErrors...)
}

// midjourneyRefundRequestID derives a bounded, immutable idempotency identity
// from the persisted task. Including the primary key and all billing routing
// fields prevents two legacy rows (or a changed execution channel) from
// sharing a refund operation while the SHA-256 digest keeps the database key
// within the BillingOperation request-id limit.
func midjourneyRefundRequestID(task *model.Midjourney, quota, billingChannelID int) string {
	if task == nil || task.Id <= 0 || quota <= 0 {
		return ""
	}
	raw := fmt.Sprintf("midjourney-refund|id=%d|mj=%s|user=%d|token=%d|channel=%d|quota=%d",
		task.Id, strings.TrimSpace(task.MjId), task.UserId, task.TokenId, billingChannelID, quota)
	return "legacy-mj-refund-" + model.BillingOperationKey(raw, model.BillingOperationLegacyTaskRefundComponent)
}

func GetMjRequestModel(relayMode int, midjRequest *dto.MidjourneyRequest) (string, *dto.MidjourneyResponse, bool) {
	action := ""
	if relayMode == relayconstant.RelayModeMidjourneyAction {
		// plus request
		err := CoverPlusActionToNormalAction(midjRequest)
		if err != nil {
			return "", err, false
		}
		action = midjRequest.Action
	} else {
		switch relayMode {
		case relayconstant.RelayModeMidjourneyImagine:
			action = constant.MjActionImagine
		case relayconstant.RelayModeMidjourneyVideo:
			action = constant.MjActionVideo
		case relayconstant.RelayModeMidjourneyEdits:
			action = constant.MjActionEdits
		case relayconstant.RelayModeMidjourneyDescribe:
			action = constant.MjActionDescribe
		case relayconstant.RelayModeMidjourneyBlend:
			action = constant.MjActionBlend
		case relayconstant.RelayModeMidjourneyShorten:
			action = constant.MjActionShorten
		case relayconstant.RelayModeMidjourneyChange:
			action = midjRequest.Action
		case relayconstant.RelayModeMidjourneyModal:
			action = constant.MjActionModal
		case relayconstant.RelayModeSwapFace:
			action = constant.MjActionSwapFace
		case relayconstant.RelayModeMidjourneyUpload:
			action = constant.MjActionUpload
		case relayconstant.RelayModeMidjourneySimpleChange:
			params := ConvertSimpleChangeParams(midjRequest.Content)
			if params == nil {
				return "", MidjourneyErrorWrapper(constant.MjRequestError, "invalid_request"), false
			}
			action = params.Action
		case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition, relayconstant.RelayModeMidjourneyNotify:
			return "", nil, true
		default:
			return "", MidjourneyErrorWrapper(constant.MjRequestError, "unknown_relay_action"), false
		}
	}
	modelName := CovertMjpActionToModelName(action)
	return modelName, nil, true
}

func CoverPlusActionToNormalAction(midjRequest *dto.MidjourneyRequest) *dto.MidjourneyResponse {
	if midjRequest == nil {
		return MidjourneyErrorWrapper(constant.MjRequestError, "request_is_required")
	}
	// "customId": "MJ::JOB::upsample::2::3dbbd469-36af-4a0f-8f02-df6c579e7011"
	customId := midjRequest.CustomId
	if customId == "" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "custom_id_is_required")
	}
	splits := strings.Split(customId, "::")
	if len(splits) < 2 {
		return MidjourneyErrorWrapper(constant.MjRequestError, "invalid_custom_id")
	}
	var action string
	if splits[1] == "JOB" {
		if len(splits) < 3 || splits[2] == "" {
			return MidjourneyErrorWrapper(constant.MjRequestError, "invalid_custom_id")
		}
		action = splits[2]
	} else {
		action = splits[1]
	}

	if action == "" {
		return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action")
	}
	if strings.Contains(action, "upsample") {
		if len(splits) < 4 {
			return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
		}
		index, err := strconv.Atoi(splits[3])
		if err != nil {
			return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
		}
		midjRequest.Index = index
		midjRequest.Action = constant.MjActionUpscale
	} else if strings.Contains(action, "variation") {
		if action == "variation" && len(splits) < 4 {
			return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
		}
		midjRequest.Index = 1
		if action == "variation" {
			index, err := strconv.Atoi(splits[3])
			if err != nil {
				return MidjourneyErrorWrapper(constant.MjRequestError, "index_parse_failed")
			}
			midjRequest.Index = index
			midjRequest.Action = constant.MjActionVariation
		} else if action == "low_variation" {
			midjRequest.Action = constant.MjActionLowVariation
		} else if action == "high_variation" {
			midjRequest.Action = constant.MjActionHighVariation
		}
	} else if strings.Contains(action, "pan") {
		midjRequest.Action = constant.MjActionPan
		midjRequest.Index = 1
	} else if strings.Contains(action, "reroll") {
		midjRequest.Action = constant.MjActionReRoll
		midjRequest.Index = 1
	} else if action == "Outpaint" {
		midjRequest.Action = constant.MjActionZoom
		midjRequest.Index = 1
	} else if action == "CustomZoom" {
		midjRequest.Action = constant.MjActionCustomZoom
		midjRequest.Index = 1
	} else if action == "Inpaint" {
		midjRequest.Action = constant.MjActionInPaint
		midjRequest.Index = 1
	} else {
		return MidjourneyErrorWrapper(constant.MjRequestError, "unknown_action")
	}
	return nil
}

func ConvertSimpleChangeParams(content string) *dto.MidjourneyRequest {
	split := strings.Fields(content)
	if len(split) != 2 {
		return nil
	}

	action := strings.ToLower(split[1])
	changeParams := &dto.MidjourneyRequest{}
	changeParams.TaskId = split[0]

	// Reroll does not carry an index. Check it before indexing the action
	// string so malformed input such as "task " cannot panic the handler.
	if action == "r" {
		changeParams.Action = constant.MjActionReRoll
		return changeParams
	}
	if len(action) < 2 {
		return nil
	}
	switch action[0] {
	case 'u':
		changeParams.Action = constant.MjActionUpscale
	case 'v':
		changeParams.Action = constant.MjActionVariation
	default:
		return nil
	}

	index, err := strconv.Atoi(action[1:])
	if err != nil || index < 1 || index > 4 {
		return nil
	}
	changeParams.Index = index
	return changeParams
}

func DoMidjourneyHttpRequest(c *gin.Context, timeout time.Duration, fullRequestURL string) (*dto.MidjourneyResponseWithStatusCode, []byte, error) {
	midjourneyConfig := setting.GetMidjourneyConfig()
	var nullBytes []byte
	//var requestBody io.Reader
	//requestBody = c.Request.Body
	// read request body to json, delete accountFilter and notifyHook
	var mapResult map[string]interface{}
	// if get request, no need to read request body
	if c.Request.Method != "GET" {
		err := common.DecodeJson(c.Request.Body, &mapResult)
		if err != nil {
			return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "read_request_body_failed", http.StatusInternalServerError), nullBytes, err
		}
		if !midjourneyConfig.AccountFilterEnabled {
			delete(mapResult, "accountFilter")
		}
		if !midjourneyConfig.NotifyEnabled {
			delete(mapResult, "notifyHook")
		}
		//req, err := http.NewRequest(c.Request.Method, fullRequestURL, requestBody)
		// make new request with mapResult
	}
	if midjourneyConfig.ModeClearEnabled {
		if prompt, ok := mapResult["prompt"].(string); ok {
			prompt = strings.Replace(prompt, "--fast", "", -1)
			prompt = strings.Replace(prompt, "--relax", "", -1)
			prompt = strings.Replace(prompt, "--turbo", "", -1)

			mapResult["prompt"] = prompt
		}
	}
	reqBody, err := common.Marshal(mapResult)
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "marshal_request_body_failed", http.StatusInternalServerError), nullBytes, err
	}
	req, err := http.NewRequest(c.Request.Method, fullRequestURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "create_request_failed", http.StatusInternalServerError), nullBytes, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// 使用带有超时的 context 创建新的请求
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	req.Header.Set("Accept", c.Request.Header.Get("Accept"))
	auth := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	if auth != "" {
		auth = strings.TrimPrefix(auth, "Bearer ")
		req.Header.Set("mj-api-secret", auth)
	}
	defer cancel()
	resp, err := GetHttpClient().Do(req)
	if err != nil {
		common.SysLog("do request failed error_meta=" + common.SensitiveLogMeta(err.Error()))
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "do_request_failed", http.StatusInternalServerError), nullBytes, err
	}
	statusCode := resp.StatusCode
	//if statusCode != 200  {
	//	return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "bad_response_status_code", statusCode), nullBytes, nil
	//}
	err = req.Body.Close()
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "close_request_body_failed", statusCode), nullBytes, err
	}
	err = c.Request.Body.Close()
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "close_request_body_failed", statusCode), nullBytes, err
	}
	var midjResponse dto.MidjourneyResponse
	var midjourneyUploadsResponse dto.MidjourneyUploadResponse
	defer CloseResponseBodyGracefully(resp)
	responseBody, err := ReadProviderResponseBody(resp, DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "read_response_body_failed", statusCode), nullBytes, err
	}
	logger.LogDebug(c, "midjourney response_meta=%s", common.SensitiveLogBody(responseBody))
	if len(responseBody) == 0 {
		return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "empty_response_body", statusCode), responseBody, nil
	} else {
		err = common.Unmarshal(responseBody, &midjResponse)
		if err != nil {
			err2 := common.Unmarshal(responseBody, &midjourneyUploadsResponse)
			if err2 != nil {
				return MidjourneyErrorWithStatusCodeWrapper(constant.MjErrorUnknown, "unmarshal_response_body_failed", statusCode), responseBody, err
			}
		}
	}
	//for k, v := range resp.Header {
	//	c.Writer.Header().Set(k, v[0])
	//}
	return &dto.MidjourneyResponseWithStatusCode{
		StatusCode: statusCode,
		Response:   midjResponse,
	}, responseBody, nil
}
