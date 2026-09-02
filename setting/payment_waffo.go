package setting

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// WaffoConfig is a coherent runtime snapshot of the Waffo gateway settings.
// The credential pair selected by Sandbox, merchant binding, callback URLs,
// and pricing controls must be consumed from one tuple during a request.
type WaffoConfig struct {
	Enabled               bool
	ApiKey                string
	PrivateKey            string
	PublicCert            string
	SandboxPublicCert     string
	SandboxApiKey         string
	SandboxPrivateKey     string
	Sandbox               bool
	MerchantID            string
	NotifyURL             string
	ReturnURL             string
	SubscriptionReturnURL string
	Currency              string
	UnitPrice             float64
	MinTopUp              int
}

var (
	WaffoEnabled               bool
	WaffoApiKey                string
	WaffoPrivateKey            string
	WaffoPublicCert            string
	WaffoSandboxPublicCert     string
	WaffoSandboxApiKey         string
	WaffoSandboxPrivateKey     string
	WaffoSandbox               bool
	WaffoMerchantId            string
	WaffoNotifyUrl             string
	WaffoReturnUrl             string
	WaffoSubscriptionReturnUrl string
	WaffoCurrency              string
	WaffoUnitPrice             float64 = 1.0
	WaffoMinTopUp              int     = 1
)

var waffoConfigMu sync.RWMutex

func waffoConfigFromLegacy() WaffoConfig {
	return WaffoConfig{
		Enabled:               WaffoEnabled,
		ApiKey:                WaffoApiKey,
		PrivateKey:            WaffoPrivateKey,
		PublicCert:            WaffoPublicCert,
		SandboxPublicCert:     WaffoSandboxPublicCert,
		SandboxApiKey:         WaffoSandboxApiKey,
		SandboxPrivateKey:     WaffoSandboxPrivateKey,
		Sandbox:               WaffoSandbox,
		MerchantID:            WaffoMerchantId,
		NotifyURL:             WaffoNotifyUrl,
		ReturnURL:             WaffoReturnUrl,
		SubscriptionReturnURL: WaffoSubscriptionReturnUrl,
		Currency:              WaffoCurrency,
		UnitPrice:             WaffoUnitPrice,
		MinTopUp:              WaffoMinTopUp,
	}
}

func applyWaffoConfigToLegacy(cfg WaffoConfig) {
	WaffoEnabled = cfg.Enabled
	WaffoApiKey = cfg.ApiKey
	WaffoPrivateKey = cfg.PrivateKey
	WaffoPublicCert = cfg.PublicCert
	WaffoSandboxPublicCert = cfg.SandboxPublicCert
	WaffoSandboxApiKey = cfg.SandboxApiKey
	WaffoSandboxPrivateKey = cfg.SandboxPrivateKey
	WaffoSandbox = cfg.Sandbox
	WaffoMerchantId = cfg.MerchantID
	WaffoNotifyUrl = cfg.NotifyURL
	WaffoReturnUrl = cfg.ReturnURL
	WaffoSubscriptionReturnUrl = cfg.SubscriptionReturnURL
	WaffoCurrency = cfg.Currency
	WaffoUnitPrice = cfg.UnitPrice
	WaffoMinTopUp = cfg.MinTopUp
}

// GetWaffoConfig returns an immutable copy for one request/webhook. The
// OptionMap read lock is the publication fence used by bulk option updates.
func GetWaffoConfig() WaffoConfig {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	waffoConfigMu.RLock()
	defer waffoConfigMu.RUnlock()
	return waffoConfigFromLegacy()
}

// UpdateWaffoConfig atomically publishes the related Waffo settings.
func UpdateWaffoConfig(update func(*WaffoConfig)) {
	if update == nil {
		return
	}
	waffoConfigMu.Lock()
	defer waffoConfigMu.Unlock()
	cfg := waffoConfigFromLegacy()
	update(&cfg)
	applyWaffoConfigToLegacy(cfg)
}

// GetWaffoPayMethods 从 options 读取 Waffo 支付方式配置
func GetWaffoPayMethods() []constant.WaffoPayMethod {
	common.OptionMapRWMutex.RLock()
	jsonStr := common.OptionMap["WaffoPayMethods"]
	common.OptionMapRWMutex.RUnlock()

	if jsonStr == "" {
		return copyDefaultWaffoPayMethods()
	}
	var methods []constant.WaffoPayMethod
	if err := common.UnmarshalJsonStr(jsonStr, &methods); err != nil {
		return copyDefaultWaffoPayMethods()
	}
	return methods
}

// SetWaffoPayMethods 序列化 Waffo 支付方式配置并更新 OptionMap
func SetWaffoPayMethods(methods []constant.WaffoPayMethod) error {
	jsonBytes, err := common.Marshal(methods)
	if err != nil {
		return err
	}
	common.OptionMapRWMutex.Lock()
	common.OptionMap["WaffoPayMethods"] = string(jsonBytes)
	common.OptionMapRWMutex.Unlock()
	return nil
}

func copyDefaultWaffoPayMethods() []constant.WaffoPayMethod {
	cp := make([]constant.WaffoPayMethod, len(constant.DefaultWaffoPayMethods))
	copy(cp, constant.DefaultWaffoPayMethods)
	return cp
}

// WaffoPayMethods2JsonString 将默认 WaffoPayMethods 序列化为 JSON 字符串（供 InitOptionMap 使用）
func WaffoPayMethods2JsonString() string {
	jsonBytes, err := common.Marshal(constant.DefaultWaffoPayMethods)
	if err != nil {
		return "[]"
	}
	return string(jsonBytes)
}
