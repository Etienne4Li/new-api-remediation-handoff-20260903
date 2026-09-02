package model_setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

// GrokSettings defines Grok model configuration.
type GrokSettings struct {
	ViolationDeductionEnabled bool    `json:"violation_deduction_enabled"`
	ViolationDeductionAmount  float64 `json:"violation_deduction_amount"`
}

var defaultGrokSettings = GrokSettings{
	ViolationDeductionEnabled: true,
	ViolationDeductionAmount:  0.05,
}

var grokSettings = defaultGrokSettings
var grokSettingsMu sync.RWMutex

type grokSettingsFields GrokSettings

func init() {
	config.GlobalConfig.Register("grok", &grokSettings)
}

func GetGrokSettings() *GrokSettings {
	grokSettingsMu.RLock()
	defer grokSettingsMu.RUnlock()
	settings := grokSettings
	return &settings
}

func (s *GrokSettings) ConfigSnapshot() interface{} {
	if s == nil {
		return GrokSettings{}
	}
	grokSettingsMu.RLock()
	defer grokSettingsMu.RUnlock()
	return *s
}

func (s *GrokSettings) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&GrokSettings{}, values)
	}
	grokSettingsMu.RLock()
	staged := grokSettingsFields(*s)
	grokSettingsMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (s *GrokSettings) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("grok settings must not be nil")
	}
	grokSettingsMu.Lock()
	defer grokSettingsMu.Unlock()
	staged := grokSettingsFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	*s = GrokSettings(staged)
	return nil
}
