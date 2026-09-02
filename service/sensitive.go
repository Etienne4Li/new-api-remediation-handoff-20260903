package service

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting"
)

func CheckSensitiveMessages(messages []dto.Message) ([]string, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	for _, message := range messages {
		arrayContent := message.ParseContent()
		for _, m := range arrayContent {
			if m.Type == "image_url" {
				// TODO: check image url
				continue
			}
			// 检查 text 是否为空
			if m.Text == "" {
				continue
			}
			if ok, words := SensitiveWordContains(m.Text); ok {
				return words, errors.New("sensitive words detected")
			}
		}
	}
	return nil, nil
}

func CheckSensitiveText(text string) (bool, []string) {
	return SensitiveWordContains(text)
}

// SensitiveWordContains 是否包含敏感词，返回是否包含敏感词和敏感词列表
func SensitiveWordContains(text string) (bool, []string) {
	words := setting.GetSensitiveConfig().SensitiveWords
	if len(words) == 0 {
		return false, nil
	}
	if len(text) == 0 {
		return false, nil
	}
	checkText := strings.ToLower(text)
	return AcSearch(checkText, words, true)
}

// SensitiveWordReplace 敏感词替换，返回是否包含敏感词和替换后的文本
func SensitiveWordReplace(text string, returnImmediately bool) (bool, []string, string) {
	words := setting.GetSensitiveConfig().SensitiveWords
	if len(words) == 0 {
		return false, nil, text
	}
	checkText := strings.ToLower(text)
	m := getOrBuildAC(words)
	hits := m.MultiPatternSearch([]rune(checkText), returnImmediately)
	if len(hits) > 0 {
		// Aho-Corasick reports positions in runes, while Go string slices use
		// byte offsets. Convert once to runes so non-ASCII text cannot be split
		// in the middle of a UTF-8 sequence (or panic on a short byte slice).
		textRunes := []rune(text)
		words := make([]string, 0, len(hits))
		var builder strings.Builder
		builder.Grow(len(text))
		lastPos := 0

		for _, hit := range hits {
			pos := hit.Pos
			word := string(hit.Word)
			if pos < lastPos || pos > len(textRunes) {
				continue
			}
			wordLen := len(hit.Word)
			if wordLen == 0 || pos+wordLen > len(textRunes) {
				continue
			}
			builder.WriteString(string(textRunes[lastPos:pos]))
			builder.WriteString("**###**")
			lastPos = pos + wordLen
			words = append(words, word)
		}
		if len(words) == 0 {
			return false, nil, text
		}
		builder.WriteString(string(textRunes[lastPos:]))
		return true, words, builder.String()
	}
	return false, nil, text
}
