package model

// This file contains the compatibility-only settlement path used by the old
// Recharge* helpers.  Provider webhooks must use SettleTopUpProvider (or the
// verified EPay equivalent); these helpers do not receive a provider payload
// and therefore cannot authenticate one.  They still use the same durable
// evidence-first state machine so a wallet/database failure can never leave a
// paid order looking successful without a recoverable reconciliation state.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrLegacyTopUpEvidenceMissing = errors.New("legacy topup evidence missing")

const legacyTopUpEvidencePrefix = "legacy-topup:v1:"

// legacyTopUpEvidenceMarker is deterministic for an order, but does not put
// the order number in a provider-facing field.  It is used only as an
// internal compatibility marker and as the idempotency identity in the
// payment-event ledger when the historical row has no provider transaction.
func legacyTopUpEvidenceMarker(provider, tradeNo string) string {
	provider = strings.TrimSpace(provider)
	tradeNo = strings.TrimSpace(tradeNo)
	return legacyTopUpEvidencePrefix + provider + ":" +
		hex.EncodeToString(common.Sha256Raw([]byte(provider+"\x00"+tradeNo)))
}

func legacyTopUpOwnsEvidence(topUp *TopUp, provider, tradeNo string) bool {
	if topUp == nil {
		return false
	}
	marker := legacyTopUpEvidenceMarker(provider, tradeNo)
	if strings.TrimSpace(topUp.ProviderEventID) == marker || strings.TrimSpace(topUp.ProviderPayload) == marker {
		return true
	}
	if topUp.ProviderTradeNo != nil && strings.TrimSpace(*topUp.ProviderTradeNo) == marker {
		return true
	}
	return false
}

type legacyTopUpSettlementResult struct {
	AlreadyCompleted bool
	PaidUncredited   bool
	Credited         bool
	UserID           int
	CreditedQuota    int
	Amount           int64
	Money            float64
	PaymentMethod    string
	Provider         string
}

// settleLegacyTopUp performs a compatibility settlement in two transactions:
//
//  1. lock the order, freeze the quota, and persist a synthetic internal
//     payment event plus paid_uncredited/credit_pending;
//  2. invoke the common locked wallet retry helper.
//
// The second phase is deliberately outside the evidence transaction.  A
// process crash or a database error after phase one therefore leaves a
// durable operator-retryable order instead of rolling the payment fact back
// to pending (or, worse, reporting success with no credit).
func settleLegacyTopUp(referenceID, provider string, userUpdates map[string]interface{}) (result legacyTopUpSettlementResult, err error) {
	referenceID = strings.TrimSpace(referenceID)
	provider = strings.TrimSpace(provider)
	if referenceID == "" {
		return result, errors.New("未提供支付单号")
	}
	if provider == "" {
		return result, ErrPaymentMethodMismatch
	}
	if DB == nil {
		return result, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	marker := legacyTopUpEvidenceMarker(provider, referenceID)
	var phaseOne legacyTopUpSettlementResult
	err = DB.Transaction(func(tx *gorm.DB) error {
		var topUp TopUp
		if err := lockForUpdate(tx).Where(refCol+" = ?", referenceID).First(&topUp).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if topUp.PaymentProvider != provider {
			return ErrPaymentMethodMismatch
		}

		phaseOne.UserID = topUp.UserId
		phaseOne.CreditedQuota = topUp.CreditedQuota
		phaseOne.Amount = topUp.Amount
		phaseOne.Money = topUp.Money
		phaseOne.PaymentMethod = topUp.PaymentMethod
		phaseOne.Provider = topUp.PaymentProvider

		switch {
		case topUp.Status == common.TopUpStatusSuccess:
			phaseOne.AlreadyCompleted = true
			return nil
		case isTopUpPaidUncredited(&topUp):
			// A legacy compatibility helper may retry only evidence that it
			// created itself.  This prevents a call that lacks a verified
			// provider payload from hijacking a modern paid_uncredited order
			// (which may have been deliberately marked refund_required).
			if !legacyTopUpOwnsEvidence(&topUp, provider, referenceID) {
				return ErrLegacyTopUpEvidenceMissing
			}
			phaseOne.PaidUncredited = true
			return nil
		case topUp.Status != common.TopUpStatusPending:
			return ErrTopUpStatusInvalid
		}

		quota, quotaErr := topUpQuotaForSettlement(&topUp, provider)
		if quotaErr != nil {
			return quotaErr
		}
		// topUpQuotaForSettlement returns CreditedQuota verbatim for modern
		// rows and derives it only for fully legacy rows.  Freezing a derived
		// value here makes phase two independent of mutable pricing settings.
		if topUp.CreditedQuota == 0 {
			topUp.CreditedQuota = quota
		}
		if topUp.CreditedQuota != quota {
			return ErrInvalidTopUpQuota
		}

		// Preserve a provider transaction already recorded by a trusted older
		// caller.  Otherwise use the deterministic compatibility marker.  The
		// payment-event ledger rejects a marker already bound to another order.
		providerTradeNo := ""
		if topUp.ProviderTradeNo != nil {
			providerTradeNo = strings.TrimSpace(*topUp.ProviderTradeNo)
		}
		if providerTradeNo == "" {
			providerTradeNo = marker
			topUp.ProviderTradeNo = &providerTradeNo
		}
		if topUp.ProviderEventID != "" && strings.TrimSpace(topUp.ProviderEventID) != marker {
			return ErrProviderEventConflict
		}
		topUp.ProviderEventID = marker
		if topUp.ProviderPayload == "" {
			topUp.ProviderPayload = marker
		}
		if err := bindPaymentEventTx(tx, provider, providerTradeNo, referenceID, PaymentEventOrderTopUp, marker); err != nil {
			return err
		}
		markTopUpPaidUncredited(&topUp, "")
		if err := tx.Save(&topUp).Error; err != nil {
			return err
		}
		phaseOne.PaidUncredited = true
		phaseOne.CreditedQuota = topUp.CreditedQuota
		return nil
	})
	if err != nil {
		return result, err
	}
	if phaseOne.AlreadyCompleted {
		return phaseOne, nil
	}

	// The evidence transaction has committed.  Even when this attempt fails,
	// the row remains paid_uncredited and can be retried by the admin endpoint.
	attempt, retryErr := retryPaidUncreditedTopUpWithUpdates(referenceID, userUpdates)
	if retryErr != nil {
		return legacyTopUpSettlementResult{
			PaidUncredited: true,
			UserID:         phaseOne.UserID,
			CreditedQuota:  phaseOne.CreditedQuota,
			Amount:         phaseOne.Amount,
			Money:          phaseOne.Money,
			PaymentMethod:  phaseOne.PaymentMethod,
			Provider:       phaseOne.Provider,
		}, retryErr
	}
	result = legacyTopUpSettlementResult{
		AlreadyCompleted: attempt.AlreadyCompleted,
		PaidUncredited:   !attempt.Credited && !attempt.AlreadyCompleted,
		Credited:         attempt.Credited,
		UserID:           attempt.UserID,
		CreditedQuota:    attempt.CreditedQuota,
		Amount:           0,
		Money:            attempt.Money,
		PaymentMethod:    attempt.PaymentMethod,
		Provider:         attempt.Provider,
	}
	if result.UserID == 0 {
		result.UserID = phaseOne.UserID
	}
	if result.CreditedQuota == 0 {
		result.CreditedQuota = phaseOne.CreditedQuota
	}
	if result.Amount == 0 {
		result.Amount = phaseOne.Amount
	}
	if result.Money == 0 {
		result.Money = phaseOne.Money
	}
	if result.PaymentMethod == "" {
		result.PaymentMethod = phaseOne.PaymentMethod
	}
	if result.Provider == "" {
		result.Provider = phaseOne.Provider
	}
	if attempt.PendingCode != "" {
		return result, topUpCreditError(attempt.PendingCode)
	}
	return result, nil
}
