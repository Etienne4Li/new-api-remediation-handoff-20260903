package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/require"
)

func TestNormalizeWaffoPancakeProductTypeAllowsOnlyOneTimeProducts(t *testing.T) {
	for _, value := range []string{"onetime", " ONETIME "} {
		normalized, err := NormalizeWaffoPancakeProductType(value)
		require.NoError(t, err)
		require.Equal(t, WaffoPancakeProductTypeOneTime, normalized)
	}

	for _, value := range []string{"", "subscription", "recurring", "one-time"} {
		_, err := NormalizeWaffoPancakeProductType(value)
		require.ErrorIs(t, err, ErrWaffoPancakeUnsupportedProductType)
	}
}

func TestCreateWaffoPancakeCheckoutRejectsRecurringTypeBeforeProviderCall(t *testing.T) {
	_, err := CreateWaffoPancakeCheckoutSessionWithConfig(context.Background(), &WaffoPancakeCreateSessionParams{
		ProductID:               "PROD_test",
		ProductType:             "subscription",
		Currency:                "USD",
		BuyerIdentity:           "buyer-1",
		OrderMerchantExternalID: "order-1",
	}, setting.WaffoPancakeConfig{})
	require.ErrorIs(t, err, ErrWaffoPancakeUnsupportedProductType)
}

func TestCreateWaffoPancakeCheckoutRejectsConflictingProductTypeMetadata(t *testing.T) {
	_, err := CreateWaffoPancakeCheckoutSessionWithConfig(context.Background(), &WaffoPancakeCreateSessionParams{
		ProductID:               "PROD_test",
		ProductType:             WaffoPancakeProductTypeOneTime,
		Currency:                "USD",
		BuyerIdentity:           "buyer-1",
		OrderMerchantExternalID: "order-1",
		Metadata: map[string]string{
			WaffoPancakeProductTypeMetadataKey: "recurring",
		},
	}, setting.WaffoPancakeConfig{})
	require.ErrorIs(t, err, ErrWaffoPancakeUnsupportedProductType)
}

func TestValidateWaffoPancakeBindingRequiresActiveOneTimeProductInSelectedStore(t *testing.T) {
	catalog := &WaffoPancakeCatalog{Stores: []WaffoPancakeCatalogStore{
		{
			ID:     "STO_good",
			Status: "active",
			OnetimeProducts: []WaffoPancakeCatalogProduct{
				{ID: "PROD_active", Status: "active", ProductType: WaffoPancakeProductTypeOneTime},
				{ID: "PROD_inactive", Status: "inactive", ProductType: WaffoPancakeProductTypeOneTime},
				{ID: "PROD_recurring", Status: "active", ProductType: "recurring"},
			},
		},
		{ID: "STO_other", Status: "active"},
	}}

	require.NoError(t, validateWaffoPancakeBindingInCatalog(catalog, "STO_good", "PROD_active"))
	for _, tc := range []struct {
		name      string
		storeID   string
		productID string
	}{
		{name: "inactive product", storeID: "STO_good", productID: "PROD_inactive"},
		{name: "recurring product", storeID: "STO_good", productID: "PROD_recurring"},
		{name: "wrong store", storeID: "STO_other", productID: "PROD_active"},
		{name: "missing store", storeID: "STO_missing", productID: "PROD_active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, validateWaffoPancakeBindingInCatalog(catalog, tc.storeID, tc.productID), ErrWaffoPancakeBindingInvalid)
		})
	}
}

func TestValidateWaffoPancakeBindingRejectsInactiveStore(t *testing.T) {
	catalog := &WaffoPancakeCatalog{Stores: []WaffoPancakeCatalogStore{
		{
			ID:     "STO_suspended",
			Status: "suspended",
			OnetimeProducts: []WaffoPancakeCatalogProduct{
				{ID: "PROD_active", Status: "active", ProductType: WaffoPancakeProductTypeOneTime},
			},
		},
	}}
	require.ErrorIs(t, validateWaffoPancakeBindingInCatalog(catalog, "STO_suspended", "PROD_active"), ErrWaffoPancakeBindingInvalid)
}
