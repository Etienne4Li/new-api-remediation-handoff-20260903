package service

import (
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/require"
)

func TestSensitiveWordReplaceUsesRuneOffsets(t *testing.T) {
	previous := setting.GetSensitiveConfig()
	t.Cleanup(func() { setting.UpdateSensitiveConfig(func(cfg *setting.SensitiveConfig) { *cfg = previous }) })
	setting.UpdateSensitiveConfig(func(cfg *setting.SensitiveConfig) {
		cfg.SensitiveWords = []string{"敏感词"}
	})

	found, words, replaced := SensitiveWordReplace("前缀敏感词后缀", false)
	require.True(t, found)
	require.Equal(t, []string{"敏感词"}, words)
	require.Equal(t, "前缀**###**后缀", replaced)
}

func TestSensitiveWordReplaceHandlesMultipleUnicodeHits(t *testing.T) {
	previous := setting.GetSensitiveConfig()
	t.Cleanup(func() { setting.UpdateSensitiveConfig(func(cfg *setting.SensitiveConfig) { *cfg = previous }) })
	setting.UpdateSensitiveConfig(func(cfg *setting.SensitiveConfig) {
		cfg.SensitiveWords = []string{"猫", "狗"}
	})

	found, words, replaced := SensitiveWordReplace("猫和狗", false)
	require.True(t, found)
	require.Len(t, words, 2)
	require.Equal(t, "**###**和**###**", replaced)
}
