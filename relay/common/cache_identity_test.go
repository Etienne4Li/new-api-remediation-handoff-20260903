package common

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestApplyDynamicPromptCacheIdentity(t *testing.T) {
	info := &RelayInfo{UserId: 42, ChannelMeta: &ChannelMeta{ChannelId: 63}, RequestHeaders: map[string]string{"X-Conversation-ID": "conv-a"}}
	body := []byte(`{"model":"gpt-5.6-sol","input":[]}`)
	got, err := ApplyDynamicPromptCacheIdentity(info, body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("42:conv-a"))
	suffix := hex.EncodeToString(sum[:])[:32]
	if payload["prompt_cache_key"] != "gpt56-v1:"+suffix {
		t.Fatalf("unexpected key: %v", payload["prompt_cache_key"])
	}
	if info.RuntimeHeadersOverride["session_id"] != "sub2-"+suffix {
		t.Fatalf("unexpected session: %v", info.RuntimeHeadersOverride["session_id"])
	}

	info2 := &RelayInfo{UserId: 42, ChannelMeta: &ChannelMeta{ChannelId: 63}, RequestHeaders: map[string]string{"X-Conversation-ID": "conv-b"}}
	got2, err := ApplyDynamicPromptCacheIdentity(info2, body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) == string(got) {
		t.Fatal("different conversations must not share generated body")
	}
}

func TestApplyDynamicPromptCacheIdentityPreservesExplicitKey(t *testing.T) {
	info := &RelayInfo{UserId: 42, ChannelMeta: &ChannelMeta{ChannelId: 63}, RequestHeaders: map[string]string{"conversation_id": "conv-a"}}
	body := []byte(`{"prompt_cache_key":"client-key"}`)
	got, err := ApplyDynamicPromptCacheIdentity(info, body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("explicit key changed: %s", got)
	}
}

func TestApplyDynamicPromptCacheIdentityScopedToChannel(t *testing.T) {
	info := &RelayInfo{UserId: 42, ChannelMeta: &ChannelMeta{ChannelId: 47}, RequestHeaders: map[string]string{"conversation_id": "conv-a"}}
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	got, err := ApplyDynamicPromptCacheIdentity(info, body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) || info.UseRuntimeHeadersOverride {
		t.Fatal("non-63 channel was modified")
	}
}
