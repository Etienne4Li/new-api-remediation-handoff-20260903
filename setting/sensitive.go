package setting

import (
	"strings"
	"sync"
)

var CheckSensitiveEnabled = true
var CheckSensitiveOnPromptEnabled = true

//var CheckSensitiveOnCompletionEnabled = true

// StopOnSensitiveEnabled 如果检测到敏感词，是否立刻停止生成，否则替换敏感词
var StopOnSensitiveEnabled = true

// StreamCacheQueueLength 流模式缓存队列长度，0表示无缓存
var StreamCacheQueueLength = 0

// SensitiveWords 敏感词
// var SensitiveWords []string
var SensitiveWords = []string{
	"test_sensitive",
}

type SensitiveConfig struct {
	CheckEnabled         bool
	CheckOnPromptEnabled bool
	StopOnSensitive      bool
	StreamCacheQueueLen  int
	SensitiveWords       []string
}

var sensitiveConfigMu sync.RWMutex

func cloneSensitiveWords(words []string) []string {
	if words == nil {
		return nil
	}
	return append([]string(nil), words...)
}

func sensitiveConfigFromLegacy() SensitiveConfig {
	return SensitiveConfig{
		CheckEnabled:         CheckSensitiveEnabled,
		CheckOnPromptEnabled: CheckSensitiveOnPromptEnabled,
		StopOnSensitive:      StopOnSensitiveEnabled,
		StreamCacheQueueLen:  StreamCacheQueueLength,
		SensitiveWords:       cloneSensitiveWords(SensitiveWords),
	}
}

func applySensitiveConfigToLegacy(cfg SensitiveConfig) {
	CheckSensitiveEnabled = cfg.CheckEnabled
	CheckSensitiveOnPromptEnabled = cfg.CheckOnPromptEnabled
	StopOnSensitiveEnabled = cfg.StopOnSensitive
	StreamCacheQueueLength = cfg.StreamCacheQueueLen
	SensitiveWords = cloneSensitiveWords(cfg.SensitiveWords)
}

func GetSensitiveConfig() SensitiveConfig {
	sensitiveConfigMu.RLock()
	defer sensitiveConfigMu.RUnlock()
	return sensitiveConfigFromLegacy()
}

func UpdateSensitiveConfig(update func(*SensitiveConfig)) {
	if update == nil {
		return
	}
	sensitiveConfigMu.Lock()
	defer sensitiveConfigMu.Unlock()
	cfg := sensitiveConfigFromLegacy()
	update(&cfg)
	cfg.SensitiveWords = cloneSensitiveWords(cfg.SensitiveWords)
	applySensitiveConfigToLegacy(cfg)
}

func SensitiveWordsToString() string {
	return strings.Join(GetSensitiveConfig().SensitiveWords, "\n")
}

func SensitiveWordsFromString(s string) {
	words := []string{}
	sw := strings.Split(s, "\n")
	for _, w := range sw {
		w = strings.TrimSpace(w)
		if w != "" {
			words = append(words, w)
		}
	}
	UpdateSensitiveConfig(func(cfg *SensitiveConfig) { cfg.SensitiveWords = words })
}

func ShouldCheckPromptSensitive() bool {
	cfg := GetSensitiveConfig()
	return cfg.CheckEnabled && cfg.CheckOnPromptEnabled
}

//func ShouldCheckCompletionSensitive() bool {
//	return CheckSensitiveEnabled && CheckSensitiveOnCompletionEnabled
//}
