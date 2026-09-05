package common

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const dynamicCacheIdentityChannelID = 63

// ApplyDynamicPromptCacheIdentity adds a stable, privacy-preserving cache identity
// for channel 63. Explicit prompt_cache_key values remain client-controlled.
// The identity is derived from the authenticated new-api user and a stable
// conversation/session identifier supplied by the client.
func ApplyDynamicPromptCacheIdentity(info *RelayInfo, body []byte) ([]byte, error) {
	if info == nil || info.ChannelMeta == nil || info.ChannelMeta.ChannelId != dynamicCacheIdentityChannelID || len(body) == 0 {
		return body, nil
	}
	conversationID := dynamicConversationID(info, body)
	if conversationID == "" {
		return body, nil
	}

	digest := sha256.Sum256([]byte(itoa(info.UserId) + ":" + conversationID))
	suffix := hex.EncodeToString(digest[:])[:32]
	sessionID := "sub2-" + suffix
	promptCacheKey := "gpt56-v1:" + suffix

	// Keep an explicit client key intact. The generated session header still
	// provides stable affinity for requests that omitted one.
	if strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()) == "" {
		updated, err := sjson.SetBytes(body, "prompt_cache_key", promptCacheKey)
		if err != nil {
			return nil, err
		}
		body = updated
	}
	if info.RuntimeHeadersOverride == nil {
		info.RuntimeHeadersOverride = make(map[string]interface{})
	}
	info.RuntimeHeadersOverride["session_id"] = sessionID
	info.UseRuntimeHeadersOverride = true
	return body, nil
}

func dynamicConversationID(info *RelayInfo, body []byte) string {
	// Header names are case-insensitive. Prefer an explicit conversation ID,
	// then the common session-affinity headers used by OpenAI clients.
	for _, name := range []string{"conversation_id", "x-conversation-id", "session-id", "session_id", "x-session-id", "x-session-affinity", "x-opencode-session"} {
		for key, value := range info.RequestHeaders {
			if strings.EqualFold(key, name) && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	for _, path := range []string{"conversation_id", "metadata.conversation_id", "session_id"} {
		value := strings.TrimSpace(gjson.GetBytes(body, path).String())
		if value != "" {
			return value
		}
	}
	return ""
}

func itoa(v int) string {
	// Avoid pulling formatting into the request hot path.
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
