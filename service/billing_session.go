package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// BillingSession — 统一计费会话
// ---------------------------------------------------------------------------

// BillingSession 封装单次请求的预扣费/结算/退款生命周期。
// 实现 relaycommon.BillingSettler 接口。
type BillingSession struct {
	relayInfo        *relaycommon.RelayInfo
	funding          FundingSource
	preConsumedQuota int  // 实际预扣额度（信任用户可能为 0）
	tokenConsumed    int  // 令牌额度实际扣减量
	extraReserved    int  // 发送前补充预扣的额度（订阅退款时需要单独回滚）
	trusted          bool // 是否命中信任额度旁路

	// Settlement is a two-ledger operation. Keep the committed funding delta
	// and token status separately so a transient token write failure can be
	// retried without charging funding a second time.
	fundingSettled     bool
	settlementDelta    int
	settlementDeltaSet bool
	tokenSettled       bool
	postDeltaApplied   bool
	settled            bool // Settle 全部完成（资金 + 令牌）

	// Refund is likewise tracked per component. A component is marked complete
	// only after its write succeeds; failed components remain retryable.
	refunded        bool
	fundingRefunded bool
	extraRefunded   bool
	tokenRefunded   bool

	// Reserve can leave a funding-only increment when token reservation fails.
	// Keep it explicit so Refund/Settle can retry the rollback instead of losing
	// the increment in process-local state.
	pendingFundingReserve int

	// Test hooks. They are nil in production and deliberately operate on signed
	// deltas (positive charge, negative refund).
	adjustTokenFn     func(delta int) error
	reserveFundingFn  func(delta int) error
	rollbackFundingFn func(delta int) error
	reserveTokenFn    func(delta int) error

	mu sync.Mutex
}

// Settle 根据实际消耗额度进行结算。
// 资金来源和令牌额度分两步提交。若资金来源已提交但令牌调整失败，
// 会保留 fundingSettled、返回错误并允许后续调用只重试令牌调整。
func (s *BillingSession) Settle(actualQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.relayInfo == nil || s.funding == nil {
		return errors.New("billing session is not initialized")
	}
	if actualQuota < 0 || actualQuota > common.MaxQuota {
		return fmt.Errorf("billing settlement quota is out of range: %d", actualQuota)
	}
	if s.settled {
		return nil
	}
	// A failed Reserve may have deducted funding before token reservation was
	// attempted. Do not settle while that funding-only increment is unresolved.
	if s.pendingFundingReserve > 0 {
		if err := s.rollbackPendingFundingReserveLocked(); err != nil {
			return fmt.Errorf("pending funding reserve rollback failed: %w", err)
		}
	}
	if !s.fundingSettled {
		if subscription, ok := s.funding.(*SubscriptionFunding); ok && subscription != nil &&
			strings.TrimSpace(s.relayInfo.RequestId) != "" && model.DB != nil && s.adjustTokenFn == nil {
			if err := s.restoreDurableSubscriptionReservation(subscription); err != nil {
				return fmt.Errorf("restore subscription reservation: %w", err)
			}
		}
	}
	delta := actualQuota - s.preConsumedQuota
	// Once funding has been committed, changing the requested actual amount
	// would charge the two ledgers different deltas. Fail closed and leave the
	// session retryable for the original amount.
	if s.settlementDeltaSet && delta != s.settlementDelta {
		return fmt.Errorf("billing settlement amount changed after funding commit: previous=%d current=%d", s.settlementDelta, delta)
	}
	if delta == 0 {
		// A zero-delta settlement is still a successful lifecycle transition.
		// Persist the settlement marker for durable wallet/subscription sessions
		// before marking the in-memory session complete; otherwise a process crash
		// after this return leaves only the pre-consume marker and a later
		// reconstructed worker cannot distinguish success from an abandoned
		// reservation (and may refund it).
		if !s.fundingSettled && !s.tokenSettled {
			used, durableErr := s.applyDurableBillingOperation("settle", 0, 0, 0, false, false)
			if used {
				if durableErr != nil {
					return durableErr
				}
				s.fundingSettled = true
			}
		}
		s.settlementDelta = 0
		s.settlementDeltaSet = true
		s.tokenSettled = true
		s.settled = true
		return nil
	}

	// For normal production sessions commit funding and token adjustments in a
	// single durable operation.  A failed transaction changes neither ledger,
	// so the same request can be retried safely after a process/database fault.
	// Injected test hooks intentionally use the component-wise path below.
	if !s.fundingSettled && !s.tokenSettled {
		tokenDelta := int64(delta)
		if s.relayInfo.IsPlayground {
			tokenDelta = 0
		}
		walletDelta := int64(0)
		subscriptionDelta := int64(0)
		switch s.funding.(type) {
		case *WalletFunding:
			walletDelta = -int64(delta)
		case *SubscriptionFunding:
			subscriptionDelta = int64(delta)
		}
		used, durableErr := s.applyDurableBillingOperation("settle", walletDelta, tokenDelta, subscriptionDelta, false, false)
		if used {
			if durableErr != nil {
				return durableErr
			}
			s.fundingSettled = true
			s.settlementDelta = delta
			s.settlementDeltaSet = true
			s.tokenSettled = true
			if !s.postDeltaApplied && s.funding.Source() == BillingSourceSubscription {
				s.relayInfo.SubscriptionPostDelta += int64(delta)
				s.postDeltaApplied = true
			}
			s.settled = true
			return nil
		}
	}
	// 1) 调整资金来源（仅在尚未提交时执行，防止重复调用）
	if !s.fundingSettled {
		if err := s.funding.Settle(delta); err != nil {
			return err
		}
		s.fundingSettled = true
		s.settlementDelta = delta
		s.settlementDeltaSet = true
	}
	// 2) 调整令牌额度
	if !s.tokenSettled {
		if tokenErr := s.adjustToken(delta); tokenErr != nil {
			// 资金来源已提交，但令牌还没有提交成功。不要标记 settled，
			// 也不要让 Refund 退回已经提交的 funding。
			common.SysLog(fmt.Sprintf("error adjusting token quota after funding settled (userId=%d, tokenId=%d, delta=%d): %s",
				s.relayInfo.UserId, s.relayInfo.TokenId, delta, tokenErr.Error()))
			return tokenErr
		}
		s.tokenSettled = true
	}
	// 3) 更新 relayInfo 上的订阅 PostDelta（用于日志）
	if !s.postDeltaApplied && s.funding.Source() == BillingSourceSubscription {
		s.relayInfo.SubscriptionPostDelta += int64(delta)
		s.postDeltaApplied = true
	}
	s.settled = true
	return nil
}

// Refund 退还所有预扣费，幂等安全，同步执行。
//
// The interface historically exposed a void method. Keep that API for relay
// callers, while RefundWithError below gives controllers/metrics an observable
// result and leaves a durable intent for a background retry whenever possible.
func (s *BillingSession) Refund(c *gin.Context) {
	_ = s.RefundWithError(c)
}

// RefundWithError performs the refund and returns the first error observed.
// Components that already committed remain marked complete; callers may invoke
// this method again and only failed components are retried.
func (s *BillingSession) RefundWithError(c *gin.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.relayInfo == nil || s.funding == nil || s.settled || s.fundingSettled || s.refunded || !s.needsRefundLocked() {
		return nil
	}

	logger.LogInfo(c, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（token_quota=%s, funding=%s）",
		s.relayInfo.UserId,
		logger.FormatQuota(s.tokenConsumed),
		s.funding.Source(),
	))

	var firstErr error
	recordErr := func(prefix string, err error) {
		if err == nil {
			return
		}
		common.SysLog(prefix + err.Error())
		if firstErr == nil {
			firstErr = err
		}
	}

	// A failed Reserve may have deducted funding before token reservation was
	// attempted. Roll that increment back first. WalletFunding.consumed does
	// not include this pending increment, so a failed rollback cannot be
	// accidentally returned a second time by funding.Refund below.
	if s.pendingFundingReserve > 0 {
		pending := s.pendingFundingReserve
		if err := s.rollbackFundingReserve(pending); err != nil {
			recordErr("error rolling back pending funding reserve: ", err)
		} else {
			s.pendingFundingReserve = 0
		}
	}

	// Wallet reservations and token reservations can be refunded together in a
	// single journaled transaction.  If this call fails, neither ledger has
	// changed and the component flags remain retryable; do not fall through to
	// the legacy per-ledger writes, which could turn an ambiguous result into a
	// double refund.
	durableWalletRefundAttempted := false
	// Subscription pre-consume historically used a separate reservation marker
	// and then refunded the subscription before the token.  A process crash in
	// that gap left a consumed token with a permanently "refunded" subscription
	// marker, so a reconstructed session could no longer complete the rollback.
	// Production subscription sessions now refund both ledgers and close the
	// reservation marker in one durable operation. If that transaction fails,
	// no non-idempotent fallback is attempted and the whole refund remains
	// retryable.
	durableSubscriptionRefundAttempted := false
	if s.pendingFundingReserve == 0 && !s.fundingRefunded && !s.tokenRefunded {
		if subscription, ok := s.funding.(*SubscriptionFunding); ok && subscription != nil && subscription.preConsumed > 0 {
			var refundErr error
			durableSubscriptionRefundAttempted, refundErr = s.applyDurableSubscriptionRefund(subscription)
			if durableSubscriptionRefundAttempted {
				if refundErr != nil {
					recordErr("error refunding subscription and token atomically: ", refundErr)
				} else {
					s.fundingRefunded = true
					s.extraRefunded = true
					s.tokenRefunded = true
					s.tokenConsumed = 0
					s.extraReserved = 0
					subscription.preConsumed = 0
				}
			}
		}
	}
	if s.pendingFundingReserve == 0 && !s.fundingRefunded && !s.tokenRefunded {
		if wallet, ok := s.funding.(*WalletFunding); ok && wallet != nil {
			var refundErr error
			durableWalletRefundAttempted, refundErr = s.ensureAndApplyDurableWalletRefund(wallet)
			if durableWalletRefundAttempted {
				if refundErr != nil {
					recordErr("error refunding wallet and token atomically: ", refundErr)
				} else {
					s.fundingRefunded = true
					s.tokenRefunded = true
					wallet.consumed = 0
					s.tokenConsumed = 0
				}
			}
		}
	}

	// 1) 退还原始资金来源预扣
	if !s.fundingRefunded && !durableWalletRefundAttempted && !durableSubscriptionRefundAttempted {
		if err := s.funding.Refund(); err != nil {
			recordErr("error refunding billing source: ", err)
		} else {
			s.fundingRefunded = true
		}
	}

	// 2) 退还 Reserve 产生的额外订阅预扣
	if !s.extraRefunded && !durableSubscriptionRefundAttempted {
		if s.extraReserved <= 0 || s.funding.Source() != BillingSourceSubscription || s.relayInfo.SubscriptionId <= 0 {
			s.extraRefunded = true
		} else {
			used, err := s.applyDurableBillingOperation("refund_extra", 0, 0, -int64(s.extraReserved), false, false)
			if used {
				if err != nil {
					recordErr("error refunding subscription extra reserved quota: ", err)
				} else {
					s.extraRefunded = true
					s.extraReserved = 0
				}
			} else if err := model.PostConsumeUserSubscriptionDelta(s.relayInfo.SubscriptionId, -int64(s.extraReserved)); err != nil {
				recordErr("error refunding subscription extra reserved quota: ", err)
			} else {
				s.extraRefunded = true
				s.extraReserved = 0
			}
		}
	}

	// 3) 退还令牌额度
	if !s.tokenRefunded && !durableWalletRefundAttempted && !durableSubscriptionRefundAttempted {
		if s.tokenConsumed <= 0 || s.relayInfo.IsPlayground {
			s.tokenRefunded = true
		} else {
			used, err := s.applyDurableBillingOperation("refund_token", 0, -int64(s.tokenConsumed), 0, false, false)
			if used {
				if err != nil {
					recordErr("error refunding token quota: ", err)
				} else {
					s.tokenRefunded = true
					s.tokenConsumed = 0
				}
			} else if err := s.adjustToken(-s.tokenConsumed); err != nil {
				recordErr("error refunding token quota: ", err)
			} else {
				s.tokenRefunded = true
				s.tokenConsumed = 0
			}
		}
	}

	if firstErr == nil && s.pendingFundingReserve == 0 && s.fundingRefunded && s.extraRefunded && s.tokenRefunded {
		s.refunded = true
	}
	return firstErr
}

// NeedsRefund 返回是否存在需要退还的预扣状态。
func (s *BillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsRefundLocked()
}

func (s *BillingSession) needsRefundLocked() bool {
	if s.relayInfo == nil || s.funding == nil {
		return false
	}
	if s.settled || s.refunded || s.fundingSettled {
		// fundingSettled 时资金来源已提交结算，不能再退预扣费
		return false
	}
	if s.pendingFundingReserve > 0 {
		return true
	}
	if s.tokenConsumed > 0 && !s.tokenRefunded {
		return true
	}
	if s.extraReserved > 0 && !s.extraRefunded {
		return true
	}
	// 资金来源可能在 tokenConsumed=0 时仍有预扣（例如异步 Reserve
	// 回滚失败）。按具体来源检查，避免把这类状态误判为已清理。
	if !s.fundingRefunded {
		switch funding := s.funding.(type) {
		case *WalletFunding:
			if funding.consumed > 0 {
				return true
			}
		case *SubscriptionFunding:
			if funding.preConsumed > 0 {
				return true
			}
		}
	}
	return false
}

// adjustToken applies a signed token delta. Positive values charge the token;
// negative values refund it. Tests may inject a deterministic failure hook.
func (s *BillingSession) adjustToken(delta int) error {
	if delta == 0 || s.relayInfo.IsPlayground {
		return nil
	}
	if s.adjustTokenFn != nil {
		return s.adjustTokenFn(delta)
	}
	if delta > 0 {
		return model.DecreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, delta)
	}
	return model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, -delta)
}

// rollbackPendingFundingReserveLocked retries a funding rollback that failed
// while Reserve was unwinding. The caller must hold s.mu.
func (s *BillingSession) rollbackPendingFundingReserveLocked() error {
	if s.pendingFundingReserve <= 0 {
		return nil
	}
	delta := s.pendingFundingReserve
	if err := s.rollbackFundingReserve(delta); err != nil {
		return err
	}
	s.pendingFundingReserve = 0
	return nil
}

// GetPreConsumedQuota 返回实际预扣的额度。
func (s *BillingSession) GetPreConsumedQuota() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preConsumedQuota
}

func (s *BillingSession) Reserve(targetQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.relayInfo == nil || s.funding == nil {
		return errors.New("billing session is not initialized")
	}
	if targetQuota < 0 || targetQuota > common.MaxQuota {
		return fmt.Errorf("reserve quota is out of range: %d", targetQuota)
	}

	// Always resolve a funding-only rollback before evaluating the requested
	// target. In particular, targetQuota may be <= preConsumedQuota after a
	// failed attempt; returning early there would leak the pending increment.
	if err := s.rollbackPendingFundingReserveLocked(); err != nil {
		return fmt.Errorf("pending funding reserve rollback failed: %w", err)
	}

	if s.settled || s.refunded || s.trusted {
		return nil
	}
	if s.fundingSettled {
		return errors.New("cannot reserve after funding settlement has started")
	}
	if subscription, ok := s.funding.(*SubscriptionFunding); ok && subscription != nil &&
		strings.TrimSpace(s.relayInfo.RequestId) != "" && model.DB != nil &&
		s.adjustTokenFn == nil && s.reserveFundingFn == nil && s.reserveTokenFn == nil {
		if err := s.restoreDurableSubscriptionReservation(subscription); err != nil {
			return fmt.Errorf("restore subscription reservation: %w", err)
		}
	}
	if targetQuota <= s.preConsumedQuota {
		return nil
	}

	delta := targetQuota - s.preConsumedQuota
	if delta <= 0 {
		return nil
	}

	// Tiered retries on a normal wallet or subscription session reserve the
	// funding and token ledgers in one transaction.  The component includes the
	// target so each monotonic top-up has its own idempotency fence; a failed
	// transaction leaves no reservation marker and can be retried with the same
	// target.
	if _, isWallet := s.funding.(*WalletFunding); isWallet {
		tokenDelta := int64(delta)
		if s.relayInfo.IsPlayground {
			tokenDelta = 0
		}
		used, durableErr := s.applyDurableBillingOperation(
			fmt.Sprintf("reserve:%d", targetQuota), -int64(delta), tokenDelta,
			0, false, tokenDelta > 0 && !s.relayInfo.TokenUnlimited,
		)
		if used {
			if durableErr != nil {
				return durableErr
			}
			s.pendingFundingReserve = 0
			if funding, ok := s.funding.(*WalletFunding); ok {
				funding.consumed += delta
			}
			s.preConsumedQuota += delta
			s.tokenConsumed += delta
			s.extraReserved += delta
			s.syncRelayInfo()
			return nil
		}
	} else if subscription, isSubscription := s.funding.(*SubscriptionFunding); isSubscription {
		durableAttempted := false
		for attempt := 0; attempt < 3; attempt++ {
			delta = targetQuota - s.preConsumedQuota
			if delta <= 0 {
				return nil
			}
			used, durableErr := s.applyDurableSubscriptionReserve(subscription, targetQuota, delta)
			if !used {
				break
			}
			durableAttempted = true
			if errors.Is(durableErr, model.ErrBillingReservationChanged) {
				if err := s.restoreDurableSubscriptionReservation(subscription); err != nil {
					return fmt.Errorf("restore changed subscription reservation: %w", err)
				}
				continue
			}
			if durableErr != nil {
				return durableErr
			}
			s.pendingFundingReserve = 0
			if err := s.restoreDurableSubscriptionReservation(subscription); err != nil {
				return fmt.Errorf("restore applied subscription reservation: %w", err)
			}
			return nil
		}
		if durableAttempted {
			return model.ErrBillingReservationChanged
		}
	}

	if err := s.reserveFunding(delta); err != nil {
		return err
	}
	// Keep this marker until both funding and token reservation succeed.
	s.pendingFundingReserve = delta
	if err := s.reserveToken(delta); err != nil {
		if rollbackErr := s.rollbackFundingReserve(delta); rollbackErr != nil {
			common.SysLog("error rolling back funding reserve: " + rollbackErr.Error())
		} else {
			s.pendingFundingReserve = 0
		}
		return err
	}

	s.pendingFundingReserve = 0
	if funding, ok := s.funding.(*WalletFunding); ok {
		// Count the increment as refundable only after token reservation also
		// succeeded. This prevents a failed rollback from being returned twice.
		funding.consumed += delta
	}
	s.preConsumedQuota += delta
	s.tokenConsumed += delta
	s.extraReserved += delta
	s.syncRelayInfo()
	return nil
}

// ---------------------------------------------------------------------------
// PreConsume — 统一预扣费入口（含信任额度旁路）
// ---------------------------------------------------------------------------

// preConsume 执行预扣费：信任检查 -> 令牌预扣 -> 资金来源预扣。
// 任一步骤失败时原子回滚已完成的步骤。
func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError {
	effectiveQuota := quota

	// ---- 信任额度旁路 ----
	if s.shouldTrust(c) {
		s.trusted = true
		effectiveQuota = 0
		logger.LogInfo(c, fmt.Sprintf("用户 %d 额度充足, 信任且不需要预扣费 (funding=%s)", s.relayInfo.UserId, s.funding.Source()))
	} else if effectiveQuota > 0 {
		logger.LogInfo(c, fmt.Sprintf("用户 %d 需要预扣费 %s (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(effectiveQuota), s.funding.Source()))
	}

	if effectiveQuota <= 0 {
		s.preConsumedQuota = effectiveQuota
		s.syncRelayInfo()
		return nil
	}

	// ---- Wallet: one transaction for funding + token ----
	// Concrete wallet sessions use one journaled operation.  This closes the
	// crash window between the old token-first/funding-second sequence and makes
	// a repeated request ID a true no-op.
	if _, isWallet := s.funding.(*WalletFunding); isWallet {
		tokenDelta := int64(effectiveQuota)
		if s.relayInfo.IsPlayground {
			tokenDelta = 0
		}
		used, durableErr := s.applyDurableBillingOperation("preconsume", -int64(effectiveQuota), tokenDelta, 0, true, tokenDelta > 0)
		if used {
			if durableErr != nil {
				if errors.Is(durableErr, model.ErrBillingWalletInsufficient) {
					userQuota, quotaErr := model.GetUserQuota(s.relayInfo.UserId, false)
					if quotaErr != nil {
						userQuota = 0
					}
					return types.NewErrorWithStatusCode(fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
				}
				if errors.Is(durableErr, model.ErrBillingTokenInsufficient) {
					return types.NewErrorWithStatusCode(durableErr, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
				}
				return types.NewError(durableErr, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
			}
			s.tokenConsumed = effectiveQuota
			if funding, ok := s.funding.(*WalletFunding); ok {
				funding.consumed = effectiveQuota
			}
			s.preConsumedQuota = effectiveQuota
			s.syncRelayInfo()
			return nil
		}
	}

	// ---- Subscription reservation ----
	// Production sessions use SubscriptionFunding's combined transaction,
	// which commits the subscription record, token ledger, and token operation
	// marker together. Legacy/test funding instances retain the historical
	// split path below for compatibility.
	if _, isSubscription := s.funding.(*SubscriptionFunding); isSubscription && strings.TrimSpace(s.relayInfo.RequestId) != "" && model.DB != nil && s.adjustTokenFn == nil && s.reserveTokenFn == nil {
		if err := s.funding.PreConsume(effectiveQuota); err != nil {
			return s.preConsumeFundingError(err)
		}
		if subscription, ok := s.funding.(*SubscriptionFunding); ok && subscription.combined {
			if err := s.restoreDurableSubscriptionReservation(subscription); err != nil {
				return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
			}
			return nil
		}
		tokenDelta := int64(effectiveQuota)
		if s.relayInfo.IsPlayground {
			tokenDelta = 0
		}
		used, durableErr := s.applyDurableBillingOperation("preconsume_token", 0, tokenDelta, 0, false, tokenDelta > 0)
		if used {
			if durableErr != nil {
				if refundErr := model.RefundSubscriptionPreConsume(s.relayInfo.RequestId); refundErr != nil {
					common.SysLog("error rolling back subscription after token pre-consume failure: " + refundErr.Error())
				}
				return types.NewErrorWithStatusCode(durableErr, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
			}
			s.tokenConsumed = effectiveQuota
			s.preConsumedQuota = effectiveQuota
			s.syncRelayInfo()
			return nil
		}
	}

	// ---- Legacy/test fallback: token first, then funding with compensation ----
	if err := PreConsumeTokenQuota(s.relayInfo, effectiveQuota); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	s.tokenConsumed = effectiveQuota

	if err := s.funding.PreConsume(effectiveQuota); err != nil {
		// 预扣费失败，回滚令牌额度
		if s.tokenConsumed > 0 && !s.relayInfo.IsPlayground {
			if rollbackErr := model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.tokenConsumed); rollbackErr != nil {
				common.SysLog(fmt.Sprintf("error rolling back token quota (userId=%d, tokenId=%d, amount=%d, fundingErr=%s): %s",
					s.relayInfo.UserId, s.relayInfo.TokenId, s.tokenConsumed, err.Error(), rollbackErr.Error()))
			}
			s.tokenConsumed = 0
		}
		return s.preConsumeFundingError(err)
	}

	s.preConsumedQuota = effectiveQuota

	// ---- 同步 RelayInfo 兼容字段 ----
	s.syncRelayInfo()

	return nil
}

// preConsumeFundingError centralizes the historical error-to-API mapping so
// both durable and fallback funding paths expose the same client contract.
func (s *BillingSession) preConsumeFundingError(err error) *types.NewAPIError {
	if errors.Is(err, ErrInsufficientWalletQuota) {
		userQuota, quotaErr := model.GetUserQuota(s.relayInfo.UserId, false)
		if quotaErr != nil {
			userQuota = 0
		}
		return types.NewErrorWithStatusCode(fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "no active subscription") || strings.Contains(errMsg, "subscription quota insufficient") {
		return types.NewErrorWithStatusCode(fmt.Errorf("订阅额度不足或未配置订阅: %s", errMsg), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
}

func (s *BillingSession) reserveFunding(delta int) error {
	if s.reserveFundingFn != nil {
		return s.reserveFundingFn(delta)
	}
	switch funding := s.funding.(type) {
	case *WalletFunding:
		// 与结算补扣（SettleBilling 正差额 → WalletFunding.Settle）语义一致：
		// 全额无条件扣减，余额不足的部分记为欠费（余额可为负），不中断请求，
		// 保证日志记录的预扣额度与用户余额的实际变动始终对账一致。
		// DecreaseUserQuota 仅在数据库错误时失败。
		if err := model.DecreaseUserQuota(funding.userId, delta, false); err != nil {
			return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
		}
		return nil
	case *SubscriptionFunding:
		if err := model.PostConsumeUserSubscriptionDelta(funding.subscriptionId, int64(delta)); err != nil {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("订阅额度不足或未配置订阅: %s", err.Error()),
				types.ErrorCodeInsufficientUserQuota,
				http.StatusForbidden,
				types.ErrOptionWithSkipRetry(),
				types.ErrOptionWithNoRecordErrorLog(),
			)
		}
		return nil
	default:
		return types.NewError(fmt.Errorf("unsupported funding source: %s", s.funding.Source()), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
}

func (s *BillingSession) rollbackFundingReserve(delta int) error {
	if delta <= 0 {
		return nil
	}
	if s.rollbackFundingFn != nil {
		return s.rollbackFundingFn(delta)
	}
	switch funding := s.funding.(type) {
	case *WalletFunding:
		return model.IncreaseUserQuota(funding.userId, delta, false)
	case *SubscriptionFunding:
		return model.PostConsumeUserSubscriptionDelta(funding.subscriptionId, -int64(delta))
	default:
		return fmt.Errorf("unsupported funding source: %s", s.funding.Source())
	}
}

func (s *BillingSession) reserveToken(delta int) error {
	if delta <= 0 || s.relayInfo.IsPlayground {
		return nil
	}
	if s.reserveTokenFn != nil {
		return s.reserveTokenFn(delta)
	}
	if err := PreConsumeTokenQuota(s.relayInfo, delta); err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return nil
}

// shouldTrust 统一信任额度检查，适用于钱包和订阅。
func (s *BillingSession) shouldTrust(c *gin.Context) bool {
	// 异步任务（ForcePreConsume=true）必须预扣全额，不允许信任旁路
	if s.relayInfo.ForcePreConsume {
		return false
	}

	trustQuota := common.GetTrustQuota()
	if trustQuota <= 0 {
		return false
	}

	// 检查令牌是否充足
	tokenTrusted := s.relayInfo.TokenUnlimited
	if !tokenTrusted {
		tokenQuota := c.GetInt("token_quota")
		tokenTrusted = tokenQuota > trustQuota
	}
	if !tokenTrusted {
		return false
	}

	switch s.funding.Source() {
	case BillingSourceWallet:
		return s.relayInfo.UserQuota > trustQuota
	case BillingSourceSubscription:
		// 订阅不能启用信任旁路。原因：
		// 1. PreConsumeUserSubscription 要求 amount>0 来创建预扣记录并锁定订阅
		// 2. SubscriptionFunding.PreConsume 忽略参数，始终用 s.amount 预扣
		// 3. 若信任旁路将 effectiveQuota 设为 0，会导致 preConsumedQuota 与实际订阅预扣不一致
		return false
	default:
		return false
	}
}

// syncRelayInfo 将 BillingSession 的状态同步到 RelayInfo 的兼容字段上。
func (s *BillingSession) syncRelayInfo() {
	info := s.relayInfo
	info.FinalPreConsumedQuota = s.preConsumedQuota
	info.BillingSource = s.funding.Source()

	if sub, ok := s.funding.(*SubscriptionFunding); ok {
		info.SubscriptionId = sub.subscriptionId
		info.SubscriptionPreConsumed = sub.preConsumed + int64(s.extraReserved)
		info.SubscriptionPostDelta = 0
		info.SubscriptionAmountTotal = sub.AmountTotal
		info.SubscriptionAmountUsedAfterPreConsume = sub.AmountUsedAfter + int64(s.extraReserved)
		info.SubscriptionPlanId = sub.PlanId
		info.SubscriptionPlanTitle = sub.PlanTitle
	} else {
		info.SubscriptionId = 0
		info.SubscriptionPreConsumed = 0
	}
}

// ---------------------------------------------------------------------------
// NewBillingSession 工厂 — 根据计费偏好创建会话并处理回退
// ---------------------------------------------------------------------------

// NewBillingSession 根据用户计费偏好创建 BillingSession，处理 subscription_first / wallet_first 的回退。
func NewBillingSession(c *gin.Context, relayInfo *relaycommon.RelayInfo, preConsumedQuota int) (*BillingSession, *types.NewAPIError) {
	if relayInfo == nil {
		return nil, types.NewError(fmt.Errorf("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	// A negative or unbounded pre-consume amount is never a valid billing
	// request.  Reject it before selecting a funding source: the subscription
	// path otherwise normalizes negatives to 1, while the wallet path would
	// retain a negative session balance and distort settlement deltas.
	if preConsumedQuota < 0 || preConsumedQuota > common.MaxQuota {
		return nil, types.NewError(fmt.Errorf("pre-consume quota is out of range: %d", preConsumedQuota), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	// RelayInfo normally receives a request ID during construction.  Keep the
	// factory defensive for internal callers/tests that build it manually: a
	// stable ID is required for the durable billing journal, and generating it
	// here is safer than silently falling back to non-idempotent mutations.
	if strings.TrimSpace(relayInfo.RequestId) == "" {
		relayInfo.RequestId = common.NewRequestId()
	}

	pref := common.NormalizeBillingPreference(relayInfo.UserSetting.BillingPreference)

	// 钱包路径需要先检查用户额度
	tryWallet := func() (*BillingSession, *types.NewAPIError) {
		userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		// Do not reject from a non-atomic snapshot here.  The durable
		// preconsume operation below performs the conditional balance check while
		// taking the operation lock; an early snapshot check could reject a
		// legitimate replay whose original operation already consumed the last
		// available units.  It also races with concurrent requests and makes
		// wallet_first fallback decisions based on stale data.
		relayInfo.UserQuota = userQuota

		session := &BillingSession{
			relayInfo: relayInfo,
			funding:   &WalletFunding{userId: relayInfo.UserId, requestId: relayInfo.RequestId},
		}
		if apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	trySubscription := func() (*BillingSession, *types.NewAPIError) {
		subConsume := int64(preConsumedQuota)
		if subConsume <= 0 {
			subConsume = 1
		}
		session := &BillingSession{
			relayInfo: relayInfo,
			funding: &SubscriptionFunding{
				requestId:      relayInfo.RequestId,
				userId:         relayInfo.UserId,
				modelName:      relayInfo.OriginModelName,
				amount:         subConsume,
				tokenId:        relayInfo.TokenId,
				tokenKey:       relayInfo.TokenKey,
				tokenUnlimited: relayInfo.TokenUnlimited,
				playground:     relayInfo.IsPlayground,
				combined:       strings.TrimSpace(relayInfo.RequestId) != "" && model.DB != nil,
			},
		}
		// 必须传 subConsume 而非 preConsumedQuota，保证 SubscriptionFunding.amount、
		// preConsume 参数和 FinalPreConsumedQuota 三者一致，避免订阅多扣费。
		if apiErr := session.preConsume(c, int(subConsume)); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	switch pref {
	case "subscription_only":
		return trySubscription()
	case "wallet_only":
		return tryWallet()
	case "wallet_first":
		session, err := tryWallet()
		if err != nil {
			if err.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				return trySubscription()
			}
			return nil, err
		}
		return session, nil
	case "subscription_first":
		fallthrough
	default:
		hasSub, subCheckErr := model.HasActiveUserSubscription(relayInfo.UserId)
		if subCheckErr != nil {
			return nil, types.NewError(subCheckErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if !hasSub {
			return tryWallet()
		}
		session, apiErr := trySubscription()
		if apiErr != nil {
			if apiErr.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				// 仅当用户的活跃订阅允许钱包回退时才回退到钱包，否则返回订阅额度不足错误
				allowOverflow, overflowErr := model.UserActiveSubscriptionsAllowWalletOverflow(relayInfo.UserId)
				if overflowErr != nil {
					return nil, types.NewError(overflowErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
				}
				if allowOverflow {
					return tryWallet()
				}
				return nil, apiErr
			}
			return nil, apiErr
		}
		return session, nil
	}
}
