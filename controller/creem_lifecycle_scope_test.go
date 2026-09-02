package controller

import (
	"net/http/httptest"
	"path/filepath"
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

type creemLifecycleScopeFixture struct {
	subscription *model.UserSubscription
	config       setting.CreemConfig
}

func setupCreemLifecycleScopeFixture(t *testing.T) creemLifecycleScopeFixture {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "creem-lifecycle-scope.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.SubscriptionPlan{},
		&model.SubscriptionOrder{},
		&model.UserSubscription{},
		&model.SubscriptionProviderBindingRecord{},
		&model.ProviderPaymentBinding{},
		&model.PaymentEvent{},
	))
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	cfg := setting.CreemConfig{TestMode: false, WebhookSecret: "creem-lifecycle-original-secret"}
	accountID := creemMerchantSnapshotWithConfig(cfg)
	const (
		userID         = 9451
		planID         = 9451
		tradeNo        = "creem-lifecycle-scope-order"
		subscriptionID = "sub_creem_lifecycle_scope"
		productID      = "prod_creem_lifecycle_scope"
	)
	require.NoError(t, db.Create(&model.User{Id: userID, Username: "creem-lifecycle-scope-user", Group: "default"}).Error)
	plan := &model.SubscriptionPlan{
		Id: planID, Title: "Creem lifecycle scope plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		TotalAmount: 1000, CreemProductId: productID,
	}
	require.NoError(t, db.Create(plan).Error)
	providerTradeNo := "txn_creem_lifecycle_scope"
	order := &model.SubscriptionOrder{
		UserId: userID, PlanId: planID, Money: 10, TradeNo: tradeNo,
		PaymentMethod: model.PaymentMethodCreem, PaymentProvider: model.PaymentProviderCreem,
		Status: common.TopUpStatusSuccess, ProviderTradeNo: &providerTradeNo,
		ProviderMerchantID: accountID, ProviderAmount: "10.00", ProviderCurrency: "USD",
		ProviderProductID: productID, ProviderSubscriptionID: subscriptionID,
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, db.Create(order).Error)
	subscription := &model.UserSubscription{
		UserId: userID, PlanId: planID, AmountTotal: 1000, AmountUsed: 25,
		Status: "active", EndTime: 500,
		ProviderSubscriptionID: subscriptionID, ProviderSubscriptionProvider: model.PaymentProviderCreem,
		SubscriptionOrderTradeNo: tradeNo,
	}
	require.NoError(t, db.Create(subscription).Error)
	require.NoError(t, model.BindSubscriptionProviderIDToOrder(model.SubscriptionProviderBinding{
		OrderTradeNo: tradeNo, Provider: model.PaymentProviderCreem, ProviderSubscriptionID: subscriptionID,
		ProductID: productID, Currency: "USD", Amount: "10.00",
	}))
	require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
		Provider: model.PaymentProviderCreem, ProviderAccountID: accountID,
		ProviderEnvironment: creemProviderEnvironment(cfg),
		OrderTradeNo:        tradeNo, OrderKind: model.PaymentEventOrderSubscription,
		Objects: []model.ProviderPaymentObject{{
			ObjectType: model.ProviderPaymentObjectSubscription,
			ObjectID:   subscriptionID,
		}},
	}))
	return creemLifecycleScopeFixture{subscription: subscription, config: cfg}
}

func creemPaymentFailedLifecycleEvent(eventID, subscriptionID, mode string) *CreemWebhookEvent {
	event := &CreemWebhookEvent{Id: eventID, EventType: "subscription.payment_failed", CreatedAt: 600}
	event.Object.Mode = mode
	event.Object.Subscription = []byte(`{"id":"` + subscriptionID + `","mode":"` + mode + `"}`)
	return event
}

func callCreemLifecycleForTest(t *testing.T, event *CreemWebhookEvent, cfg setting.CreemConfig) error {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/webhook/creem", nil)
	return handleCreemSubscriptionLifecycleWithConfig(ctx, event, cfg)
}

func TestCreemLifecycleScopeAllowsOwningCredential(t *testing.T) {
	fixture := setupCreemLifecycleScopeFixture(t)
	event := creemPaymentFailedLifecycleEvent(
		"evt_creem_lifecycle_owned",
		fixture.subscription.ProviderSubscriptionID,
		creemProviderEnvironment(fixture.config),
	)

	require.NoError(t, callCreemLifecycleForTest(t, event, fixture.config))

	var got model.UserSubscription
	require.NoError(t, model.DB.First(&got, fixture.subscription.Id).Error)
	assert.Equal(t, "past_due", got.Status)
	assert.EqualValues(t, event.CreatedAt, got.ProviderLifecycleEventTime)
	var eventCount int64
	require.NoError(t, model.DB.Model(&model.PaymentEvent{}).
		Where("provider = ? AND provider_trade_no = ?", model.PaymentProviderCreem, event.Id).
		Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)
}

func TestCreemLifecycleScopeRejectsRotatedCredentialAndEnvironment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*setting.CreemConfig)
	}{
		{
			name: "rotated webhook secret",
			mutate: func(cfg *setting.CreemConfig) {
				cfg.WebhookSecret = "creem-lifecycle-rotated-secret"
			},
		},
		{
			name: "different environment",
			mutate: func(cfg *setting.CreemConfig) {
				cfg.TestMode = true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := setupCreemLifecycleScopeFixture(t)
			incomingConfig := fixture.config
			tt.mutate(&incomingConfig)
			event := creemPaymentFailedLifecycleEvent(
				"evt_creem_lifecycle_rejected_"+creemProviderEnvironment(incomingConfig),
				fixture.subscription.ProviderSubscriptionID,
				creemProviderEnvironment(incomingConfig),
			)

			err := callCreemLifecycleForTest(t, event, incomingConfig)
			require.ErrorIs(t, err, model.ErrProviderPaymentBindingNotFound)

			var got model.UserSubscription
			require.NoError(t, model.DB.First(&got, fixture.subscription.Id).Error)
			assert.Equal(t, "active", got.Status)
			assert.EqualValues(t, 25, got.AmountUsed)
			assert.Zero(t, got.ProviderLifecycleEventTime)
			var eventCount int64
			require.NoError(t, model.DB.Model(&model.PaymentEvent{}).
				Where("provider = ? AND provider_trade_no = ?", model.PaymentProviderCreem, event.Id).
				Count(&eventCount).Error)
			assert.Zero(t, eventCount)
		})
	}
}
