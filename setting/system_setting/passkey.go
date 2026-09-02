package system_setting

import (
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

type PasskeySettings struct {
	Enabled              bool   `json:"enabled"`
	RPDisplayName        string `json:"rp_display_name"`
	RPID                 string `json:"rp_id"`
	Origins              string `json:"origins"`
	AllowInsecureOrigin  bool   `json:"allow_insecure_origin"`
	UserVerification     string `json:"user_verification"`
	AttachmentPreference string `json:"attachment_preference"`
}

var defaultPasskeySettings = PasskeySettings{
	Enabled:              false,
	RPDisplayName:        common.GetSystemName(),
	RPID:                 "",
	Origins:              "",
	AllowInsecureOrigin:  false,
	UserVerification:     "preferred",
	AttachmentPreference: "",
}
var passkeySettingsMu sync.RWMutex

type passkeySettingsFields PasskeySettings

func init() {
	config.GlobalConfig.Register("passkey", &defaultPasskeySettings)
}

func GetPasskeySettings() *PasskeySettings {
	passkeySettingsMu.RLock()
	defer passkeySettingsMu.RUnlock()
	systemConfig := GetRuntimeConfig()
	settings := defaultPasskeySettings
	if settings.RPID == "" && systemConfig.ServerAddress != "" {
		// 从ServerAddress提取域名作为RPID
		// ServerAddress可能是 "https://newapi.pro" 这种格式
		serverAddr := strings.TrimSpace(systemConfig.ServerAddress)
		if parsed, err := url.Parse(serverAddr); err == nil && parsed.Host != "" {
			settings.RPID = parsed.Host
		} else {
			settings.RPID = serverAddr
		}
	}
	if settings.Origins == "" || settings.Origins == "[]" {
		settings.Origins = systemConfig.ServerAddress
	}
	return &settings
}

func (s *PasskeySettings) ConfigSnapshot() interface{} {
	if s == nil {
		return PasskeySettings{}
	}
	passkeySettingsMu.RLock()
	defer passkeySettingsMu.RUnlock()
	return *s
}

func (s *PasskeySettings) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&PasskeySettings{}, values)
	}
	passkeySettingsMu.RLock()
	staged := passkeySettingsFields(*s)
	passkeySettingsMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (s *PasskeySettings) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("passkey settings must not be nil")
	}
	passkeySettingsMu.Lock()
	defer passkeySettingsMu.Unlock()
	staged := passkeySettingsFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	*s = PasskeySettings(staged)
	return nil
}
