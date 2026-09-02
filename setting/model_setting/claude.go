package model_setting

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

//var claudeHeadersSettings = map[string][]string{}
//
//var ClaudeThinkingAdapterEnabled = true
//var ClaudeThinkingAdapterMaxTokens = 8192
//var ClaudeThinkingAdapterBudgetTokensPercentage = 0.8

// ClaudeSettings 定义Claude模型的配置
type ClaudeSettings struct {
	HeadersSettings                       map[string]map[string][]string `json:"model_headers_settings"`
	DefaultMaxTokens                      map[string]int                 `json:"default_max_tokens"`
	ThinkingAdapterEnabled                bool                           `json:"thinking_adapter_enabled"`
	ThinkingAdapterBudgetTokensPercentage float64                        `json:"thinking_adapter_budget_tokens_percentage"`
}

// 默认配置
var defaultClaudeSettings = ClaudeSettings{
	HeadersSettings:        map[string]map[string][]string{},
	ThinkingAdapterEnabled: true,
	DefaultMaxTokens: map[string]int{
		"default": 8192,
	},
	ThinkingAdapterBudgetTokensPercentage: 0.8,
}

// 全局实例
var claudeSettings = defaultClaudeSettings
var claudeSettingsMu sync.RWMutex

type claudeSettingsFields ClaudeSettings

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("claude", &claudeSettings)
}

// GetClaudeSettings 获取Claude配置
func GetClaudeSettings() *ClaudeSettings {
	claudeSettingsMu.RLock()
	defer claudeSettingsMu.RUnlock()
	settings := claudeSettings
	settings.HeadersSettings = cloneClaudeHeaders(claudeSettings.HeadersSettings)
	settings.DefaultMaxTokens = cloneIntMap(claudeSettings.DefaultMaxTokens)
	if _, ok := settings.DefaultMaxTokens["default"]; !ok {
		settings.DefaultMaxTokens["default"] = 8192
	}
	return &settings
}

func cloneIntMap(input map[string]int) map[string]int {
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneClaudeHeaders(input map[string]map[string][]string) map[string]map[string][]string {
	output := make(map[string]map[string][]string, len(input))
	for model, headers := range input {
		copyHeaders := make(map[string][]string, len(headers))
		for key, values := range headers {
			copyHeaders[key] = append([]string(nil), values...)
		}
		output[model] = copyHeaders
	}
	return output
}

func (c *ClaudeSettings) WriteHeaders(originModel string, httpHeader *http.Header) {
	if headers, ok := c.HeadersSettings[originModel]; ok {
		for headerKey, headerValues := range headers {
			mergedValues := normalizeHeaderListValues(
				append(append([]string(nil), httpHeader.Values(headerKey)...), headerValues...),
			)
			if len(mergedValues) == 0 {
				continue
			}
			httpHeader.Set(headerKey, strings.Join(mergedValues, ","))
		}
	}
}

func normalizeHeaderListValues(values []string) []string {
	normalizedValues := make([]string, 0, len(values))
	seenValues := make(map[string]struct{}, len(values))
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			normalizedItem := strings.TrimSpace(item)
			if normalizedItem == "" {
				continue
			}
			if _, exists := seenValues[normalizedItem]; exists {
				continue
			}
			seenValues[normalizedItem] = struct{}{}
			normalizedValues = append(normalizedValues, normalizedItem)
		}
	}
	return normalizedValues
}

func (c *ClaudeSettings) GetDefaultMaxTokens(model string) int {
	if maxTokens, ok := c.DefaultMaxTokens[model]; ok {
		return maxTokens
	}
	return c.DefaultMaxTokens["default"]
}

func (c *ClaudeSettings) ConfigSnapshot() interface{} {
	if c == nil {
		return ClaudeSettings{}
	}
	claudeSettingsMu.RLock()
	defer claudeSettingsMu.RUnlock()
	settings := *c
	settings.HeadersSettings = cloneClaudeHeaders(c.HeadersSettings)
	settings.DefaultMaxTokens = cloneIntMap(c.DefaultMaxTokens)
	return settings
}

func (c *ClaudeSettings) ValidateConfigMap(values map[string]string) error {
	if c == nil {
		return config.ValidateConfigFromMap(&ClaudeSettings{}, values)
	}
	claudeSettingsMu.RLock()
	staged := claudeSettingsFields(ClaudeSettings{
		HeadersSettings:                       cloneClaudeHeaders(c.HeadersSettings),
		DefaultMaxTokens:                      cloneIntMap(c.DefaultMaxTokens),
		ThinkingAdapterEnabled:                c.ThinkingAdapterEnabled,
		ThinkingAdapterBudgetTokensPercentage: c.ThinkingAdapterBudgetTokensPercentage,
	})
	claudeSettingsMu.RUnlock()
	return config.ValidateConfigFromMap(&staged, values)
}

func (c *ClaudeSettings) UpdateConfigMap(values map[string]string) error {
	if c == nil {
		return fmt.Errorf("claude settings must not be nil")
	}
	claudeSettingsMu.Lock()
	defer claudeSettingsMu.Unlock()
	staged := claudeSettingsFields(ClaudeSettings{
		HeadersSettings:                       cloneClaudeHeaders(c.HeadersSettings),
		DefaultMaxTokens:                      cloneIntMap(c.DefaultMaxTokens),
		ThinkingAdapterEnabled:                c.ThinkingAdapterEnabled,
		ThinkingAdapterBudgetTokensPercentage: c.ThinkingAdapterBudgetTokensPercentage,
	})
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	updated := ClaudeSettings(staged)
	updated.HeadersSettings = cloneClaudeHeaders(staged.HeadersSettings)
	updated.DefaultMaxTokens = cloneIntMap(staged.DefaultMaxTokens)
	if _, ok := updated.DefaultMaxTokens["default"]; !ok {
		updated.DefaultMaxTokens["default"] = 8192
	}
	*c = updated
	return nil
}

// ValidateClaudeDefaultMaxTokens validates the JSON persisted by the option
// API. Zero stays allowed — the current Messages API accepts max_tokens: 0 as
// cache pre-warming — but negative values are rejected because they would
// wrap into huge unsigned values during request conversion.
func ValidateClaudeDefaultMaxTokens(value string) error {
	var settings map[string]int
	if err := common.UnmarshalJsonStr(value, &settings); err != nil {
		return fmt.Errorf("Claude default max tokens must be a JSON map of model to integer: %w", err)
	}
	if settings == nil {
		return fmt.Errorf("Claude default max tokens must be a JSON map of model to integer")
	}
	for model, maxTokens := range settings {
		if maxTokens < 0 {
			return fmt.Errorf("negative Claude default max_tokens %d for %q", maxTokens, model)
		}
	}
	return nil
}
