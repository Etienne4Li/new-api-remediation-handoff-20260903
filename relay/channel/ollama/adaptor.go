package ollama

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatGemini))
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	// Native Ollama Anthropic endpoints expect the original Messages document.
	// Keep conversion to the legacy /api/chat dialect private to the fallback
	// path so a successful native request never loses Claude-only fields.
	return request, nil
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatOpenAIAudio))
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatOpenAIImage))
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info == nil {
		return "", errors.New("ollama relay info is nil")
	}
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		return ollamaClaudeURL(info), nil
	default:
		switch info.RelayMode {
		case relayconstant.RelayModeEmbeddings:
			return ollamaURL(info.ChannelBaseUrl, "/api/embed"), nil
		case relayconstant.RelayModeResponses:
			return ollamaURL(info.ChannelBaseUrl, "/v1/responses"), nil
		case relayconstant.RelayModeResponsesCompact:
			return ollamaURL(info.ChannelBaseUrl, "/v1/responses/compact"), nil
		case relayconstant.RelayModeCompletions:
			return ollamaURL(info.ChannelBaseUrl, "/api/generate"), nil
		default:
			return ollamaURL(info.ChannelBaseUrl, "/api/chat"), nil
		}
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	if req == nil {
		return errors.New("request headers are nil")
	}
	if info == nil {
		return errors.New("ollama relay info is nil")
	}
	// SetupApiRequestHeader mirrors client headers, but older callers/tests may
	// not provide a Gin request or a Content-Type.  Never dereference a nil
	// context and never send an empty media type to the upstream API.
	if c != nil && c.Request != nil {
		channel.SetupApiRequestHeader(info, c, req)
	}
	if strings.TrimSpace(req.Get("Content-Type")) == "" && info.RelayFormat == types.RelayFormatClaude {
		req.Set("Content-Type", "application/json")
	}
	req.Set("Authorization", "Bearer "+info.ApiKey)
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		// Ollama's native Anthropic-compatible endpoint follows the Claude
		// Messages authentication contract and expects x-api-key. Keep the
		// Bearer header too for Ollama builds that use it for access control,
		// but never rely on Bearer alone (the native route otherwise answers
		// with an opaque 401/404 depending on the version).
		req.Set("x-api-key", info.ApiKey)
		if c != nil {
			claude.CommonClaudeHeadersOperation(c, req, info)
		}
		anthropicVersion := ""
		if c != nil && c.Request != nil {
			anthropicVersion = c.Request.Header.Get("anthropic-version")
		}
		if anthropicVersion == "" {
			anthropicVersion = "2023-06-01"
		}
		req.Set("anthropic-version", anthropicVersion)
	}
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch info.RelayMode {
	case relayconstant.RelayModeCompletions:
		return openAIToGenerate(c, request)
	default:
		return openAIChatToOllamaChat(c, request)
	}
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatRerank))
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return requestOpenAI2Embeddings(request), nil
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	adaptor := openai.Adaptor{}
	return adaptor.ConvertOpenAIResponsesRequest(c, info, request)
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	if info != nil && info.RelayFormat == types.RelayFormatClaude {
		// Pass-through mode promises byte-for-byte forwarding.  Avoid reading or
		// converting the body just to discover that a legacy fallback is disabled;
		// this also preserves disk-backed replayable bodies for large requests.
		if !ollamaClaudeFallbackAllowed(info) {
			if c != nil {
				c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolNative)
			}
			return channel.DoApiRequest(a, c, info, requestBody)
		}
		return a.doClaudeRequest(c, info, requestBody)
	}
	return channel.DoApiRequest(a, c, info, requestBody)
}

func ollamaURL(baseURL, path string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(strings.TrimSpace(baseURL), "/") + path
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawPath = ""
	return parsed.String()
}

func ollamaClaudeURL(info *relaycommon.RelayInfo) string {
	if info == nil {
		return ""
	}
	result := ollamaURL(info.ChannelBaseUrl, "/v1/messages")
	if !info.IsClaudeBetaQuery && (info.ChannelMeta == nil || !info.ChannelOtherSettings.ClaudeBetaQuery) {
		return result
	}
	parsed, err := url.Parse(result)
	if err != nil {
		return result
	}
	query := parsed.Query()
	query.Set("beta", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		// The native /v1/messages route is the default.  DoResponse is also
		// callable from retry/test seams where the request marker may be absent;
		// treating an empty marker as legacy would feed a valid Anthropic
		// response to the Ollama JSON-lines parser and lose usage/content.  Only
		// an explicit legacy marker selects the compatibility parser.
		if ollamaClaudeProtocol(c) != ollamaClaudeProtocolLegacy {
			// Native Ollama Anthropic responses already implement the Claude
			// Messages contract; do not run them through the legacy OpenAI
			// compatibility parser.
			adaptor := claude.Adaptor{}
			return adaptor.DoResponse(c, resp, info)
		}
		if info.IsStream {
			return ollamaClaudeStreamHandler(c, info, resp)
		}
		return ollamaClaudeHandler(c, info, resp)
	default:
		switch info.RelayMode {
		case relayconstant.RelayModeEmbeddings:
			return ollamaEmbeddingHandler(c, info, resp)
		case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
			adaptor := openai.Adaptor{}
			return adaptor.DoResponse(c, resp, info)
		default:
			if info.IsStream {
				return ollamaStreamHandler(c, info, resp)
			}
			return ollamaChatHandler(c, info, resp)
		}
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
