package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ProviderSettlement is the small, verified settlement envelope shared by
// payment adapters. Adapters must populate it only after authenticating the
// provider payload (signature/API lookup, mode, and schema checks).
//
// ProviderTradeNo identifies the provider payment/order object. ProviderEventID
// identifies the webhook delivery/event; when a provider has no separate event
// identifier, callers may leave it blank and the transaction id is used as the
// idempotency key.
type ProviderSettlement struct {
	OrderTradeNo           string
	Provider               string
	ProviderTradeNo        string
	ProviderEventID        string
	ProviderAccountID      string
	ProviderEnvironment    string
	ProviderKeyFingerprint string
	MerchantID             string
	ProductID              string
	StoreID                string
	CheckoutID             string
	// ProviderSubscriptionID is optional because some historical/provider
	// payloads omit the expanded subscription object. When present it is bound
	// to the local order and never used by itself to grant entitlement.
	ProviderSubscriptionID string
	Currency               string
	Amount                 string
	OrderName              string
	CustomerID             string
	Payload                string
	PaymentObjects         []ProviderPaymentObject
}

var (
	ErrProviderSettlementInvalid = errors.New("invalid provider settlement")
	ErrProviderSnapshotMissing   = errors.New("provider order snapshot missing")
	ErrProviderSnapshotMismatch  = errors.New("provider settlement does not match order snapshot")
	ErrProviderEventConflict     = errors.New("provider event conflicts with settled order")
)

// providerSettlementUsesScopedIdentity reports whether a provider callback
// must carry an immutable account/environment namespace.  EPay has its own
// signing-key fingerprint and callback validation path; balance/internal
// settlements never come through this provider webhook primitive.  Every
// hosted provider that does use SettleTopUpProvider (including Pancake, whose
// webhook does not expose PaymentObjects) must still be fenced by this scope.
func providerSettlementUsesScopedIdentity(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case PaymentProviderEpay, PaymentProviderBalance, "":
		return false
	default:
		return true
	}
}

// validateProviderSettlementScope compares the provider account/environment
// carried by a verified callback with the immutable scope fingerprint frozen
// on the local order.  ProviderKeyFingerprint is deliberately reused here as
// the non-secret scope digest for all hosted providers; EPay remains on its
// dedicated signing-key path above.  Empty fingerprints fail closed so a
// legacy order can never be claimed by whichever environment is configured
// when its callback arrives.
func validateProviderSettlementScope(settlement ProviderSettlement, frozenFingerprint string) error {
	if !providerSettlementUsesScopedIdentity(settlement.Provider) {
		return nil
	}
	settlement = settlement.normalized()
	frozenFingerprint = strings.TrimSpace(frozenFingerprint)
	if settlement.ProviderAccountID == "" || settlement.ProviderEnvironment == "" || settlement.ProviderKeyFingerprint == "" || frozenFingerprint == "" {
		return ErrProviderSnapshotMissing
	}
	expected, ok := ProviderPaymentScopeFingerprint(settlement.Provider, settlement.ProviderAccountID, settlement.ProviderEnvironment)
	if !ok || settlement.ProviderKeyFingerprint != expected || frozenFingerprint != expected {
		return ErrProviderSnapshotMismatch
	}
	return nil
}

// ensureProviderTradeNoAvailableTx closes the gap left by historical rows
// whose provider_trade_no was written without a PaymentEvent (Pancake used to
// have no PaymentObjects, and older deployments could lose the event row).
// The target order is already locked by its caller; lock matching rows here so
// a stale provider transaction cannot be claimed by a different local order.
// The same trade_no is allowed in the opposite ledger because subscription
// completion intentionally creates a zero-quota TopUp mirror for history.
func ensureProviderTradeNoAvailableTx(tx *gorm.DB, provider, providerTradeNo, orderTradeNo string) error {
	if tx == nil {
		return ErrProviderSettlementInvalid
	}
	provider = strings.TrimSpace(provider)
	providerTradeNo = strings.TrimSpace(providerTradeNo)
	orderTradeNo = strings.TrimSpace(orderTradeNo)
	if provider == "" || providerTradeNo == "" || orderTradeNo == "" {
		return ErrProviderSettlementInvalid
	}
	var topUps []TopUp
	if err := lockForUpdate(tx).
		Select("id", "trade_no").
		Where("payment_provider = ? AND provider_trade_no = ? AND trade_no <> ?", provider, providerTradeNo, orderTradeNo).
		Find(&topUps).Error; err != nil {
		return err
	}
	if len(topUps) > 0 {
		return ErrProviderEventConflict
	}
	var subscriptionOrders []SubscriptionOrder
	if err := lockForUpdate(tx).
		Select("id", "trade_no").
		Where("payment_provider = ? AND provider_trade_no = ? AND trade_no <> ?", provider, providerTradeNo, orderTradeNo).
		Find(&subscriptionOrders).Error; err != nil {
		return err
	}
	if len(subscriptionOrders) > 0 {
		return ErrProviderEventConflict
	}
	return nil
}

func (s ProviderSettlement) normalized() ProviderSettlement {
	s.OrderTradeNo = strings.TrimSpace(s.OrderTradeNo)
	s.Provider = strings.TrimSpace(s.Provider)
	s.ProviderTradeNo = strings.TrimSpace(s.ProviderTradeNo)
	s.ProviderEventID = strings.TrimSpace(s.ProviderEventID)
	s.ProviderAccountID = strings.TrimSpace(s.ProviderAccountID)
	s.ProviderEnvironment = strings.ToLower(strings.TrimSpace(s.ProviderEnvironment))
	s.ProviderKeyFingerprint = strings.TrimSpace(s.ProviderKeyFingerprint)
	s.MerchantID = strings.TrimSpace(s.MerchantID)
	s.ProductID = strings.TrimSpace(s.ProductID)
	s.StoreID = strings.TrimSpace(s.StoreID)
	s.CheckoutID = strings.TrimSpace(s.CheckoutID)
	s.ProviderSubscriptionID = strings.TrimSpace(s.ProviderSubscriptionID)
	s.Currency = strings.ToUpper(strings.TrimSpace(s.Currency))
	s.Amount = strings.TrimSpace(s.Amount)
	s.OrderName = strings.TrimSpace(s.OrderName)
	s.CustomerID = strings.TrimSpace(s.CustomerID)
	if len(s.PaymentObjects) > 0 {
		s.PaymentObjects = append([]ProviderPaymentObject(nil), s.PaymentObjects...)
		for i := range s.PaymentObjects {
			s.PaymentObjects[i].ObjectType = strings.ToLower(strings.TrimSpace(s.PaymentObjects[i].ObjectType))
			s.PaymentObjects[i].ObjectID = strings.TrimSpace(s.PaymentObjects[i].ObjectID)
		}
	}
	return s
}

func (s ProviderSettlement) validate() error {
	s = s.normalized()
	if s.OrderTradeNo == "" || s.Provider == "" || s.ProviderTradeNo == "" || s.Amount == "" || s.Currency == "" {
		return ErrProviderSettlementInvalid
	}
	if strings.EqualFold(s.Provider, PaymentProviderStripe) && len(s.PaymentObjects) == 0 {
		return ErrProviderPaymentBindingInvalid
	}
	if len(s.PaymentObjects) > 0 {
		if err := validateProviderPaymentBindingScope(s.Provider, s.ProviderAccountID, s.ProviderEnvironment, s.PaymentObjects); err != nil {
			return err
		}
	}
	amount, err := decimal.NewFromString(s.Amount)
	if err != nil || !amount.IsPositive() || !amount.Equal(amount.Round(8)) {
		return ErrProviderSettlementInvalid
	}
	return nil
}

// ValidateProviderSettlement exposes envelope validation for HTTP adapters and
// tests without exposing the internal normalization details.
func ValidateProviderSettlement(settlement ProviderSettlement) error {
	return settlement.validate()
}

func compareProviderAmount(expected, actual string) bool {
	expectedDecimal, expectedErr := decimal.NewFromString(strings.TrimSpace(expected))
	actualDecimal, actualErr := decimal.NewFromString(strings.TrimSpace(actual))
	if expectedErr != nil || actualErr != nil {
		return false
	}
	return expectedDecimal.Equal(actualDecimal)
}

func validateStripeCheckoutSnapshot(checkoutID, providerScopeFingerprint string, settlement ProviderSettlement) error {
	checkoutID = strings.TrimSpace(checkoutID)
	if checkoutID == "" {
		return ErrProviderSnapshotMissing
	}
	if settlement.CheckoutID == "" || checkoutID != settlement.CheckoutID {
		return ErrProviderSnapshotMismatch
	}
	providerScopeFingerprint = strings.TrimSpace(providerScopeFingerprint)
	if providerScopeFingerprint == "" {
		return ErrProviderSnapshotMissing
	}
	expectedFingerprint, ok := ProviderPaymentScopeFingerprint(settlement.Provider, settlement.ProviderAccountID, settlement.ProviderEnvironment)
	if !ok || settlement.ProviderKeyFingerprint == "" || settlement.ProviderKeyFingerprint != expectedFingerprint || providerScopeFingerprint != expectedFingerprint {
		return ErrProviderSnapshotMismatch
	}
	for _, object := range settlement.PaymentObjects {
		if object.ObjectType == ProviderPaymentObjectCheckoutSession && object.ObjectID == checkoutID {
			return nil
		}
	}
	return ErrProviderPaymentBindingInvalid
}

// validateTopUpProviderSnapshot compares every field that was frozen before a
// checkout was handed to the provider. Empty snapshot fields are rejected for
// externally-triggered settlements; allowing a callback to fill them would
// make the callback itself the source of truth.
func validateTopUpProviderSnapshot(topUp *TopUp, settlement ProviderSettlement) error {
	if topUp == nil {
		return ErrTopUpNotFound
	}
	settlement = settlement.normalized()
	if topUp.PaymentProvider != settlement.Provider {
		return ErrPaymentMethodMismatch
	}
	if err := validateProviderSettlementScope(settlement, topUp.ProviderKeyFingerprint); err != nil {
		return err
	}
	if topUp.CreditedQuota <= 0 || strings.TrimSpace(topUp.ProviderAmount) == "" || strings.TrimSpace(topUp.ProviderCurrency) == "" {
		return ErrProviderSnapshotMissing
	}
	if strings.TrimSpace(topUp.ProviderProductID) == "" || strings.TrimSpace(topUp.ProviderMerchantID) == "" {
		return ErrProviderSnapshotMissing
	}
	if !compareProviderAmount(topUp.ProviderAmount, settlement.Amount) {
		return ErrProviderSnapshotMismatch
	}
	if strings.ToUpper(strings.TrimSpace(topUp.ProviderCurrency)) != settlement.Currency {
		return ErrProviderSnapshotMismatch
	}
	if strings.TrimSpace(topUp.ProviderProductID) != settlement.ProductID || strings.TrimSpace(topUp.ProviderMerchantID) != settlement.MerchantID {
		return ErrProviderSnapshotMismatch
	}
	if len(settlement.PaymentObjects) > 0 && strings.TrimSpace(topUp.ProviderMerchantID) != settlement.ProviderAccountID {
		return ErrProviderSnapshotMismatch
	}
	if strings.TrimSpace(topUp.ProviderStoreID) != "" && strings.TrimSpace(topUp.ProviderStoreID) != settlement.StoreID {
		return ErrProviderSnapshotMismatch
	}
	// Some providers (notably Waffo Pancake) do not echo the checkout session
	// in a signed webhook. Compare it only when the adapter has independently
	// received a provider-side checkout identifier; a local-only session ID is
	// still retained on the order for reconciliation.
	if settlement.Provider == PaymentProviderStripe {
		if err := validateStripeCheckoutSnapshot(topUp.ProviderCheckoutID, topUp.ProviderKeyFingerprint, settlement); err != nil {
			return err
		}
	} else if settlement.Provider != PaymentProviderWaffoPancake && strings.TrimSpace(topUp.ProviderCheckoutID) != "" && strings.TrimSpace(topUp.ProviderCheckoutID) != settlement.CheckoutID {
		return ErrProviderSnapshotMismatch
	}
	if strings.TrimSpace(topUp.ProviderOrderName) != "" && topUp.ProviderOrderName != settlement.OrderName {
		return ErrProviderSnapshotMismatch
	}
	if topUp.ProviderTradeNo != nil && strings.TrimSpace(*topUp.ProviderTradeNo) != settlement.ProviderTradeNo {
		return ErrProviderEventConflict
	}
	if strings.TrimSpace(topUp.ProviderSubscriptionID) != "" && settlement.ProviderSubscriptionID != "" &&
		strings.TrimSpace(topUp.ProviderSubscriptionID) != settlement.ProviderSubscriptionID {
		return ErrProviderEventConflict
	}
	if strings.TrimSpace(topUp.ProviderEventID) != "" && strings.TrimSpace(topUp.ProviderEventID) != settlement.ProviderEventID {
		// A provider may emit a second event for the same payment (for example a
		// delayed payment confirmation). The transaction identity still binds it;
		// do not reject a legitimate second delivery solely because its event id
		// differs. Conflicting transaction ids remain rejected above.
	}
	return nil
}

// TopUpSettlementResult contains the outcome needed by an adapter for logging
// and response handling. No provider-controlled values are exposed to users.
type TopUpSettlementResult struct {
	AlreadyCompleted bool
	PaidUncredited   bool
	UserID           int
	CreditedQuota    int
	Provider         string
}

// SettleTopUpProvider atomically settles a verified provider callback. The
// caller must pass a complete immutable snapshot; legacy rows fail closed.
// userUpdates is optional and is applied in the same transaction (for example,
// Stripe's customer id or Creem's first-use email).
func SettleTopUpProvider(settlement ProviderSettlement, callerIP string, userUpdates map[string]interface{}) (result TopUpSettlementResult, err error) {
	settlement = settlement.normalized()
	if err := settlement.validate(); err != nil {
		return result, err
	}
	if userUpdates == nil {
		userUpdates = map[string]interface{}{}
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	var topUp TopUp
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", settlement.OrderTradeNo).First(&topUp).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			// Do not collapse a driver/connection/transaction error into
			// ErrTopUpNotFound. Webhook adapters classify a genuine missing order
			// as permanent, while infrastructure failures must remain retryable.
			return err
		}
		if topUp.PaymentProvider != settlement.Provider {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == common.TopUpStatusSuccess {
			// A replayed callback must be checked against the same immutable
			// checkout snapshot as the first delivery.  Comparing only the
			// provider transaction id leaves a gap where an attacker (or a
			// malformed provider retry) can reuse that id with a different
			// amount/product/currency and still reach the idempotent branch.
			if err := validateTopUpProviderSnapshot(&topUp, settlement); err != nil {
				return err
			}
			if err := ensureProviderTradeNoAvailableTx(tx, settlement.Provider, settlement.ProviderTradeNo, settlement.OrderTradeNo); err != nil {
				return err
			}
			if topUp.ProviderTradeNo == nil || strings.TrimSpace(*topUp.ProviderTradeNo) != settlement.ProviderTradeNo {
				return ErrProviderEventConflict
			}
			if strings.TrimSpace(topUp.ProviderSubscriptionID) != "" && settlement.ProviderSubscriptionID != "" &&
				strings.TrimSpace(topUp.ProviderSubscriptionID) != settlement.ProviderSubscriptionID {
				return ErrProviderEventConflict
			}
			if strings.TrimSpace(topUp.ProviderSubscriptionID) == "" && settlement.ProviderSubscriptionID != "" {
				if err := tx.Model(&TopUp{}).Where("id = ?", topUp.Id).Update("provider_subscription_id", settlement.ProviderSubscriptionID).Error; err != nil {
					return err
				}
			}
			result = TopUpSettlementResult{AlreadyCompleted: true, UserID: topUp.UserId, CreditedQuota: topUp.CreditedQuota, Provider: topUp.PaymentProvider}
			// Re-bind the same transaction id so an operator can repair a missing
			// event row left by an older deployment without crediting again.
			if err := bindPaymentEventTx(tx, settlement.Provider, settlement.ProviderTradeNo, settlement.OrderTradeNo, PaymentEventOrderTopUp, settlement.Payload); err != nil {
				return err
			}
			return bindProviderSettlementPaymentObjectsTx(tx, settlement, PaymentEventOrderTopUp)
		}
		if isTopUpPaidUncredited(&topUp) {
			if err := validateTopUpProviderSnapshot(&topUp, settlement); err != nil {
				return err
			}
			if err := ensureProviderTradeNoAvailableTx(tx, settlement.Provider, settlement.ProviderTradeNo, settlement.OrderTradeNo); err != nil {
				return err
			}
			if topUp.ProviderTradeNo == nil || strings.TrimSpace(*topUp.ProviderTradeNo) != settlement.ProviderTradeNo {
				return ErrProviderEventConflict
			}
			if strings.TrimSpace(topUp.ProviderSubscriptionID) != "" && settlement.ProviderSubscriptionID != "" &&
				strings.TrimSpace(topUp.ProviderSubscriptionID) != settlement.ProviderSubscriptionID {
				return ErrProviderEventConflict
			}
			if strings.TrimSpace(topUp.ProviderSubscriptionID) == "" && settlement.ProviderSubscriptionID != "" {
				if err := tx.Model(&TopUp{}).Where("id = ?", topUp.Id).Update("provider_subscription_id", settlement.ProviderSubscriptionID).Error; err != nil {
					return err
				}
			}
			if err := bindPaymentEventTx(tx, settlement.Provider, settlement.ProviderTradeNo, settlement.OrderTradeNo, PaymentEventOrderTopUp, settlement.Payload); err != nil {
				return err
			}
			if err := bindProviderSettlementPaymentObjectsTx(tx, settlement, PaymentEventOrderTopUp); err != nil {
				return err
			}
			result = TopUpSettlementResult{PaidUncredited: true, UserID: topUp.UserId, CreditedQuota: topUp.CreditedQuota, Provider: topUp.PaymentProvider}
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		if err := validateTopUpProviderSnapshot(&topUp, settlement); err != nil {
			return err
		}
		if err := ensureProviderTradeNoAvailableTx(tx, settlement.Provider, settlement.ProviderTradeNo, settlement.OrderTradeNo); err != nil {
			return err
		}
		if err := bindPaymentEventTx(tx, settlement.Provider, settlement.ProviderTradeNo, settlement.OrderTradeNo, PaymentEventOrderTopUp, settlement.Payload); err != nil {
			return err
		}
		if err := bindProviderSettlementPaymentObjectsTx(tx, settlement, PaymentEventOrderTopUp); err != nil {
			return err
		}
		providerTradeNo := settlement.ProviderTradeNo
		topUp.ProviderTradeNo = &providerTradeNo
		if topUp.ProviderEventID == "" {
			topUp.ProviderEventID = firstNonEmptyProviderValue(settlement.ProviderEventID, settlement.ProviderTradeNo)
		}
		if topUp.ProviderSubscriptionID == "" {
			topUp.ProviderSubscriptionID = settlement.ProviderSubscriptionID
		}
		if topUp.ProviderPayload == "" && settlement.Payload != "" {
			topUp.ProviderPayload = settlement.Payload
		}
		// Persist the authenticated payment fact before attempting the wallet
		// mutation. A deterministic ceiling/user failure becomes a durable
		// paid_uncredited order; infrastructure errors still abort this
		// transaction and leave the order pending for provider retry.
		markTopUpPaidUncredited(&topUp, "")
		if err := tx.Save(&topUp).Error; err != nil {
			return err
		}
		result = TopUpSettlementResult{PaidUncredited: true, UserID: topUp.UserId, CreditedQuota: topUp.CreditedQuota, Provider: topUp.PaymentProvider}
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrTopUpNotFound) && !errors.Is(err, ErrPaymentMethodMismatch) && !errors.Is(err, ErrTopUpStatusInvalid) && !errors.Is(err, ErrProviderSnapshotMissing) && !errors.Is(err, ErrProviderSnapshotMismatch) && !errors.Is(err, ErrProviderEventConflict) {
			common.SysError(fmt.Sprintf("provider topup settlement failed provider=%s trade_no=%s error=%v", settlement.Provider, settlement.OrderTradeNo, err))
		}
		return result, err
	}
	if result.PaidUncredited {
		// The evidence/state transaction has committed. A separate retry attempt
		// may therefore fail without losing the authenticated payment; the
		// operator endpoint can retry it later and the provider callback can be
		// acknowledged safely.
		attempt, retryErr := retryPaidUncreditedTopUpWithUpdates(settlement.OrderTradeNo, userUpdates)
		if retryErr != nil {
			common.SysError(fmt.Sprintf("provider topup credit retry failed provider=%s trade_no=%s provider_trade_no=%s error=%v", settlement.Provider, settlement.OrderTradeNo, settlement.ProviderTradeNo, retryErr))
			return result, nil
		}
		if attempt.Credited {
			result.PaidUncredited = false
			result.AlreadyCompleted = false
			result.UserID = attempt.UserID
			result.CreditedQuota = attempt.CreditedQuota
			syncCreditUserQuotaCache(attempt.UserID, attempt.CreditedQuota, settlement.Provider+" topup")
			RecordTopupLog(attempt.UserID, fmt.Sprintf("使用在线充值成功，充值额度: %v，支付金额: %s", logger.FormatQuota(attempt.CreditedQuota), settlement.Amount), callerIP, "", settlement.Provider)
			return result, nil
		}
		common.SysError(fmt.Sprintf("provider topup payment authenticated but remains paid_uncredited provider=%s trade_no=%s provider_trade_no=%s error_code=%s", settlement.Provider, settlement.OrderTradeNo, settlement.ProviderTradeNo, attempt.PendingCode))
		return result, nil
	}
	if !result.AlreadyCompleted {
		syncCreditUserQuotaCache(result.UserID, result.CreditedQuota, settlement.Provider+" topup")
		RecordTopupLog(result.UserID, fmt.Sprintf("使用在线充值成功，充值额度: %v，支付金额: %s", logger.FormatQuota(result.CreditedQuota), settlement.Amount), callerIP, "", settlement.Provider)
	}
	return result, nil
}

func firstNonEmptyProviderValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ValidateSubscriptionProviderSnapshot performs the same strict comparison
// for subscription orders. Entitlement fields are checked by the subscription
// completion transaction; this helper covers provider-facing fields.
func ValidateSubscriptionProviderSnapshot(order *SubscriptionOrder, settlement ProviderSettlement) error {
	if order == nil {
		return ErrSubscriptionOrderNotFound
	}
	settlement = settlement.normalized()
	if order.PaymentProvider != settlement.Provider {
		return ErrPaymentMethodMismatch
	}
	if err := validateProviderSettlementScope(settlement, order.ProviderKeyFingerprint); err != nil {
		return err
	}
	if strings.TrimSpace(order.ProviderAmount) == "" || strings.TrimSpace(order.ProviderCurrency) == "" || strings.TrimSpace(order.ProviderProductID) == "" || strings.TrimSpace(order.ProviderMerchantID) == "" {
		return ErrProviderSnapshotMissing
	}
	if !compareProviderAmount(order.ProviderAmount, settlement.Amount) || strings.ToUpper(strings.TrimSpace(order.ProviderCurrency)) != settlement.Currency || strings.TrimSpace(order.ProviderProductID) != settlement.ProductID || strings.TrimSpace(order.ProviderMerchantID) != settlement.MerchantID {
		return ErrProviderSnapshotMismatch
	}
	if len(settlement.PaymentObjects) > 0 && strings.TrimSpace(order.ProviderMerchantID) != settlement.ProviderAccountID {
		return ErrProviderSnapshotMismatch
	}
	if strings.TrimSpace(order.ProviderStoreID) != "" && strings.TrimSpace(order.ProviderStoreID) != settlement.StoreID {
		return ErrProviderSnapshotMismatch
	}
	if settlement.Provider == PaymentProviderStripe {
		if err := validateStripeCheckoutSnapshot(order.ProviderCheckoutID, order.ProviderKeyFingerprint, settlement); err != nil {
			return err
		}
	} else if settlement.Provider != PaymentProviderWaffoPancake && strings.TrimSpace(order.ProviderCheckoutID) != "" && strings.TrimSpace(order.ProviderCheckoutID) != settlement.CheckoutID {
		return ErrProviderSnapshotMismatch
	}
	if strings.TrimSpace(order.ProviderOrderName) != "" && order.ProviderOrderName != settlement.OrderName {
		return ErrProviderSnapshotMismatch
	}
	if order.ProviderTradeNo != nil && strings.TrimSpace(*order.ProviderTradeNo) != settlement.ProviderTradeNo {
		return ErrProviderEventConflict
	}
	if strings.TrimSpace(order.ProviderSubscriptionID) != "" && settlement.ProviderSubscriptionID != "" &&
		strings.TrimSpace(order.ProviderSubscriptionID) != settlement.ProviderSubscriptionID {
		return ErrProviderEventConflict
	}
	return nil
}
