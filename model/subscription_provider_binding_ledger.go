package model

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	subscriptionProviderBindingIdentityIndexName    = "idx_spb_identity"
	subscriptionProviderBindingEntitlementIndexName = "idx_spb_entitlement"
	subscriptionProviderBindingOrderIndexName       = "idx_spb_order"
)

// SubscriptionProviderBindingRecord is the durable identity ledger for a
// recurring provider subscription.  ProviderSubscriptionKey is a SHA-256
// digest of provider + NUL + provider_subscription_id and is the actual
// database uniqueness fence.  Keeping the raw values alongside the digest
// allows collision detection and operator diagnostics without relying on a
// potentially oversized MySQL utf8mb4 composite index.
type SubscriptionProviderBindingRecord struct {
	Id int `json:"id" gorm:"primaryKey"`

	UserSubscriptionID int `json:"user_subscription_id" gorm:"not null;uniqueIndex:idx_spb_entitlement"`
	// A legacy entitlement may not have a recoverable checkout order.  A NULL
	// value is therefore intentional; all three supported engines permit
	// multiple NULLs under a unique index, while verified checkout rows use a
	// concrete trade number and are one-to-one with their order.
	OrderTradeNo            *string `json:"order_trade_no" gorm:"type:varchar(255);uniqueIndex:idx_spb_order"`
	Provider                string  `json:"provider" gorm:"type:varchar(50);not null;index:idx_spb_provider_id,priority:1"`
	ProviderSubscriptionID  string  `json:"provider_subscription_id" gorm:"type:varchar(255);not null;index:idx_spb_provider_id,priority:2"`
	ProviderSubscriptionKey string  `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_spb_identity"`
	CreatedAt               int64   `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt               int64   `json:"updated_at" gorm:"not null;default:0"`
}

func (SubscriptionProviderBindingRecord) TableName() string {
	return "subscription_provider_bindings"
}

// SubscriptionProviderBindingKey returns the bounded canonical key used by
// the unique identity index. Provider names are case-insensitive in the
// application; provider subscription IDs remain case-sensitive.
func SubscriptionProviderBindingKey(provider, providerSubscriptionID string) (string, bool) {
	provider = normalizeSubscriptionBindingProvider(provider)
	providerSubscriptionID = strings.TrimSpace(providerSubscriptionID)
	if provider == "" || providerSubscriptionID == "" || len(provider) > 50 || len(providerSubscriptionID) > 255 {
		return "", false
	}
	data := append([]byte(provider), 0)
	data = append(data, []byte(providerSubscriptionID)...)
	return hex.EncodeToString(common.Sha256Raw(data)), true
}

func normalizeSubscriptionBindingProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// BeforeSave only maintains the derived key and canonical formatting. It is
// deliberately not the concurrency guard; bindSubscriptionProviderIDLedgerTx
// performs a conflict-safe insert and locked re-read inside its transaction.
func (b *SubscriptionProviderBindingRecord) BeforeSave(tx *gorm.DB) error {
	if b == nil {
		return nil
	}
	b.Provider = normalizeSubscriptionBindingProvider(b.Provider)
	b.ProviderSubscriptionID = strings.TrimSpace(b.ProviderSubscriptionID)
	if b.Provider == "" || b.ProviderSubscriptionID == "" || b.UserSubscriptionID <= 0 {
		return ErrProviderSettlementInvalid
	}
	key, ok := SubscriptionProviderBindingKey(b.Provider, b.ProviderSubscriptionID)
	if !ok {
		return ErrProviderSettlementInvalid
	}
	b.ProviderSubscriptionKey = key
	if b.OrderTradeNo != nil {
		tradeNo := strings.TrimSpace(*b.OrderTradeNo)
		if tradeNo == "" {
			b.OrderTradeNo = nil
		} else if len(tradeNo) > 255 {
			return ErrProviderSettlementInvalid
		} else {
			b.OrderTradeNo = &tradeNo
		}
	}
	if b.CreatedAt == 0 {
		b.CreatedAt = common.GetTimestamp()
	}
	b.UpdatedAt = common.GetTimestamp()
	return nil
}

// bindSubscriptionProviderIDLedgerTx is the sole write primitive for a
// recurring identity. The unique ledger indexes serialize two concurrent
// requests even when they lock different orders/entitlements. After the
// conflict-safe insert, a locking read validates that the winner belongs to
// this exact order and entitlement.
func bindSubscriptionProviderIDLedgerTx(tx *gorm.DB, order *SubscriptionOrder, sub *UserSubscription, providerSubscriptionID, provider string) error {
	if tx == nil || order == nil {
		return ErrProviderSettlementInvalid
	}
	return bindSubscriptionProviderIDLedgerValuesTx(tx, order, sub, providerSubscriptionID, provider)
}

// bindSubscriptionProviderIDLegacyLedgerTx adopts a legacy entitlement whose
// checkout order is unavailable.  The entitlement primary key and provider
// identity are still fenced by the ledger; OrderTradeNo remains NULL because
// inventing a local order reference would make reconciliation ambiguous.
func bindSubscriptionProviderIDLegacyLedgerTx(tx *gorm.DB, sub *UserSubscription, providerSubscriptionID, provider string) error {
	if tx == nil || sub == nil {
		return ErrProviderSettlementInvalid
	}
	return bindSubscriptionProviderIDLedgerValuesTx(tx, nil, sub, providerSubscriptionID, provider)
}

func bindSubscriptionProviderIDLedgerValuesTx(tx *gorm.DB, order *SubscriptionOrder, sub *UserSubscription, providerSubscriptionID, provider string) error {
	if tx == nil || sub == nil || sub.Id <= 0 {
		return ErrProviderSettlementInvalid
	}
	provider = normalizeSubscriptionBindingProvider(provider)
	providerSubscriptionID = strings.TrimSpace(providerSubscriptionID)
	tradeNo := ""
	if order != nil {
		tradeNo = strings.TrimSpace(order.TradeNo)
	}
	if provider == "" || providerSubscriptionID == "" || (order != nil && tradeNo == "") {
		return ErrProviderSettlementInvalid
	}
	if order != nil && order.PaymentProvider != "" && normalizeSubscriptionBindingProvider(order.PaymentProvider) != provider {
		return ErrPaymentMethodMismatch
	}
	if order != nil {
		// The order-to-entitlement link is an accounting identity, not merely a
		// convenient lookup.  Legacy/manual rows can contain the same trade
		// number while belonging to another user or plan; binding a provider
		// object to such a row would let lifecycle events mutate the wrong
		// customer's entitlement.  Fail closed before touching either row.
		if sub.UserId != order.UserId || sub.PlanId != order.PlanId {
			return ErrProviderEventConflict
		}
	}
	if order != nil {
		if current := strings.TrimSpace(order.ProviderSubscriptionID); current != "" && current != providerSubscriptionID {
			return ErrProviderEventConflict
		}
	}
	if current := strings.TrimSpace(sub.ProviderSubscriptionID); current != "" && current != providerSubscriptionID {
		return ErrProviderEventConflict
	}
	if currentProvider := normalizeSubscriptionBindingProvider(sub.ProviderSubscriptionProvider); currentProvider != "" && currentProvider != provider {
		return ErrProviderEventConflict
	}
	key, ok := SubscriptionProviderBindingKey(provider, providerSubscriptionID)
	if !ok {
		return ErrProviderSettlementInvalid
	}

	// Legacy rows may predate the ledger. Check them as an additional guard;
	// migration backfills every unambiguous provider+ID row, while this keeps a
	// manually-created legacy collision fail-closed.
	var rawMatches []UserSubscription
	if err := lockForUpdate(tx).
		Where("provider_subscription_id = ?", providerSubscriptionID).
		Find(&rawMatches).Error; err != nil {
		return err
	}
	// If the candidate itself has no provider namespace, any other row carrying
	// the same opaque ID makes ownership unknowable—even when that other row is
	// explicitly namespaced to a different provider.  Do not let a Stripe event
	// adopt a legacy row that could in fact belong to Creem (or vice versa).
	// An order-linked bind has an authenticated provider namespace from the
	// checkout itself, even when the denormalized entitlement column is still
	// empty.  Only the order-less legacy adoption path lacks that proof and must
	// reject every competing raw row.
	legacyCandidate := order == nil && normalizeSubscriptionBindingProvider(sub.ProviderSubscriptionProvider) == ""
	for _, candidate := range rawMatches {
		if candidate.Id == sub.Id {
			continue
		}
		candidateProvider := normalizeSubscriptionBindingProvider(candidate.ProviderSubscriptionProvider)
		if legacyCandidate || candidateProvider == "" || candidateProvider == provider {
			return ErrProviderEventConflict
		}
	}

	var tradeNoPtr *string
	if tradeNo != "" {
		tradeNoCopy := tradeNo
		tradeNoPtr = &tradeNoCopy
	}
	candidate := &SubscriptionProviderBindingRecord{
		UserSubscriptionID:      sub.Id,
		OrderTradeNo:            tradeNoPtr,
		Provider:                provider,
		ProviderSubscriptionID:  providerSubscriptionID,
		ProviderSubscriptionKey: key,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(candidate).Error; err != nil {
		return err
	}

	// On MySQL's default REPEATABLE READ a plain read can retain a snapshot from
	// before a concurrent insert. FOR UPDATE forces a current read after the
	// conflict-safe insert; SQLite omits the unsupported clause via the shared
	// lockForUpdate helper.
	var bound SubscriptionProviderBindingRecord
	if err := lockForUpdate(tx).
		Where("provider_subscription_key = ?", key).
		First(&bound).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrProviderEventConflict
		}
		return err
	}
	boundTradeNo := ""
	if bound.OrderTradeNo != nil {
		boundTradeNo = strings.TrimSpace(*bound.OrderTradeNo)
	}
	if bound.UserSubscriptionID != sub.Id || boundTradeNo != tradeNo ||
		normalizeSubscriptionBindingProvider(bound.Provider) != provider ||
		strings.TrimSpace(bound.ProviderSubscriptionID) != providerSubscriptionID {
		// This also catches an (astronomically unlikely) digest collision.
		return ErrProviderEventConflict
	}

	updates := map[string]interface{}{
		"provider_subscription_id":       providerSubscriptionID,
		"provider_subscription_provider": provider,
	}
	if err := tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(updates).Error; err != nil {
		return err
	}
	sub.ProviderSubscriptionID = providerSubscriptionID
	sub.ProviderSubscriptionProvider = provider
	if order != nil && strings.TrimSpace(order.ProviderSubscriptionID) == "" {
		if err := tx.Model(&SubscriptionOrder{}).Where("id = ?", order.Id).
			Update("provider_subscription_id", providerSubscriptionID).Error; err != nil {
			return err
		}
		order.ProviderSubscriptionID = providerSubscriptionID
	}
	return nil
}

// findSubscriptionProviderBindingTx resolves a provider identity through the
// durable ledger. Callers can then load the entitlement by its primary key.
func findSubscriptionProviderBindingTx(tx *gorm.DB, provider, providerSubscriptionID string) (*SubscriptionProviderBindingRecord, error) {
	_, err := findSubscriptionProviderBindingSnapshotTx(tx, provider, providerSubscriptionID)
	if err != nil {
		return nil, err
	}
	key, _ := SubscriptionProviderBindingKey(provider, providerSubscriptionID)
	// Callers of this legacy helper expect the returned identity to remain
	// stable for the rest of their transaction. Keep the locking behaviour here
	// for compatibility; lifecycle resolution uses the snapshot variant below
	// so it can acquire the canonical order -> user -> subscription -> binding
	// lock sequence without taking the binding lock first.
	var locked SubscriptionProviderBindingRecord
	if err := lockForUpdate(tx).Where("provider_subscription_key = ?", key).First(&locked).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSubscriptionOrderNotFound
		}
		return nil, err
	}
	if normalizeSubscriptionBindingProvider(locked.Provider) != normalizeSubscriptionBindingProvider(provider) ||
		strings.TrimSpace(locked.ProviderSubscriptionID) != strings.TrimSpace(providerSubscriptionID) {
		return nil, ErrProviderEventConflict
	}
	return &locked, nil
}

// findSubscriptionProviderBindingSnapshotTx performs an intentionally
// non-locking identity lookup. It is used only as the first step of lifecycle
// resolution, where the caller must discover the linked order/user before
// taking row locks. The identity is re-read with FOR UPDATE immediately after
// the owner rows are locked, so this snapshot is never trusted as the final
// source of truth.
func findSubscriptionProviderBindingSnapshotTx(tx *gorm.DB, provider, providerSubscriptionID string) (*SubscriptionProviderBindingRecord, error) {
	if tx == nil {
		return nil, ErrProviderSettlementInvalid
	}
	provider = normalizeSubscriptionBindingProvider(provider)
	providerSubscriptionID = strings.TrimSpace(providerSubscriptionID)
	key, ok := SubscriptionProviderBindingKey(provider, providerSubscriptionID)
	if !ok {
		return nil, ErrProviderSettlementInvalid
	}
	var binding SubscriptionProviderBindingRecord
	if err := tx.Where("provider_subscription_key = ?", key).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSubscriptionOrderNotFound
		}
		return nil, err
	}
	if normalizeSubscriptionBindingProvider(binding.Provider) != provider || strings.TrimSpace(binding.ProviderSubscriptionID) != providerSubscriptionID {
		return nil, ErrProviderEventConflict
	}
	return &binding, nil
}

func lockSubscriptionProviderBindingByKeyTx(tx *gorm.DB, key string) (*SubscriptionProviderBindingRecord, error) {
	if tx == nil || strings.TrimSpace(key) == "" {
		return nil, ErrProviderSettlementInvalid
	}
	var binding SubscriptionProviderBindingRecord
	if err := lockForUpdate(tx).Where("provider_subscription_key = ?", key).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProviderEventConflict
		}
		return nil, err
	}
	return &binding, nil
}

// validateSubscriptionProviderBindingLinkTx verifies the denormalized rows
// behind a ledger entry before a lifecycle event is allowed to mutate them.
// The binding table intentionally has no foreign keys (legacy databases and
// the three supported dialects do not share the same migration guarantees), so
// a stale/manual row can otherwise look valid by digest alone.  A missing or
// mismatched entitlement/order is an accounting conflict and must fail closed.
func validateSubscriptionProviderBindingLinkTx(tx *gorm.DB, binding *SubscriptionProviderBindingRecord, sub *UserSubscription, provider, providerSubscriptionID string) error {
	if tx == nil || binding == nil || sub == nil {
		return ErrProviderSettlementInvalid
	}
	provider = normalizeSubscriptionBindingProvider(provider)
	providerSubscriptionID = strings.TrimSpace(providerSubscriptionID)
	if provider == "" || providerSubscriptionID == "" || binding.UserSubscriptionID != sub.Id ||
		normalizeSubscriptionBindingProvider(binding.Provider) != provider ||
		strings.TrimSpace(binding.ProviderSubscriptionID) != providerSubscriptionID ||
		strings.TrimSpace(sub.ProviderSubscriptionID) != providerSubscriptionID ||
		normalizeSubscriptionBindingProvider(sub.ProviderSubscriptionProvider) != provider {
		return ErrProviderEventConflict
	}

	bindingTradeNo := ""
	if binding.OrderTradeNo != nil {
		bindingTradeNo = strings.TrimSpace(*binding.OrderTradeNo)
		if bindingTradeNo == "" {
			return ErrProviderEventConflict
		}
	}
	subTradeNo := strings.TrimSpace(sub.SubscriptionOrderTradeNo)
	// A ledger row linked to an order must agree with the entitlement's own
	// order link, and vice versa.  One-sided links are stale/manual data and are
	// not safe evidence for a recurring callback.
	if bindingTradeNo != subTradeNo {
		return ErrProviderEventConflict
	}
	if bindingTradeNo == "" {
		return nil
	}

	var order SubscriptionOrder
	if err := tx.Where("trade_no = ?", bindingTradeNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrProviderEventConflict
		}
		return err
	}
	if order.UserId != sub.UserId || order.PlanId != sub.PlanId ||
		normalizeSubscriptionBindingProvider(order.PaymentProvider) != provider {
		return ErrProviderEventConflict
	}
	// A recurring lifecycle callback is valid only for an order that was
	// already settled.  An order row in pending/failed/paid_uncredited state is
	// not proof that this entitlement was purchased, even when a stale ledger
	// pointer happens to reference it.
	if order.Status != common.TopUpStatusSuccess {
		return ErrProviderEventConflict
	}
	// Older successful orders may not have persisted the recurring ID.  If the
	// field is present, however, a disagreement proves that this ledger row is
	// attached to the wrong checkout and cannot be used.
	if orderProviderID := strings.TrimSpace(order.ProviderSubscriptionID); orderProviderID != "" && orderProviderID != providerSubscriptionID {
		return ErrProviderEventConflict
	}
	return nil
}

// resolveSubscriptionProviderEntitlementTx resolves lifecycle callbacks with
// provider isolation. New rows use the ledger's digest key; legacy rows are
// accepted only when the denormalized provider namespace matches (or is still
// empty and there is exactly one row). A provider object from gateway A can
// therefore never select a gateway B entitlement just because their opaque IDs
// happen to be equal.
func resolveSubscriptionProviderEntitlementTx(tx *gorm.DB, provider, providerSubscriptionID string) (*UserSubscription, error) {
	if tx == nil {
		return nil, ErrProviderSettlementInvalid
	}
	provider = normalizeSubscriptionBindingProvider(provider)
	providerSubscriptionID = strings.TrimSpace(providerSubscriptionID)
	if provider == "" || providerSubscriptionID == "" {
		return nil, ErrProviderSettlementInvalid
	}
	bindingKey, keyOK := SubscriptionProviderBindingKey(provider, providerSubscriptionID)
	if !keyOK {
		return nil, ErrProviderSettlementInvalid
	}
	if binding, err := findSubscriptionProviderBindingSnapshotTx(tx, provider, providerSubscriptionID); err == nil {
		// The initial binding lookup above is only an identity hint. Establish the
		// owner lock order before taking the binding lock: order (when present),
		// user, subscription, then binding. This matches checkout completion and
		// prevents a lifecycle callback from deadlocking a concurrent payment
		// settlement that already owns the order row.
		if binding.OrderTradeNo != nil && strings.TrimSpace(*binding.OrderTradeNo) != "" {
			var order SubscriptionOrder
			tradeNo := strings.TrimSpace(*binding.OrderTradeNo)
			if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, ErrProviderEventConflict
				}
				return nil, err
			}
			if order.UserId > 0 {
				var owner User
				if err := lockForUpdate(tx).Select("id").Where("id = ?", order.UserId).First(&owner).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, err
				}
			}
		} else {
			// Legacy rows may not retain a checkout order. Read the owner id only
			// to establish the same user fence when that row still exists.
			var ownerHint UserSubscription
			if err := tx.Select("user_id").Where("id = ?", binding.UserSubscriptionID).First(&ownerHint).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, ErrProviderEventConflict
				}
				return nil, err
			}
			if ownerHint.UserId > 0 {
				var owner User
				if err := lockForUpdate(tx).Select("id").Where("id = ?", ownerHint.UserId).First(&owner).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, err
				}
			}
		}

		var sub UserSubscription
		if err := lockForUpdate(tx).Where("id = ?", binding.UserSubscriptionID).First(&sub).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrProviderEventConflict
			}
			return nil, err
		}
		lockedBinding, err := lockSubscriptionProviderBindingByKeyTx(tx, bindingKey)
		if err != nil {
			return nil, err
		}
		binding = lockedBinding
		if binding.UserSubscriptionID != sub.Id ||
			normalizeSubscriptionBindingProvider(binding.Provider) != provider ||
			strings.TrimSpace(binding.ProviderSubscriptionID) != providerSubscriptionID {
			return nil, ErrProviderEventConflict
		}
		if strings.TrimSpace(sub.ProviderSubscriptionID) != providerSubscriptionID {
			return nil, ErrProviderEventConflict
		}
		if err := validateSubscriptionProviderBindingLinkTx(tx, binding, &sub, provider, providerSubscriptionID); err != nil {
			return nil, err
		}
		// A duplicate raw row means the denormalized columns no longer agree
		// with the ledger. Do not silently choose the ledger winner.
		var rawMatches []UserSubscription
		if err := lockForUpdate(tx).
			Where("provider_subscription_id = ? AND (LOWER(TRIM(provider_subscription_provider)) = ? OR provider_subscription_provider IS NULL OR TRIM(provider_subscription_provider) = '')", providerSubscriptionID, provider).
			Find(&rawMatches).Error; err != nil {
			return nil, err
		}
		if len(rawMatches) != 1 || rawMatches[0].Id != sub.Id {
			return nil, ErrProviderEventConflict
		}
		return &sub, nil
	} else if !errors.Is(err, ErrSubscriptionOrderNotFound) {
		return nil, err
	}

	// Legacy fallback: provider-scoped query first, then a single unnamespaced
	// row can be adopted by a verified lifecycle event. Multiple candidates are
	// always ambiguous and remain blocked.
	var matches []UserSubscription
	if err := lockForUpdate(tx).
		Where("provider_subscription_id = ? AND (LOWER(TRIM(provider_subscription_provider)) = ? OR provider_subscription_provider IS NULL OR TRIM(provider_subscription_provider) = '')", providerSubscriptionID, provider).
		Find(&matches).Error; err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, ErrSubscriptionOrderNotFound
	}
	if len(matches) > 1 {
		return nil, ErrProviderEventConflict
	}
	// This is the only compatibility path for a legacy row whose provider
	// namespace was empty (or whose ledger row was not backfilled). The row is
	// adopted only when it is the single provider-ID candidate; the ledger then
	// supplies the durable uniqueness fence before the lifecycle caller mutates
	// state. We deliberately do not infer a user/plan or invent an order link.
	if err := bindSubscriptionProviderIDLegacyLedgerTx(tx, &matches[0], providerSubscriptionID, provider); err != nil {
		return nil, err
	}
	return &matches[0], nil
}
