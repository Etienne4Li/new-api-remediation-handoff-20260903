package setting

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// CreemConfig is the complete runtime configuration used by Creem checkout
// and webhook handlers.  Keep the values together so a hot reload cannot
// expose a mixture of credentials, endpoint mode, and product catalogue to a
// single request.
type CreemConfig struct {
	ApiKey        string
	Products      string
	TestMode      bool
	WebhookSecret string
}

// The legacy exported variables are retained for source compatibility with
// existing integrations and tests.  Production code should use
// GetCreemConfig/UpdateCreemConfig instead of reading or writing them
// directly.
var (
	CreemApiKey        = ""
	CreemProducts      = "[]"
	CreemTestMode      = false
	CreemWebhookSecret = ""
)

var creemConfigMu sync.RWMutex

func creemConfigFromLegacy() CreemConfig {
	return CreemConfig{
		ApiKey:        CreemApiKey,
		Products:      CreemProducts,
		TestMode:      CreemTestMode,
		WebhookSecret: CreemWebhookSecret,
	}
}

func applyCreemConfigToLegacy(cfg CreemConfig) {
	CreemApiKey = cfg.ApiKey
	CreemProducts = cfg.Products
	CreemTestMode = cfg.TestMode
	CreemWebhookSecret = cfg.WebhookSecret
}

// GetCreemConfig returns an immutable copy suitable for one request or
// webhook.  Later hot updates cannot mutate the copy held by the caller.
func GetCreemConfig() CreemConfig {
	// OptionMapRWMutex is the publication fence used by the admin option
	// endpoint and database synchronizer. Taking it before the per-config
	// mutex means a bulk update cannot expose a partially applied Creem tuple.
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	creemConfigMu.RLock()
	defer creemConfigMu.RUnlock()
	return creemConfigFromLegacy()
}

// UpdateCreemConfig atomically publishes related Creem fields.  The callback
// mutates a private copy, and all fields become visible together only after it
// returns.  This is the publication primitive used by the option hot-update
// path and bootstrap loader.
func UpdateCreemConfig(update func(*CreemConfig)) {
	if update == nil {
		return
	}
	creemConfigMu.Lock()
	defer creemConfigMu.Unlock()
	cfg := creemConfigFromLegacy()
	update(&cfg)
	applyCreemConfigToLegacy(cfg)
}
