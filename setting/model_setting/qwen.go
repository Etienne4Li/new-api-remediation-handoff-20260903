package model_setting

import (
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

// QwenSettings defines Qwen model configuration. 注意bool要以enabled结尾才可以生效编辑
type QwenSettings struct {
	SyncImageModels []string `json:"sync_image_models"`
}

// 默认配置
var defaultQwenSettings = QwenSettings{
	SyncImageModels: []string{
		"z-image",
		"qwen-image",
		"wan2.6",
		"wan2.7",
		"qwen-image-edit",
		"qwen-image-edit-max",
		"qwen-image-edit-max-2026-01-16",
		"qwen-image-edit-plus",
		"qwen-image-edit-plus-2025-12-15",
		"qwen-image-edit-plus-2025-10-30",
	},
}

// 全局实例
var qwenSettings = defaultQwenSettings
var qwenSettingsMu sync.RWMutex

type qwenSettingsFields QwenSettings

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("qwen", &qwenSettings)
}

// GetQwenSettings
func GetQwenSettings() *QwenSettings {
	qwenSettingsMu.RLock()
	defer qwenSettingsMu.RUnlock()
	settings := qwenSettings
	settings.SyncImageModels = append([]string(nil), qwenSettings.SyncImageModels...)
	return &settings
}

// IsSyncImageModel
func IsSyncImageModel(model string) bool {
	qwenSettingsMu.RLock()
	defer qwenSettingsMu.RUnlock()
	for _, m := range qwenSettings.SyncImageModels {
		if strings.Contains(model, m) {
			return true
		}
	}
	return false
}

func (s *QwenSettings) ConfigSnapshot() interface{} {
	if s == nil {
		return QwenSettings{}
	}
	qwenSettingsMu.RLock()
	defer qwenSettingsMu.RUnlock()
	settings := *s
	settings.SyncImageModels = append([]string(nil), s.SyncImageModels...)
	return settings
}

func (s *QwenSettings) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&QwenSettings{}, values)
	}
	qwenSettingsMu.RLock()
	staged := qwenSettingsFields(*s)
	qwenSettingsMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (s *QwenSettings) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("qwen settings must not be nil")
	}
	qwenSettingsMu.Lock()
	defer qwenSettingsMu.Unlock()
	staged := qwenSettingsFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	updated := QwenSettings(staged)
	updated.SyncImageModels = append([]string(nil), staged.SyncImageModels...)
	*s = updated
	return nil
}
