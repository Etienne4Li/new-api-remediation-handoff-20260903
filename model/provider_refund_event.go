package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProviderRefundEventStatus is the durable state of a provider refund
// delivery. The refund fact and the local remediation decision are deliberately
// separate from PaymentEvent: a provider can reuse the payment/business event
// ID for a subscription lifecycle delivery while each refund delivery has its
// own idempotency identity.
type ProviderRefundEventStatus string

const (
	ProviderRefundEventProcessing           ProviderRefundEventStatus = "processing"
	ProviderRefundEventApplied              ProviderRefundEventStatus = "applied"
	ProviderRefundEventEntitlementRevoked   ProviderRefundEventStatus = "entitlement_revoked"
	ProviderRefundEventManualReconciliation ProviderRefundEventStatus = "manual_reconciliation"
	ProviderRefundEventRefundRequired       ProviderRefundEventStatus = "refund_required"

	ProviderRefundManualReasonPartialRefundUnsupported = "provider_partial_refund_unsupported"
	ProviderRefundManualReasonPaymentBindingMissing    = "provider_payment_binding_missing"
	ProviderRefundManualReasonPaymentBindingConflict   = "provider_payment_binding_conflict"
	ProviderRefundManualReasonNonTerminal              = "provider_refund_non_terminal"
)

var (
	ErrProviderRefundInvalid  = errors.New("invalid provider refund event")
	ErrProviderRefundConflict = errors.New("provider refund event conflicts with existing delivery")
)

// ProviderRefundEvent is an append-once delivery ledger for provider refund
// webhooks. Scoped integrations include provider account and environment in
// EventKey; legacy callers with an empty scope retain the original
// SHA-256(provider + NUL + deliveryID) identity.
type ProviderRefundEvent struct {
	ID                             int64                     `json:"id" gorm:"primaryKey"`
	EventKey                       string                    `json:"event_key" gorm:"type:char(64);not null;uniqueIndex:idx_provider_refund_event_key"`
	Provider                       string                    `json:"provider" gorm:"type:varchar(50);not null;index"`
	ProviderAccountID              string                    `json:"provider_account_id" gorm:"type:varchar(255);default:'';index:idx_provider_refund_events_provider_account_id,length:191"`
	ProviderEnvironment            string                    `json:"provider_environment" gorm:"type:varchar(32);default:'';index"`
	DeliveryID                     string                    `json:"delivery_id" gorm:"type:varchar(255);not null;index:idx_provider_refund_events_delivery_id,length:191"`
	BusinessEventID                string                    `json:"business_event_id" gorm:"type:varchar(255);default:''"`
	RefundTicketMerchantExternalID string                    `json:"refund_ticket_external_id" gorm:"type:varchar(128);default:''"`
	EffectKey                      string                    `json:"effect_key" gorm:"type:char(64);default:'';index"`
	EffectID                       string                    `json:"effect_id" gorm:"type:varchar(255);default:'';index:idx_provider_refund_events_effect_id,length:191"`
	RelatedEffectID                string                    `json:"related_effect_id" gorm:"type:varchar(255);default:''"`
	ProviderObjectType             string                    `json:"provider_object_type" gorm:"type:varchar(32);default:''"`
	ProviderTradeNo                string                    `json:"provider_trade_no" gorm:"type:varchar(255);default:'';index:idx_provider_refund_events_provider_trade_no,length:191"`
	EventType                      string                    `json:"event_type" gorm:"type:varchar(64);not null;default:'';index"`
	RefundStatus                   string                    `json:"refund_status" gorm:"type:varchar(32);default:''"`
	OrderTradeNo                   string                    `json:"order_trade_no" gorm:"type:varchar(255);default:'';index:idx_provider_refund_events_order_trade_no,length:191"`
	OrderKind                      string                    `json:"order_kind" gorm:"type:varchar(32);default:''"`
	Amount                         string                    `json:"amount" gorm:"type:varchar(64);default:''"`
	Currency                       string                    `json:"currency" gorm:"type:varchar(16);default:''"`
	WalletDelta                    int64                     `json:"wallet_delta" gorm:"type:bigint;not null;default:0"`
	Status                         ProviderRefundEventStatus `json:"status" gorm:"type:varchar(32);not null;index"`
	DecisionReason                 string                    `json:"decision_reason" gorm:"type:varchar(64);default:'';index"`
	Reason                         string                    `json:"reason" gorm:"type:text"`
	Payload                        string                    `json:"payload" gorm:"type:text"`
	CreatedAt                      int64                     `json:"created_at" gorm:"index"`
	UpdatedAt                      int64                     `json:"updated_at"`
}

// ProviderRefundEventKey returns the stable database idempotency key for a
// provider delivery. Empty inputs are rejected by ProcessProviderRefundEvent,
// but this helper remains deterministic for callers that need to inspect a
// candidate key before persisting it.
func ProviderRefundEventKey(provider, deliveryID string) string {
	return ProviderRefundEventScopedKey(provider, "", "", deliveryID)
}

func ProviderRefundEventScopedKey(provider, providerAccountID, providerEnvironment, deliveryID string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerAccountID = strings.TrimSpace(providerAccountID)
	providerEnvironment = strings.ToLower(strings.TrimSpace(providerEnvironment))
	deliveryID = strings.TrimSpace(deliveryID)
	if providerAccountID == "" && providerEnvironment == "" {
		digest := sha256.Sum256([]byte(provider + "\x00" + deliveryID))
		return hex.EncodeToString(digest[:])
	}
	digest := sha256.Sum256([]byte(provider + "\x00" + providerAccountID + "\x00" + providerEnvironment + "\x00" + deliveryID))
	return hex.EncodeToString(digest[:])
}

// ProviderRefundEventInput is the verified, provider-neutral refund envelope
// passed by webhook controllers. The controller must authenticate the payload
// before constructing it.
type ProviderRefundEventInput struct {
	Provider                       string
	ProviderAccountID              string
	ProviderEnvironment            string
	DeliveryID                     string
	BusinessEventID                string
	RefundTicketMerchantExternalID string
	EffectID                       string
	RelatedEffectID                string
	ProviderObjectType             string
	ProviderTradeNo                string
	OrderTradeNo                   string
	OrderKind                      string
	EventType                      string
	RefundStatus                   string
	Amount                         string
	Currency                       string
	ForceManualReason              string
	Reason                         string
	Payload                        string
}

// ProviderRefundEventResult describes the durable remediation selected for a
// refund delivery. AlreadyProcessed is true when the exact delivery was
// previously committed, so a provider retry can be acknowledged safely.
type ProviderRefundEventResult struct {
	Status           ProviderRefundEventStatus
	OrderTradeNo     string
	OrderKind        string
	WalletDelta      int64
	AlreadyProcessed bool
}

func isProviderRefundTerminal(status ProviderRefundEventStatus) bool {
	switch status {
	case ProviderRefundEventApplied, ProviderRefundEventEntitlementRevoked, ProviderRefundEventManualReconciliation, ProviderRefundEventRefundRequired:
		return true
	default:
		return false
	}
}

func normalizeProviderRefundForceManualReason(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", ProviderRefundManualReasonPartialRefundUnsupported,
		ProviderRefundManualReasonPaymentBindingMissing,
		ProviderRefundManualReasonPaymentBindingConflict,
		ProviderRefundManualReasonNonTerminal:
		return value, nil
	default:
		return "", ErrProviderRefundInvalid
	}
}

// ProcessProviderRefundEvent records a verified provider refund/dispute
// delivery. Deliveries without complete amount, currency, order-kind, and
// provider-payment evidence remain audit-only. Complete evidence is passed to
// the logical reversal ledger, whose stable effect identity, cumulative amount
// checks, BillingOperation, and entitlement mutation share this transaction.
func ProcessProviderRefundEvent(input ProviderRefundEventInput) (result ProviderRefundEventResult, err error) {
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ProviderAccountID = strings.TrimSpace(input.ProviderAccountID)
	input.ProviderEnvironment = strings.ToLower(strings.TrimSpace(input.ProviderEnvironment))
	input.DeliveryID = strings.TrimSpace(input.DeliveryID)
	input.BusinessEventID = strings.TrimSpace(input.BusinessEventID)
	input.RefundTicketMerchantExternalID = strings.TrimSpace(input.RefundTicketMerchantExternalID)
	input.EffectID = strings.TrimSpace(input.EffectID)
	if input.EffectID == "" {
		input.EffectID = input.RefundTicketMerchantExternalID
	}
	input.RelatedEffectID = strings.TrimSpace(input.RelatedEffectID)
	input.ProviderObjectType = strings.ToLower(strings.TrimSpace(input.ProviderObjectType))
	input.ProviderTradeNo = strings.TrimSpace(input.ProviderTradeNo)
	input.OrderTradeNo = strings.TrimSpace(input.OrderTradeNo)
	input.OrderKind = strings.ToLower(strings.TrimSpace(input.OrderKind))
	input.EventType = strings.ToLower(strings.TrimSpace(input.EventType))
	input.RefundStatus = strings.ToLower(strings.TrimSpace(input.RefundStatus))
	input.Amount = strings.TrimSpace(input.Amount)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.ForceManualReason, err = normalizeProviderRefundForceManualReason(input.ForceManualReason)
	if err != nil {
		return result, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Provider == "" || input.DeliveryID == "" || input.EventType == "" {
		return result, ErrProviderRefundInvalid
	}
	switch input.EventType {
	case "refund.succeeded", "refund.failed", "refund.observed", "dispute.funds_withdrawn", "dispute.funds_reinstated":
	default:
		return result, ErrProviderRefundInvalid
	}
	if input.EffectID == "" {
		return result, ErrProviderRefundInvalid
	}
	if input.ForceManualReason != "" {
		switch input.ForceManualReason {
		case ProviderRefundManualReasonPartialRefundUnsupported:
			if input.EventType != "refund.succeeded" && input.EventType != "dispute.funds_withdrawn" {
				return result, ErrProviderRefundInvalid
			}
		case ProviderRefundManualReasonNonTerminal:
			if input.EventType != "refund.observed" {
				return result, ErrProviderRefundInvalid
			}
		case ProviderRefundManualReasonPaymentBindingMissing, ProviderRefundManualReasonPaymentBindingConflict:
			if input.EventType != "refund.succeeded" && input.EventType != "dispute.funds_withdrawn" && input.EventType != "dispute.funds_reinstated" {
				return result, ErrProviderRefundInvalid
			}
		default:
			return result, ErrProviderRefundInvalid
		}
	}
	if input.RefundStatus != "" {
		if input.EventType == "refund.succeeded" && input.RefundStatus != "succeeded" {
			return result, ErrProviderRefundInvalid
		}
		if input.EventType == "refund.failed" && input.RefundStatus != "failed" {
			return result, ErrProviderRefundInvalid
		}
		if strings.HasPrefix(input.EventType, "dispute.") {
			return result, ErrProviderRefundInvalid
		}
	}
	if input.EventType == "refund.observed" && (input.RefundStatus == "" || input.ForceManualReason != ProviderRefundManualReasonNonTerminal) {
		return result, ErrProviderRefundInvalid
	}
	if len(input.Provider) > 50 || len(input.ProviderAccountID) > 255 || len(input.ProviderEnvironment) > 32 ||
		len(input.DeliveryID) > 255 || len(input.BusinessEventID) > 255 || len(input.RefundTicketMerchantExternalID) > 128 ||
		len(input.EffectID) > 255 ||
		len(input.RelatedEffectID) > 255 || len(input.ProviderObjectType) > 32 || len(input.ProviderTradeNo) > 255 || len(input.OrderTradeNo) > 255 ||
		len(input.OrderKind) > 32 || len(input.EventType) > 64 || len(input.RefundStatus) > 32 ||
		len(input.Amount) > 64 || len(input.Currency) > 16 {
		return result, ErrProviderRefundInvalid
	}
	if DB == nil {
		return result, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}

	eventKey := ProviderRefundEventScopedKey(input.Provider, input.ProviderAccountID, input.ProviderEnvironment, input.DeliveryID)
	var billingSpec BillingOperationSpec
	var billingApplied bool
	var refreshGroupUserID int
	err = DB.Transaction(func(tx *gorm.DB) error {
		now := common.GetTimestamp()
		candidate := ProviderRefundEvent{
			EventKey:                       eventKey,
			Provider:                       input.Provider,
			ProviderAccountID:              input.ProviderAccountID,
			ProviderEnvironment:            input.ProviderEnvironment,
			DeliveryID:                     input.DeliveryID,
			BusinessEventID:                input.BusinessEventID,
			RefundTicketMerchantExternalID: input.RefundTicketMerchantExternalID,
			EffectID:                       input.EffectID,
			RelatedEffectID:                input.RelatedEffectID,
			ProviderObjectType:             input.ProviderObjectType,
			ProviderTradeNo:                input.ProviderTradeNo,
			EventType:                      input.EventType,
			RefundStatus:                   input.RefundStatus,
			OrderTradeNo:                   input.OrderTradeNo,
			OrderKind:                      input.OrderKind,
			Amount:                         input.Amount,
			Currency:                       input.Currency,
			Status:                         ProviderRefundEventProcessing,
			Reason:                         input.Reason,
			Payload:                        input.Payload,
			CreatedAt:                      now,
			UpdatedAt:                      now,
		}
		// Insert before taking a locking read. On InnoDB, SELECT ... FOR UPDATE
		// against a missing unique key takes a gap lock; two first deliveries can
		// then deadlock when both try to insert. The unique key is the concurrency
		// fence, and the current winner is locked and validated immediately below.
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			return err
		}
		var ledger ProviderRefundEvent
		if err := lockForUpdate(tx).Where("event_key = ?", eventKey).First(&ledger).Error; err != nil {
			return err
		}

		if ledger.Provider != input.Provider || ledger.ProviderAccountID != input.ProviderAccountID ||
			ledger.ProviderEnvironment != input.ProviderEnvironment || ledger.DeliveryID != input.DeliveryID ||
			strings.TrimSpace(ledger.RefundTicketMerchantExternalID) != input.RefundTicketMerchantExternalID {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.EventType) != "" && ledger.EventType != input.EventType {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.RefundStatus) != "" && ledger.RefundStatus != input.RefundStatus {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.BusinessEventID) != "" && input.BusinessEventID != "" && ledger.BusinessEventID != input.BusinessEventID {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.OrderKind) != "" && input.OrderKind != "" && ledger.OrderKind != input.OrderKind {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.OrderTradeNo) != "" && input.OrderTradeNo != "" && ledger.OrderTradeNo != input.OrderTradeNo {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.EffectID) != "" && ledger.EffectID != input.EffectID {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.RelatedEffectID) != "" && ledger.RelatedEffectID != input.RelatedEffectID {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.ProviderObjectType) != "" && input.ProviderObjectType != "" && ledger.ProviderObjectType != input.ProviderObjectType {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.ProviderTradeNo) != "" && input.ProviderTradeNo != "" && ledger.ProviderTradeNo != input.ProviderTradeNo {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.Amount) != "" && input.Amount != "" && !compareProviderAmount(ledger.Amount, input.Amount) {
			return ErrProviderRefundConflict
		}
		if strings.TrimSpace(ledger.Currency) != "" && input.Currency != "" && !strings.EqualFold(ledger.Currency, input.Currency) {
			return ErrProviderRefundConflict
		}
		if isProviderRefundTerminal(ledger.Status) {
			result = ProviderRefundEventResult{
				Status:           ledger.Status,
				OrderTradeNo:     ledger.OrderTradeNo,
				OrderKind:        ledger.OrderKind,
				AlreadyProcessed: true,
			}
			return nil
		}

		// Keep the first verified delivery's identity immutable, while allowing
		// a retry from a partial/old deployment to fill diagnostics that were not
		// available on the original attempt.
		if ledger.BusinessEventID == "" {
			ledger.BusinessEventID = input.BusinessEventID
		}
		if ledger.EventType == "" {
			ledger.EventType = input.EventType
		}
		if ledger.RefundStatus == "" {
			ledger.RefundStatus = input.RefundStatus
		}
		if ledger.EffectID == "" {
			ledger.EffectID = input.EffectID
		}
		if ledger.RelatedEffectID == "" {
			ledger.RelatedEffectID = input.RelatedEffectID
		}
		if ledger.ProviderObjectType == "" {
			ledger.ProviderObjectType = input.ProviderObjectType
		}
		if ledger.ProviderTradeNo == "" {
			ledger.ProviderTradeNo = input.ProviderTradeNo
		}
		if ledger.OrderTradeNo == "" {
			ledger.OrderTradeNo = input.OrderTradeNo
		}
		if ledger.OrderKind == "" {
			ledger.OrderKind = input.OrderKind
		}
		if ledger.Amount == "" {
			ledger.Amount = input.Amount
		}
		if ledger.Currency == "" {
			ledger.Currency = input.Currency
		}
		if ledger.Reason == "" {
			ledger.Reason = input.Reason
		}
		if ledger.Payload == "" {
			ledger.Payload = input.Payload
		}

		if input.EventType == "refund.failed" {
			ledger.Status = ProviderRefundEventRefundRequired
			ledger.DecisionReason = "provider_refund_failed"
			ledger.UpdatedAt = common.GetTimestamp()
			if err := tx.Save(&ledger).Error; err != nil {
				return err
			}
			result = ProviderRefundEventResult{Status: ledger.Status, OrderTradeNo: ledger.OrderTradeNo, OrderKind: ledger.OrderKind}
			return nil
		}

		if input.ForceManualReason != "" {
			ledger.Status = ProviderRefundEventManualReconciliation
			ledger.DecisionReason = input.ForceManualReason
			ledger.UpdatedAt = common.GetTimestamp()
			if err := tx.Save(&ledger).Error; err != nil {
				return err
			}
			result = ProviderRefundEventResult{Status: ledger.Status, OrderTradeNo: ledger.OrderTradeNo, OrderKind: ledger.OrderKind}
			return nil
		}

		if input.ProviderAccountID == "" || input.ProviderEnvironment == "" || input.Amount == "" || input.Currency == "" ||
			input.OrderTradeNo == "" || input.OrderKind == "" || input.ProviderTradeNo == "" {
			ledger.Status = ProviderRefundEventManualReconciliation
			ledger.DecisionReason = "missing_reversal_evidence"
			ledger.UpdatedAt = common.GetTimestamp()
			if err := tx.Save(&ledger).Error; err != nil {
				return err
			}
			result = ProviderRefundEventResult{Status: ledger.Status, OrderTradeNo: ledger.OrderTradeNo, OrderKind: ledger.OrderKind}
			return nil
		}

		if input.ProviderObjectType != "" {
			binding, bindingErr := findProviderPaymentBindingTx(
				tx,
				input.Provider,
				input.ProviderAccountID,
				input.ProviderEnvironment,
				input.ProviderObjectType,
				input.ProviderTradeNo,
			)
			decisionReason := ""
			switch {
			case errors.Is(bindingErr, ErrProviderPaymentBindingNotFound):
				decisionReason = ProviderRefundManualReasonPaymentBindingMissing
			case errors.Is(bindingErr, ErrProviderPaymentBindingConflict):
				decisionReason = ProviderRefundManualReasonPaymentBindingConflict
			case bindingErr != nil:
				return bindingErr
			case !providerPaymentBindingOwns(binding, input.OrderTradeNo, input.OrderKind):
				decisionReason = ProviderRefundManualReasonPaymentBindingConflict
			}
			if decisionReason != "" {
				ledger.Status = ProviderRefundEventManualReconciliation
				ledger.DecisionReason = decisionReason
				ledger.UpdatedAt = common.GetTimestamp()
				if err := tx.Save(&ledger).Error; err != nil {
					return err
				}
				result = ProviderRefundEventResult{Status: ledger.Status, OrderTradeNo: ledger.OrderTradeNo, OrderKind: ledger.OrderKind}
				return nil
			}
		}

		decision, processErr := processProviderReversalTx(tx, input)
		if processErr != nil {
			return processErr
		}
		ledger.EffectKey = decision.EffectKey
		ledger.WalletDelta = decision.WalletDelta
		ledger.Status = decision.Status
		ledger.DecisionReason = decision.Reason
		ledger.UpdatedAt = common.GetTimestamp()
		if err := tx.Save(&ledger).Error; err != nil {
			return err
		}
		result = ProviderRefundEventResult{
			Status:           ledger.Status,
			OrderTradeNo:     ledger.OrderTradeNo,
			OrderKind:        ledger.OrderKind,
			WalletDelta:      decision.WalletDelta,
			AlreadyProcessed: decision.AlreadyProcessed,
		}
		billingSpec = decision.BillingSpec
		billingApplied = decision.BillingApplied
		refreshGroupUserID = decision.RefreshGroupUserID
		return nil
	})
	if err != nil {
		return result, err
	}
	if billingSpec.RequestID != "" {
		syncBillingOperationCaches(billingSpec, billingApplied)
	}
	if refreshGroupUserID > 0 {
		refreshSubscriptionUserGroupCache(refreshGroupUserID, "provider refund entitlement revoke")
	}
	return result, nil
}
