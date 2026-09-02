package system_setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

type LegalSettings struct {
	UserAgreement string `json:"user_agreement"`
	PrivacyPolicy string `json:"privacy_policy"`
}

var defaultLegalSettings = LegalSettings{
	UserAgreement: "",
	PrivacyPolicy: "",
}
var legalSettingsMu sync.RWMutex

type legalSettingsFields LegalSettings

func init() {
	config.GlobalConfig.Register("legal", &defaultLegalSettings)
}

func GetLegalSettings() *LegalSettings {
	legalSettingsMu.RLock()
	defer legalSettingsMu.RUnlock()
	settings := defaultLegalSettings
	return &settings
}

func (s *LegalSettings) ConfigSnapshot() interface{} {
	if s == nil {
		return LegalSettings{}
	}
	legalSettingsMu.RLock()
	defer legalSettingsMu.RUnlock()
	return *s
}

func (s *LegalSettings) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&LegalSettings{}, values)
	}
	legalSettingsMu.RLock()
	staged := legalSettingsFields(*s)
	legalSettingsMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (s *LegalSettings) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("legal settings must not be nil")
	}
	legalSettingsMu.Lock()
	defer legalSettingsMu.Unlock()
	staged := legalSettingsFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	*s = LegalSettings(staged)
	return nil
}
