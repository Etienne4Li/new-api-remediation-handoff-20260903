package service

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// ---------------------------------------------------------------------------
// FundingSource — 资金来源接口（钱包 or 订阅）
// ---------------------------------------------------------------------------

// FundingSource 抽象了预扣费的资金来源。
type FundingSource interface {
	// Source 返回资金来源标识："wallet" 或 "subscription"
	Source() string
	// PreConsume 从该资金来源预扣 amount 额度
	PreConsume(amount int) error
	// Settle 根据差额调整资金来源（正数补扣，负数退还）
	Settle(delta int) error
	// Refund 退还所有预扣费
	Refund() error
}

// ---------------------------------------------------------------------------
// WalletFunding — 钱包资金来源实现
// ---------------------------------------------------------------------------

// ErrInsufficientWalletQuota 钱包原子预扣失败（余额不足），未发生任何扣减。
// BillingSession 据此映射为 ErrorCodeInsufficientUserQuota，
// 使 wallet_first 等计费偏好可以回退到订阅。
var ErrInsufficientWalletQuota = errors.New("wallet quota insufficient")

type WalletFunding struct {
	userId    int
	requestId string // stable request identity used by the durable billing journal
	consumed  int    // 实际预扣的用户额度
}

func (w *WalletFunding) Source() string { return BillingSourceWallet }

func (w *WalletFunding) PreConsume(amount int) error {
	if amount <= 0 {
		return nil
	}
	// BillingSession supplies a stable request ID.  Keep the standalone
	// fallback for legacy callers/tests that construct WalletFunding directly;
	// those callers retain the historical conditional reservation semantics.
	if w.requestId != "" {
		err := model.ApplyBillingOperation(model.BillingOperationSpec{
			RequestID:            w.requestId,
			Component:            "wallet_preconsume",
			UserID:               w.userId,
			WalletDelta:          -int64(amount),
			RequireWalletBalance: true,
		})
		if err != nil {
			if errors.Is(err, model.ErrBillingOperationInsufficient) {
				return ErrInsufficientWalletQuota
			}
			return err
		}
		w.consumed = amount
		return nil
	}
	reserved, err := model.TryReserveUserQuota(w.userId, amount)
	if err != nil {
		return err
	}
	if !reserved {
		return ErrInsufficientWalletQuota
	}
	w.consumed = amount
	return nil
}

func (w *WalletFunding) Settle(delta int) error {
	if delta == 0 {
		return nil
	}
	if w.requestId != "" {
		return model.ApplyBillingOperation(model.BillingOperationSpec{
			RequestID:   w.requestId,
			Component:   "wallet_settle",
			UserID:      w.userId,
			WalletDelta: -int64(delta),
		})
	}
	if delta > 0 {
		return model.DecreaseUserQuota(w.userId, delta, false)
	}
	return model.IncreaseUserQuota(w.userId, -delta, false)
}

func (w *WalletFunding) Refund() error {
	if w.consumed <= 0 {
		return nil
	}
	if w.requestId != "" {
		if err := model.ApplyBillingOperation(model.BillingOperationSpec{
			RequestID:   w.requestId,
			Component:   "wallet_refund",
			UserID:      w.userId,
			WalletDelta: int64(w.consumed),
		}); err != nil {
			return err
		}
		w.consumed = 0
		return nil
	}
	// IncreaseUserQuota 是 quota += N 的非幂等操作，不能重试，否则会多退额度。
	// BillingSession 在成功后也会清零 consumed，保证同一进程内的重复
	// Refund 不会再次增加钱包余额。跨进程崩溃窗口仍需持久 outbox/操作
	// marker（后续治理项）才能完全消除重复退款风险。
	if err := model.IncreaseUserQuota(w.userId, w.consumed, false); err != nil {
		return err
	}
	w.consumed = 0
	return nil
}

// ---------------------------------------------------------------------------
// SubscriptionFunding — 订阅资金来源实现
// ---------------------------------------------------------------------------

type SubscriptionFunding struct {
	requestId string
	userId    int
	modelName string
	amount    int64 // 预扣的订阅额度（subConsume）
	// Token fields are populated by NewBillingSession. When present, the
	// subscription and token reservations are committed in one transaction.
	tokenId        int
	tokenKey       string
	tokenUnlimited bool
	playground     bool
	combined       bool
	subscriptionId int
	preConsumed    int64
	// 以下字段在 PreConsume 成功后填充，供 RelayInfo 同步使用
	AmountTotal     int64
	AmountUsedAfter int64
	PlanId          int
	PlanTitle       string
}

func (s *SubscriptionFunding) Source() string { return BillingSourceSubscription }

func (s *SubscriptionFunding) PreConsume(_ int) error {
	// amount 参数被忽略，使用内部 s.amount（已在构造时根据 preConsumedQuota 计算）
	var (
		res *model.SubscriptionPreConsumeResult
		err error
	)
	if s.combined {
		res, err = model.PreConsumeUserSubscriptionAndToken(
			s.requestId, s.userId, s.modelName, 0, s.amount,
			s.tokenId, s.tokenKey, s.tokenUnlimited, s.playground,
		)
	} else {
		res, err = model.PreConsumeUserSubscription(s.requestId, s.userId, s.modelName, 0, s.amount)
	}
	if err != nil {
		return err
	}
	s.subscriptionId = res.UserSubscriptionId
	s.preConsumed = res.PreConsumed
	s.AmountTotal = res.AmountTotal
	s.AmountUsedAfter = res.AmountUsedAfter
	// 获取订阅计划信息
	if planInfo, err := model.GetSubscriptionPlanInfoByUserSubscriptionId(res.UserSubscriptionId); err == nil && planInfo != nil {
		s.PlanId = planInfo.PlanId
		s.PlanTitle = planInfo.PlanTitle
	}
	return nil
}

func (s *SubscriptionFunding) Settle(delta int) error {
	if delta == 0 {
		return nil
	}
	return model.PostConsumeUserSubscriptionDelta(s.subscriptionId, int64(delta))
}

func (s *SubscriptionFunding) Refund() error {
	if s.preConsumed <= 0 {
		return nil
	}
	return refundWithRetry(func() error {
		if err := model.RefundSubscriptionPreConsume(s.requestId); err != nil {
			return err
		}
		// The DB marker is idempotent; clearing the local amount prevents a
		// second call from doing unnecessary work in this process.
		s.preConsumed = 0
		return nil
	})
}

// refundWithRetry 尝试多次执行退款操作以提高成功率，只能用于基于事务的退款函数！！！！！！
// try to refund with retries, only for refund functions based on transactions!!!
func refundWithRetry(fn func() error) error {
	if fn == nil {
		return nil
	}
	const maxAttempts = 3
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if i < maxAttempts-1 {
			time.Sleep(time.Duration(200*(i+1)) * time.Millisecond)
		}
	}
	return lastErr
}
