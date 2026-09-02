package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// SubscriptionProviderBinding is the minimum authenticated identity needed to
// attach a recurring provider object to the entitlement created by one local
// checkout.  The order trade number is deliberately required: looking up an
// entitlement by user/plan (or by provider id alone) is ambiguous when a user
// has purchased the same plan more than once.
type SubscriptionProviderBinding struct {
	OrderTradeNo           string
	Provider               string
	ProviderSubscriptionID string
	// ProductID/Currency/Amount are optional provider-side assertions.  When a
	// webhook carries them, they are checked against the immutable checkout
	// snapshot before the ID is bound.  Empty values are allowed for lifecycle
	// status events that do not expand the product, but never make the local
	// snapshot optional.
	ProductID string
	Currency  string
	Amount    string
}

// BindSubscriptionProviderIDToOrder binds a provider subscription ID to the
// exact successful order and entitlement identified by OrderTradeNo.
//
// This is intentionally separate from the lifecycle state transition.  Creem
// may deliver checkout.completed without an expanded subscription object and
// reveal the ID only on a later lifecycle event.  The later event can safely
// repair that identity only when the signed payload also carries the original
// local reference; it must never guess among same-user/same-plan rows.
//
// The function is idempotent for the same provider/id pair.  It fails closed
// when the order snapshot is incomplete, the order is not settled, the
// reference is not linked to exactly one entitlement, or the provider ID is
// already attached to another entitlement.
func BindSubscriptionProviderIDToOrder(binding SubscriptionProviderBinding) error {
	binding.OrderTradeNo = strings.TrimSpace(binding.OrderTradeNo)
	binding.Provider = normalizeSubscriptionBindingProvider(binding.Provider)
	binding.ProviderSubscriptionID = strings.TrimSpace(binding.ProviderSubscriptionID)
	binding.ProductID = strings.TrimSpace(binding.ProductID)
	binding.Currency = strings.ToUpper(strings.TrimSpace(binding.Currency))
	binding.Amount = strings.TrimSpace(binding.Amount)
	if binding.OrderTradeNo == "" || binding.Provider == "" || binding.ProviderSubscriptionID == "" {
		return ErrProviderSettlementInvalid
	}
	if DB == nil {
		return errors.Join(ErrDatabase, errors.New("database is not initialized"))
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).
			Where("trade_no = ?", binding.OrderTradeNo).
			First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if normalizeSubscriptionBindingProvider(order.PaymentProvider) != binding.Provider {
			return ErrPaymentMethodMismatch
		}
		if order.Status != common.TopUpStatusSuccess {
			// A pending/failed/paid_uncredited order has no safe entitlement to
			// bind.  In particular, never let a lifecycle callback turn a paid
			// but uncredited payment into a new entitlement by attaching an ID.
			return ErrSubscriptionOrderStatusInvalid
		}
		if strings.TrimSpace(order.ProviderAmount) == "" ||
			strings.TrimSpace(order.ProviderCurrency) == "" ||
			strings.TrimSpace(order.ProviderProductID) == "" ||
			strings.TrimSpace(order.ProviderMerchantID) == "" {
			return ErrProviderSnapshotMissing
		}
		if binding.ProductID != "" && binding.ProductID != strings.TrimSpace(order.ProviderProductID) {
			return ErrProviderSnapshotMismatch
		}
		if binding.Currency != "" && binding.Currency != strings.ToUpper(strings.TrimSpace(order.ProviderCurrency)) {
			return ErrProviderSnapshotMismatch
		}
		if binding.Amount != "" && !compareProviderAmount(binding.Amount, order.ProviderAmount) {
			return ErrProviderSnapshotMismatch
		}
		if existing := strings.TrimSpace(order.ProviderSubscriptionID); existing != "" && existing != binding.ProviderSubscriptionID {
			return ErrProviderEventConflict
		}

		// The order-to-entitlement link is the only safe way to identify the row
		// that belongs to this checkout.  A missing link is a legacy/data-repair
		// case, not permission to choose an arbitrary row.
		var linked []UserSubscription
		if err := lockForUpdate(tx).
			Where("subscription_order_trade_no = ?", order.TradeNo).
			Find(&linked).Error; err != nil {
			return err
		}
		if len(linked) == 0 {
			return ErrSubscriptionOrderNotFound
		}
		if len(linked) > 1 {
			return ErrProviderEventConflict
		}
		sub := linked[0]
		if current := strings.TrimSpace(sub.ProviderSubscriptionID); current != "" && current != binding.ProviderSubscriptionID {
			return ErrProviderEventConflict
		}
		if boundProvider := normalizeSubscriptionBindingProvider(sub.ProviderSubscriptionProvider); boundProvider != "" && boundProvider != binding.Provider {
			return ErrProviderEventConflict
		}

		// The durable ledger insert is the concurrency fence.  It also updates
		// the denormalized columns in this same transaction so old readers remain
		// compatible while lifecycle readers migrate to the ledger.
		return bindSubscriptionProviderIDLedgerTx(tx, &order, &sub, binding.ProviderSubscriptionID, binding.Provider)
	})
}
