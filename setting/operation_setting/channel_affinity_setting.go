package operation_setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

type ChannelAffinityKeySource struct {
	Type string `json:"type"` // context_int, context_string, request_header, gjson
	Key  string `json:"key,omitempty"`
	Path string `json:"path,omitempty"`
}

type ChannelAffinityRule struct {
	Name             string                     `json:"name"`
	ModelRegex       []string                   `json:"model_regex"`
	PathRegex        []string                   `json:"path_regex"`
	UserAgentInclude []string                   `json:"user_agent_include,omitempty"`
	KeySources       []ChannelAffinityKeySource `json:"key_sources"`

	ValueRegex string `json:"value_regex"`
	TTLSeconds int    `json:"ttl_seconds"`

	ParamOverrideTemplate map[string]interface{} `json:"param_override_template,omitempty"`

	SkipRetryOnFailure bool `json:"skip_retry_on_failure"`

	IncludeUsingGroup bool `json:"include_using_group"`
	IncludeModelName  bool `json:"include_model_name"`
	IncludeRuleName   bool `json:"include_rule_name"`
}

type ChannelAffinitySetting struct {
	Enabled               bool                  `json:"enabled"`
	SwitchOnSuccess       bool                  `json:"switch_on_success"`
	KeepOnChannelDisabled bool                  `json:"keep_on_channel_disabled"`
	MaxEntries            int                   `json:"max_entries"`
	DefaultTTLSeconds     int                   `json:"default_ttl_seconds"`
	Rules                 []ChannelAffinityRule `json:"rules"`
}

// Cache settings are administrator-controlled but are consumed on hot paths
// that allocate memory and convert seconds to time.Duration. Keep the limits
// generous for large installations while making malformed values harmless.
const (
	DefaultChannelAffinityMaxEntries = 100_000
	MaxChannelAffinityMaxEntries     = 1_000_000
	DefaultChannelAffinityTTLSeconds = 3600
	MaxChannelAffinityTTLSeconds     = 30 * 24 * 60 * 60
)

// Keep Codex CLI passthrough aligned with upstream. Codex uses lower-case
// header names, while HTTP matching here is case-insensitive.
// Request session/thread headers:
// https://github.com/openai/codex/commit/7c7b4861d88960f7e3bd5b7f30f8351be666dd84
// Responses metadata headers/client_metadata:
// https://github.com/openai/codex/commit/14df0e8833aad0d6d78287954b61ffac67af936c
// x-codex-turn-state response/request round trip:
// https://github.com/openai/codex/commit/ebdd8795e924a8149b616e46ca2ed7848c207a4b
var codexCliPassThroughHeaders = []string{
	"Originator",
	"Session_id",
	"Thread_id",
	"Session-Id",
	"Thread-Id",
	"X-Client-Request-Id",
	"User-Agent",
	"X-Codex-Beta-Features",
	"X-Codex-Turn-State",
	"X-Codex-Turn-Metadata",
	"X-Codex-Window-Id",
	"X-Codex-Parent-Thread-Id",
	//"X-Codex-Installation-Id",
	"X-OpenAI-Subagent",
	"X-OpenAI-Memgen-Request",
	//"X-OAI-Attestation",
	"X-ResponsesAPI-Include-Timing-Metrics",
	"X-OpenAI-Internal-Codex-Responses-Lite",
}

var claudeCliPassThroughHeaders = []string{
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-Os",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
	"User-Agent",
	"X-App",
	"Anthropic-Beta",
	"Anthropic-Dangerous-Direct-Browser-Access",
	"Anthropic-Version",
}

func buildPassHeaderTemplate(headers []string) map[string]interface{} {
	clonedHeaders := make([]string, 0, len(headers))
	clonedHeaders = append(clonedHeaders, headers...)
	return map[string]interface{}{
		"operations": []map[string]interface{}{
			{
				"mode":        "pass_headers",
				"value":       clonedHeaders,
				"keep_origin": true,
			},
		},
	}
}

func buildCodexPassHeaderTemplate() map[string]interface{} {
	requestHeaders := make([]string, 0, len(codexCliPassThroughHeaders))
	requestHeaders = append(requestHeaders, codexCliPassThroughHeaders...)
	return map[string]interface{}{
		"operations": []map[string]interface{}{
			{
				"mode":        "pass_headers",
				"value":       requestHeaders,
				"keep_origin": true,
			},
		},
	}
}

var channelAffinitySetting = ChannelAffinitySetting{
	Enabled:               true,
	SwitchOnSuccess:       true,
	KeepOnChannelDisabled: false,
	MaxEntries:            DefaultChannelAffinityMaxEntries,
	DefaultTTLSeconds:     DefaultChannelAffinityTTLSeconds,
	Rules: []ChannelAffinityRule{
		{
			Name:       "codex cli trace",
			ModelRegex: []string{"^gpt-.*$"},
			PathRegex:  []string{"/v1/responses"},
			KeySources: []ChannelAffinityKeySource{
				{Type: "gjson", Path: "prompt_cache_key"},
			},
			ValueRegex:            "",
			TTLSeconds:            0,
			ParamOverrideTemplate: buildCodexPassHeaderTemplate(),
			SkipRetryOnFailure:    true,
			IncludeUsingGroup:     true,
			IncludeRuleName:       true,
			UserAgentInclude:      nil,
		},
		{
			Name:       "claude cli trace",
			ModelRegex: []string{"^claude-.*$"},
			PathRegex:  []string{"/v1/messages"},
			KeySources: []ChannelAffinityKeySource{
				{Type: "gjson", Path: "metadata.user_id"},
			},
			ValueRegex:            "",
			TTLSeconds:            0,
			ParamOverrideTemplate: buildPassHeaderTemplate(claudeCliPassThroughHeaders),
			SkipRetryOnFailure:    true,
			IncludeUsingGroup:     true,
			IncludeRuleName:       true,
			UserAgentInclude:      nil,
		},
	},
}

var channelAffinitySettingMu sync.RWMutex

type channelAffinitySettingFields ChannelAffinitySetting

func init() {
	config.GlobalConfig.Register("channel_affinity_setting", &channelAffinitySetting)
}

func GetChannelAffinitySetting() *ChannelAffinitySetting {
	channelAffinitySettingMu.RLock()
	defer channelAffinitySettingMu.RUnlock()
	return &channelAffinitySetting
}

func normalizeChannelAffinitySetting(source ChannelAffinitySetting) ChannelAffinitySetting {
	if source.MaxEntries < 0 || source.MaxEntries > MaxChannelAffinityMaxEntries {
		source.MaxEntries = DefaultChannelAffinityMaxEntries
	}
	if source.DefaultTTLSeconds < 0 || source.DefaultTTLSeconds > MaxChannelAffinityTTLSeconds {
		source.DefaultTTLSeconds = DefaultChannelAffinityTTLSeconds
	}
	for i := range source.Rules {
		if source.Rules[i].TTLSeconds < 0 || source.Rules[i].TTLSeconds > MaxChannelAffinityTTLSeconds {
			// Zero means "use the setting default" for a rule.
			source.Rules[i].TTLSeconds = 0
		}
	}
	return source
}

func validateChannelAffinitySetting(source ChannelAffinitySetting) error {
	if source.MaxEntries < 0 || source.MaxEntries > MaxChannelAffinityMaxEntries {
		return fmt.Errorf("max_entries must be between 0 and %d", MaxChannelAffinityMaxEntries)
	}
	if source.DefaultTTLSeconds < 0 || source.DefaultTTLSeconds > MaxChannelAffinityTTLSeconds {
		return fmt.Errorf("default_ttl_seconds must be between 0 and %d", MaxChannelAffinityTTLSeconds)
	}
	for i, rule := range source.Rules {
		if rule.TTLSeconds < 0 || rule.TTLSeconds > MaxChannelAffinityTTLSeconds {
			return fmt.Errorf("rules[%d].ttl_seconds must be between 0 and %d", i, MaxChannelAffinityTTLSeconds)
		}
	}
	return nil
}

// cloneChannelAffinitySetting deep-copies all slices/maps in affinity rules.
// A shallow copy would still let a request race with an administrator
// replacing a nested rule or parameter template.
func cloneChannelAffinitySetting(source ChannelAffinitySetting) ChannelAffinitySetting {
	clone := source
	if source.Rules != nil {
		clone.Rules = make([]ChannelAffinityRule, len(source.Rules))
		for i, rule := range source.Rules {
			clone.Rules[i] = cloneChannelAffinityRule(rule)
		}
	}
	return clone
}

func cloneChannelAffinityRule(source ChannelAffinityRule) ChannelAffinityRule {
	clone := source
	clone.ModelRegex = append([]string(nil), source.ModelRegex...)
	clone.PathRegex = append([]string(nil), source.PathRegex...)
	clone.UserAgentInclude = append([]string(nil), source.UserAgentInclude...)
	clone.KeySources = append([]ChannelAffinityKeySource(nil), source.KeySources...)
	if source.ParamOverrideTemplate != nil {
		clone.ParamOverrideTemplate = cloneInterfaceMap(source.ParamOverrideTemplate)
	}
	return clone
}

// cloneInterfaceMap recursively copies JSON-like maps/slices used by
// ParamOverrideTemplate. Values are otherwise immutable scalars.
func cloneInterfaceMap(source map[string]interface{}) map[string]interface{} {
	clone := make(map[string]interface{}, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]interface{}:
			clone[key] = cloneInterfaceMap(typed)
		case []interface{}:
			clone[key] = cloneInterfaceSlice(typed)
		case []string:
			clone[key] = append([]string(nil), typed...)
		case []map[string]interface{}:
			items := make([]map[string]interface{}, len(typed))
			for i, item := range typed {
				items[i] = cloneInterfaceMap(item)
			}
			clone[key] = items
		case map[string]string:
			items := make(map[string]string, len(typed))
			for nestedKey, nestedValue := range typed {
				items[nestedKey] = nestedValue
			}
			clone[key] = items
		default:
			clone[key] = value
		}
	}
	return clone
}

func cloneInterfaceSlice(source []interface{}) []interface{} {
	clone := make([]interface{}, len(source))
	for i, value := range source {
		switch typed := value.(type) {
		case map[string]interface{}:
			clone[i] = cloneInterfaceMap(typed)
		case []interface{}:
			clone[i] = cloneInterfaceSlice(typed)
		case []string:
			clone[i] = append([]string(nil), typed...)
		default:
			clone[i] = value
		}
	}
	return clone
}

// GetChannelAffinitySettingSnapshot returns a detached deep copy for request
// processing. It prevents nested rule data from changing mid-request.
func GetChannelAffinitySettingSnapshot() ChannelAffinitySetting {
	channelAffinitySettingMu.RLock()
	defer channelAffinitySettingMu.RUnlock()
	return normalizeChannelAffinitySetting(cloneChannelAffinitySetting(channelAffinitySetting))
}

// UpdateChannelAffinitySetting atomically publishes all changed fields.
func UpdateChannelAffinitySetting(update func(*ChannelAffinitySetting)) {
	if update == nil {
		return
	}
	channelAffinitySettingMu.Lock()
	defer channelAffinitySettingMu.Unlock()
	next := cloneChannelAffinitySetting(channelAffinitySetting)
	update(&next)
	channelAffinitySetting = normalizeChannelAffinitySetting(cloneChannelAffinitySetting(next))
}

func (s *ChannelAffinitySetting) ConfigSnapshot() interface{} {
	channelAffinitySettingMu.RLock()
	defer channelAffinitySettingMu.RUnlock()
	return normalizeChannelAffinitySetting(cloneChannelAffinitySetting(channelAffinitySetting))
}

func (s *ChannelAffinitySetting) ValidateConfigMap(values map[string]string) error {
	channelAffinitySettingMu.RLock()
	staged := channelAffinitySettingFields(cloneChannelAffinitySetting(channelAffinitySetting))
	channelAffinitySettingMu.RUnlock()
	if err := config.ValidateConfigFromMap(&staged, values); err != nil {
		return err
	}
	return validateChannelAffinitySetting(ChannelAffinitySetting(staged))
}

func (s *ChannelAffinitySetting) UpdateConfigMap(values map[string]string) error {
	channelAffinitySettingMu.Lock()
	defer channelAffinitySettingMu.Unlock()
	staged := channelAffinitySettingFields(cloneChannelAffinitySetting(channelAffinitySetting))
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	if err := validateChannelAffinitySetting(ChannelAffinitySetting(staged)); err != nil {
		return err
	}
	channelAffinitySetting = cloneChannelAffinitySetting(ChannelAffinitySetting(staged))
	return nil
}
