package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProviderReversalEffectStatus string

const (
	ProviderReversalEffectProcessing ProviderReversalEffectStatus = "processing"
	ProviderReversalEffectApplied    ProviderReversalEffectStatus = "applied"
	ProviderReversalEffectObserved   ProviderReversalEffectStatus = "observed"
	ProviderReversalEffectRevoked    ProviderReversalEffectStatus = "entitlement_revoked"
	ProviderReversalEffectRejected   ProviderReversalEffectStatus = "rejected"
)

// ProviderReversalEffect is the provider-neutral logical money-movement
// ledger. A provider can deliver the same refund or dispute through several
// webhook events; EffectKey collapses those deliveries to one accounting
// effect while ProviderRefundEvent retains every authenticated observation.
type ProviderReversalEffect struct {
	ID                  int64                        `json:"id" gorm:"primaryKey"`
	EffectKey           string                       `json:"effect_key" gorm:"type:char(64);not null;uniqueIndex:idx_provider_reversal_effect_key"`
	Provider            string                       `json:"provider" gorm:"type:varchar(50);not null;index;index:idx_provider_reversal_order,priority:1,length:12"`
	ProviderAccountID   string                       `json:"provider_account_id" gorm:"type:varchar(255);not null;index:idx_provider_reversal_effects_provider_account_id,length:191;index:idx_provider_reversal_order,priority:2,length:48"`
	ProviderEnvironment string                       `json:"provider_environment" gorm:"type:varchar(32);not null;index;index:idx_provider_reversal_order,priority:3,length:12"`
	EffectID            string                       `json:"effect_id" gorm:"type:varchar(255);not null;index:idx_provider_reversal_effects_effect_id,length:191"`
	RelatedEffectID     string                       `json:"related_effect_id" gorm:"type:varchar(255);default:'';index:idx_provider_reversal_effects_related_effect_id,length:191"`
	RelatedEffectKey    *string                      `json:"related_effect_key" gorm:"type:char(64);uniqueIndex:idx_provider_reversal_related_effect"`
	ProviderTradeNo     string                       `json:"provider_trade_no" gorm:"type:varchar(255);not null;index:idx_provider_reversal_effects_provider_trade_no,length:191"`
	OrderTradeNo        string                       `json:"order_trade_no" gorm:"type:varchar(255);not null;index:idx_provider_reversal_order,priority:4,length:96"`
	OrderKind           string                       `json:"order_kind" gorm:"type:varchar(32);not null;index:idx_provider_reversal_order,priority:5,length:16"`
	UserID              int                          `json:"user_id" gorm:"index"`
	UserSubscriptionID  int                          `json:"user_subscription_id" gorm:"index"`
	EventType           string                       `json:"event_type" gorm:"type:varchar(64);not null;index"`
	Amount              string                       `json:"amount" gorm:"type:varchar(64);not null"`
	Currency            string                       `json:"currency" gorm:"type:varchar(16);not null"`
	PaidAmount          string                       `json:"paid_amount" gorm:"type:varchar(64);not null;default:''"`
	CreditedQuota       int64                        `json:"credited_quota" gorm:"type:bigint;not null;default:0"`
	CumulativeAmount    string                       `json:"cumulative_amount" gorm:"type:varchar(64);not null;default:''"`
	CumulativeQuota     int64                        `json:"cumulative_quota" gorm:"type:bigint;not null;default:0"`
	WalletDelta         int64                        `json:"wallet_delta" gorm:"type:bigint;not null;default:0"`
	Status              ProviderReversalEffectStatus `json:"status" gorm:"type:varchar(32);not null;index"`
	Outcome             ProviderRefundEventStatus    `json:"outcome" gorm:"type:varchar(32);not null;index"`
	DecisionReason      string                       `json:"decision_reason" gorm:"type:varchar(64);default:'';index"`
	FirstDeliveryKey    string                       `json:"first_delivery_key" gorm:"type:char(64);not null;default:'';index"`
	Payload             string                       `json:"payload" gorm:"type:text"`
	CreatedAt           int64                        `json:"created_at" gorm:"index"`
	UpdatedAt           int64                        `json:"updated_at"`
}

type providerReversalDecision struct {
	Status             ProviderRefundEventStatus
	Reason             string
	EffectKey          string
	WalletDelta        int64
	AlreadyProcessed   bool
	BillingSpec        BillingOperationSpec
	BillingApplied     bool
	RefreshGroupUserID int
}

func ProviderReversalEffectKey(provider, eventType, effectID string) string {
	return ProviderReversalEffectScopedKey(provider, "", "", eventType, effectID)
}

func ProviderReversalEffectScopedKey(provider, providerAccountID, providerEnvironment, eventType, effectID string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerAccountID = strings.TrimSpace(providerAccountID)
	providerEnvironment = strings.ToLower(strings.TrimSpace(providerEnvironment))
	eventType = strings.ToLower(strings.TrimSpace(eventType))
	effectID = strings.TrimSpace(effectID)
	digest := sha256.Sum256([]byte(provider + "\x00" + providerAccountID + "\x00" + providerEnvironment + "\x00" + eventType + "\x00" + effectID))
	return hex.EncodeToString(digest[:])
}

func providerReversalDirection(eventType string) (int, error) {
	switch eventType {
	case "refund.succeeded", "dispute.funds_withdrawn":
		return 1, nil
	case "dispute.funds_reinstated":
		return -1, nil
	default:
		return 0, ErrProviderRefundInvalid
	}
}

func providerReversalEffectTerminal(status ProviderReversalEffectStatus) bool {
	switch status {
	case ProviderReversalEffectApplied, ProviderReversalEffectObserved, ProviderReversalEffectRevoked, ProviderReversalEffectRejected:
		return true
	default:
		return false
	}
}

func providerReversalEffectCounts(status ProviderReversalEffectStatus) bool {
	switch status {
	case ProviderReversalEffectApplied, ProviderReversalEffectObserved, ProviderReversalEffectRevoked:
		return true
	default:
		return false
	}
}

func parseProviderReversalAmount(value string) (decimal.Decimal, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || !amount.IsPositive() || !amount.Equal(amount.Round(8)) {
		return decimal.Zero, ErrProviderRefundInvalid
	}
	return amount, nil
}

func providerReversalTargetQuota(creditedQuota int, paidAmount, reversedAmount decimal.Decimal) (int, error) {
	if creditedQuota <= 0 || creditedQuota > common.MaxWalletQuota || !paidAmount.IsPositive() || reversedAmount.IsNegative() || reversedAmount.GreaterThan(paidAmount) {
		return 0, ErrProviderRefundConflict
	}
	if reversedAmount.IsZero() {
		return 0, nil
	}
	if reversedAmount.Equal(paidAmount) {
		return creditedQuota, nil
	}
	return common.WalletQuotaFromDecimalStrict(
		decimal.NewFromInt(int64(creditedQuota)).Mul(reversedAmount).Div(paidAmount),
	)
}

func providerReversalMatchesTradeNo(input string, stored *string, checkoutID string) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return false
	}
	if stored != nil && strings.TrimSpace(*stored) == input {
		return true
	}
	return strings.TrimSpace(checkoutID) == input
}

func providerReversalEffectMatches(effect *ProviderReversalEffect, input ProviderRefundEventInput) bool {
	if effect == nil {
		return false
	}
	return effect.EffectKey == ProviderReversalEffectScopedKey(input.Provider, input.ProviderAccountID, input.ProviderEnvironment, input.EventType, input.EffectID) &&
		effect.Provider == input.Provider && effect.ProviderAccountID == input.ProviderAccountID &&
		effect.ProviderEnvironment == input.ProviderEnvironment && effect.EffectID == input.EffectID &&
		effect.RelatedEffectID == input.RelatedEffectID && effect.ProviderTradeNo == input.ProviderTradeNo &&
		effect.OrderTradeNo == input.OrderTradeNo && effect.OrderKind == input.OrderKind &&
		effect.EventType == input.EventType && compareProviderAmount(effect.Amount, input.Amount) &&
		strings.EqualFold(effect.Currency, input.Currency)
}

func rejectProviderReversalEffectTx(tx *gorm.DB, effect *ProviderReversalEffect, reason string) (providerReversalDecision, error) {
	decision := providerReversalDecision{
		Status:    ProviderRefundEventManualReconciliation,
		Reason:    reason,
		EffectKey: effect.EffectKey,
	}
	effect.Status = ProviderReversalEffectRejected
	effect.Outcome = decision.Status
	effect.DecisionReason = reason
	effect.RelatedEffectKey = nil
	effect.UpdatedAt = common.GetTimestamp()
	if err := tx.Save(effect).Error; err != nil {
		return providerReversalDecision{}, err
	}
	return decision, nil
}

func processProviderReversalTx(tx *gorm.DB, input ProviderRefundEventInput) (providerReversalDecision, error) {
	if tx == nil {
		return providerReversalDecision{}, ErrProviderRefundInvalid
	}
	amount, err := parseProviderReversalAmount(input.Amount)
	if err != nil {
		return providerReversalDecision{}, err
	}
	direction, err := providerReversalDirection(input.EventType)
	if err != nil {
		return providerReversalDecision{}, err
	}
	if input.EventType == "dispute.funds_reinstated" {
		if input.RelatedEffectID == "" {
			return providerReversalDecision{}, ErrProviderRefundInvalid
		}
	} else if input.RelatedEffectID != "" {
		return providerReversalDecision{}, ErrProviderRefundInvalid
	}
	if input.OrderKind != PaymentEventOrderTopUp && input.OrderKind != PaymentEventOrderSubscription {
		return providerReversalDecision{}, ErrProviderRefundInvalid
	}

	now := common.GetTimestamp()
	effectKey := ProviderReversalEffectScopedKey(input.Provider, input.ProviderAccountID, input.ProviderEnvironment, input.EventType, input.EffectID)
	effect := ProviderReversalEffect{
		EffectKey:           effectKey,
		Provider:            input.Provider,
		ProviderAccountID:   input.ProviderAccountID,
		ProviderEnvironment: input.ProviderEnvironment,
		EffectID:            input.EffectID,
		RelatedEffectID:     input.RelatedEffectID,
		ProviderTradeNo:     input.ProviderTradeNo,
		OrderTradeNo:        input.OrderTradeNo,
		OrderKind:           input.OrderKind,
		EventType:           input.EventType,
		Amount:              input.Amount,
		Currency:            input.Currency,
		Status:              ProviderReversalEffectProcessing,
		Outcome:             ProviderRefundEventProcessing,
		Payload:             input.Payload,
		FirstDeliveryKey:    ProviderRefundEventScopedKey(input.Provider, input.ProviderAccountID, input.ProviderEnvironment, input.DeliveryID),
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&effect).Error; err != nil {
		return providerReversalDecision{}, err
	}
	if err := lockForUpdate(tx).Where("effect_key = ?", effectKey).First(&effect).Error; err != nil {
		return providerReversalDecision{}, err
	}
	if !providerReversalEffectMatches(&effect, input) {
		return providerReversalDecision{}, ErrProviderRefundConflict
	}
	if providerReversalEffectTerminal(effect.Status) {
		return providerReversalDecision{
			Status:           effect.Outcome,
			Reason:           effect.DecisionReason,
			EffectKey:        effect.EffectKey,
			AlreadyProcessed: true,
		}, nil
	}
	if effect.Status != ProviderReversalEffectProcessing {
		return providerReversalDecision{}, ErrProviderRefundConflict
	}

	var userID int
	var creditedQuota int
	var paidAmount decimal.Decimal
	providerPaymentBound := input.ProviderObjectType != ""
	switch input.OrderKind {
	case PaymentEventOrderTopUp:
		var order TopUp
		lookupErr := lockForUpdate(tx).Where("trade_no = ?", input.OrderTradeNo).First(&order).Error
		if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return rejectProviderReversalEffectTx(tx, &effect, "order_not_found")
		}
		if lookupErr != nil {
			return providerReversalDecision{}, lookupErr
		}
		paidAmount, err = parseProviderReversalAmount(order.ProviderAmount)
		if err != nil || order.UserId <= 0 || order.CreditedQuota <= 0 ||
			order.Status != common.TopUpStatusSuccess || !strings.EqualFold(order.PaymentProvider, input.Provider) ||
			strings.TrimSpace(order.ProviderMerchantID) != input.ProviderAccountID ||
			!strings.EqualFold(order.ProviderCurrency, input.Currency) ||
			(!providerPaymentBound && !providerReversalMatchesTradeNo(input.ProviderTradeNo, order.ProviderTradeNo, order.ProviderCheckoutID)) {
			return rejectProviderReversalEffectTx(tx, &effect, "order_snapshot_mismatch")
		}
		userID = order.UserId
		creditedQuota = order.CreditedQuota

	case PaymentEventOrderSubscription:
		var order SubscriptionOrder
		lookupErr := lockForUpdate(tx).Where("trade_no = ?", input.OrderTradeNo).First(&order).Error
		if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return rejectProviderReversalEffectTx(tx, &effect, "order_not_found")
		}
		if lookupErr != nil {
			return providerReversalDecision{}, lookupErr
		}
		paidAmount, err = parseProviderReversalAmount(order.ProviderAmount)
		if err != nil || order.UserId <= 0 || order.PlanId <= 0 || order.Status != common.TopUpStatusSuccess ||
			!strings.EqualFold(order.PaymentProvider, input.Provider) || !strings.EqualFold(order.ProviderCurrency, input.Currency) ||
			strings.TrimSpace(order.ProviderMerchantID) != input.ProviderAccountID ||
			(!providerPaymentBound && !providerReversalMatchesTradeNo(input.ProviderTradeNo, order.ProviderTradeNo, order.ProviderCheckoutID)) {
			return rejectProviderReversalEffectTx(tx, &effect, "order_snapshot_mismatch")
		}
		userID = order.UserId
	}
	effect.UserID = userID
	effect.PaidAmount = paidAmount.String()
	effect.CreditedQuota = int64(creditedQuota)

	// Every reversal for an order locks the order before any existing effect.
	// Reinstatement used to lock its withdrawal parent first, which inverted the
	// order used by concurrent refund/withdrawal transactions and could deadlock.
	var parent ProviderReversalEffect
	if input.EventType == "dispute.funds_reinstated" {
		parentKey := ProviderReversalEffectScopedKey(input.Provider, input.ProviderAccountID, input.ProviderEnvironment, "dispute.funds_withdrawn", input.RelatedEffectID)
		if err := lockForUpdate(tx).Where("effect_key = ?", parentKey).First(&parent).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return rejectProviderReversalEffectTx(tx, &effect, "missing_dispute_withdrawal")
			}
			return providerReversalDecision{}, err
		}
		if !providerReversalEffectCounts(parent.Status) || parent.EventType != "dispute.funds_withdrawn" ||
			parent.OrderTradeNo != input.OrderTradeNo || parent.OrderKind != input.OrderKind ||
			parent.ProviderTradeNo != input.ProviderTradeNo || !strings.EqualFold(parent.Currency, input.Currency) ||
			!compareProviderAmount(parent.Amount, input.Amount) {
			return rejectProviderReversalEffectTx(tx, &effect, "dispute_reinstatement_mismatch")
		}
		var priorRestoration ProviderReversalEffect
		priorErr := lockForUpdate(tx).Select("id").
			Where("provider = ? AND provider_account_id = ? AND provider_environment = ? AND event_type = ? AND related_effect_id = ? AND effect_key <> ? AND status IN ?",
				input.Provider, input.ProviderAccountID, input.ProviderEnvironment, "dispute.funds_reinstated", input.RelatedEffectID, effectKey,
				[]ProviderReversalEffectStatus{ProviderReversalEffectApplied, ProviderReversalEffectObserved, ProviderReversalEffectRevoked}).
			Take(&priorRestoration).Error
		if priorErr == nil {
			return rejectProviderReversalEffectTx(tx, &effect, "dispute_already_reinstated")
		}
		if !errors.Is(priorErr, gorm.ErrRecordNotFound) {
			return providerReversalDecision{}, priorErr
		}
		parentKeyCopy := parentKey
		effect.RelatedEffectKey = &parentKeyCopy
	}
	if !providerPaymentBound {
		if err := bindPaymentEventTx(tx, input.Provider, input.ProviderTradeNo, input.OrderTradeNo, input.OrderKind, input.Payload); err != nil {
			return providerReversalDecision{}, err
		}
	}

	var priorEffects []ProviderReversalEffect
	if err := lockForUpdate(tx).
		Where("provider = ? AND provider_account_id = ? AND provider_environment = ? AND order_trade_no = ? AND order_kind = ? AND effect_key <> ? AND status IN ?",
			input.Provider, input.ProviderAccountID, input.ProviderEnvironment, input.OrderTradeNo, input.OrderKind, effectKey,
			[]ProviderReversalEffectStatus{ProviderReversalEffectApplied, ProviderReversalEffectObserved, ProviderReversalEffectRevoked}).
		Order("id asc").Find(&priorEffects).Error; err != nil {
		return providerReversalDecision{}, err
	}
	currentAmount := decimal.Zero
	var currentWalletDelta int64
	for i := range priorEffects {
		prior := &priorEffects[i]
		if !providerReversalEffectCounts(prior.Status) || !strings.EqualFold(prior.Currency, input.Currency) || prior.ProviderTradeNo != input.ProviderTradeNo {
			return providerReversalDecision{}, ErrProviderRefundConflict
		}
		priorAmount, parseErr := parseProviderReversalAmount(prior.Amount)
		if parseErr != nil {
			return providerReversalDecision{}, ErrProviderRefundConflict
		}
		priorDirection, directionErr := providerReversalDirection(prior.EventType)
		if directionErr != nil {
			return providerReversalDecision{}, ErrProviderRefundConflict
		}
		currentAmount = currentAmount.Add(priorAmount.Mul(decimal.NewFromInt(int64(priorDirection))))
		if prior.WalletDelta > 0 && currentWalletDelta > math.MaxInt64-prior.WalletDelta ||
			prior.WalletDelta < 0 && currentWalletDelta < math.MinInt64-prior.WalletDelta {
			return providerReversalDecision{}, ErrProviderRefundConflict
		}
		currentWalletDelta += prior.WalletDelta
	}
	if currentAmount.IsNegative() || currentAmount.GreaterThan(paidAmount) {
		return providerReversalDecision{}, ErrProviderRefundConflict
	}
	newAmount := currentAmount.Add(amount.Mul(decimal.NewFromInt(int64(direction))))
	if newAmount.IsNegative() || newAmount.GreaterThan(paidAmount) {
		return rejectProviderReversalEffectTx(tx, &effect, "cumulative_amount_out_of_range")
	}
	effect.CumulativeAmount = newAmount.String()

	decision := providerReversalDecision{EffectKey: effectKey}
	if input.OrderKind == PaymentEventOrderTopUp {
		currentTarget, quotaErr := providerReversalTargetQuota(creditedQuota, paidAmount, currentAmount)
		if quotaErr != nil || currentWalletDelta != -int64(currentTarget) {
			return providerReversalDecision{}, ErrProviderRefundConflict
		}
		newTarget, quotaErr := providerReversalTargetQuota(creditedQuota, paidAmount, newAmount)
		if quotaErr != nil {
			return providerReversalDecision{}, quotaErr
		}
		walletDelta := int64(currentTarget) - int64(newTarget)
		if input.EventType == "dispute.funds_reinstated" && walletDelta != -parent.WalletDelta {
			return rejectProviderReversalEffectTx(tx, &effect, "dispute_reinstatement_not_exact_inverse")
		}
		var wallet User
		if err := lockForUpdate(tx).Select("id", "quota").Where("id = ?", userID).First(&wallet).Error; err != nil {
			return providerReversalDecision{}, err
		}
		walletQuota := int64(wallet.Quota)
		if walletDelta < 0 && walletQuota < -int64(common.MaxWalletQuota)-walletDelta ||
			walletDelta > 0 && walletQuota > int64(common.MaxWalletQuota)-walletDelta {
			return rejectProviderReversalEffectTx(tx, &effect, "wallet_quota_limit")
		}
		spec, normalizeErr := normalizeBillingOperationSpec(BillingOperationSpec{
			RequestID:            effectKey,
			Component:            "provider_reversal",
			UserID:               userID,
			WalletDelta:          walletDelta,
			RequireWalletBalance: false,
		})
		if normalizeErr != nil {
			return providerReversalDecision{}, normalizeErr
		}
		applied, applyErr := applyBillingOperationTx(tx, spec)
		if applyErr != nil {
			return providerReversalDecision{}, applyErr
		}
		effect.WalletDelta = walletDelta
		effect.CumulativeQuota = int64(newTarget)
		effect.Status = ProviderReversalEffectApplied
		effect.Outcome = ProviderRefundEventApplied
		effect.DecisionReason = "wallet_quota_reversed"
		decision.Status = effect.Outcome
		decision.Reason = effect.DecisionReason
		decision.WalletDelta = walletDelta
		decision.BillingSpec = spec
		decision.BillingApplied = applied
	} else {
		if currentWalletDelta != 0 {
			return providerReversalDecision{}, ErrProviderRefundConflict
		}
		if direction < 0 || newAmount.LessThan(paidAmount) {
			effect.Status = ProviderReversalEffectObserved
			effect.Outcome = ProviderRefundEventManualReconciliation
			if direction < 0 {
				effect.DecisionReason = "subscription_reinstatement_requires_manual"
			} else {
				effect.DecisionReason = "partial_subscription_refund"
			}
			decision.Status = effect.Outcome
			decision.Reason = effect.DecisionReason
		} else {
			var user User
			if err := lockForUpdate(tx).Select("id").Where("id = ?", userID).First(&user).Error; err != nil {
				return providerReversalDecision{}, err
			}
			var linked []UserSubscription
			if err := lockForUpdate(tx).Where("subscription_order_trade_no = ?", input.OrderTradeNo).Find(&linked).Error; err != nil {
				return providerReversalDecision{}, err
			}
			var order SubscriptionOrder
			if err := tx.Select("user_id", "plan_id").Where("trade_no = ?", input.OrderTradeNo).First(&order).Error; err != nil {
				return providerReversalDecision{}, err
			}
			if len(linked) != 1 || linked[0].UserId != order.UserId || linked[0].PlanId != order.PlanId {
				return rejectProviderReversalEffectTx(tx, &effect, "ambiguous_subscription_binding")
			}
			effect.UserSubscriptionID = linked[0].Id
			now := common.GetTimestamp()
			updates := map[string]interface{}{"status": "cancelled", "updated_at": now}
			if linked[0].EndTime == 0 || linked[0].EndTime > now {
				updates["end_time"] = now
			}
			if err := tx.Model(&UserSubscription{}).Where("id = ?", linked[0].Id).Updates(updates).Error; err != nil {
				return providerReversalDecision{}, err
			}
			target, downgradeErr := downgradeUserGroupForSubscriptionTx(tx, &linked[0], now)
			if downgradeErr != nil {
				return providerReversalDecision{}, downgradeErr
			}
			if target != "" {
				decision.RefreshGroupUserID = userID
			}
			effect.Status = ProviderReversalEffectRevoked
			effect.Outcome = ProviderRefundEventEntitlementRevoked
			effect.DecisionReason = "subscription_entitlement_revoked"
			decision.Status = effect.Outcome
			decision.Reason = effect.DecisionReason
		}
	}

	effect.UpdatedAt = common.GetTimestamp()
	if err := tx.Save(&effect).Error; err != nil {
		return providerReversalDecision{}, fmt.Errorf("persist provider reversal effect: %w", err)
	}
	return decision, nil
}
