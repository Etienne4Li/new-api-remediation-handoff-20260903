package system_setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

type DiscordSettings struct {
	Enabled      bool   `json:"enabled"`
	ClientId     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// 默认配置
var defaultDiscordSettings = DiscordSettings{}
var discordSettingsMu sync.RWMutex

type discordSettingsFields DiscordSettings

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("discord", &defaultDiscordSettings)
}

func GetDiscordSettings() *DiscordSettings {
	discordSettingsMu.RLock()
	defer discordSettingsMu.RUnlock()
	settings := defaultDiscordSettings
	return &settings
}

func (s *DiscordSettings) ConfigSnapshot() interface{} {
	if s == nil {
		return DiscordSettings{}
	}
	discordSettingsMu.RLock()
	defer discordSettingsMu.RUnlock()
	return *s
}

func (s *DiscordSettings) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&DiscordSettings{}, values)
	}
	discordSettingsMu.RLock()
	staged := discordSettingsFields(*s)
	discordSettingsMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (s *DiscordSettings) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("discord settings must not be nil")
	}
	discordSettingsMu.Lock()
	defer discordSettingsMu.Unlock()
	staged := discordSettingsFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	*s = DiscordSettings(staged)
	return nil
}
