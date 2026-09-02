package operation_setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// CheckinSetting 签到功能配置
type CheckinSetting struct {
	Enabled  bool `json:"enabled"`   // 是否启用签到功能
	MinQuota int  `json:"min_quota"` // 签到最小额度奖励
	MaxQuota int  `json:"max_quota"` // 签到最大额度奖励
}

const (
	DefaultCheckinMinQuota = 1000
	DefaultCheckinMaxQuota = 10000
	// Check-in is a single wallet credit. Keep it within the same signed
	// per-operation quota boundary used by billing so the reward cannot become
	// an unexpectedly enormous grant or overflow a downstream integer.
	MaxCheckinQuota = common.MaxQuota
)

// 默认配置
var checkinSetting = CheckinSetting{
	Enabled:  false,                  // 默认关闭
	MinQuota: DefaultCheckinMinQuota, // 默认最小额度 1000 (约 0.002 USD)
	MaxQuota: DefaultCheckinMaxQuota, // 默认最大额度 10000 (约 0.02 USD)
}

// checkinSettingMu protects the legacy exported pointer used by older
// callers. Runtime readers should use GetCheckinSettingSnapshot, while the
// configuration manager publishes a complete value under this lock.
var checkinSettingMu sync.RWMutex

type checkinSettingFields CheckinSetting

func normalizeCheckinSetting(source CheckinSetting) CheckinSetting {
	if source.MinQuota < 0 || source.MinQuota > MaxCheckinQuota {
		source.MinQuota = DefaultCheckinMinQuota
	}
	if source.MaxQuota < 0 || source.MaxQuota > MaxCheckinQuota {
		source.MaxQuota = DefaultCheckinMaxQuota
	}
	if source.MinQuota > source.MaxQuota {
		// A malformed direct mutation must not produce a negative random span.
		source.MinQuota = DefaultCheckinMinQuota
		source.MaxQuota = DefaultCheckinMaxQuota
	}
	return source
}

func validateCheckinSetting(source CheckinSetting) error {
	if source.MinQuota < 0 || source.MinQuota > MaxCheckinQuota {
		return fmt.Errorf("min_quota must be between 0 and %d", MaxCheckinQuota)
	}
	if source.MaxQuota < 0 || source.MaxQuota > MaxCheckinQuota {
		return fmt.Errorf("max_quota must be between 0 and %d", MaxCheckinQuota)
	}
	if source.MinQuota > source.MaxQuota {
		return fmt.Errorf("min_quota must not exceed max_quota")
	}
	return nil
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("checkin_setting", &checkinSetting)
}

// GetCheckinSetting 获取签到配置
func GetCheckinSetting() *CheckinSetting {
	// Keep the historical pointer-returning API for source compatibility. The
	// pointer is intentionally not replaced; callers that need a request-safe
	// value should use GetCheckinSettingSnapshot.
	checkinSettingMu.RLock()
	defer checkinSettingMu.RUnlock()
	return &checkinSetting
}

// GetCheckinSettingSnapshot returns a detached scalar snapshot for request
// handlers and background workers.
func GetCheckinSettingSnapshot() CheckinSetting {
	checkinSettingMu.RLock()
	defer checkinSettingMu.RUnlock()
	return normalizeCheckinSetting(checkinSetting)
}

// UpdateCheckinSetting publishes related fields atomically. The callback
// receives a private copy, so readers never observe a partially applied
// update.
func UpdateCheckinSetting(update func(*CheckinSetting)) {
	if update == nil {
		return
	}
	checkinSettingMu.Lock()
	defer checkinSettingMu.Unlock()
	next := checkinSetting
	update(&next)
	checkinSetting = normalizeCheckinSetting(next)
}

// ConfigSnapshot implements config.ConfigSnapshotProvider.
func (s *CheckinSetting) ConfigSnapshot() interface{} {
	if s == nil {
		return CheckinSetting{MinQuota: DefaultCheckinMinQuota, MaxQuota: DefaultCheckinMaxQuota}
	}
	checkinSettingMu.RLock()
	defer checkinSettingMu.RUnlock()
	return normalizeCheckinSetting(*s)
}

func (s *CheckinSetting) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		staged := CheckinSetting{MinQuota: DefaultCheckinMinQuota, MaxQuota: DefaultCheckinMaxQuota}
		if err := config.ValidateConfigFromMap(&staged, values); err != nil {
			return err
		}
		return validateCheckinSetting(staged)
	}
	checkinSettingMu.RLock()
	staged := checkinSettingFields(*s)
	checkinSettingMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (s *CheckinSetting) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("checkin setting must not be nil")
	}
	checkinSettingMu.Lock()
	defer checkinSettingMu.Unlock()
	staged := checkinSettingFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	if err := validateCheckinSetting(CheckinSetting(staged)); err != nil {
		return err
	}
	*s = CheckinSetting(staged)
	return nil
}

// IsCheckinEnabled 是否启用签到功能
func IsCheckinEnabled() bool {
	return GetCheckinSettingSnapshot().Enabled
}

// GetCheckinQuotaRange 获取签到额度范围
func GetCheckinQuotaRange() (min, max int) {
	snapshot := GetCheckinSettingSnapshot()
	return snapshot.MinQuota, snapshot.MaxQuota
}
