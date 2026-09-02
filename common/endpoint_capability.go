package common

import (
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/constant"
)

// CanonicalRelayRequestPath normalizes the public relay path used by routing
// and Advanced Custom matching. Playground requests are mounted below /pg,
// while channel routes are defined in their canonical /v1 namespace; keeping
// this conversion in one place prevents capability and route checks from
// disagreeing about the same request.
func CanonicalRelayRequestPath(requestPath string) string {
	path := strings.TrimSpace(requestPath)
	if path == "" {
		return ""
	}
	// RetryParam normally carries URL.Path, but affinity callers and tests may
	// pass a complete URL or a path with a query string.
	if parsed, err := url.Parse(path); err == nil && parsed.Path != "" {
		path = parsed.Path
	}
	if strings.HasPrefix(path, "/pg/") {
		path = strings.TrimPrefix(path, "/pg")
		// genBaseRelayInfo applies the same /v1 prefix to playground routes.
		if !strings.HasPrefix(path, "/v1/") && path != "/v1" {
			path = "/v1" + path
		}
	}
	return path
}

// EndpointTypeForRequestPath maps a public relay path to the endpoint
// capability used by channel selection.  Paths are intentionally matched
// here, instead of importing relay/constant, so the model and middleware
// packages can apply the same gate without introducing an import cycle.
//
// The boolean is false for paths whose capability cannot be inferred (custom
// routes, task endpoints, audio endpoints, etc.).  Unknown paths are left
// eligible and are validated by the selected adaptor at request time.
func EndpointTypeForRequestPath(requestPath string) (constant.EndpointType, bool) {
	path := CanonicalRelayRequestPath(requestPath)
	if path == "" {
		return "", false
	}

	switch {
	case hasEndpointPathPrefix(path, "/v1/responses/compact"):
		return constant.EndpointTypeOpenAIResponseCompact, true
	case hasEndpointPathPrefix(path, "/v1/responses"):
		return constant.EndpointTypeOpenAIResponse, true
	case hasEndpointPathPrefix(path, "/v1/messages"):
		return constant.EndpointTypeAnthropic, true
	case hasEndpointPathPrefix(path, "/v1/alpha/search"):
		return constant.EndpointTypeOpenAIAlphaSearch, true
	case hasEndpointPathPrefix(path, "/v1/chat/completions"),
		hasEndpointPathPrefix(path, "/v1/completions"),
		hasEndpointPathPrefix(path, "/v1/realtime"):
		return constant.EndpointTypeOpenAI, true
	case strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/v1/models/"):
		if strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent") {
			return constant.EndpointTypeGemini, true
		}
	case hasEndpointPathPrefix(path, "/v1/rerank"):
		return constant.EndpointTypeJinaRerank, true
	case hasEndpointPathPrefix(path, "/v1/embeddings"):
		return constant.EndpointTypeEmbeddings, true
	case hasEndpointPathPrefix(path, "/v1/images/generations"),
		hasEndpointPathPrefix(path, "/v1/images/edits"):
		return constant.EndpointTypeImageGeneration, true
	}
	return "", false
}

// hasEndpointPathPrefix avoids treating a similarly named custom route (for
// example /v1/messages-debug) as a built-in endpoint.
func hasEndpointPathPrefix(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	switch path[len(prefix)] {
	case '/', '?', '#':
		return true
	default:
		return false
	}
}

// ChannelSupportsRequestPath reports whether a channel type is a known
// implementation of the endpoint represented by requestPath.  A true result
// for an unknown path means "capability not known" and preserves support for
// user-defined/legacy routes; known unsupported endpoints are filtered before
// a channel is selected.
func ChannelSupportsRequestPath(channelType int, modelName, requestPath string) bool {
	endpoint, ok := EndpointTypeForRequestPath(requestPath)
	if !ok {
		return true
	}
	return ChannelSupportsEndpoint(channelType, modelName, endpoint)
}

// ChannelSupportsEndpoint contains the conservative capability matrix used by
// routing.  It deliberately lists only capabilities that are known to be
// unsupported.  This keeps newly added/custom channel types eligible until an
// adaptor declares otherwise, while preventing the historical Claude and
// Responses adaptor panics from being reached by normal channel selection.
func ChannelSupportsEndpoint(channelType int, modelName string, endpoint constant.EndpointType) bool {
	// Preserve the existing consistency-error path for stale/unknown channel
	// rows.  Capability filtering should never turn a missing channel into a
	// misleading "no available channel" result.
	if channelType == constant.ChannelTypeUnknown {
		return true
	}
	switch endpoint {
	case constant.EndpointTypeAnthropic:
		_, unsupported := unsupportedAnthropicChannels[channelType]
		return !unsupported
	case constant.EndpointTypeOpenAIResponse:
		_, unsupported := unsupportedResponsesChannels[channelType]
		return !unsupported
	case constant.EndpointTypeOpenAIResponseCompact:
		apiType, _ := ChannelType2APIType(channelType)
		return SupportsResponsesCompact(channelType, apiType)
	case constant.EndpointTypeGemini:
		return supportsGeminiEndpoint(channelType)
	default:
		// OpenAI-compatible, image, embedding, and custom endpoints are either
		// implemented by many adaptors or model-dependent.  Leave them to the
		// adaptor/configuration rather than rejecting a potentially valid route.
		return true
	}
}

// These adaptors have no Claude Messages conversion. Keeping this list in a
// dependency-free package lets both the DB and memory-cache selectors enforce
// the same rule. Adaptors that dispatch based on credentials are deliberately
// excluded from this type-only list: a single channel may support Claude for
// one credential and reject it for another. Such adaptors must return a typed
// UnsupportedEndpointError so the retry loop can fail over when the selected
// credential cannot serve the endpoint.
var unsupportedAnthropicChannels = map[int]struct{}{
	constant.ChannelTypePaLM:        {},
	constant.ChannelTypeBaidu:       {},
	constant.ChannelTypeZhipu:       {},
	constant.ChannelTypeXunfei:      {},
	constant.ChannelTypeCohere:      {},
	constant.ChannelTypeDify:        {},
	constant.ChannelTypeJina:        {},
	constant.ChannelCloudflare:      {},
	constant.ChannelTypeMistral:     {},
	constant.ChannelTypeMokaAI:      {},
	constant.ChannelTypeCodex:       {},
	constant.ChannelTypeJimeng:      {},
	constant.ChannelTypeCoze:        {},
	constant.ChannelTypeReplicate:   {},
	constant.ChannelTypeSubmodel:    {},
	constant.ChannelTypeXai:         {},
	constant.ChannelTypeSunoAPI:     {},
	constant.ChannelTypeKling:       {},
	constant.ChannelTypeVidu:        {},
	constant.ChannelTypeDoubaoVideo: {},
	constant.ChannelTypeSora:        {},
}

// Responses support is intentionally stricter than ordinary OpenAI chat.
// The listed adaptors return an UnsupportedEndpointError for Responses (or
// have no Responses request conversion at all).
var unsupportedResponsesChannels = map[int]struct{}{
	constant.ChannelTypeAnthropic:   {},
	constant.ChannelTypePaLM:        {},
	constant.ChannelTypeBaidu:       {},
	constant.ChannelTypeBaiduV2:     {},
	constant.ChannelTypeZhipu:       {},
	constant.ChannelTypeXunfei:      {},
	constant.ChannelTypeCohere:      {},
	constant.ChannelTypeDify:        {},
	constant.ChannelTypeJina:        {},
	constant.ChannelTypeMistral:     {},
	constant.ChannelTypeMokaAI:      {},
	constant.ChannelTypeMoonshot:    {},
	constant.ChannelTypeMiniMax:     {},
	constant.ChannelTypeReplicate:   {},
	constant.ChannelTypeSubmodel:    {},
	constant.ChannelTypeJimeng:      {},
	constant.ChannelTypeCoze:        {},
	constant.ChannelTypeSiliconFlow: {},
	constant.ChannelTypeVertexAi:    {},
}

// Gemini generateContent conversion is implemented by the native Gemini and
// Vertex adaptors, the OpenAI-compatible adaptor, and the multiprotocol
// gateways.  Other providers expose unrelated APIs and should not be selected
// for a Gemini path.
func supportsGeminiEndpoint(channelType int) bool {
	switch channelType {
	case constant.ChannelTypeOpenAI,
		constant.ChannelTypeAzure,
		constant.ChannelTypeOpenAIMax,
		constant.ChannelTypeOhMyGPT,
		constant.ChannelTypeAIProxy,
		constant.ChannelTypeAPI2GPT,
		constant.ChannelTypeAIGC2D,
		constant.ChannelTypeOpenRouter,
		constant.ChannelTypeFastGPT,
		constant.ChannelTypeGemini,
		constant.ChannelTypeVertexAi,
		constant.ChannelTypeXinference,
		constant.ChannelTypeSora,
		constant.ChannelTypeSub2API,
		constant.ChannelTypeNewAPI,
		constant.ChannelTypeAdvancedCustom:
		return true
	default:
		return false
	}
}
