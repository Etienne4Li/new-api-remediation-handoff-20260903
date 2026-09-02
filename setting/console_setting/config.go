package console_setting

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

type ConsoleSetting struct {
	ApiInfo              string `json:"api_info"`              // 控制台 API 信息 (JSON 数组字符串)
	UptimeKumaGroups     string `json:"uptime_kuma_groups"`    // Uptime Kuma 分组配置 (JSON 数组字符串)
	Announcements        string `json:"announcements"`         // 系统公告 (JSON 数组字符串)
	FAQ                  string `json:"faq"`                   // 常见问题 (JSON 数组字符串)
	ApiInfoEnabled       bool   `json:"api_info_enabled"`      // 是否启用 API 信息面板
	UptimeKumaEnabled    bool   `json:"uptime_kuma_enabled"`   // 是否启用 Uptime Kuma 面板
	AnnouncementsEnabled bool   `json:"announcements_enabled"` // 是否启用系统公告面板
	FAQEnabled           bool   `json:"faq_enabled"`           // 是否启用常见问答面板
}

// 默认配置
var defaultConsoleSetting = ConsoleSetting{
	ApiInfo:              "",
	UptimeKumaGroups:     "",
	Announcements:        "",
	FAQ:                  "",
	ApiInfoEnabled:       true,
	UptimeKumaEnabled:    true,
	AnnouncementsEnabled: true,
	FAQEnabled:           true,
}

// 全局实例
var consoleSetting = defaultConsoleSetting
var consoleSettingMu sync.RWMutex

func init() {
	// 注册到全局配置管理器，键名为 console_setting
	config.GlobalConfig.Register("console_setting", &consoleSetting)
}

// GetConsoleSetting 获取 ConsoleSetting 配置实例
func GetConsoleSetting() *ConsoleSetting {
	consoleSettingMu.RLock()
	defer consoleSettingMu.RUnlock()
	settings := consoleSetting
	return &settings
}

// ConfigSnapshot returns a detached value for persistence and status reads.
func (s *ConsoleSetting) ConfigSnapshot() interface{} {
	if s == nil {
		return ConsoleSetting{}
	}
	consoleSettingMu.RLock()
	defer consoleSettingMu.RUnlock()
	return *s
}

func (s *ConsoleSetting) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&ConsoleSetting{}, values)
	}
	consoleSettingMu.RLock()
	candidate := *s
	consoleSettingMu.RUnlock()
	return applyConsoleConfig(&candidate, values)
}

func (s *ConsoleSetting) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("console setting must not be nil")
	}
	consoleSettingMu.Lock()
	defer consoleSettingMu.Unlock()
	candidate := *s
	if err := applyConsoleConfig(&candidate, values); err != nil {
		return err
	}
	*s = candidate
	return nil
}

func applyConsoleConfig(candidate *ConsoleSetting, values map[string]string) error {
	for key, value := range values {
		switch key {
		case "api_info", "uptime_kuma_groups", "announcements", "faq":
			if err := ValidateConsoleSettings(value, key); err != nil {
				return err
			}
			if key == "api_info" {
				candidate.ApiInfo = value
			}
			if key == "uptime_kuma_groups" {
				candidate.UptimeKumaGroups = value
			}
			if key == "announcements" {
				candidate.Announcements = value
			}
			if key == "faq" {
				candidate.FAQ = value
			}
		case "api_info_enabled":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			candidate.ApiInfoEnabled = parsed
		case "uptime_kuma_enabled":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			candidate.UptimeKumaEnabled = parsed
		case "announcements_enabled":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			candidate.AnnouncementsEnabled = parsed
		case "faq_enabled":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			candidate.FAQEnabled = parsed
		}
	}
	return nil
}
