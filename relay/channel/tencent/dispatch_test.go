package tencent

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatchAdaptorClaudeCapabilityFollowsCredential(t *testing.T) {
	tests := []struct {
		name          string
		apiKey        string
		wantSupported bool
	}{
		{
			name:          "native TC3 key returns typed unsupported endpoint",
			apiKey:        "1300000000|AKIDxxxxxxxx|secretxxxxxxxx",
			wantSupported: false,
		},
		{
			name:          "TokenHub key uses OpenAI-compatible Claude conversion",
			apiKey:        "sk-xxxxxxxxxxxxxxxx",
			wantSupported: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:       constant.ChannelTypeTencent,
				ApiKey:            tt.apiKey,
				ChannelBaseUrl:    constant.ChannelBaseURLs[constant.ChannelTypeTencent],
				UpstreamModelName: "claude-sonnet-4",
			}, OriginModelName: "claude-sonnet-4"}
			dispatch := &DispatchAdaptor{}
			dispatch.Init(info)

			request := &dto.ClaudeRequest{
				Model:    "claude-sonnet-4",
				Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
			}
			var converted any
			var err error
			require.NotPanics(t, func() {
				converted, err = dispatch.ConvertClaudeRequest(nil, info, request)
			})
			if tt.wantSupported {
				require.NoError(t, err)
				_, ok := converted.(*dto.GeneralOpenAIRequest)
				require.True(t, ok, "TokenHub dispatch should use OpenAI Claude conversion, got %T", converted)
			} else {
				require.Nil(t, converted)
				var capabilityErr *types.UnsupportedEndpointError
				require.ErrorAs(t, err, &capabilityErr)
				require.Equal(t, string(types.RelayFormatClaude), capabilityErr.Endpoint)
			}
		})
	}
}

func TestDispatchAdaptorInit(t *testing.T) {
	tests := []struct {
		name        string
		apiKey      string
		baseURL     string
		wantTC3     bool
		wantBaseURL string
	}{
		{
			name:        "legacy three-segment key selects TC3 adaptor and keeps base url",
			apiKey:      "1300000000|AKIDxxxxxxxx|secretxxxxxxxx",
			baseURL:     constant.ChannelBaseURLs[constant.ChannelTypeTencent],
			wantTC3:     true,
			wantBaseURL: constant.ChannelBaseURLs[constant.ChannelTypeTencent],
		},
		{
			name:        "tokenhub key with default base url rewrites to tokenhub",
			apiKey:      "sk-xxxxxxxxxxxxxxxx",
			baseURL:     constant.ChannelBaseURLs[constant.ChannelTypeTencent],
			wantTC3:     false,
			wantBaseURL: tokenHubBaseURL,
		},
		{
			name:        "tokenhub key with empty base url rewrites to tokenhub",
			apiKey:      "sk-xxxxxxxxxxxxxxxx",
			baseURL:     "",
			wantTC3:     false,
			wantBaseURL: tokenHubBaseURL,
		},
		{
			name:        "tokenhub key with custom base url is preserved",
			apiKey:      "sk-xxxxxxxxxxxxxxxx",
			baseURL:     "https://proxy.example.com",
			wantTC3:     false,
			wantBaseURL: "https://proxy.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType:    constant.ChannelTypeTencent,
				ApiKey:         tt.apiKey,
				ChannelBaseUrl: tt.baseURL,
			}}

			dispatch := &DispatchAdaptor{}
			dispatch.Init(info)

			require.NotNil(t, dispatch.Adaptor)
			if tt.wantTC3 {
				assert.IsType(t, &Adaptor{}, dispatch.Adaptor)
			} else {
				assert.IsType(t, &openai.Adaptor{}, dispatch.Adaptor)
			}
			assert.Equal(t, tt.wantBaseURL, info.ChannelBaseUrl)
		})
	}
}
