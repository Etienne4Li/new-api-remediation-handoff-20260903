package ollama

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOllamaClaudeRouteMissingClassifierIsConservative(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want bool
	}{
		{name: "empty 404", code: http.StatusNotFound, want: true},
		{name: "plain router 404", code: http.StatusNotFound, body: "404 page not found", want: true},
		{name: "json route 404", code: http.StatusNotFound, body: `{"error":"endpoint not found"}`, want: true},
		{name: "method not allowed", code: http.StatusMethodNotAllowed, body: `{"error":"unsupported"}`, want: true},
		{name: "not implemented", code: http.StatusNotImplemented, want: true},
		{name: "model missing", code: http.StatusNotFound, body: `{"error":"model not found"}`, want: false},
		{name: "generic 404", code: http.StatusNotFound, body: `{"error":"not found"}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tt.code, Body: io.NopCloser(strings.NewReader(tt.body))}
			assert.Equal(t, tt.want, isOllamaClaudeRouteMissing(resp))
			if tt.code == http.StatusNotFound {
				// Classification must preserve the body for the eventual upstream
				// error response, regardless of whether fallback was selected.
				got, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Equal(t, tt.body, string(got))
			}
		})
	}
}

func TestConvertClaudeBodyToOllamaChatMapsThinkingModes(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3.1"}}
	tests := []struct {
		name      string
		body      string
		wantThink string
	}{
		{name: "disabled", body: `{"model":"llama3.1","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`, wantThink: "false"},
		{name: "enabled", body: `{"model":"llama3.1","thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`, wantThink: "true"},
		{name: "adaptive effort", body: `{"model":"llama3.1","thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, wantThink: `"high"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := convertClaudeBodyToOllamaChat(nil, info, []byte(tt.body))
			require.NoError(t, err)
			var got OllamaChatRequest
			require.NoError(t, common.Unmarshal(payload, &got))
			assert.Equal(t, tt.wantThink, string(got.Think))
		})
	}
}

func newOllamaClaudeTestContext(t *testing.T, url string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, url+"/v1/messages", strings.NewReader(`{"model":"llama3.1","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	return context, recorder
}

func TestOllamaClaudeDoRequestUsesNativeThenFallbacksOnlyForMissingRoute(t *testing.T) {
	var paths []string
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		bodies = append(bodies, string(body))
		if request.URL.Path == "/v1/messages" {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte("404 page not found"))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"model":"llama3.1","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":1,"eval_count":2}`))
	}))
	defer server.Close()

	c, _ := newOllamaClaudeTestContext(t, server.URL)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, UpstreamModelName: "llama3.1"},
	}
	body := []byte(`{"model":"llama3.1","messages":[{"role":"user","content":"hi"}]}`)
	result, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader(string(body)))
	require.NoError(t, err)
	resp, ok := result.(*http.Response)
	require.True(t, ok)
	require.NotNil(t, resp)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, []string{"/v1/messages", "/api/chat"}, paths)
	assert.Equal(t, body, []byte(bodies[0]))
	assert.Contains(t, bodies[1], `"messages":[{"role":"user","content":"hi"}]`)
	assert.Equal(t, ollamaClaudeProtocolLegacy, ollamaClaudeProtocol(c))
}

func TestOllamaClaudeDoRequestDoesNotFallbackModelNotFound(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"error":"model not found"}`))
	}))
	defer server.Close()
	c, _ := newOllamaClaudeTestContext(t, server.URL)
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, UpstreamModelName: "missing"},
	}
	result, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader(`{"model":"missing","messages":[]}`))
	require.NoError(t, err)
	resp, ok := result.(*http.Response)
	require.True(t, ok)
	require.NotNil(t, resp)
	assert.Equal(t, 1, calls)
	assert.Equal(t, ollamaClaudeProtocolNative, ollamaClaudeProtocol(c))
	data, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "model not found")
}

func TestOllamaClaudeHeadersIncludeNativeAnthropicAPIKey(t *testing.T) {
	c, _ := newOllamaClaudeTestContext(t, "http://ollama")
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3.1", ApiKey: "upstream-secret"},
	}
	header := make(http.Header)
	require.NoError(t, (&Adaptor{}).SetupRequestHeader(c, &header, info))
	assert.Equal(t, "Bearer upstream-secret", header.Get("Authorization"))
	assert.Equal(t, "upstream-secret", header.Get("x-api-key"))
	assert.Equal(t, "2023-06-01", header.Get("anthropic-version"))
}

func TestOllamaClaudeDoResponseDefaultsToNativeWhenMarkerAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","model":"llama3.1","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)),
	}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3.1"}}
	usage, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.(*dto.Usage).TotalTokens)
	assert.Contains(t, recorder.Body.String(), `"type":"message"`)
	assert.Contains(t, recorder.Body.String(), `"input_tokens":3`)
}

func TestOllamaClaudeLegacyResponsePreservesThinkingBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"model":"llama3.1","message":{"role":"assistant","thinking":"step by step","content":"answer"},"done":true,"prompt_eval_count":2,"eval_count":4}`))}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3.1"}}
	usage, apiErr := ollamaClaudeHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.Equal(t, 6, usage.TotalTokens)
	var output dto.ClaudeResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &output))
	require.Len(t, output.Content, 2)
	assert.Equal(t, "thinking", output.Content[0].Type)
	assert.Equal(t, "step by step", *output.Content[0].Thinking)
	assert.Equal(t, "text", output.Content[1].Type)
}
