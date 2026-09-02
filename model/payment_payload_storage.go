package model

import (
	"crypto/hmac"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	paymentPayloadAuditPrefix             = "payment-audit:v1:"
	paymentPayloadAuditNamespace          = "newapi-payment-payload-audit-v1\x00"
	paymentPayloadMigrationBatchSize      = 100
	paymentPayloadMigrationMaxCASAttempts = 5
)

var (
	ErrPaymentPayloadAuditInvalid = errors.New("payment payload audit marker is invalid")
	errPaymentPayloadCASConflict  = errors.New("payment payload changed during migration")
)

// paymentPayloadAuditEnvelope intentionally contains no provider-owned values.
// The surrounding typed ledger columns are the reconciliation projection; this
// marker only proves whether an independently obtained provider payload is the
// exact authenticated delivery that was accepted by this deployment.
type paymentPayloadAuditEnvelope struct {
	HMACSHA256 string `json:"hmac_sha256"`
	Bytes      int    `json:"bytes"`
}

func paymentPayloadAuditDigest(payload string) (string, error) {
	secret := strings.TrimSpace(common.CryptoSecret)
	if !common.CredentialEncryptionReady() || secret == "" {
		return "", common.ErrCredentialSecretUnavailable
	}
	return common.GenerateHMACWithKey([]byte(paymentPayloadAuditNamespace+secret), payload), nil
}

func parsePaymentPayloadAuditMarker(value string) (paymentPayloadAuditEnvelope, string, bool, error) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, paymentPayloadAuditPrefix) {
		if strings.HasPrefix(trimmed, "payment-audit:") {
			return paymentPayloadAuditEnvelope{}, "", false, ErrPaymentPayloadAuditInvalid
		}
		return paymentPayloadAuditEnvelope{}, "", false, nil
	}

	encoded := strings.TrimPrefix(trimmed, paymentPayloadAuditPrefix)
	var fields map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(encoded, &fields); err != nil || len(fields) != 2 {
		return paymentPayloadAuditEnvelope{}, "", true, ErrPaymentPayloadAuditInvalid
	}
	var envelope paymentPayloadAuditEnvelope
	digestField, hasDigest := fields["hmac_sha256"]
	bytesField, hasBytes := fields["bytes"]
	if !hasDigest || !hasBytes || common.Unmarshal(digestField, &envelope.HMACSHA256) != nil || common.Unmarshal(bytesField, &envelope.Bytes) != nil {
		return paymentPayloadAuditEnvelope{}, "", true, ErrPaymentPayloadAuditInvalid
	}
	envelope.HMACSHA256 = strings.ToLower(strings.TrimSpace(envelope.HMACSHA256))
	digest, err := hex.DecodeString(envelope.HMACSHA256)
	if err != nil || len(digest) != 32 || envelope.Bytes < 0 {
		return paymentPayloadAuditEnvelope{}, "", true, ErrPaymentPayloadAuditInvalid
	}
	canonical, err := common.Marshal(envelope)
	if err != nil {
		return paymentPayloadAuditEnvelope{}, "", true, fmt.Errorf("encode payment payload audit marker: %w", err)
	}
	return envelope, paymentPayloadAuditPrefix + string(canonical), true, nil
}

func isLegacyTopUpEvidenceMarker(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 4 || parts[0] != "legacy-topup" || parts[1] != "v1" || parts[2] == "" || len(parts[2]) > 50 {
		return false
	}
	for _, char := range parts[2] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	digest, err := hex.DecodeString(parts[3])
	return err == nil && len(digest) == 32
}

func isBalanceSubscriptionEvidenceMarker(value string) bool {
	const prefix = "charged_quota="
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	quota := strings.TrimPrefix(value, prefix)
	parsed, err := strconv.ParseInt(quota, 10, 64)
	return err == nil && parsed >= 0 && strconv.FormatInt(parsed, 10) == quota
}

func isInternalPaymentEvidenceMarker(value string) bool {
	trimmed := strings.TrimSpace(value)
	return isLegacyTopUpEvidenceMarker(trimmed) || isBalanceSubscriptionEvidenceMarker(trimmed)
}

func normalizePaymentPayloadForStorage(payload string) (string, error) {
	if payload == "" {
		return "", nil
	}
	_, canonical, isAudit, err := parsePaymentPayloadAuditMarker(payload)
	if err != nil {
		return "", err
	}
	if isAudit {
		return canonical, nil
	}
	if isInternalPaymentEvidenceMarker(payload) {
		return payload, nil
	}

	digest, err := paymentPayloadAuditDigest(payload)
	if err != nil {
		return "", err
	}
	envelope := paymentPayloadAuditEnvelope{HMACSHA256: digest, Bytes: len([]byte(payload))}
	encoded, err := common.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode payment payload audit marker: %w", err)
	}
	return paymentPayloadAuditPrefix + string(encoded), nil
}

// PaymentPayloadAuditMatches lets an operator compare a provider export with a
// stored audit marker without restoring webhook PII to the database.
func PaymentPayloadAuditMatches(storedMarker, providerPayload string) (bool, error) {
	envelope, _, isAudit, err := parsePaymentPayloadAuditMarker(storedMarker)
	if err != nil {
		return false, err
	}
	if !isAudit {
		return false, ErrPaymentPayloadAuditInvalid
	}
	digest, err := paymentPayloadAuditDigest(providerPayload)
	if err != nil {
		return false, err
	}
	storedDigest, _ := hex.DecodeString(envelope.HMACSHA256)
	candidateDigest, _ := hex.DecodeString(digest)
	return envelope.Bytes == len([]byte(providerPayload)) && hmac.Equal(storedDigest, candidateDigest), nil
}

func protectPaymentPayload(value *string) error {
	if value == nil {
		return nil
	}
	protected, err := normalizePaymentPayloadForStorage(*value)
	if err != nil {
		return err
	}
	*value = protected
	return nil
}

func normalizePaymentPayloadUpdateMap(values map[string]interface{}, column, field string) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		present  bool
	)
	for _, name := range []string{column, field} {
		if value, ok := values[name]; ok {
			if present {
				return errors.New("payment payload update is ambiguous")
			}
			rawValue = value
			present = true
		}
		delete(values, name)
	}
	if !present {
		return nil
	}
	payload := ""
	switch value := rawValue.(type) {
	case nil:
		values[column] = nil
		return nil
	case string:
		payload = value
	case *string:
		if value == nil {
			values[column] = nil
			return nil
		}
		payload = *value
	default:
		return errors.New("payment payload must be a string")
	}
	protected, err := normalizePaymentPayloadForStorage(payload)
	if err != nil {
		return err
	}
	values[column] = protected
	return nil
}

func (event *PaymentEvent) preparePayload() error {
	if event == nil {
		return nil
	}
	return protectPaymentPayload(&event.Payload)
}

func (event *PaymentEvent) BeforeSave(tx *gorm.DB) error {
	if event == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizePaymentPayloadUpdateMap(values, "payload", "Payload")
		}
		handled, err := prepareProtectedStructDestination(tx, (*PaymentEvent).preparePayload, "payload")
		if handled || err != nil {
			return err
		}
	}
	return event.preparePayload()
}

func (topUp *TopUp) prepareProviderPayload() error {
	if topUp == nil {
		return nil
	}
	return protectPaymentPayload(&topUp.ProviderPayload)
}

func (topUp *TopUp) BeforeSave(tx *gorm.DB) error {
	if topUp == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizePaymentPayloadUpdateMap(values, "provider_payload", "ProviderPayload")
		}
		handled, err := prepareProtectedStructDestination(tx, (*TopUp).prepareProviderPayload, "provider_payload")
		if handled || err != nil {
			return err
		}
	}
	return topUp.prepareProviderPayload()
}

func (order *SubscriptionOrder) prepareProviderPayload() error {
	if order == nil {
		return nil
	}
	return protectPaymentPayload(&order.ProviderPayload)
}

func (order *SubscriptionOrder) BeforeSave(tx *gorm.DB) error {
	if order == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizePaymentPayloadUpdateMap(values, "provider_payload", "ProviderPayload")
		}
		handled, err := prepareProtectedStructDestination(tx, (*SubscriptionOrder).prepareProviderPayload, "provider_payload")
		if handled || err != nil {
			return err
		}
	}
	return order.prepareProviderPayload()
}

func (event *ProviderRefundEvent) preparePayload() error {
	if event == nil {
		return nil
	}
	return protectPaymentPayload(&event.Payload)
}

func (event *ProviderRefundEvent) BeforeSave(tx *gorm.DB) error {
	if event == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizePaymentPayloadUpdateMap(values, "payload", "Payload")
		}
		handled, err := prepareProtectedStructDestination(tx, (*ProviderRefundEvent).preparePayload, "payload")
		if handled || err != nil {
			return err
		}
	}
	return event.preparePayload()
}

func (effect *ProviderReversalEffect) preparePayload() error {
	if effect == nil {
		return nil
	}
	return protectPaymentPayload(&effect.Payload)
}

func (effect *ProviderReversalEffect) BeforeSave(tx *gorm.DB) error {
	if effect == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizePaymentPayloadUpdateMap(values, "payload", "Payload")
		}
		handled, err := prepareProtectedStructDestination(tx, (*ProviderReversalEffect).preparePayload, "payload")
		if handled || err != nil {
			return err
		}
	}
	return effect.preparePayload()
}

type paymentPayloadMigrationSpec struct {
	model  any
	table  string
	column string
}

var paymentPayloadMigrationSpecs = []paymentPayloadMigrationSpec{
	{model: &PaymentEvent{}, table: "payment_events", column: "payload"},
	{model: &TopUp{}, table: "top_ups", column: "provider_payload"},
	{model: &SubscriptionOrder{}, table: "subscription_orders", column: "provider_payload"},
	{model: &ProviderRefundEvent{}, table: "provider_refund_events", column: "payload"},
	{model: &ProviderReversalEffect{}, table: "provider_reversal_effects", column: "payload"},
}

type paymentPayloadMigrationRow struct {
	ID    int64          `gorm:"column:id"`
	Value sql.NullString `gorm:"column:value"`
}

// MigrateLegacyPaymentPayloads replaces historical raw webhook bodies with a
// secret-backed audit marker. Rows are paged by primary key; each row is locked
// and updated with the exact value read so concurrent reconciliation edits are
// never overwritten by a marker derived from stale data.
func MigrateLegacyPaymentPayloads(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	for _, spec := range paymentPayloadMigrationSpecs {
		if !db.Migrator().HasTable(spec.model) || !db.Migrator().HasColumn(spec.model, spec.column) {
			continue
		}
		lastID := int64(0)
		for {
			var rows []paymentPayloadMigrationRow
			if err := db.Session(&gorm.Session{SkipHooks: true}).Table(spec.table).
				Select("id, "+spec.column+" AS value").Where("id > ?", lastID).
				Order("id").Limit(paymentPayloadMigrationBatchSize).Find(&rows).Error; err != nil {
				return fmt.Errorf("read legacy %s.%s payloads: %w", spec.table, spec.column, err)
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				if row.ID > lastID {
					lastID = row.ID
				}
				if err := migratePaymentPayloadRow(db, spec, row); err != nil {
					return fmt.Errorf("migrate %s.%s id=%d: %w", spec.table, spec.column, row.ID, err)
				}
			}
			if len(rows) < paymentPayloadMigrationBatchSize {
				break
			}
		}
	}
	return nil
}

func migratePaymentPayloadRow(db *gorm.DB, spec paymentPayloadMigrationSpec, observed paymentPayloadMigrationRow) error {
	if observed.ID <= 0 {
		return nil
	}
	for attempt := 0; attempt < paymentPayloadMigrationMaxCASAttempts; attempt++ {
		err := db.Transaction(func(tx *gorm.DB) error {
			var current paymentPayloadMigrationRow
			query := lockForUpdate(tx.Session(&gorm.Session{SkipHooks: true})).Table(spec.table).
				Select("id, "+spec.column+" AS value").Where("id = ?", observed.ID)
			if err := query.Take(&current).Error; err != nil {
				return err
			}
			observed = current
			if !current.Value.Valid || current.Value.String == "" {
				return nil
			}
			protected, err := normalizePaymentPayloadForStorage(current.Value.String)
			if err != nil {
				return err
			}
			if protected == current.Value.String {
				return nil
			}
			result := tx.Session(&gorm.Session{SkipHooks: true}).Table(spec.table).
				Where("id = ? AND "+spec.column+" = ?", current.ID, current.Value.String).
				Update(spec.column, protected)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errPaymentPayloadCASConflict
			}
			return nil
		})
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if errors.Is(err, errPaymentPayloadCASConflict) {
			continue
		}
		return err
	}
	return errors.New("payment payload changed repeatedly during migration")
}
