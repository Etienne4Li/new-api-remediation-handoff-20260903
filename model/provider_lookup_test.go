package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProviderOrderLookupDistinguishesMissingRows(t *testing.T) {
	// Keep this contract explicit: webhook callers may ACK a genuinely unknown
	// order, but must retry when the database itself cannot answer the lookup.
	truncateTables(t)

	_, err := GetTopUpByTradeNoWithError("missing-topup-for-lookup-test")
	require.ErrorIs(t, err, ErrTopUpNotFound)

	_, err = GetSubscriptionOrderByTradeNoWithError("missing-subscription-for-lookup-test")
	require.ErrorIs(t, err, ErrSubscriptionOrderNotFound)
}

func TestProviderOrderLookupAndStateUpdatesPreserveDatabaseErrors(t *testing.T) {
	// A closed SQL handle deterministically produces a driver error. None of
	// these paths may translate it to a domain "not found" error, because an
	// HTTP webhook would otherwise acknowledge a payment that was not settled.
	originalDB := DB
	brokenDB, err := gorm.Open(sqlite.Open("file:provider-lookup-closed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := brokenDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	DB = brokenDB
	t.Cleanup(func() { DB = originalDB })

	_, err = GetTopUpByTradeNoWithError("db-error-topup")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTopUpNotFound)

	_, err = GetSubscriptionOrderByTradeNoWithError("db-error-subscription")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrSubscriptionOrderNotFound)

	err = UpdatePendingTopUpStatus("db-error-topup", PaymentProviderWaffoPancake, "failed")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTopUpNotFound)

	err = ExpireSubscriptionOrder("db-error-subscription", PaymentProviderWaffoPancake)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrSubscriptionOrderNotFound)

	_, err = SettleTopUpProvider(ProviderSettlement{
		OrderTradeNo:    "db-error-topup",
		Provider:        PaymentProviderWaffoPancake,
		ProviderTradeNo: "provider-db-error",
		MerchantID:      "merchant",
		ProductID:       "product",
		Currency:        "USD",
		Amount:          "10.00",
	}, "127.0.0.1", nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTopUpNotFound)
}
