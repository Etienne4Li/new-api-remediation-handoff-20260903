package model_setting

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

type ChatCompletionsToResponsesPolicy struct {
	Enabled       bool     `json:"enabled"`
	AllChannels   bool     `json:"all_channels"`
	ChannelIDs    []int    `json:"channel_ids,omitempty"`
	ChannelTypes  []int    `json:"channel_types,omitempty"`
	ModelPatterns []string `json:"model_patterns,omitempty"`
}

func (p ChatCompletionsToResponsesPolicy) IsChannelEnabled(channelID int, channelType int) bool {
	if !p.Enabled {
		return false
	}
	if p.AllChannels {
		return true
	}

	if channelID > 0 && len(p.ChannelIDs) > 0 && slices.Contains(p.ChannelIDs, channelID) {
		return true
	}
	if channelType > 0 && len(p.ChannelTypes) > 0 && slices.Contains(p.ChannelTypes, channelType) {
		return true
	}
	return false
}

type GlobalSettings struct {
	PassThroughRequestEnabled        bool                             `json:"pass_through_request_enabled"`
	ThinkingModelBlacklist           []string                         `json:"thinking_model_blacklist"`
	ChatCompletionsToResponsesPolicy ChatCompletionsToResponsesPolicy `json:"chat_completions_to_responses_policy"`
}

// 默认配置
var defaultOpenaiSettings = GlobalSettings{
	PassThroughRequestEnabled: false,
	ThinkingModelBlacklist: []string{
		"moonshotai/kimi-k2-thinking",
		"kimi-k2-thinking",
	},
	ChatCompletionsToResponsesPolicy: ChatCompletionsToResponsesPolicy{
		Enabled:     false,
		AllChannels: true,
	},
}

// 全局实例
var globalSettings = defaultOpenaiSettings
var globalSettingsMu sync.RWMutex

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("global", &globalSettings)
}

func (s *GlobalSettings) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&GlobalSettings{}, values)
	}
	globalSettingsMu.RLock()
	candidate := cloneGlobalSettings(*s)
	globalSettingsMu.RUnlock()
	return applyGlobalConfig(&candidate, values)
}

func (s *GlobalSettings) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("global settings must not be nil")
	}
	globalSettingsMu.Lock()
	defer globalSettingsMu.Unlock()
	candidate := cloneGlobalSettings(*s)
	if err := applyGlobalConfig(&candidate, values); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// ConfigSnapshot returns a detached value so persistence never traverses live
// slices while a hot reload is publishing a new generation.
func (s *GlobalSettings) ConfigSnapshot() interface{} {
	if s == nil {
		return GlobalSettings{}
	}
	globalSettingsMu.RLock()
	defer globalSettingsMu.RUnlock()
	return cloneGlobalSettings(*s)
}

func cloneGlobalSettings(settings GlobalSettings) GlobalSettings {
	settings.ThinkingModelBlacklist = append([]string(nil), settings.ThinkingModelBlacklist...)
	settings.ChatCompletionsToResponsesPolicy.ChannelIDs = append([]int(nil), settings.ChatCompletionsToResponsesPolicy.ChannelIDs...)
	settings.ChatCompletionsToResponsesPolicy.ChannelTypes = append([]int(nil), settings.ChatCompletionsToResponsesPolicy.ChannelTypes...)
	settings.ChatCompletionsToResponsesPolicy.ModelPatterns = append([]string(nil), settings.ChatCompletionsToResponsesPolicy.ModelPatterns...)
	return settings
}

func applyGlobalConfig(candidate *GlobalSettings, values map[string]string) error {
	for key, value := range values {
		switch key {
		case "pass_through_request_enabled":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			candidate.PassThroughRequestEnabled = parsed
		case "thinking_model_blacklist":
			if err := common.UnmarshalJsonStr(value, &candidate.ThinkingModelBlacklist); err != nil {
				return err
			}
		case "chat_completions_to_responses_policy":
			if err := common.UnmarshalJsonStr(value, &candidate.ChatCompletionsToResponsesPolicy); err != nil {
				return err
			}
		default:
			continue
		}
	}
	return nil
}

func GetGlobalSettings() *GlobalSettings {
	globalSettingsMu.RLock()
	defer globalSettingsMu.RUnlock()
	settings := globalSettings
	settings.ThinkingModelBlacklist = append([]string(nil), globalSettings.ThinkingModelBlacklist...)
	settings.ChatCompletionsToResponsesPolicy.ChannelIDs = append([]int(nil), globalSettings.ChatCompletionsToResponsesPolicy.ChannelIDs...)
	settings.ChatCompletionsToResponsesPolicy.ChannelTypes = append([]int(nil), globalSettings.ChatCompletionsToResponsesPolicy.ChannelTypes...)
	settings.ChatCompletionsToResponsesPolicy.ModelPatterns = append([]string(nil), globalSettings.ChatCompletionsToResponsesPolicy.ModelPatterns...)
	return &settings
}

// ShouldPreserveThinkingSuffix 判断模型是否配置为保留 thinking/-nothinking/-low/-high/-medium 后缀
func ShouldPreserveThinkingSuffix(modelName string) bool {
	target := strings.TrimSpace(modelName)
	if target == "" {
		return false
	}

	globalSettingsMu.RLock()
	blacklist := append([]string(nil), globalSettings.ThinkingModelBlacklist...)
	globalSettingsMu.RUnlock()
	for _, entry := range blacklist {
		if strings.TrimSpace(entry) == target {
			return true
		}
	}
	return false
}
