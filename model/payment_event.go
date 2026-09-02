package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PaymentEvent records the first accepted provider transaction. It is shared
// by top-up and subscription orders so a provider transaction cannot be
// replayed against a different local order type. The fixed-width event-key
// unique index is the database-level idempotency fence; application checks
// below provide a stable domain error and protect older databases during the
// race window.
type PaymentEvent struct {
	Id              int    `json:"id"`
	EventKey        string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_payment_event_key"`
	Provider        string `json:"provider" gorm:"type:varchar(50);not null"`
	ProviderTradeNo string `json:"provider_trade_no" gorm:"type:varchar(255);not null"`
	OrderTradeNo    string `json:"order_trade_no" gorm:"type:varchar(255);not null;index:idx_payment_events_order_trade_no,length:191"`
	OrderKind       string `json:"order_kind" gorm:"type:varchar(32);not null"`
	Payload         string `json:"payload" gorm:"type:text"`
	CreateTime      int64  `json:"create_time"`
}

const (
	PaymentEventOrderTopUp        = "topup"
	PaymentEventOrderSubscription = "subscription"
)

// PaymentEventKey is the fixed-width database identity for a provider
// transaction. Length-prefixing prevents ambiguous concatenation while keeping
// the unique key below legacy InnoDB's 767-byte utf8mb4 index limit.
func PaymentEventKey(provider, providerTradeNo string) (string, bool) {
	provider = strings.TrimSpace(provider)
	providerTradeNo = strings.TrimSpace(providerTradeNo)
	if provider == "" || len(provider) > 50 || providerTradeNo == "" || len(providerTradeNo) > 255 {
		return "", false
	}
	hash := sha256.New()
	for _, part := range []string{provider, providerTradeNo} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(part)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil)), true
}

func (event *PaymentEvent) BeforeCreate(tx *gorm.DB) error {
	if event == nil {
		return ErrEpayCallbackInvalid
	}
	event.Provider = strings.TrimSpace(event.Provider)
	event.ProviderTradeNo = strings.TrimSpace(event.ProviderTradeNo)
	event.OrderTradeNo = strings.TrimSpace(event.OrderTradeNo)
	event.OrderKind = strings.TrimSpace(event.OrderKind)
	if event.OrderTradeNo == "" || len(event.OrderTradeNo) > 255 || event.OrderKind == "" || len(event.OrderKind) > 32 {
		return ErrEpayCallbackInvalid
	}
	key, ok := PaymentEventKey(event.Provider, event.ProviderTradeNo)
	if !ok {
		return ErrEpayCallbackInvalid
	}
	if strings.TrimSpace(event.EventKey) != "" && event.EventKey != key {
		return ErrEpayCallbackInvalid
	}
	event.EventKey = key
	return nil
}

// bindPaymentEventTx atomically binds a provider transaction to one local
// order. It is intentionally small and provider-agnostic so future gateways
// can reuse the same idempotency ledger.
func bindPaymentEventTx(tx *gorm.DB, provider, providerTradeNo, orderTradeNo, orderKind, payload string) error {
	if tx == nil || strings.TrimSpace(provider) == "" || strings.TrimSpace(providerTradeNo) == "" || strings.TrimSpace(orderTradeNo) == "" || strings.TrimSpace(orderKind) == "" {
		return ErrEpayCallbackInvalid
	}
	provider = strings.TrimSpace(provider)
	providerTradeNo = strings.TrimSpace(providerTradeNo)
	orderTradeNo = strings.TrimSpace(orderTradeNo)
	orderKind = strings.TrimSpace(orderKind)
	eventKey, ok := PaymentEventKey(provider, providerTradeNo)
	if !ok || len(orderTradeNo) > 255 || len(orderKind) > 32 {
		return ErrEpayCallbackInvalid
	}
	event := PaymentEvent{
		EventKey:        eventKey,
		Provider:        provider,
		ProviderTradeNo: providerTradeNo,
		OrderTradeNo:    orderTradeNo,
		OrderKind:       orderKind,
		Payload:         payload,
		CreateTime:      common.GetTimestamp(),
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&event).Error; err != nil {
		return err
	}
	// Re-read after the conflict-safe insert using a locking/current read. This
	// matters on MySQL's default REPEATABLE READ isolation: a plain SELECT can
	// keep the snapshot taken before a concurrent insert and incorrectly report
	// the event as missing after INSERT ... ON DUPLICATE KEY/DO NOTHING.
	var existing PaymentEvent
	if err := lockForUpdate(tx).
		Where("event_key = ?", eventKey).
		First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The insert was accepted by the database, so a missing row here is
			// an unexpected dialect/transaction failure rather than a conflict.
			return ErrEpayCallbackInvalid
		}
		return err
	}
	if existing.EventKey != eventKey || existing.Provider != provider || existing.ProviderTradeNo != providerTradeNo ||
		existing.OrderTradeNo != orderTradeNo || existing.OrderKind != orderKind {
		return ErrEpayProviderTradeConflict
	}
	return nil
}
