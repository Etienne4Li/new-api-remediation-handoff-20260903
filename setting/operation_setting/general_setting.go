package operation_setting

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// 额度展示类型
const (
	QuotaDisplayTypeUSD    = "USD"
	QuotaDisplayTypeCNY    = "CNY"
	QuotaDisplayTypeTokens = "TOKENS"
	QuotaDisplayTypeCustom = "CUSTOM"
)

type GeneralSetting struct {
	DocsLink            string `json:"docs_link"`
	PingIntervalEnabled bool   `json:"ping_interval_enabled"`
	PingIntervalSeconds int    `json:"ping_interval_seconds"`
	// 当前站点额度展示类型：USD / CNY / TOKENS
	QuotaDisplayType string `json:"quota_display_type"`
	// 自定义货币符号，用于 CUSTOM 展示类型
	CustomCurrencySymbol string `json:"custom_currency_symbol"`
	// 自定义货币与美元汇率（1 USD = X Custom）
	CustomCurrencyExchangeRate float64 `json:"custom_currency_exchange_rate"`
}

// 默认配置
var generalSetting = GeneralSetting{
	DocsLink:                   "https://docs.newapi.pro",
	PingIntervalEnabled:        false,
	PingIntervalSeconds:        60,
	QuotaDisplayType:           QuotaDisplayTypeUSD,
	CustomCurrencySymbol:       "¤",
	CustomCurrencyExchangeRate: 1.0,
}

// The registered struct is kept for compatibility with the configuration
// manager, while readers use an immutable copy-on-write value. Reflection
// based hot reloads must never mutate a struct that a request is reading.
var generalSettingState atomic.Pointer[GeneralSetting]
var generalSettingUpdateMu sync.Mutex

const (
	// Ping intervals are used to construct stream keep-alive timers. Keep the
	// value within a practical range so a bad hot-reload cannot create a busy
	// loop or an effectively disabled timer.
	MaxPingIntervalSeconds = 24 * 60 * 60
	MaxCustomExchangeRate  = 1_000_000_000_000.0
)

type generalSettingFields GeneralSetting

func init() {
	initial := generalSetting
	generalSettingState.Store(&initial)
	// 注册到全局配置管理器
	config.GlobalConfig.Register("general_setting", &generalSetting)
}

func currentGeneralSetting() *GeneralSetting {
	if current := generalSettingState.Load(); current != nil {
		return current
	}
	initial := generalSetting
	if generalSettingState.CompareAndSwap(nil, &initial) {
		return &initial
	}
	return generalSettingState.Load()
}

// GetGeneralSetting returns the currently published snapshot pointer. Treat
// it as read-only and use UpdateGeneralSetting for changes. The pointer is
// stable for its lifetime even when a later update publishes a new snapshot.
func GetGeneralSetting() *GeneralSetting {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return currentGeneralSetting()
}

// GetGeneralSettingSnapshot returns a value copy suitable for a request.
func GetGeneralSettingSnapshot() GeneralSetting {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return *currentGeneralSetting()
}

// UpdateGeneralSetting atomically publishes all fields changed by update.
func UpdateGeneralSetting(update func(*GeneralSetting)) {
	if update == nil {
		return
	}
	generalSettingUpdateMu.Lock()
	defer generalSettingUpdateMu.Unlock()
	next := *currentGeneralSetting()
	update(&next)
	published := next
	generalSettingState.Store(&published)
}

// ConfigSnapshot implements config.ConfigSnapshotProvider without acquiring
// OptionMapRWMutex (the config manager may call it while that lock is held).
func (g *GeneralSetting) ConfigSnapshot() interface{} {
	return *currentGeneralSetting()
}

func (g *GeneralSetting) ValidateConfigMap(values map[string]string) error {
	staged := generalSettingFields(*currentGeneralSetting())
	if err := config.ValidateConfigFromMap(&staged, values); err != nil {
		return err
	}
	return validateGeneralSetting(GeneralSetting(staged))
}

func (g *GeneralSetting) UpdateConfigMap(values map[string]string) error {
	generalSettingUpdateMu.Lock()
	defer generalSettingUpdateMu.Unlock()
	staged := generalSettingFields(*currentGeneralSetting())
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	if err := validateGeneralSetting(GeneralSetting(staged)); err != nil {
		return err
	}
	published := GeneralSetting(staged)
	generalSettingState.Store(&published)
	return nil
}

func validateGeneralSetting(setting GeneralSetting) error {
	if setting.PingIntervalSeconds < 1 || setting.PingIntervalSeconds > MaxPingIntervalSeconds {
		return fmt.Errorf("ping_interval_seconds must be between 1 and %d", MaxPingIntervalSeconds)
	}
	if math.IsNaN(setting.CustomCurrencyExchangeRate) ||
		math.IsInf(setting.CustomCurrencyExchangeRate, 0) ||
		setting.CustomCurrencyExchangeRate <= 0 ||
		setting.CustomCurrencyExchangeRate > MaxCustomExchangeRate {
		return fmt.Errorf("custom_currency_exchange_rate must be finite and between 0 and %g", MaxCustomExchangeRate)
	}
	switch setting.QuotaDisplayType {
	case QuotaDisplayTypeUSD, QuotaDisplayTypeCNY, QuotaDisplayTypeTokens, QuotaDisplayTypeCustom:
		return nil
	default:
		return fmt.Errorf("quota_display_type must be one of %s, %s, %s, %s", QuotaDisplayTypeUSD, QuotaDisplayTypeCNY, QuotaDisplayTypeTokens, QuotaDisplayTypeCustom)
	}
}

// IsCurrencyDisplay 是否以货币形式展示（美元或人民币）
func IsCurrencyDisplay() bool {
	return currentGeneralSetting().QuotaDisplayType != QuotaDisplayTypeTokens
}

// IsCNYDisplay 是否以人民币展示
func IsCNYDisplay() bool {
	return currentGeneralSetting().QuotaDisplayType == QuotaDisplayTypeCNY
}

// GetQuotaDisplayType 返回额度展示类型
func GetQuotaDisplayType() string {
	return currentGeneralSetting().QuotaDisplayType
}

// GetCurrencySymbol 返回当前展示类型对应符号
func GetCurrencySymbol() string {
	current := currentGeneralSetting()
	switch current.QuotaDisplayType {
	case QuotaDisplayTypeUSD:
		return "$"
	case QuotaDisplayTypeCNY:
		return "¥"
	case QuotaDisplayTypeCustom:
		if current.CustomCurrencySymbol != "" {
			return current.CustomCurrencySymbol
		}
		return "¤"
	default:
		return ""
	}
}

// GetUsdToCurrencyRate 返回 1 USD = X <currency> 的 X（TOKENS 不适用）
func GetUsdToCurrencyRate(usdToCny float64) float64 {
	current := currentGeneralSetting()
	switch current.QuotaDisplayType {
	case QuotaDisplayTypeUSD:
		return 1
	case QuotaDisplayTypeCNY:
		return usdToCny
	case QuotaDisplayTypeCustom:
		if current.CustomCurrencyExchangeRate > 0 {
			return current.CustomCurrencyExchangeRate
		}
		return 1
	default:
		return 1
	}
}
