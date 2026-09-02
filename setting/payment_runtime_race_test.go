package setting

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWaffoConfigPublishesCoherentSnapshots(t *testing.T) {
	previous := GetWaffoConfig()
	t.Cleanup(func() { UpdateWaffoConfig(func(cfg *WaffoConfig) { *cfg = previous }) })

	first := WaffoConfig{
		Enabled:               true,
		ApiKey:                "api-a",
		PrivateKey:            "private-a",
		PublicCert:            "public-a",
		SandboxPublicCert:     "sandbox-public-a",
		SandboxApiKey:         "sandbox-api-a",
		SandboxPrivateKey:     "sandbox-private-a",
		Sandbox:               false,
		MerchantID:            "merchant-a",
		NotifyURL:             "https://a.example/notify",
		ReturnURL:             "https://a.example/return",
		SubscriptionReturnURL: "https://a.example/subscription",
		Currency:              "USD",
		UnitPrice:             1.25,
		MinTopUp:              1,
	}
	second := first
	second.ApiKey = "api-b"
	second.PrivateKey = "private-b"
	second.PublicCert = "public-b"
	second.Sandbox = true
	second.MerchantID = "merchant-b"
	second.Currency = "EUR"
	second.UnitPrice = 2.5
	second.MinTopUp = 5
	UpdateWaffoConfig(func(cfg *WaffoConfig) { *cfg = first })

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			value := first
			if i%2 == 1 {
				value = second
			}
			UpdateWaffoConfig(func(cfg *WaffoConfig) { *cfg = value })
		}
	}()

	seen := make(chan WaffoConfig, 128)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			seen <- GetWaffoConfig()
		}
	}()
	close(start)
	wg.Wait()
	close(seen)
	for got := range seen {
		require.Truef(t, got == first || got == second, "mixed Waffo snapshot: %+v", got)
	}
}

func TestWaffoPancakeConfigPublishesCoherentSnapshots(t *testing.T) {
	previous := GetWaffoPancakeConfig()
	t.Cleanup(func() {
		UpdateWaffoPancakeConfig(func(cfg *WaffoPancakeConfig) { *cfg = previous })
	})

	first := WaffoPancakeConfig{
		MerchantID: "merchant-a", PrivateKey: "private-a", ReturnURL: "https://a.example/return",
		UnitPrice: 1.25, MinTopUp: 1, StoreID: "store-a", ProductID: "product-a",
	}
	second := WaffoPancakeConfig{
		MerchantID: "merchant-b", PrivateKey: "private-b", ReturnURL: "https://b.example/return",
		UnitPrice: 2.5, MinTopUp: 5, StoreID: "store-b", ProductID: "product-b",
	}
	UpdateWaffoPancakeConfig(func(cfg *WaffoPancakeConfig) { *cfg = first })

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			value := first
			if i%2 == 1 {
				value = second
			}
			UpdateWaffoPancakeConfig(func(cfg *WaffoPancakeConfig) { *cfg = value })
		}
	}()

	seen := make(chan WaffoPancakeConfig, 128)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			seen <- GetWaffoPancakeConfig()
		}
	}()
	close(start)
	wg.Wait()
	close(seen)
	for got := range seen {
		require.Truef(t, got == first || got == second, "mixed Waffo Pancake snapshot: %+v", got)
	}
}
