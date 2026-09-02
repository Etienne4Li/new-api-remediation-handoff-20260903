package operation_setting

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

type MonitorSetting struct {
	AutoTestChannelEnabled bool    `json:"auto_test_channel_enabled"`
	AutoTestChannelMinutes float64 `json:"auto_test_channel_minutes"`
	ChannelTestMode        string  `json:"channel_test_mode"`
	ChannelTestConcurrency int     `json:"channel_test_concurrency"`
}

const (
	ChannelTestModeScheduledAll    = "scheduled_all"
	ChannelTestModeAutoBanOnly     = "auto_ban_only"
	ChannelTestModePassiveRecovery = "passive_recovery"

	ChannelTestConcurrencyOptionKey = "monitor_setting.channel_test_concurrency"
	DefaultChannelTestConcurrency   = 1
	MaxChannelTestConcurrency       = 32
	MaxAutoTestChannelMinutes       = 7 * 24 * 60
)

// 默认配置
var monitorSetting = MonitorSetting{
	AutoTestChannelEnabled: false,
	AutoTestChannelMinutes: 10,
	ChannelTestMode:        ChannelTestModeScheduledAll,
	ChannelTestConcurrency: DefaultChannelTestConcurrency,
}

// monitorSettingMu protects the legacy in-place value. The configuration
// manager uses the custom updater below so a hot reload parses and publishes
// all monitor fields while holding one lock. Request/background code should
// prefer GetMonitorSettingSnapshot.
var monitorSettingMu sync.RWMutex

type monitorSettingFields MonitorSetting

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("monitor_setting", &monitorSetting)
}

func GetMonitorSetting() *MonitorSetting {
	monitorSettingMu.Lock()
	defer monitorSettingMu.Unlock()
	applyMonitorEnvironmentOverridesLocked()
	return &monitorSetting
}

// GetMonitorSettingSnapshot returns one coherent monitor configuration value.
// Environment overrides retain their historical precedence but are applied
// under the same lock instead of mutating the setting during an unlocked read.
func GetMonitorSettingSnapshot() MonitorSetting {
	monitorSettingMu.Lock()
	defer monitorSettingMu.Unlock()
	applyMonitorEnvironmentOverridesLocked()
	return monitorSetting
}

func applyMonitorEnvironmentOverridesLocked() {
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency := common.GetEnvOrDefaultBounded("CHANNEL_TEST_FREQUENCY", 0, 1, MaxAutoTestChannelMinutes)
		if frequency >= 1 {
			monitorSetting.AutoTestChannelEnabled = true
			monitorSetting.AutoTestChannelMinutes = float64(frequency)
			monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
		}
	}
	if enabled, ok := os.LookupEnv("CHANNEL_TEST_ENABLED"); ok {
		parsed, err := strconv.ParseBool(enabled)
		if err == nil {
			monitorSetting.AutoTestChannelEnabled = parsed
		}
	}
	switch monitorSetting.ChannelTestMode {
	case ChannelTestModeAutoBanOnly, ChannelTestModePassiveRecovery:
	default:
		monitorSetting.ChannelTestMode = ChannelTestModeScheduledAll
	}
	monitorSetting.ChannelTestConcurrency = NormalizeChannelTestConcurrency(monitorSetting.ChannelTestConcurrency)
}

// UpdateMonitorSetting atomically publishes related monitor fields.
func UpdateMonitorSetting(update func(*MonitorSetting)) {
	if update == nil {
		return
	}
	monitorSettingMu.Lock()
	defer monitorSettingMu.Unlock()
	next := monitorSetting
	update(&next)
	monitorSetting = next
}

func (s *MonitorSetting) ConfigSnapshot() interface{} {
	if s == nil {
		return MonitorSetting{}
	}
	monitorSettingMu.RLock()
	defer monitorSettingMu.RUnlock()
	return *s
}

func (s *MonitorSetting) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&MonitorSetting{}, values)
	}
	monitorSettingMu.RLock()
	staged := monitorSettingFields(*s)
	monitorSettingMu.RUnlock()
	if err := config.ValidateConfigFromMap(&staged, values); err != nil {
		return err
	}
	return validateMonitorSetting(MonitorSetting(staged))
}

func (s *MonitorSetting) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("monitor setting must not be nil")
	}
	monitorSettingMu.Lock()
	defer monitorSettingMu.Unlock()
	staged := monitorSettingFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	if err := validateMonitorSetting(MonitorSetting(staged)); err != nil {
		return err
	}
	*s = MonitorSetting(staged)
	return nil
}

func validateMonitorSetting(setting MonitorSetting) error {
	if math.IsNaN(setting.AutoTestChannelMinutes) ||
		math.IsInf(setting.AutoTestChannelMinutes, 0) ||
		setting.AutoTestChannelMinutes < 1 ||
		setting.AutoTestChannelMinutes > MaxAutoTestChannelMinutes {
		return fmt.Errorf("auto_test_channel_minutes must be between 1 and %d", MaxAutoTestChannelMinutes)
	}
	if _, err := normalizeMonitorChannelTestMode(setting.ChannelTestMode); err != nil {
		return err
	}
	if setting.ChannelTestConcurrency < 1 || setting.ChannelTestConcurrency > MaxChannelTestConcurrency {
		return fmt.Errorf("channel test concurrency must be between 1 and %d", MaxChannelTestConcurrency)
	}
	return nil
}

func normalizeMonitorChannelTestMode(mode string) (string, error) {
	switch mode {
	case ChannelTestModeScheduledAll, ChannelTestModeAutoBanOnly, ChannelTestModePassiveRecovery:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid channel test mode %q", mode)
	}
}

func NormalizeChannelTestConcurrency(concurrency int) int {
	if concurrency < 1 {
		return DefaultChannelTestConcurrency
	}
	if concurrency > MaxChannelTestConcurrency {
		return MaxChannelTestConcurrency
	}
	return concurrency
}

func ValidateChannelTestConcurrency(value string) error {
	concurrency, err := strconv.Atoi(value)
	if err != nil || concurrency < 1 || concurrency > MaxChannelTestConcurrency {
		return fmt.Errorf("channel test concurrency must be between 1 and %d", MaxChannelTestConcurrency)
	}
	return nil
}
