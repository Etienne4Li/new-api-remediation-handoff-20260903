package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ProviderPaymentObjectCheckoutSession = "checkout_session"
	ProviderPaymentObjectPaymentIntent   = "payment_intent"
	ProviderPaymentObjectCharge          = "charge"
	ProviderPaymentObjectInvoice         = "invoice"
	ProviderPaymentObjectSubscription    = "subscription"
	ProviderPaymentObjectAcquiringOrder  = "acquiring_order"
	ProviderPaymentObjectOrder           = "order"
	ProviderPaymentObjectTransaction     = "transaction"
)

var (
	ErrProviderPaymentBindingInvalid  = errors.New("invalid provider payment binding")
	ErrProviderPaymentBindingConflict = errors.New("provider payment binding conflicts with existing order")
	ErrProviderPaymentBindingNotFound = errors.New("provider payment binding not found")
)

// ProviderPaymentBinding maps one provider object to the local order credited
// by that payment. Provider account and environment are part of the identity:
// provider object IDs from two accounts or from test/live mode are unrelated.
type ProviderPaymentBinding struct {
	ID int64 `json:"id" gorm:"primaryKey"`

	BindingKey          string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_provider_payment_binding_key"`
	Provider            string `json:"provider" gorm:"type:varchar(50);not null"`
	ProviderAccountID   string `json:"provider_account_id" gorm:"type:varchar(255);not null"`
	ProviderEnvironment string `json:"provider_environment" gorm:"type:varchar(32);not null"`
	ObjectType          string `json:"object_type" gorm:"type:varchar(32);not null"`
	ObjectID            string `json:"object_id" gorm:"type:varchar(255);not null"`
	OrderTradeNo        string `json:"order_trade_no" gorm:"type:varchar(255);not null;index:idx_provider_payment_binding_order,priority:1,length:159"`
	OrderKind           string `json:"order_kind" gorm:"type:varchar(32);not null;index:idx_provider_payment_binding_order,priority:2"`
	CreatedAt           int64  `json:"created_at" gorm:"not null"`
	UpdatedAt           int64  `json:"updated_at" gorm:"not null"`
}

func (ProviderPaymentBinding) TableName() string {
	return "provider_payment_bindings"
}

// ProviderPaymentObject is one alias exposed by a verified provider payment.
// Examples include a Checkout Session, PaymentIntent, Charge, or Invoice.
type ProviderPaymentObject struct {
	ObjectType string
	ObjectID   string
}

// ProviderPaymentBindingBatch binds every object in one verified payment to
// the same local order. The write helper is transaction-scoped so settlement
// and all aliases either commit together or all roll back.
type ProviderPaymentBindingBatch struct {
	Provider            string
	ProviderAccountID   string
	ProviderEnvironment string
	OrderTradeNo        string
	OrderKind           string
	Objects             []ProviderPaymentObject
}

// ProviderPaymentBindingScope carries the provider-owned dimensions of a
// positive recurring payment. The subscription lifecycle transaction derives
// the local order only after resolving and locking the exact entitlement.
type ProviderPaymentBindingScope struct {
	ProviderAccountID   string
	ProviderEnvironment string
	Objects             []ProviderPaymentObject
}

// ProviderLifecycleScope identifies the provider account and environment that
// delivered a recurring lifecycle event. The lifecycle transaction uses the
// immutable Subscription alias created at checkout to prove that the event
// belongs to the exact local subscription order before changing entitlement.
type ProviderLifecycleScope struct {
	ProviderAccountID   string
	ProviderEnvironment string
}

func normalizeProviderPaymentBindingValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeProviderPaymentBindingBatch(batch ProviderPaymentBindingBatch) ProviderPaymentBindingBatch {
	batch.Provider = normalizeProviderPaymentBindingValue(batch.Provider)
	batch.ProviderAccountID = strings.TrimSpace(batch.ProviderAccountID)
	batch.ProviderEnvironment = normalizeProviderPaymentBindingValue(batch.ProviderEnvironment)
	batch.OrderTradeNo = strings.TrimSpace(batch.OrderTradeNo)
	batch.OrderKind = normalizeProviderPaymentBindingValue(batch.OrderKind)
	for i := range batch.Objects {
		batch.Objects[i].ObjectType = normalizeProviderPaymentBindingValue(batch.Objects[i].ObjectType)
		batch.Objects[i].ObjectID = strings.TrimSpace(batch.Objects[i].ObjectID)
	}
	return batch
}

func validProviderPaymentIdentity(provider, providerAccountID, providerEnvironment, objectType, objectID string) bool {
	return validProviderPaymentScope(provider, providerAccountID, providerEnvironment) &&
		objectType != "" && len(objectType) <= 32 &&
		objectID != "" && len(objectID) <= 255
}

func validProviderPaymentScope(provider, providerAccountID, providerEnvironment string) bool {
	return provider != "" && len(provider) <= 50 &&
		providerAccountID != "" && len(providerAccountID) <= 255 &&
		providerEnvironment != "" && len(providerEnvironment) <= 32
}

func validProviderPaymentOwner(orderTradeNo, orderKind string) bool {
	return orderTradeNo != "" && len(orderTradeNo) <= 255 &&
		orderKind != "" && len(orderKind) <= 32
}

func validateProviderPaymentBindingScope(provider, providerAccountID, providerEnvironment string, objects []ProviderPaymentObject) error {
	provider = normalizeProviderPaymentBindingValue(provider)
	providerAccountID = strings.TrimSpace(providerAccountID)
	providerEnvironment = normalizeProviderPaymentBindingValue(providerEnvironment)
	if len(objects) == 0 {
		return ErrProviderPaymentBindingInvalid
	}
	for _, object := range objects {
		objectType := normalizeProviderPaymentBindingValue(object.ObjectType)
		objectID := strings.TrimSpace(object.ObjectID)
		if !validProviderPaymentIdentity(provider, providerAccountID, providerEnvironment, objectType, objectID) {
			return ErrProviderPaymentBindingInvalid
		}
	}
	return nil
}

func validateProviderPaymentBindingBatch(batch ProviderPaymentBindingBatch) error {
	batch = normalizeProviderPaymentBindingBatch(batch)
	if !validProviderPaymentOwner(batch.OrderTradeNo, batch.OrderKind) {
		return ErrProviderPaymentBindingInvalid
	}
	return validateProviderPaymentBindingScope(batch.Provider, batch.ProviderAccountID, batch.ProviderEnvironment, batch.Objects)
}

// ProviderPaymentBindingKey returns the bounded identity key used by the
// database uniqueness fence. Length prefixes keep the digest unambiguous even
// if a future provider permits separator bytes inside an identifier.
func ProviderPaymentBindingKey(provider, providerAccountID, providerEnvironment, objectType, objectID string) (string, bool) {
	provider = normalizeProviderPaymentBindingValue(provider)
	providerAccountID = strings.TrimSpace(providerAccountID)
	providerEnvironment = normalizeProviderPaymentBindingValue(providerEnvironment)
	objectType = normalizeProviderPaymentBindingValue(objectType)
	objectID = strings.TrimSpace(objectID)
	if !validProviderPaymentIdentity(provider, providerAccountID, providerEnvironment, objectType, objectID) {
		return "", false
	}
	hash := sha256.New()
	for _, part := range []string{provider, providerAccountID, providerEnvironment, objectType, objectID} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(part)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil)), true
}

// ProviderPaymentScopeFingerprint is the immutable, non-secret snapshot of a
// provider account and environment stored on an order before redirecting to
// checkout. It lets settlement reject a test/live or account switch before
// any new aliases are attached, without adding provider-specific columns.
func ProviderPaymentScopeFingerprint(provider, providerAccountID, providerEnvironment string) (string, bool) {
	provider = normalizeProviderPaymentBindingValue(provider)
	providerAccountID = strings.TrimSpace(providerAccountID)
	providerEnvironment = normalizeProviderPaymentBindingValue(providerEnvironment)
	if !validProviderPaymentScope(provider, providerAccountID, providerEnvironment) {
		return "", false
	}
	hash := sha256.New()
	for _, part := range []string{"provider-payment-scope-v1", provider, providerAccountID, providerEnvironment} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(part)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil)), true
}

// BeforeSave keeps direct GORM writes canonical. Concurrency and ownership are
// enforced by bindProviderPaymentBindingsTx, not by this hook.
func (binding *ProviderPaymentBinding) BeforeSave(tx *gorm.DB) error {
	if binding == nil {
		return nil
	}
	binding.Provider = normalizeProviderPaymentBindingValue(binding.Provider)
	binding.ProviderAccountID = strings.TrimSpace(binding.ProviderAccountID)
	binding.ProviderEnvironment = normalizeProviderPaymentBindingValue(binding.ProviderEnvironment)
	binding.ObjectType = normalizeProviderPaymentBindingValue(binding.ObjectType)
	binding.ObjectID = strings.TrimSpace(binding.ObjectID)
	binding.OrderTradeNo = strings.TrimSpace(binding.OrderTradeNo)
	binding.OrderKind = normalizeProviderPaymentBindingValue(binding.OrderKind)
	if !validProviderPaymentIdentity(
		binding.Provider,
		binding.ProviderAccountID,
		binding.ProviderEnvironment,
		binding.ObjectType,
		binding.ObjectID,
	) || !validProviderPaymentOwner(binding.OrderTradeNo, binding.OrderKind) {
		return ErrProviderPaymentBindingInvalid
	}
	key, ok := ProviderPaymentBindingKey(binding.Provider, binding.ProviderAccountID, binding.ProviderEnvironment, binding.ObjectType, binding.ObjectID)
	if !ok {
		return ErrProviderPaymentBindingInvalid
	}
	binding.BindingKey = key
	now := common.GetTimestamp()
	if binding.CreatedAt == 0 {
		binding.CreatedAt = now
	}
	binding.UpdatedAt = now
	return nil
}

// Provider payment identities are append-only. Ownership changes must create a
// conflict in the binding primitive rather than rewriting audit history.
func (binding *ProviderPaymentBinding) BeforeUpdate(tx *gorm.DB) error {
	return ErrProviderPaymentBindingConflict
}

// BindProviderPaymentBindings is the standalone transaction wrapper. Positive
// settlement paths use bindProviderPaymentBindingsTx directly so the credit,
// provider event fence, and aliases share one commit.
func BindProviderPaymentBindings(batch ProviderPaymentBindingBatch) error {
	if DB == nil {
		return fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, batch)
	})
}

func bindProviderPaymentBindingsTx(tx *gorm.DB, batch ProviderPaymentBindingBatch) error {
	if tx == nil {
		return ErrProviderPaymentBindingInvalid
	}
	batch = normalizeProviderPaymentBindingBatch(batch)
	if err := validateProviderPaymentBindingBatch(batch); err != nil {
		return err
	}

	seen := make(map[string]ProviderPaymentObject, len(batch.Objects))
	for _, object := range batch.Objects {
		if !validProviderPaymentIdentity(
			batch.Provider,
			batch.ProviderAccountID,
			batch.ProviderEnvironment,
			object.ObjectType,
			object.ObjectID,
		) {
			return ErrProviderPaymentBindingInvalid
		}
		key, ok := ProviderPaymentBindingKey(batch.Provider, batch.ProviderAccountID, batch.ProviderEnvironment, object.ObjectType, object.ObjectID)
		if !ok {
			return ErrProviderPaymentBindingInvalid
		}
		if previous, duplicate := seen[key]; duplicate {
			if previous.ObjectType != object.ObjectType || previous.ObjectID != object.ObjectID {
				return ErrProviderPaymentBindingConflict
			}
			continue
		}
		seen[key] = object
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		object := seen[key]
		candidate := ProviderPaymentBinding{
			BindingKey:          key,
			Provider:            batch.Provider,
			ProviderAccountID:   batch.ProviderAccountID,
			ProviderEnvironment: batch.ProviderEnvironment,
			ObjectType:          object.ObjectType,
			ObjectID:            object.ObjectID,
			OrderTradeNo:        batch.OrderTradeNo,
			OrderKind:           batch.OrderKind,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			return err
		}

		var existing ProviderPaymentBinding
		if err := lockForUpdate(tx).Where("binding_key = ?", key).First(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrProviderPaymentBindingConflict
			}
			return err
		}
		if existing.Provider != batch.Provider ||
			existing.ProviderAccountID != batch.ProviderAccountID ||
			existing.ProviderEnvironment != batch.ProviderEnvironment ||
			existing.ObjectType != object.ObjectType ||
			existing.ObjectID != object.ObjectID ||
			existing.OrderTradeNo != batch.OrderTradeNo ||
			existing.OrderKind != batch.OrderKind {
			return ErrProviderPaymentBindingConflict
		}
	}
	return nil
}

func providerPaymentBindingOwns(binding *ProviderPaymentBinding, orderTradeNo, orderKind string) bool {
	return binding != nil && binding.OrderTradeNo == strings.TrimSpace(orderTradeNo) &&
		binding.OrderKind == normalizeProviderPaymentBindingValue(orderKind)
}

func bindProviderSettlementPaymentObjectsTx(tx *gorm.DB, settlement ProviderSettlement, orderKind string) error {
	if len(settlement.PaymentObjects) == 0 {
		return nil
	}
	return bindProviderPaymentBindingsTx(tx, ProviderPaymentBindingBatch{
		Provider:            settlement.Provider,
		ProviderAccountID:   settlement.ProviderAccountID,
		ProviderEnvironment: settlement.ProviderEnvironment,
		OrderTradeNo:        settlement.OrderTradeNo,
		OrderKind:           orderKind,
		Objects:             settlement.PaymentObjects,
	})
}

// FindProviderPaymentBinding resolves only an exact scoped provider identity.
// Missing legacy aliases return ErrProviderPaymentBindingNotFound so callers
// can route the event to manual reconciliation without guessing from an order.
func FindProviderPaymentBinding(provider, providerAccountID, providerEnvironment, objectType, objectID string) (*ProviderPaymentBinding, error) {
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	return findProviderPaymentBindingTx(DB, provider, providerAccountID, providerEnvironment, objectType, objectID)
}

func findProviderPaymentBindingTx(tx *gorm.DB, provider, providerAccountID, providerEnvironment, objectType, objectID string) (*ProviderPaymentBinding, error) {
	if tx == nil {
		return nil, ErrProviderPaymentBindingInvalid
	}
	provider = normalizeProviderPaymentBindingValue(provider)
	providerAccountID = strings.TrimSpace(providerAccountID)
	providerEnvironment = normalizeProviderPaymentBindingValue(providerEnvironment)
	objectType = normalizeProviderPaymentBindingValue(objectType)
	objectID = strings.TrimSpace(objectID)
	key, ok := ProviderPaymentBindingKey(provider, providerAccountID, providerEnvironment, objectType, objectID)
	if !ok {
		return nil, ErrProviderPaymentBindingInvalid
	}
	var binding ProviderPaymentBinding
	if err := tx.Where("binding_key = ?", key).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProviderPaymentBindingNotFound
		}
		return nil, err
	}
	if binding.Provider != provider || binding.ProviderAccountID != providerAccountID ||
		binding.ProviderEnvironment != providerEnvironment || binding.ObjectType != objectType || binding.ObjectID != objectID {
		return nil, ErrProviderPaymentBindingConflict
	}
	return &binding, nil
}
