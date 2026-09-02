package model

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPaymentPayloadStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	previousMainDBType := common.MainDatabaseType()
	previousLogDBType := common.LogDatabaseType()
	t.Cleanup(func() {
		DB = previousDB
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
		common.SetDatabaseTypes(previousMainDBType, previousLogDBType)
	})

	common.CryptoSecret = "payment-payload-storage-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&PaymentEvent{},
		&TopUp{},
		&SubscriptionOrder{},
		&ProviderRefundEvent{},
		&ProviderReversalEffect{},
	))
	DB = db
	return db
}

func paymentPayloadTestRaw() string {
	return `{"email":"alice@example.com","name":"Alice Example","country":"FR","metadata":{"freeform":"sensitive"}}`
}

func assertPaymentPayloadProtected(t *testing.T, stored, raw string) {
	t.Helper()
	assert.True(t, strings.HasPrefix(stored, paymentPayloadAuditPrefix))
	assert.NotContains(t, stored, "alice@example.com")
	assert.NotContains(t, stored, "Alice Example")
	assert.NotContains(t, stored, "FR")
	assert.NotContains(t, stored, "freeform")
	matched, err := PaymentPayloadAuditMatches(stored, raw)
	require.NoError(t, err)
	assert.True(t, matched)
	matched, err = PaymentPayloadAuditMatches(stored, raw+" ")
	require.NoError(t, err)
	assert.False(t, matched)
}

func readPaymentPayloadColumn(t *testing.T, db *gorm.DB, table, column string, id int64) string {
	t.Helper()
	var value sql.NullString
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table(table).
		Select(column).Where("id = ?", id).Scan(&value).Error)
	if !value.Valid {
		return ""
	}
	return value.String
}

func TestPaymentPayloadNewWritesPersistOnlyAuditMarkers(t *testing.T) {
	db := setupPaymentPayloadStorageTest(t)
	raw := paymentPayloadTestRaw()
	eventKey, ok := PaymentEventKey(PaymentProviderStripe, "pi-payload-new-write")
	require.True(t, ok)
	paymentEvent := &PaymentEvent{
		EventKey: eventKey, Provider: PaymentProviderStripe, ProviderTradeNo: "pi-payload-new-write",
		OrderTradeNo: "payload-new-write", OrderKind: PaymentEventOrderTopUp, Payload: raw,
	}
	topUp := &TopUp{TradeNo: "payload-new-write-topup", ProviderPayload: raw}
	subscription := &SubscriptionOrder{TradeNo: "payload-new-write-subscription", ProviderPayload: raw}
	refund := &ProviderRefundEvent{
		EventKey: ProviderRefundEventKey(PaymentProviderStripe, "evt-payload-new-write-refund"),
		Provider: PaymentProviderStripe, DeliveryID: "evt-payload-new-write-refund",
		EventType: "refund.failed", Status: ProviderRefundEventRefundRequired, Payload: raw,
	}
	effect := &ProviderReversalEffect{
		EffectKey: ProviderReversalEffectKey(PaymentProviderStripe, "refund.succeeded", "re-payload-new-write"),
		Provider:  PaymentProviderStripe, ProviderAccountID: "acct_test", ProviderEnvironment: "test",
		EffectID: "re-payload-new-write", ProviderTradeNo: "pi-payload-new-write",
		OrderTradeNo: "payload-new-write", OrderKind: PaymentEventOrderTopUp,
		EventType: "refund.succeeded", Amount: "1.00", Currency: "USD",
		Status: ProviderReversalEffectApplied, Outcome: ProviderRefundEventApplied, Payload: raw,
	}

	for _, value := range []any{paymentEvent, topUp, subscription, refund, effect} {
		require.NoError(t, db.Create(value).Error)
	}
	stored := []string{
		readPaymentPayloadColumn(t, db, "payment_events", "payload", int64(paymentEvent.Id)),
		readPaymentPayloadColumn(t, db, "top_ups", "provider_payload", int64(topUp.Id)),
		readPaymentPayloadColumn(t, db, "subscription_orders", "provider_payload", int64(subscription.Id)),
		readPaymentPayloadColumn(t, db, "provider_refund_events", "payload", refund.ID),
		readPaymentPayloadColumn(t, db, "provider_reversal_effects", "payload", effect.ID),
	}
	for _, value := range stored {
		assertPaymentPayloadProtected(t, value, raw)
		assert.Equal(t, stored[0], value, "the same authenticated payload must have one stable audit identity")
	}
}

func TestPaymentPayloadStructAndMapUpdatesPersistOnlyAuditMarkers(t *testing.T) {
	db := setupPaymentPayloadStorageTest(t)
	eventKey, ok := PaymentEventKey(PaymentProviderStripe, "pi-payload-update")
	require.True(t, ok)
	paymentEvent := &PaymentEvent{
		EventKey: eventKey, Provider: PaymentProviderStripe, ProviderTradeNo: "pi-payload-update",
		OrderTradeNo: "payload-update", OrderKind: PaymentEventOrderTopUp,
	}
	topUp := &TopUp{TradeNo: "payload-update-topup"}
	subscription := &SubscriptionOrder{TradeNo: "payload-update-subscription"}
	refund := &ProviderRefundEvent{
		EventKey: ProviderRefundEventKey(PaymentProviderStripe, "evt-payload-update-refund"),
		Provider: PaymentProviderStripe, DeliveryID: "evt-payload-update-refund",
		EventType: "refund.failed", Status: ProviderRefundEventRefundRequired,
	}
	effect := &ProviderReversalEffect{
		EffectKey: ProviderReversalEffectKey(PaymentProviderStripe, "refund.succeeded", "re-payload-update"),
		Provider:  PaymentProviderStripe, ProviderAccountID: "acct_test", ProviderEnvironment: "test",
		EffectID: "re-payload-update", ProviderTradeNo: "pi-payload-update",
		OrderTradeNo: "payload-update", OrderKind: PaymentEventOrderTopUp,
		EventType: "refund.succeeded", Amount: "1.00", Currency: "USD",
		Status: ProviderReversalEffectApplied, Outcome: ProviderRefundEventApplied,
	}
	for _, value := range []any{paymentEvent, topUp, subscription, refund, effect} {
		require.NoError(t, db.Create(value).Error)
	}

	structRaw := `{"email":"struct@example.com","name":"Struct Update","country":"DE"}`
	structUpdates := []func() error{
		func() error {
			return db.Model(&PaymentEvent{}).Where("id = ?", paymentEvent.Id).Updates(PaymentEvent{Payload: structRaw}).Error
		},
		func() error {
			return db.Model(&TopUp{}).Where("id = ?", topUp.Id).Updates(TopUp{ProviderPayload: structRaw}).Error
		},
		func() error {
			return db.Model(&SubscriptionOrder{}).Where("id = ?", subscription.Id).Updates(SubscriptionOrder{ProviderPayload: structRaw}).Error
		},
		func() error {
			return db.Model(&ProviderRefundEvent{}).Where("id = ?", refund.ID).Updates(ProviderRefundEvent{Payload: structRaw}).Error
		},
		func() error {
			return db.Model(&ProviderReversalEffect{}).Where("id = ?", effect.ID).Updates(ProviderReversalEffect{Payload: structRaw}).Error
		},
	}
	for _, update := range structUpdates {
		require.NoError(t, update())
	}
	locations := []struct {
		table  string
		column string
		id     int64
	}{
		{table: "payment_events", column: "payload", id: int64(paymentEvent.Id)},
		{table: "top_ups", column: "provider_payload", id: int64(topUp.Id)},
		{table: "subscription_orders", column: "provider_payload", id: int64(subscription.Id)},
		{table: "provider_refund_events", column: "payload", id: refund.ID},
		{table: "provider_reversal_effects", column: "payload", id: effect.ID},
	}
	for _, location := range locations {
		assertPaymentPayloadProtected(t, readPaymentPayloadColumn(t, db, location.table, location.column, location.id), structRaw)
	}

	mapRaw := `{"email":"map@example.com","name":"Map Update","country":"JP","metadata":{"open":true}}`
	mapUpdates := []func() error{
		func() error {
			return db.Model(&PaymentEvent{}).Where("id = ?", paymentEvent.Id).Updates(map[string]interface{}{"Payload": mapRaw}).Error
		},
		func() error {
			return db.Model(&TopUp{}).Where("id = ?", topUp.Id).Updates(map[string]interface{}{"provider_payload": mapRaw}).Error
		},
		func() error {
			return db.Model(&SubscriptionOrder{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{"ProviderPayload": mapRaw}).Error
		},
		func() error {
			return db.Model(&ProviderRefundEvent{}).Where("id = ?", refund.ID).Updates(map[string]interface{}{"payload": mapRaw}).Error
		},
		func() error {
			return db.Model(&ProviderReversalEffect{}).Where("id = ?", effect.ID).Updates(map[string]interface{}{"Payload": mapRaw}).Error
		},
	}
	for _, update := range mapUpdates {
		require.NoError(t, update())
	}
	for _, location := range locations {
		assertPaymentPayloadProtected(t, readPaymentPayloadColumn(t, db, location.table, location.column, location.id), mapRaw)
	}
}

func TestSubscriptionOrderJSONOmitsProviderPayload(t *testing.T) {
	raw := paymentPayloadTestRaw()
	encoded, err := common.Marshal(SubscriptionOrder{TradeNo: "json-hidden", ProviderPayload: raw})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "provider_payload")
	assert.NotContains(t, string(encoded), raw)
}

func TestPaymentPayloadStoragePreservesInternalEvidenceMarkers(t *testing.T) {
	db := setupPaymentPayloadStorageTest(t)
	legacyMarker := legacyTopUpEvidenceMarker(PaymentProviderWaffo, "internal-marker-order")
	eventKey, ok := PaymentEventKey(PaymentProviderWaffo, legacyMarker)
	require.True(t, ok)
	paymentEvent := &PaymentEvent{
		EventKey: eventKey, Provider: PaymentProviderWaffo, ProviderTradeNo: legacyMarker,
		OrderTradeNo: "internal-marker-order", OrderKind: PaymentEventOrderTopUp, Payload: legacyMarker,
	}
	topUp := &TopUp{TradeNo: "internal-marker-order", ProviderPayload: legacyMarker}
	subscription := &SubscriptionOrder{TradeNo: "balance-marker-order", ProviderPayload: "charged_quota=12345"}
	require.NoError(t, db.Create(paymentEvent).Error)
	require.NoError(t, db.Create(topUp).Error)
	require.NoError(t, db.Create(subscription).Error)
	require.NoError(t, MigrateLegacyPaymentPayloads(db))
	assert.Equal(t, legacyMarker, readPaymentPayloadColumn(t, db, "payment_events", "payload", int64(paymentEvent.Id)))
	assert.Equal(t, legacyMarker, readPaymentPayloadColumn(t, db, "top_ups", "provider_payload", int64(topUp.Id)))
	assert.Equal(t, "charged_quota=12345", readPaymentPayloadColumn(t, db, "subscription_orders", "provider_payload", int64(subscription.Id)))
}

func TestPaymentPayloadStorageRejectsUnsupportedAuditEnvelope(t *testing.T) {
	db := setupPaymentPayloadStorageTest(t)
	event := &PaymentEvent{
		Provider: PaymentProviderStripe, ProviderTradeNo: "pi-unsupported-audit",
		OrderTradeNo: "unsupported-audit", OrderKind: PaymentEventOrderTopUp,
		Payload: `payment-audit:v2:{"email":"still-plaintext@example.com"}`,
	}
	err := db.Create(event).Error
	require.ErrorIs(t, err, ErrPaymentPayloadAuditInvalid)
	var count int64
	require.NoError(t, db.Model(&PaymentEvent{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestMigrateLegacyPaymentPayloadsCoversAllTablesAndBatches(t *testing.T) {
	db := setupPaymentPayloadStorageTest(t)
	raw := paymentPayloadTestRaw()

	for i := 0; i < paymentPayloadMigrationBatchSize+3; i++ {
		providerTradeNo := fmt.Sprintf("pi-legacy-payload-%03d", i)
		eventKey, ok := PaymentEventKey(PaymentProviderStripe, providerTradeNo)
		require.True(t, ok)
		event := &PaymentEvent{
			EventKey: eventKey, Provider: PaymentProviderStripe, ProviderTradeNo: providerTradeNo,
			OrderTradeNo: fmt.Sprintf("legacy-payload-order-%03d", i), OrderKind: PaymentEventOrderTopUp,
			Payload: raw,
		}
		require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(event).Error)
	}
	topUp := &TopUp{TradeNo: "legacy-payload-topup", ProviderPayload: raw}
	subscription := &SubscriptionOrder{TradeNo: "legacy-payload-subscription", ProviderPayload: raw}
	refund := &ProviderRefundEvent{
		EventKey: ProviderRefundEventKey(PaymentProviderStripe, "legacy-payload-refund"),
		Provider: PaymentProviderStripe, DeliveryID: "legacy-payload-refund",
		EventType: "refund.failed", Status: ProviderRefundEventRefundRequired, Payload: raw,
	}
	effect := &ProviderReversalEffect{
		EffectKey: ProviderReversalEffectKey(PaymentProviderStripe, "refund.succeeded", "legacy-payload-effect"),
		Provider:  PaymentProviderStripe, ProviderAccountID: "acct_test", ProviderEnvironment: "test",
		EffectID: "legacy-payload-effect", ProviderTradeNo: "pi-legacy-payload-effect",
		OrderTradeNo: "legacy-payload-topup", OrderKind: PaymentEventOrderTopUp,
		EventType: "refund.succeeded", Amount: "1.00", Currency: "USD",
		Status: ProviderReversalEffectApplied, Outcome: ProviderRefundEventApplied, Payload: raw,
	}
	for _, value := range []any{topUp, subscription, refund, effect} {
		require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(value).Error)
	}

	require.NoError(t, MigrateLegacyPaymentPayloads(db))
	require.NoError(t, MigrateLegacyPaymentPayloads(db), "migration must be idempotent")
	stored := []string{
		readPaymentPayloadColumn(t, db, "payment_events", "payload", int64(paymentPayloadMigrationBatchSize+3)),
		readPaymentPayloadColumn(t, db, "top_ups", "provider_payload", int64(topUp.Id)),
		readPaymentPayloadColumn(t, db, "subscription_orders", "provider_payload", int64(subscription.Id)),
		readPaymentPayloadColumn(t, db, "provider_refund_events", "payload", refund.ID),
		readPaymentPayloadColumn(t, db, "provider_reversal_effects", "payload", effect.ID),
	}
	for _, value := range stored {
		assertPaymentPayloadProtected(t, value, raw)
	}

	var remaining int64
	require.NoError(t, db.Table("payment_events").Where("payload = ?", raw).Count(&remaining).Error)
	assert.Zero(t, remaining)
}

func TestMigratePaymentPayloadRowUsesCurrentValueForCAS(t *testing.T) {
	db := setupPaymentPayloadStorageTest(t)
	oldRaw := `{"email":"old@example.com"}`
	newRaw := `{"email":"new@example.com","country":"CA"}`
	topUp := &TopUp{TradeNo: "payload-cas", ProviderPayload: oldRaw}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(topUp).Error)
	observed := paymentPayloadMigrationRow{ID: int64(topUp.Id), Value: sql.NullString{String: oldRaw, Valid: true}}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("top_ups").
		Where("id = ? AND provider_payload = ?", topUp.Id, oldRaw).Update("provider_payload", newRaw).Error)

	spec := paymentPayloadMigrationSpec{model: &TopUp{}, table: "top_ups", column: "provider_payload"}
	require.NoError(t, migratePaymentPayloadRow(db, spec, observed))
	stored := readPaymentPayloadColumn(t, db, "top_ups", "provider_payload", int64(topUp.Id))
	assertPaymentPayloadProtected(t, stored, newRaw)
	oldMatches, err := PaymentPayloadAuditMatches(stored, oldRaw)
	require.NoError(t, err)
	assert.False(t, oldMatches, "a stale observation must never overwrite a concurrent edit")
}

func TestMigrateLegacyPaymentPayloadsRequiresStableSecret(t *testing.T) {
	db := setupPaymentPayloadStorageTest(t)
	raw := paymentPayloadTestRaw()
	topUp := &TopUp{TradeNo: "payload-missing-secret", ProviderPayload: raw}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(topUp).Error)
	common.CredentialSecretConfigured = false
	common.CredentialSecretRuntimeReady = true

	err := MigrateLegacyPaymentPayloads(db)
	require.ErrorIs(t, err, common.ErrCredentialSecretUnavailable)
	assert.Equal(t, raw, readPaymentPayloadColumn(t, db, "top_ups", "provider_payload", int64(topUp.Id)))
}
