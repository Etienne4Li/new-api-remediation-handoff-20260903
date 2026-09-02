package channel_test

import (
	"testing"

	"github.com/QuantumNous/new-api/relay/channel/aws"
	"github.com/QuantumNous/new-api/relay/channel/baidu"
	"github.com/QuantumNous/new-api/relay/channel/baidu_v2"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/cloudflare"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	"github.com/QuantumNous/new-api/relay/channel/cohere"
	"github.com/QuantumNous/new-api/relay/channel/coze"
	"github.com/QuantumNous/new-api/relay/channel/deepseek"
	"github.com/QuantumNous/new-api/relay/channel/dify"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/jimeng"
	"github.com/QuantumNous/new-api/relay/channel/jina"
	"github.com/QuantumNous/new-api/relay/channel/minimax"
	"github.com/QuantumNous/new-api/relay/channel/mistral"
	"github.com/QuantumNous/new-api/relay/channel/mokaai"
	"github.com/QuantumNous/new-api/relay/channel/moonshot"
	"github.com/QuantumNous/new-api/relay/channel/palm"
	"github.com/QuantumNous/new-api/relay/channel/perplexity"
	"github.com/QuantumNous/new-api/relay/channel/replicate"
	"github.com/QuantumNous/new-api/relay/channel/siliconflow"
	"github.com/QuantumNous/new-api/relay/channel/submodel"
	"github.com/QuantumNous/new-api/relay/channel/tencent"
	"github.com/QuantumNous/new-api/relay/channel/vertex"
	"github.com/QuantumNous/new-api/relay/channel/volcengine"
	"github.com/QuantumNous/new-api/relay/channel/xai"
	"github.com/QuantumNous/new-api/relay/channel/xunfei"
	"github.com/QuantumNous/new-api/relay/channel/zhipu"
	"github.com/QuantumNous/new-api/relay/channel/zhipu_4v"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/require"
)

func TestUnsupportedClaudeConvertersReturnErrorsInsteadOfPanicking(t *testing.T) {
	tests := []struct {
		name string
		call func() (any, error)
	}{
		{name: "baidu", call: func() (any, error) {
			return (&baidu.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "cloudflare", call: func() (any, error) {
			return (&cloudflare.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "codex", call: func() (any, error) {
			return (&codex.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "cohere", call: func() (any, error) {
			return (&cohere.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "coze", call: func() (any, error) {
			return (&coze.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "dify", call: func() (any, error) {
			return (&dify.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "jina", call: func() (any, error) {
			return (&jina.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "jimeng", call: func() (any, error) {
			return (&jimeng.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "mistral", call: func() (any, error) {
			return (&mistral.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "mokaai", call: func() (any, error) {
			return (&mokaai.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "palm", call: func() (any, error) {
			return (&palm.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "replicate", call: func() (any, error) {
			return (&replicate.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "submodel", call: func() (any, error) {
			return (&submodel.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "tencent", call: func() (any, error) {
			return (&tencent.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "xai", call: func() (any, error) {
			return (&xai.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "xunfei", call: func() (any, error) {
			return (&xunfei.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
		{name: "zhipu", call: func() (any, error) {
			return (&zhipu.Adaptor{}).ConvertClaudeRequest(nil, nil, (*dto.ClaudeRequest)(nil))
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				converted any
				err       error
			)
			require.NotPanics(t, func() {
				converted, err = tt.call()
			})
			require.Nil(t, converted)
			var capabilityErr *types.UnsupportedEndpointError
			require.ErrorAs(t, err, &capabilityErr)
			require.Equal(t, string(types.RelayFormatClaude), capabilityErr.Endpoint)
		})
	}
}

func TestUnsupportedResponsesConvertersReturnCapabilityErrors(t *testing.T) {
	tests := []struct {
		name string
		call func() (any, error)
	}{
		{name: "aws", call: func() (any, error) {
			return (&aws.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "baidu", call: func() (any, error) {
			return (&baidu.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "baidu_v2", call: func() (any, error) {
			return (&baidu_v2.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "claude", call: func() (any, error) {
			return (&claude.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "cohere", call: func() (any, error) {
			return (&cohere.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "coze", call: func() (any, error) {
			return (&coze.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "dify", call: func() (any, error) {
			return (&dify.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "jina", call: func() (any, error) {
			return (&jina.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "jimeng", call: func() (any, error) {
			return (&jimeng.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "minimax", call: func() (any, error) {
			return (&minimax.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "mistral", call: func() (any, error) {
			return (&mistral.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "mokaai", call: func() (any, error) {
			return (&mokaai.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "moonshot", call: func() (any, error) {
			return (&moonshot.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "palm", call: func() (any, error) {
			return (&palm.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "replicate", call: func() (any, error) {
			return (&replicate.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "siliconflow", call: func() (any, error) {
			return (&siliconflow.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "submodel", call: func() (any, error) {
			return (&submodel.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "tencent", call: func() (any, error) {
			return (&tencent.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "vertex", call: func() (any, error) {
			return (&vertex.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "xunfei", call: func() (any, error) {
			return (&xunfei.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
		{name: "zhipu", call: func() (any, error) {
			return (&zhipu.Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, dto.OpenAIResponsesRequest{})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			converted, err := tt.call()
			require.Nil(t, converted)
			var capabilityErr *types.UnsupportedEndpointError
			require.ErrorAs(t, err, &capabilityErr)
			require.Equal(t, string(types.RelayFormatOpenAIResponses), capabilityErr.Endpoint)
		})
	}
}

func TestUnsupportedRerankConvertersReturnCapabilityErrors(t *testing.T) {
	tests := []struct {
		name string
		call func() (any, error)
	}{
		{name: "aws", call: func() (any, error) {
			return (&aws.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "baidu", call: func() (any, error) {
			return (&baidu.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "claude", call: func() (any, error) {
			return (&claude.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "deepseek", call: func() (any, error) {
			return (&deepseek.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "dify", call: func() (any, error) {
			return (&dify.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "gemini", call: func() (any, error) {
			return (&gemini.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "minimax", call: func() (any, error) {
			return (&minimax.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "mistral", call: func() (any, error) {
			return (&mistral.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "mokaai", call: func() (any, error) {
			return (&mokaai.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "palm", call: func() (any, error) {
			return (&palm.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "perplexity", call: func() (any, error) {
			return (&perplexity.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "tencent", call: func() (any, error) {
			return (&tencent.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "vertex", call: func() (any, error) {
			return (&vertex.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "volcengine", call: func() (any, error) {
			return (&volcengine.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "xai", call: func() (any, error) {
			return (&xai.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "xunfei", call: func() (any, error) {
			return (&xunfei.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "zhipu", call: func() (any, error) {
			return (&zhipu.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "zhipu_4v", call: func() (any, error) {
			return (&zhipu_4v.Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				converted any
				err       error
			)
			require.NotPanics(t, func() {
				converted, err = tt.call()
			})
			require.Nil(t, converted)
			var capabilityErr *types.UnsupportedEndpointError
			require.ErrorAs(t, err, &capabilityErr)
			require.Equal(t, string(types.RelayFormatRerank), capabilityErr.Endpoint)
		})
	}
}
