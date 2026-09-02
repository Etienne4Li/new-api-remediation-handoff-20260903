package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const (
	BillingSourceWallet              = "wallet"
	BillingSourceSubscription        = "subscription"
	legacyBillingSettlementComponent = "legacy_settle"

	// syncBillingUsageComponent is deliberately different from the async task
	// usage component.  A request may be retried in a fresh process after its
	// financial operation committed but before the informational counters were
	// written; this marker makes that second step exactly-once as well.
	syncBillingUsageComponent = "sync_settle_usage"
)

// PreConsumeBilling 根据用户计费偏好创建 BillingSession 并执行预扣费。
// 会话存储在 relayInfo.Billing 上，供后续 Settle / Refund 使用。
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	if relayInfo != nil && relayInfo.QuotaClamp != nil {
		return types.NewErrorWithStatusCode(
			relayInfo.QuotaClamp,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if preConsumedQuota < 0 {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("pre-consume quota cannot be negative: %d", preConsumedQuota),
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	session, apiErr := NewBillingSession(c, relayInfo, preConsumedQuota)
	if apiErr != nil {
		return apiErr
	}
	relayInfo.Billing = session
	return nil
}

// ---------------------------------------------------------------------------
// SettleBilling — 后结算辅助函数
// ---------------------------------------------------------------------------

// SettleBilling 执行计费结算。如果 RelayInfo 上有 BillingSession 则通过 session 结算，
// 否则回退到旧的 PostConsumeQuota 路径（兼容按次计费等场景）。
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	if relayInfo == nil {
		return fmt.Errorf("relay info is nil")
	}
	if actualQuota < 0 || actualQuota > common.MaxQuota {
		return fmt.Errorf("billing settlement quota is out of range: %d", actualQuota)
	}
	if relayInfo.Billing != nil {
		preConsumed := relayInfo.Billing.GetPreConsumedQuota()
		delta := actualQuota - preConsumed

		if delta > 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后补扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else if delta < 0 {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费后返还扣费：%s（实际消耗：%s，预扣费：%s）",
				logger.FormatQuota(-delta),
				logger.FormatQuota(actualQuota),
				logger.FormatQuota(preConsumed),
			))
		} else {
			logger.LogInfo(ctx, fmt.Sprintf("预扣费与实际消耗一致，无需调整：%s（按次计费）",
				logger.FormatQuota(actualQuota),
			))
		}

		if err := relayInfo.Billing.Settle(actualQuota); err != nil {
			return err
		}

		// 发送额度通知（订阅计费使用订阅剩余额度）
		if actualQuota != 0 {
			if relayInfo.BillingSource == BillingSourceSubscription {
				checkAndSendSubscriptionQuotaNotify(relayInfo)
			} else {
				checkAndSendQuotaNotify(relayInfo, actualQuota-preConsumed, preConsumed)
			}
		}
		return nil
	}

	// 回退：无 BillingSession 时使用旧路径
	quotaDelta := actualQuota - relayInfo.FinalPreConsumedQuota
	if quotaDelta != 0 {
		return postConsumeQuotaWithComponent(relayInfo, quotaDelta, relayInfo.FinalPreConsumedQuota, true, legacyBillingSettlementComponent)
	}
	return nil
}

// durableBillingPlan describes the financial operation that SettleBilling will
// commit and the informational operation that must follow it.  The two rows
// are installed together before a successful request is settled, so a crash
// cannot leave a usage marker with no recoverable financial companion.
type durableBillingPlan struct {
	financial model.BillingOperationSpec
	usage     model.BillingOperationSpec
}

// buildDurableBillingPlan returns applicable=false for compatibility funding
// implementations (tests/plugins) that cannot be reconstructed by the
// background reconciler. Production WalletFunding/SubscriptionFunding sessions
// and the legacy no-session path are fully reconstructible from RelayInfo.
func buildDurableBillingPlan(relayInfo *relaycommon.RelayInfo, actualQuota int, includeUsage bool) (durableBillingPlan, bool, error) {
	var plan durableBillingPlan
	if relayInfo == nil {
		return plan, false, fmt.Errorf("relay info is nil")
	}
	if actualQuota < 0 || actualQuota > common.MaxQuota {
		return plan, false, fmt.Errorf("billing settlement quota is out of range: %d", actualQuota)
	}
	requestID := strings.TrimSpace(relayInfo.RequestId)
	if requestID == "" || model.DB == nil {
		return plan, false, nil
	}

	switch billing := relayInfo.Billing.(type) {
	case *BillingSession:
		if billing == nil {
			return plan, false, fmt.Errorf("billing session is nil")
		}
		if billing.relayInfo != relayInfo {
			return plan, false, fmt.Errorf("billing session relay info mismatch")
		}
		// Hooked sessions deliberately exercise component-wise failure paths;
		// advertising a combined marker for them would let reconciliation apply a
		// different ledger operation after the hook path has already run.
		if billing.adjustTokenFn != nil || billing.reserveFundingFn != nil ||
			billing.rollbackFundingFn != nil || billing.reserveTokenFn != nil {
			return plan, false, nil
		}
		financial, err := billing.BuildSettlementOperationSpec(actualQuota)
		if err != nil {
			return plan, false, err
		}
		financial.RequestID = requestID
		financial.Component = "settle"
		if strings.TrimSpace(financial.RequestID) != requestID {
			return plan, false, fmt.Errorf("billing request id mismatch")
		}
		plan.financial = financial
	case nil:
		preConsumed := relayInfo.FinalPreConsumedQuota
		if preConsumed < 0 || preConsumed > common.MaxQuota {
			return plan, false, fmt.Errorf("pre-consumed quota is out of range: %d", preConsumed)
		}
		delta := int64(actualQuota) - int64(preConsumed)
		financial := model.BillingOperationSpec{
			RequestID:      requestID,
			Component:      legacyBillingSettlementComponent,
			UserID:         relayInfo.UserId,
			TokenID:        relayInfo.TokenId,
			TokenKey:       relayInfo.TokenKey,
			TokenUnlimited: relayInfo.TokenUnlimited,
			TokenDelta:     delta,
		}
		if relayInfo.IsPlayground {
			financial.TokenDelta = 0
			financial.RequireWalletBalance = false
			financial.RequireTokenBalance = false
		} else {
			// Keep the preflight marker identical to the operation applied by
			// postConsumeQuotaWithComponent. Positive wallet deltas must be
			// balance-checked in both paths; otherwise EnsureBillingOperations
			// creates a marker whose immutable spec conflicts with the actual
			// settlement operation.
			financial.RequireWalletBalance = delta > 0
			financial.RequireTokenBalance = delta > 0 && !relayInfo.TokenUnlimited
		}
		switch strings.TrimSpace(relayInfo.BillingSource) {
		case "", BillingSourceWallet:
			financial.WalletDelta = -delta
		case BillingSourceSubscription:
			financial.SubscriptionID = relayInfo.SubscriptionId
			financial.SubscriptionDelta = delta
		default:
			return plan, false, fmt.Errorf("unsupported billing source: %s", relayInfo.BillingSource)
		}
		plan.financial = financial
	default:
		return plan, false, nil
	}

	if includeUsage {
		if relayInfo.UserId <= 0 {
			return plan, false, fmt.Errorf("billing usage user is missing")
		}
		channelID := 0
		if relayInfo.ChannelMeta != nil {
			channelID = relayInfo.ChannelId
		}
		channelDelta := int64(actualQuota)
		if channelID <= 0 {
			channelID = 0
			channelDelta = 0
		}
		plan.usage = model.BillingOperationSpec{
			RequestID:             requestID,
			Component:             syncBillingUsageComponent,
			UserID:                relayInfo.UserId,
			UserUsedQuotaDelta:    int64(actualQuota),
			UserRequestCountDelta: 1,
			ChannelID:             channelID,
			ChannelUsedQuotaDelta: channelDelta,
		}
	}
	return plan, true, nil
}

// SettleBillingAndRecordUsage commits the billing session before updating the
// aggregate usage counters.  The counters are informational, but writing them
// first makes a failed settlement look like a successful request and leaves
// the user/channel ledgers permanently overstated.  Keeping this ordering in a
// single helper prevents the text, audio, and realtime paths from drifting.
func SettleBillingAndRecordUsage(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int, recordUsage bool) error {
	plan, durable, err := buildDurableBillingPlan(relayInfo, actualQuota, recordUsage)
	if err != nil {
		return err
	}
	if durable && recordUsage {
		// Install both markers before touching either ledger.  A pending marker is
		// intent only; ApplyBillingOperation below remains the sole side-effecting
		// step and is safe to replay after a crash.
		if err := model.EnsureBillingOperations(plan.financial, plan.usage); err != nil {
			return err
		}
	}
	if err := SettleBilling(ctx, relayInfo, actualQuota); err != nil {
		return err
	}
	if durable {
		// SettleBilling normally applies this operation itself.  Re-applying is a
		// no-op for non-zero deltas and marks the zero-delta companion applied,
		// which is required for zero-cost requests to unblock usage recovery.
		if err := model.ApplyBillingOperation(plan.financial); err != nil {
			return err
		}
	}
	if recordUsage {
		var usageErr error
		if durable {
			usageErr = recordBillingUsageOnce(relayInfo, actualQuota)
		} else {
			// Custom/test funding has no reconstructible financial companion. Keep
			// its historical in-process accounting path instead of creating an
			// orphan sync marker that the reconciler must (correctly) reject.
			usageErr = updateLegacyBillingUsage(relayInfo, actualQuota)
		}
		if usageErr != nil {
			// Keep the financial operation committed, but surface the durable
			// usage-journal failure so the caller/reconciler can retry it.  The
			// operation key prevents a later retry from double-incrementing either
			// aggregate.
			return usageErr
		}
	}
	return nil
}

// recordBillingUsageOnce journals informational usage counters after the
// corresponding financial settlement has succeeded.  Usage is not a source
// of spendable credit, but it is part of the audit invariant: a process crash
// between the two old independent UPDATEs could otherwise permanently lose a
// request from user/channel statistics, while a retry could double-count it.
// The durable marker is keyed by the request identity already attached to
// RelayInfo.  Internal legacy callers without an identity retain the old
// best-effort update path, but all HTTP relay requests have one.
func recordBillingUsageOnce(relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	if relayInfo == nil {
		return fmt.Errorf("relay info is nil")
	}
	if actualQuota < 0 || actualQuota > common.MaxQuota {
		return fmt.Errorf("billing usage quota is out of range: %d", actualQuota)
	}
	requestID := strings.TrimSpace(relayInfo.RequestId)
	if requestID == "" || model.DB == nil {
		// This branch is retained only for old in-process callers that construct
		// RelayInfo by hand.  Do not silently ignore DB errors here: callers need
		// an observable signal that the aggregate is not durable.
		if err := updateLegacyBillingUsage(relayInfo, actualQuota); err != nil {
			return err
		}
		return nil
	}
	channelID := relayInfo.ChannelId
	channelDelta := int64(actualQuota)
	if channelID <= 0 {
		channelID = 0
		channelDelta = 0
	}
	return model.ApplyBillingOperation(model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             syncBillingUsageComponent,
		UserID:                relayInfo.UserId,
		UserUsedQuotaDelta:    int64(actualQuota),
		UserRequestCountDelta: 1,
		ChannelID:             channelID,
		ChannelUsedQuotaDelta: channelDelta,
	})
}

func updateLegacyBillingUsage(relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	if relayInfo == nil || relayInfo.UserId <= 0 {
		return fmt.Errorf("billing usage user is missing")
	}
	if err := model.UpdateUserUsedQuotaAndRequestCountE(relayInfo.UserId, actualQuota); err != nil {
		return err
	}
	if relayInfo.ChannelMeta != nil && relayInfo.ChannelId > 0 {
		if err := model.UpdateChannelUsedQuotaE(relayInfo.ChannelId, actualQuota); err != nil {
			return err
		}
	}
	return nil
}
