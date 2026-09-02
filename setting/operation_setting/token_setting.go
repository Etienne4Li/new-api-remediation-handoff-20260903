package operation_setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

// TokenSetting 令牌相关配置
type TokenSetting struct {
	MaxUserTokens int `json:"max_user_tokens"` // 每用户最大令牌数量
}

const (
	DefaultMaxUserTokens = 1000
	// A token record is consulted by authentication and list/search paths.
	// Keep the administrator-configurable ceiling finite so an accidental
	// MaxInt value cannot turn token creation into an unbounded database load.
	MaxMaxUserTokens = 1_000_000
)

// 默认配置
var tokenSetting = TokenSetting{
	MaxUserTokens: DefaultMaxUserTokens, // 默认每用户最多 1000 个令牌
}

var tokenSettingMu sync.RWMutex

type tokenSettingFields TokenSetting

func normalizeTokenSetting(source TokenSetting) TokenSetting {
	if source.MaxUserTokens < 1 || source.MaxUserTokens > MaxMaxUserTokens {
		source.MaxUserTokens = DefaultMaxUserTokens
	}
	return source
}

func validateTokenSetting(source TokenSetting) error {
	if source.MaxUserTokens < 1 || source.MaxUserTokens > MaxMaxUserTokens {
		return fmt.Errorf("max_user_tokens must be between 1 and %d", MaxMaxUserTokens)
	}
	return nil
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("token_setting", &tokenSetting)
}

// GetTokenSetting 获取令牌配置
func GetTokenSetting() *TokenSetting {
	tokenSettingMu.RLock()
	defer tokenSettingMu.RUnlock()
	return &tokenSetting
}

// GetTokenSettingSnapshot returns a request-safe scalar copy.
func GetTokenSettingSnapshot() TokenSetting {
	tokenSettingMu.RLock()
	defer tokenSettingMu.RUnlock()
	return normalizeTokenSetting(tokenSetting)
}

func UpdateTokenSetting(update func(*TokenSetting)) {
	if update == nil {
		return
	}
	tokenSettingMu.Lock()
	defer tokenSettingMu.Unlock()
	next := tokenSetting
	update(&next)
	tokenSetting = normalizeTokenSetting(next)
}

func (s *TokenSetting) ConfigSnapshot() interface{} {
	if s == nil {
		return TokenSetting{MaxUserTokens: DefaultMaxUserTokens}
	}
	tokenSettingMu.RLock()
	defer tokenSettingMu.RUnlock()
	return normalizeTokenSetting(*s)
}

func (s *TokenSetting) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		staged := TokenSetting{MaxUserTokens: DefaultMaxUserTokens}
		if err := config.ValidateConfigFromMap(&staged, values); err != nil {
			return err
		}
		return validateTokenSetting(staged)
	}
	tokenSettingMu.RLock()
	staged := tokenSettingFields(*s)
	tokenSettingMu.RUnlock()
	if err := config.ValidateConfigFromMap(&staged, values); err != nil {
		return err
	}
	return validateTokenSetting(TokenSetting(staged))
}

func (s *TokenSetting) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("token setting must not be nil")
	}
	tokenSettingMu.Lock()
	defer tokenSettingMu.Unlock()
	staged := tokenSettingFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	if err := validateTokenSetting(TokenSetting(staged)); err != nil {
		return err
	}
	*s = TokenSetting(staged)
	return nil
}

// GetMaxUserTokens 获取每用户最大令牌数量
func GetMaxUserTokens() int {
	return GetTokenSettingSnapshot().MaxUserTokens
}
