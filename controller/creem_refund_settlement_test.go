package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func creemRefundTestConfig() setting.CreemConfig {
	return setting.CreemConfig{TestMode: true, WebhookSecret: "creem-refund-test-secret"}
}

type creemRefundFixture struct {
	event        *CreemWebhookEvent
	cfg          setting.CreemConfig
	user         *model.User
	topUp        *model.TopUp
	accountID    string
	effect       string
	transaction  string
	order        string
	checkout     string
	subscription string
}

func setupCreemRefundFixture(t *testing.T) creemRefundFixture {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "creem-refund.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.TopUp{},
		&model.PaymentEvent{},
		&model.ProviderPaymentBinding{},
		&model.ProviderRefundEvent{},
		&model.ProviderReversalEffect{},
		&model.BillingOperation{},
		&model.QuotaCacheRepair{},
	))
	model.DB, model.LOG_DB = db, db
	previousRedisEnabled, previousRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, nil
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled, common.RDB = previousRedisEnabled, previousRDB
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	cfg := creemRefundTestConfig()
	accountID := creemMerchantSnapshotWithConfig(cfg)
	transactionID := "txn_creem_refund_1"
	effectID := "ref_creem_refund_1"
	orderID := "ord_creem_refund_1"
	checkoutID := "chk_creem_refund_1"
	subscriptionID := "sub_creem_refund_1"
	user := &model.User{Id: 9401, Username: "creem-refund-user", Quota: 1000, Group: "default"}
	require.NoError(t, db.Create(user).Error)
	providerTradeNo := transactionID
	topUp := &model.TopUp{
		UserId: user.Id, TradeNo: "creem-refund-order", PaymentMethod: model.PaymentMethodCreem,
		PaymentProvider: model.PaymentProviderCreem, Status: common.TopUpStatusSuccess,
		CreditedQuota: 1000, ProviderTradeNo: &providerTradeNo, ProviderCheckoutID: checkoutID,
		ProviderMerchantID: accountID, ProviderAmount: "10.00", ProviderCurrency: "USD",
		ProviderSubscriptionID: subscriptionID,
	}
	require.NoError(t, db.Create(topUp).Error)
	objects := []model.ProviderPaymentObject{
		{ObjectType: model.ProviderPaymentObjectTransaction, ObjectID: transactionID},
		{ObjectType: model.ProviderPaymentObjectOrder, ObjectID: orderID},
		{ObjectType: model.ProviderPaymentObjectCheckoutSession, ObjectID: checkoutID},
		{ObjectType: model.ProviderPaymentObjectSubscription, ObjectID: subscriptionID},
	}
	require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
		Provider: model.PaymentProviderCreem, ProviderAccountID: accountID, ProviderEnvironment: "test",
		OrderTradeNo: topUp.TradeNo, OrderKind: model.PaymentEventOrderTopUp, Objects: objects,
	}))

	event := creemRefundWebhookEvent("evt_creem_refund_1", "refund.created", effectID, transactionID, orderID, checkoutID, subscriptionID, 1000, 1210, 1210, "succeeded")
	return creemRefundFixture{event: event, cfg: cfg, user: user, topUp: topUp, accountID: accountID, effect: effectID, transaction: transactionID, order: orderID, checkout: checkoutID, subscription: subscriptionID}
}

func creemRefundWebhookEvent(eventID, eventType, effectID, transactionID, orderID, checkoutID, subscriptionID string, transactionAmount, refundAmount, amountPaid int64, status string) *CreemWebhookEvent {
	event := &CreemWebhookEvent{Id: eventID, EventType: eventType, CreatedAt: 100}
	event.Object.Id = effectID
	event.Object.Mode = "test"
	event.Object.Status = status
	event.Object.Transaction.Id = transactionID
	event.Object.Transaction.Amount = transactionAmount
	event.Object.Transaction.AmountPaid = amountPaid
	event.Object.Transaction.Currency = "USD"
	event.Object.Transaction.Status = "paid"
	event.Object.Transaction.RefundedAmount = refundAmount
	event.Object.Transaction.Order = orderID
	event.Object.Transaction.Subscription = subscriptionID
	event.Object.Order.Id = orderID
	event.Object.Order.Transaction = transactionID
	event.Object.Order.Currency = "USD"
	event.Object.Order.Amount = int(transactionAmount)
	event.Object.Order.AmountPaid = int(amountPaid)
	event.Object.Order.Status = "paid"
	event.Object.Order.Mode = "test"
	event.Object.Checkout.Id = checkoutID
	event.Object.Checkout.Mode = "test"
	event.Object.Subscription = []byte(`{"id":"` + subscriptionID + `","mode":"test"}`)
	event.Object.RefundAmount = refundAmount
	event.Object.RefundCurrency = "USD"
	event.Object.Amount = transactionAmount
	event.Object.Currency = "USD"
	event.Object.Status = status
	return event
}

func assertCreemRefundDelivery(t *testing.T, deliveryID string, status model.ProviderRefundEventStatus, reason string) model.ProviderRefundEvent {
	t.Helper()
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("delivery_id = ?", deliveryID).First(&delivery).Error)
	assert.Equal(t, status, delivery.Status)
	if reason != "" {
		assert.Equal(t, reason, delivery.DecisionReason)
	}
	return delivery
}

func TestBuildCreemRefundUsesTransactionAmountAndResolvesAllAliases(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	input, objects, err := buildCreemReversalEventInputWithConfig(fixture.event, `{"eventType":"refund.created"}`, fixture.cfg)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentProviderCreem, input.Provider)
	assert.Equal(t, fixture.accountID, input.ProviderAccountID)
	assert.Equal(t, "test", input.ProviderEnvironment)
	assert.Equal(t, "10.00", input.Amount)
	assert.Equal(t, "USD", input.Currency)
	assert.Equal(t, "refund.succeeded", input.EventType)
	assert.Equal(t, "succeeded", input.RefundStatus)
	assert.Equal(t, fixture.transaction, input.ProviderTradeNo)
	assert.Equal(t, fixture.effect, input.EffectID)
	assert.Empty(t, input.OrderKind)
	assert.Equal(t, []model.ProviderPaymentObject{
		{ObjectType: model.ProviderPaymentObjectTransaction, ObjectID: fixture.transaction},
		{ObjectType: model.ProviderPaymentObjectOrder, ObjectID: fixture.order},
		{ObjectType: model.ProviderPaymentObjectCheckoutSession, ObjectID: fixture.checkout},
		{ObjectType: model.ProviderPaymentObjectSubscription, ObjectID: fixture.subscription},
	}, objects)

	manualReason, err := resolveCreemReversalBinding(&input, objects)
	require.NoError(t, err)
	assert.Empty(t, manualReason)
	assert.Equal(t, fixture.topUp.TradeNo, input.OrderTradeNo)
	assert.Equal(t, model.PaymentEventOrderTopUp, input.OrderKind)
	assert.Equal(t, model.ProviderPaymentObjectTransaction, input.ProviderObjectType)
	assert.Equal(t, fixture.transaction, input.ProviderTradeNo)
}

func TestHandleCreemRefundAppliesFullRefundWithoutTaxAndIsIdempotent(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
	require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
	var user model.User
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	assert.Zero(t, user.Quota)
	delivery := assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventApplied, "wallet_quota_reversed")
	assert.Equal(t, "10.00", delivery.Amount)
	assert.Equal(t, int64(-1000), delivery.WalletDelta)
	var effects int64
	require.NoError(t, model.DB.Model(&model.ProviderReversalEffect{}).Count(&effects).Error)
	assert.EqualValues(t, 1, effects)
	var deliveries int64
	require.NoError(t, model.DB.Model(&model.ProviderRefundEvent{}).Count(&deliveries).Error)
	assert.EqualValues(t, 1, deliveries)
}

func TestHandleCreemRefundUsesCheckoutAliasesWhenOriginalEventOmittedTransaction(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	// Creem's documented checkout.completed example has no transaction field.
	// The later refund links its transaction to the already-bound order, checkout,
	// and subscription, which must remain sufficient to select one exact owner.
	require.NoError(t, model.DB.Where(
		"provider = ? AND provider_account_id = ? AND provider_environment = ? AND object_type = ? AND object_id = ?",
		model.PaymentProviderCreem, fixture.accountID, "test", model.ProviderPaymentObjectTransaction, fixture.transaction,
	).Delete(&model.ProviderPaymentBinding{}).Error)

	require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
	var user model.User
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	assert.Zero(t, user.Quota)
	assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventApplied, "wallet_quota_reversed")
}

func TestHandleCreemRefundKeepsPartialRefundManual(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	fixture.event.Object.Transaction.RefundedAmount = 500
	fixture.event.Object.RefundAmount = 500
	require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
	var user model.User
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	assert.Equal(t, 1000, user.Quota)
	delivery := assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventManualReconciliation, model.ProviderRefundManualReasonPartialRefundUnsupported)
	assert.Equal(t, "5.00", delivery.Amount)
	var effects int64
	require.NoError(t, model.DB.Model(&model.ProviderReversalEffect{}).Count(&effects).Error)
	assert.Zero(t, effects)
}

func TestBuildCreemDisputeAcceptsDocumentedNestedSandboxMode(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	fixture.event.EventType = "dispute.created"
	fixture.event.Object.Id = "disp_creem_documented_mode"
	fixture.event.Object.Amount = fixture.event.Object.Transaction.AmountPaid
	fixture.event.Object.Transaction.Status = "chargeback"
	fixture.event.Object.Mode = "local"
	fixture.event.Object.Order.Mode = "sandbox"
	fixture.event.Object.Transaction.Mode = "sandbox"
	fixture.event.Object.Checkout.Mode = "sandbox"
	fixture.event.Object.Subscription = []byte(`{"id":"sub_creem_refund_1","mode":"sandbox"}`)

	input, _, err := buildCreemReversalEventInputWithConfig(fixture.event, "", fixture.cfg)
	require.NoError(t, err)
	assert.Equal(t, "test", input.ProviderEnvironment)
	assert.Equal(t, "dispute.funds_withdrawn", input.EventType)
}

func TestCreemWebhookRoutesRefundToReversalLedger(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	previousConfig := setting.GetCreemConfig()
	setting.UpdateCreemConfig(func(cfg *setting.CreemConfig) { *cfg = fixture.cfg })
	t.Cleanup(func() {
		setting.UpdateCreemConfig(func(cfg *setting.CreemConfig) { *cfg = previousConfig })
	})
	payload := common.GetJsonString(fixture.event)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/api/user/creem/webhook", strings.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set(CreemSignatureHeader, generateCreemSignature(payload, fixture.cfg.WebhookSecret))

	CreemWebhook(ctx)

	assert.Equal(t, 200, recorder.Code)
	var user model.User
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	assert.Zero(t, user.Quota)
	assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventApplied, "wallet_quota_reversed")
}

func TestHandleCreemRefundKeepsPendingNonTerminalManual(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	fixture.event.Object.Status = "pending"
	fixture.event.Object.Transaction.Status = "pending"
	require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
	var user model.User
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	assert.Equal(t, 1000, user.Quota)
	assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventManualReconciliation, model.ProviderRefundManualReasonNonTerminal)
}

func TestHandleCreemRefundMapsFailedRefundToRefundRequired(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	fixture.event.Object.Status = "failed"
	fixture.event.Object.Transaction.Status = "failed"
	require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
	var user model.User
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	assert.Equal(t, 1000, user.Quota)
	assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventRefundRequired, "provider_refund_failed")
}

func TestHandleCreemDisputeWithdrawsFunds(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	fixture.event.Id = "evt_creem_dispute_1"
	fixture.event.EventType = "dispute.created"
	fixture.event.Object.Id = "dispute_creem_1"
	fixture.event.Object.Amount = fixture.event.Object.Transaction.AmountPaid
	fixture.event.Object.Status = "needs_response"
	fixture.event.Object.Transaction.Status = "chargeback"
	require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
	var user model.User
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	assert.Zero(t, user.Quota)
	delivery := assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventApplied, "wallet_quota_reversed")
	assert.Equal(t, "dispute.funds_withdrawn", delivery.EventType)
}

func TestBuildCreemDisputeAcceptsProviderStatusSpellings(t *testing.T) {
	for _, status := range []string{"chargeback", "chargedBack", "charged_back"} {
		t.Run(status, func(t *testing.T) {
			fixture := setupCreemRefundFixture(t)
			fixture.event.EventType = "dispute.created"
			fixture.event.Object.Id = "dispute_creem_status_" + status
			fixture.event.Object.Amount = fixture.event.Object.Transaction.AmountPaid
			fixture.event.Object.Transaction.Status = status

			input, _, err := buildCreemReversalEventInputWithConfig(fixture.event, "", fixture.cfg)
			require.NoError(t, err)
			assert.Empty(t, input.ForceManualReason)
			assert.Equal(t, "10.00", input.Amount)
		})
	}
}

func TestBuildCreemDisputeWithoutFundsWithdrawalEvidenceStaysManual(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	fixture.event.EventType = "dispute.created"
	fixture.event.Object.Id = "dispute_creem_incomplete"
	fixture.event.Object.Amount = fixture.event.Object.Transaction.AmountPaid
	fixture.event.Object.Transaction.Status = "paid"
	fixture.event.Object.Transaction.RefundedAmount = 0

	input, _, err := buildCreemReversalEventInputWithConfig(fixture.event, "", fixture.cfg)
	require.NoError(t, err)
	assert.Equal(t, model.ProviderRefundManualReasonPartialRefundUnsupported, input.ForceManualReason)
}

func TestCreemReversalIgnoresCustomerMetadataSubscriptionAlias(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	fixture.event.Object.Subscription = nil
	fixture.event.Object.Transaction.Subscription = ""
	fixture.event.Object.Metadata = map[string]string{"subscription_id": "sub_other_customer"}
	require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
		Provider: model.PaymentProviderCreem, ProviderAccountID: fixture.accountID, ProviderEnvironment: "test",
		OrderTradeNo: "other-customer-order", OrderKind: model.PaymentEventOrderSubscription,
		Objects: []model.ProviderPaymentObject{{ObjectType: model.ProviderPaymentObjectSubscription, ObjectID: "sub_other_customer"}},
	}))

	input, objects, err := buildCreemReversalEventInputWithConfig(fixture.event, "", fixture.cfg)
	require.NoError(t, err)
	assert.NotContains(t, objects, model.ProviderPaymentObject{ObjectType: model.ProviderPaymentObjectSubscription, ObjectID: "sub_other_customer"})
	manualReason, err := resolveCreemReversalBinding(&input, objects)
	require.NoError(t, err)
	assert.Empty(t, manualReason)
	assert.Equal(t, fixture.topUp.TradeNo, input.OrderTradeNo)
}

func TestCreemReversalScopePreventsCrossAccountAndEnvironmentLookup(t *testing.T) {
	fixture := setupCreemRefundFixture(t)
	require.NoError(t, model.DB.Where("provider = ?", model.PaymentProviderCreem).Delete(&model.ProviderPaymentBinding{}).Error)
	for _, scope := range []struct {
		account     string
		environment string
	}{
		{account: "another-account", environment: "test"},
		{account: fixture.accountID, environment: "live"},
	} {
		require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
			Provider: model.PaymentProviderCreem, ProviderAccountID: scope.account, ProviderEnvironment: scope.environment,
			OrderTradeNo: fixture.topUp.TradeNo, OrderKind: model.PaymentEventOrderTopUp,
			Objects: []model.ProviderPaymentObject{{ObjectType: model.ProviderPaymentObjectTransaction, ObjectID: fixture.transaction}},
		}))
	}

	input, objects, err := buildCreemReversalEventInputWithConfig(fixture.event, "", fixture.cfg)
	require.NoError(t, err)
	manualReason, err := resolveCreemReversalBinding(&input, objects)
	require.NoError(t, err)
	assert.Equal(t, model.ProviderRefundManualReasonPaymentBindingMissing, manualReason)
}

func TestCreemSettlementHTTPStatusClassifiesPermanentAndRetryableErrors(t *testing.T) {
	for _, err := range []error{
		errCreemEventInvalid,
		errCreemEventMismatch,
		model.ErrProviderRefundInvalid,
		model.ErrProviderRefundConflict,
		model.ErrProviderPaymentBindingInvalid,
		model.ErrProviderPaymentBindingConflict,
	} {
		assert.Equalf(t, http.StatusBadRequest, creemSettlementHTTPStatus(err), "error=%v", err)
	}
	assert.Equal(t, http.StatusBadRequest, creemSettlementHTTPStatus(errors.Join(errors.New("wrapped"), model.ErrProviderRefundInvalid)))
	assert.Equal(t, http.StatusInternalServerError, creemSettlementHTTPStatus(model.ErrDatabase))
}

func TestBuildCreemRefundRejectsMissingAndConflictingAliases(t *testing.T) {
	t.Run("missing binding", func(t *testing.T) {
		fixture := setupCreemRefundFixture(t)
		require.NoError(t, model.DB.Where("provider = ?", model.PaymentProviderCreem).Delete(&model.ProviderPaymentBinding{}).Error)
		input, objects, err := buildCreemReversalEventInputWithConfig(fixture.event, "", fixture.cfg)
		require.NoError(t, err)
		manualReason, err := resolveCreemReversalBinding(&input, objects)
		require.NoError(t, err)
		assert.Equal(t, model.ProviderRefundManualReasonPaymentBindingMissing, manualReason)
		require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
		var user model.User
		require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
		assert.Equal(t, 1000, user.Quota)
		assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventManualReconciliation, model.ProviderRefundManualReasonPaymentBindingMissing)
	})

	t.Run("conflicting aliases", func(t *testing.T) {
		fixture := setupCreemRefundFixture(t)
		require.NoError(t, model.DB.Where(
			"provider = ? AND provider_account_id = ? AND provider_environment = ? AND object_type = ? AND object_id = ?",
			model.PaymentProviderCreem, fixture.accountID, "test", model.ProviderPaymentObjectOrder, fixture.order,
		).Delete(&model.ProviderPaymentBinding{}).Error)
		require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
			Provider: model.PaymentProviderCreem, ProviderAccountID: fixture.accountID, ProviderEnvironment: "test",
			OrderTradeNo: "another-creem-order", OrderKind: model.PaymentEventOrderTopUp,
			Objects: []model.ProviderPaymentObject{{ObjectType: model.ProviderPaymentObjectOrder, ObjectID: fixture.order}},
		}))
		input, objects, err := buildCreemReversalEventInputWithConfig(fixture.event, "", fixture.cfg)
		require.NoError(t, err)
		manualReason, err := resolveCreemReversalBinding(&input, objects)
		require.NoError(t, err)
		assert.Equal(t, model.ProviderRefundManualReasonPaymentBindingConflict, manualReason)
		require.NoError(t, handleCreemReversalEventWithConfig(context.Background(), fixture.event, "", fixture.cfg))
		var user model.User
		require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
		assert.Equal(t, 1000, user.Quota)
		assertCreemRefundDelivery(t, fixture.event.Id, model.ProviderRefundEventManualReconciliation, model.ProviderRefundManualReasonPaymentBindingConflict)
	})
}
