package operation_setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

type QuotaSetting struct {
	EnableFreeModelPreConsume bool `json:"enable_free_model_pre_consume"` // 是否对免费模型启用预消耗
}

// 默认配置
var quotaSetting = QuotaSetting{
	EnableFreeModelPreConsume: true,
}

var quotaSettingMu sync.RWMutex

type quotaSettingFields QuotaSetting

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("quota_setting", &quotaSetting)
}

func GetQuotaSetting() *QuotaSetting {
	quotaSettingMu.RLock()
	defer quotaSettingMu.RUnlock()
	return &quotaSetting
}

// GetQuotaSettingSnapshot returns a request-safe value copy.
func GetQuotaSettingSnapshot() QuotaSetting {
	quotaSettingMu.RLock()
	defer quotaSettingMu.RUnlock()
	return quotaSetting
}

// UpdateQuotaSetting publishes a complete scalar snapshot atomically.
func UpdateQuotaSetting(update func(*QuotaSetting)) {
	if update == nil {
		return
	}
	quotaSettingMu.Lock()
	defer quotaSettingMu.Unlock()
	next := quotaSetting
	update(&next)
	quotaSetting = next
}

func (s *QuotaSetting) ConfigSnapshot() interface{} {
	if s == nil {
		return QuotaSetting{}
	}
	quotaSettingMu.RLock()
	defer quotaSettingMu.RUnlock()
	return *s
}

func (s *QuotaSetting) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&QuotaSetting{}, values)
	}
	quotaSettingMu.RLock()
	staged := quotaSettingFields(*s)
	quotaSettingMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (s *QuotaSetting) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("quota setting must not be nil")
	}
	quotaSettingMu.Lock()
	defer quotaSettingMu.Unlock()
	staged := quotaSettingFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	*s = QuotaSetting(staged)
	return nil
}
