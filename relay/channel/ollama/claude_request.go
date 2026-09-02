package ollama

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/gin-gonic/gin"
)

const (
	ollamaClaudeProtocolContextKey = "ollama_claude_protocol"
	ollamaClaudeProtocolNative     = "native"
	ollamaClaudeProtocolLegacy     = "legacy"
)

// doClaudeRequest first tries Ollama's Anthropic-compatible endpoint.  Older
// Ollama releases do not register that route, so only an unambiguous route
// capability failure is allowed to trigger the legacy /api/chat conversion.
// Model/validation errors are returned unchanged and are never retried as a
// different protocol.
func (a *Adaptor) doClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	if info == nil {
		return nil, fmt.Errorf("ollama relay info is nil")
	}
	// A Gin context may be reused by a retry/test harness.  Never let a marker
	// from an earlier attempt select the wrong response parser.
	if c != nil {
		c.Set(ollamaClaudeProtocolContextKey, "")
	}
	body, err := readClaudeRequestBody(requestBody)
	if err != nil {
		return nil, fmt.Errorf("read Claude request body: %w", err)
	}

	nativeURL := ollamaClaudeURL(info)
	nativeRequest, err := a.newClaudeUpstreamRequest(c, info, nativeURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	nativeResponse, err := channel.DoRequest(c, nativeRequest, info)
	if err != nil {
		return nil, err
	}
	if !isOllamaClaudeRouteMissing(nativeResponse) {
		if c != nil {
			c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolNative)
		}
		return nativeResponse, nil
	}
	// Pass-through requests are explicitly opted out of conversion.  Returning
	// the native response (with its body intact) preserves the upstream error
	// contract instead of silently dropping Claude-only fields on a legacy
	// retry.
	if !ollamaClaudeFallbackAllowed(info) {
		if c != nil {
			c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolNative)
		}
		return nativeResponse, nil
	}

	legacyBody, err := convertClaudeBodyToOllamaChat(c, info, body)
	if err != nil {
		// Keep the native route-missing response available to the normal relay
		// error handler.  In particular, do not close it and then return an error
		// that would discard the provider's actionable response body.
		if c != nil {
			c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolNative)
		}
		return nativeResponse, nil
	}
	legacyURL := ollamaURL(info.ChannelBaseUrl, "/api/chat")
	legacyRequest, err := a.newClaudeUpstreamRequest(c, info, legacyURL, bytes.NewReader(legacyBody))
	if err != nil {
		if c != nil {
			c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolNative)
		}
		return nativeResponse, nil
	}
	legacyResponse, err := channel.DoRequest(c, legacyRequest, info)
	if err != nil {
		if c != nil {
			c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolNative)
		}
		return nativeResponse, nil
	}
	// Only discard the native probe after the legacy request has been accepted.
	// If conversion or transport setup fails, the caller still receives the
	// original route-missing response and its body remains readable.
	service.CloseResponseBodyGracefully(nativeResponse)
	if c != nil {
		c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolLegacy)
	}
	return legacyResponse, nil
}

func ollamaClaudeFallbackAllowed(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	settings := model_setting.GetGlobalSettings()
	channelPassThrough := info.ChannelMeta != nil && info.ChannelMeta.ChannelSetting.PassThroughBodyEnabled
	return settings == nil || (!settings.PassThroughRequestEnabled && !channelPassThrough)
}

func readClaudeRequestBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, fmt.Errorf("request body is nil")
	}
	// A replayable body may be shared with the caller's retry machinery. Use a
	// fresh cursor so deciding whether a fallback is needed never consumes it.
	if replayable, ok := body.(common.ReplayableBody); ok {
		reader, err := replayable.NewReader()
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return common.ReadBodyLimited(reader, replayable.Size(), common.GetMaxRequestBodyBytes())
	}
	return common.ReadBodyLimited(body, -1, common.GetMaxRequestBodyBytes())
}

func (a *Adaptor) newClaudeUpstreamRequest(c *gin.Context, info *relaycommon.RelayInfo, targetURL string, body io.Reader) (*http.Request, error) {
	method := http.MethodPost
	requestContext := context.Background()
	if c != nil && c.Request != nil {
		method = c.Request.Method
		requestContext = c.Request.Context()
	}
	req, err := http.NewRequestWithContext(requestContext, method, targetURL, body)
	if err != nil {
		return nil, fmt.Errorf("new Ollama request: %w", err)
	}
	channel.ApplyUpstreamBodyMetadata(req, body)
	if c != nil {
		headers := req.Header
		if err := a.SetupRequestHeader(c, &headers, info); err != nil {
			return nil, fmt.Errorf("setup Ollama request header: %w", err)
		}
		override, err := channel.ResolveHeaderOverride(info, c)
		if err != nil {
			return nil, err
		}
		for key, value := range override {
			req.Header.Set(key, value)
			if strings.EqualFold(key, "host") {
				req.Host = value
			}
		}
	} else {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+info.ApiKey)
	}
	return req, nil
}

func convertClaudeBodyToOllamaChat(c *gin.Context, info *relaycommon.RelayInfo, body []byte) ([]byte, error) {
	var request dto.ClaudeRequest
	if err := common.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	openAIRequest, err := relayconvert.ClaudeMessagesRequestToOpenAIChat(request, info)
	if err != nil {
		return nil, err
	}
	// Claude's thinking modes have no one-to-one JSON shape in Ollama's
	// legacy API. Preserve the enable/disable decision and, when adaptive
	// effort is explicit, pass the supported effort string through.
	if request.Thinking != nil {
		switch strings.ToLower(strings.TrimSpace(request.Thinking.Type)) {
		case "disabled", "none":
			openAIRequest.Think = []byte("false")
		case "enabled":
			openAIRequest.Think = []byte("true")
		case "adaptive":
			effort := strings.ToLower(strings.TrimSpace(request.GetEfforts()))
			switch effort {
			case "low", "medium", "high", "max":
				openAIRequest.Think, err = common.Marshal(effort)
			default:
				openAIRequest.Think = []byte("true")
			}
			if err != nil {
				return nil, fmt.Errorf("marshal Ollama thinking effort: %w", err)
			}
		}
	}
	legacyRequest, err := openAIChatToOllamaChat(c, openAIRequest)
	if err != nil {
		return nil, err
	}
	return common.Marshal(legacyRequest)
}

func isOllamaClaudeRouteMissing(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	case http.StatusNotFound:
		// An empty 404 is the response emitted by older Ollama routers. For a
		// non-empty body, require explicit route wording; generic "not found"
		// and model errors must not cause a second paid inference request.
		if resp.Body == nil {
			return true
		}
		return classifyOllama404Body(resp, 64<<10)
	default:
		return false
	}
}

// classifyOllama404Body reads only a bounded prefix and always puts consumed
// bytes back in front of the original stream.  A route probe must never make a
// later error handler see an empty body merely because the probe hit a chunked,
// oversized, or short-read response.
func classifyOllama404Body(resp *http.Response, maxBytes int64) bool {
	if resp == nil || resp.Body == nil {
		return true
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	if resp.ContentLength > maxBytes {
		// Do not consume a declared oversized body.  It is not safe to infer a
		// missing route from an unreadable payload; leave it for normal handling.
		return false
	}
	original := resp.Body
	body, err := io.ReadAll(io.LimitReader(original, maxBytes+1))
	if err != nil || int64(len(body)) > maxBytes {
		resp.Body = &ollamaBodyWithCloser{
			Reader: io.MultiReader(bytes.NewReader(body), original),
			closer: original,
		}
		resp.ContentLength = -1
		return false
	}
	// The bounded read consumed the complete body.  Close the exhausted stream
	// and replace it with a replayable in-memory copy for the caller.
	service.CloseResponseBodyGracefully(resp)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return ollamaRouteMissingBody(body)
}

// ollamaBodyWithCloser restores a consumed prefix while retaining ownership of
// the original network body. io.NopCloser would leak the underlying response
// body when the normal relay error handler closes the replacement.
type ollamaBodyWithCloser struct {
	io.Reader
	closer io.Closer
}

func (b *ollamaBodyWithCloser) Close() error {
	if b == nil || b.closer == nil {
		return nil
	}
	return b.closer.Close()
}

func ollamaRouteMissingBody(body []byte) bool {
	text := strings.ToLower(strings.TrimSpace(string(body)))
	if text == "" {
		return true
	}
	if strings.Contains(text, "model not found") || strings.Contains(text, "no such model") {
		return false
	}
	for _, marker := range []string{
		"page not found",
		"route not found",
		"endpoint not found",
		"unknown route",
		"no route",
		"cannot post",
		"cannot get",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return (strings.Contains(text, "route") || strings.Contains(text, "endpoint")) && strings.Contains(text, "not found")
}

func ollamaClaudeProtocol(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(ollamaClaudeProtocolContextKey)
}
