package common

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
)

func TestEndpointTypeForRequestPath(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		want  constant.EndpointType
		known bool
	}{
		{name: "responses compact", path: "/v1/responses/compact?model=gpt-5", want: constant.EndpointTypeOpenAIResponseCompact, known: true},
		{name: "responses", path: "https://gateway.example/v1/responses", want: constant.EndpointTypeOpenAIResponse, known: true},
		{name: "playground claude", path: "/pg/v1/messages", want: constant.EndpointTypeAnthropic, known: true},
		{name: "playground chat", path: "/pg/chat/completions", want: constant.EndpointTypeOpenAI, known: true},
		{name: "gemini", path: "/v1beta/models/gemini-2.5-flash:generateContent", want: constant.EndpointTypeGemini, known: true},
		{name: "custom", path: "/provider/custom", known: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, known := EndpointTypeForRequestPath(test.path)
			assert.Equal(t, test.known, known)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestCanonicalRelayRequestPath(t *testing.T) {
	tests := map[string]string{
		"/pg/chat/completions":                        "/v1/chat/completions",
		"/pg/chat/completions?model=gpt-5":            "/v1/chat/completions",
		"/pg/v1/messages":                             "/v1/messages",
		"https://gateway.example/pg/chat/completions": "/v1/chat/completions",
		"/v1/responses":                               "/v1/responses",
	}
	for input, want := range tests {
		assert.Equal(t, want, CanonicalRelayRequestPath(input), input)
	}
}

func TestChannelSupportsRequestPathFiltersKnownUnsupportedAdaptors(t *testing.T) {
	unsupported := []struct {
		name        string
		channelType int
	}{
		{name: "PaLM", channelType: constant.ChannelTypePaLM},
		{name: "Baidu", channelType: constant.ChannelTypeBaidu},
		{name: "Zhipu", channelType: constant.ChannelTypeZhipu},
		{name: "Xunfei", channelType: constant.ChannelTypeXunfei},
		{name: "Cohere", channelType: constant.ChannelTypeCohere},
		{name: "Dify", channelType: constant.ChannelTypeDify},
		{name: "Jina", channelType: constant.ChannelTypeJina},
		{name: "Cloudflare", channelType: constant.ChannelCloudflare},
		{name: "Mistral", channelType: constant.ChannelTypeMistral},
		{name: "MokaAI", channelType: constant.ChannelTypeMokaAI},
		{name: "Codex", channelType: constant.ChannelTypeCodex},
		{name: "Jimeng", channelType: constant.ChannelTypeJimeng},
		{name: "Coze", channelType: constant.ChannelTypeCoze},
		{name: "Replicate", channelType: constant.ChannelTypeReplicate},
		{name: "Submodel", channelType: constant.ChannelTypeSubmodel},
		{name: "xAI", channelType: constant.ChannelTypeXai},
		{name: "Suno", channelType: constant.ChannelTypeSunoAPI},
		{name: "Kling", channelType: constant.ChannelTypeKling},
		{name: "Vidu", channelType: constant.ChannelTypeVidu},
		{name: "Doubao video", channelType: constant.ChannelTypeDoubaoVideo},
		{name: "Sora", channelType: constant.ChannelTypeSora},
	}
	for _, test := range unsupported {
		t.Run(test.name, func(t *testing.T) {
			assert.False(t, ChannelSupportsRequestPath(test.channelType, "claude-3", "/v1/messages"))
		})
	}
	assert.True(t, ChannelSupportsRequestPath(constant.ChannelTypeOpenAI, "claude-3", "/v1/messages"))
	assert.False(t, ChannelSupportsRequestPath(constant.ChannelTypeBaiduV2, "gpt-5", "/v1/responses"))
	assert.True(t, ChannelSupportsRequestPath(constant.ChannelTypeOpenAI, "gpt-5", "/v1/responses"))
	assert.True(t, ChannelSupportsRequestPath(constant.ChannelTypeOpenAI, "gpt-5", "/provider/custom"))
}

func TestChannelSupportsRequestPathLeavesCredentialDispatchedTencentEligible(t *testing.T) {
	// Tencent's DispatchAdaptor chooses the implementation from the key shape:
	// TokenHub keys use the OpenAI-compatible Claude path, while native TC3 keys
	// return a typed UnsupportedEndpointError at conversion time. A type-only
	// capability filter must keep the channel eligible so either case can be
	// handled by the normal retry loop.
	assert.True(t, ChannelSupportsRequestPath(constant.ChannelTypeTencent, "claude-3", "/v1/messages"))
}
