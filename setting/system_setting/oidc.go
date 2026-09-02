package system_setting

import (
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

type OIDCSettings struct {
	Enabled               bool   `json:"enabled"`
	DisplayName           string `json:"display_name"`
	ClientId              string `json:"client_id"`
	ClientSecret          string `json:"client_secret"`
	WellKnown             string `json:"well_known"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"user_info_endpoint"`
}

// 默认配置
var defaultOIDCSettings = OIDCSettings{}
var oidcSettingsMu sync.RWMutex

type oidcSettingsFields OIDCSettings

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("oidc", &defaultOIDCSettings)
}

func GetOIDCSettings() *OIDCSettings {
	oidcSettingsMu.RLock()
	defer oidcSettingsMu.RUnlock()
	settings := defaultOIDCSettings
	return &settings
}

func (s *OIDCSettings) ConfigSnapshot() interface{} {
	if s == nil {
		return OIDCSettings{}
	}
	oidcSettingsMu.RLock()
	defer oidcSettingsMu.RUnlock()
	return *s
}

func (s *OIDCSettings) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&OIDCSettings{}, values)
	}
	oidcSettingsMu.RLock()
	staged := oidcSettingsFields(*s)
	oidcSettingsMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (s *OIDCSettings) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("oidc settings must not be nil")
	}
	oidcSettingsMu.Lock()
	defer oidcSettingsMu.Unlock()
	staged := oidcSettingsFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	*s = OIDCSettings(staged)
	return nil
}

// GetEffectiveDisplayName returns the admin-configured display name, or the
// literal "OIDC" when none has been set. Centralizing this fallback keeps the
// default in one place for both the OAuth provider name and the public
// status payload.
func (s *OIDCSettings) GetEffectiveDisplayName() string {
	if trimmed := strings.TrimSpace(s.DisplayName); trimmed != "" {
		return trimmed
	}
	return "OIDC"
}
