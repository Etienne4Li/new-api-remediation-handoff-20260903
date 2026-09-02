package setting

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// Waffo Pancake hosted checkout configuration. Gateway is enabled once
// MerchantID + PrivateKey + ProductID are populated (no separate Enabled
// flag, matching Stripe / Creem). StoreID + ProductID are operator-bound
// via SaveWaffoPancakeConfig.
var (
	WaffoPancakeMerchantID string
	WaffoPancakePrivateKey string
	WaffoPancakeReturnURL  string
	WaffoPancakeUnitPrice  float64 = 1.0
	WaffoPancakeMinTopUp   int     = 1
	WaffoPancakeStoreID    string
	WaffoPancakeProductID  string
)

// WaffoPancakeConfig is an immutable request-time snapshot of the hosted
// checkout binding and pricing controls.
type WaffoPancakeConfig struct {
	MerchantID string
	PrivateKey string
	ReturnURL  string
	UnitPrice  float64
	MinTopUp   int
	StoreID    string
	ProductID  string
}

var waffoPancakeConfigMu sync.RWMutex

func waffoPancakeConfigFromLegacy() WaffoPancakeConfig {
	return WaffoPancakeConfig{
		MerchantID: WaffoPancakeMerchantID,
		PrivateKey: WaffoPancakePrivateKey,
		ReturnURL:  WaffoPancakeReturnURL,
		UnitPrice:  WaffoPancakeUnitPrice,
		MinTopUp:   WaffoPancakeMinTopUp,
		StoreID:    WaffoPancakeStoreID,
		ProductID:  WaffoPancakeProductID,
	}
}

func applyWaffoPancakeConfigToLegacy(cfg WaffoPancakeConfig) {
	WaffoPancakeMerchantID = cfg.MerchantID
	WaffoPancakePrivateKey = cfg.PrivateKey
	WaffoPancakeReturnURL = cfg.ReturnURL
	WaffoPancakeUnitPrice = cfg.UnitPrice
	WaffoPancakeMinTopUp = cfg.MinTopUp
	WaffoPancakeStoreID = cfg.StoreID
	WaffoPancakeProductID = cfg.ProductID
}

// GetWaffoPancakeConfig returns an immutable copy suitable for one request.
// OptionMapRWMutex is acquired first to fence bulk option publications.
func GetWaffoPancakeConfig() WaffoPancakeConfig {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	waffoPancakeConfigMu.RLock()
	defer waffoPancakeConfigMu.RUnlock()
	return waffoPancakeConfigFromLegacy()
}

// UpdateWaffoPancakeConfig atomically publishes all Pancake settings.
func UpdateWaffoPancakeConfig(update func(*WaffoPancakeConfig)) {
	if update == nil {
		return
	}
	waffoPancakeConfigMu.Lock()
	defer waffoPancakeConfigMu.Unlock()
	cfg := waffoPancakeConfigFromLegacy()
	update(&cfg)
	applyWaffoPancakeConfigToLegacy(cfg)
}
