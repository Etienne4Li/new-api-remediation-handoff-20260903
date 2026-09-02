package ollama

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOllamaClaudeUsesNativeEndpointAndPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	maxTokens := uint(128)
	request := &dto.ClaudeRequest{
		Model: "llama3.1", System: "be concise", MaxTokens: &maxTokens,
		Messages: []dto.ClaudeMessage{{Role: "user", Content: "hello"}},
	}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3.1", ChannelBaseUrl: "http://ollama"}}

	url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "http://ollama"}})
	require.NoError(t, err)
	assert.Equal(t, "http://ollama/v1/messages", url)

	converted, err := (&Adaptor{}).ConvertClaudeRequest(c, info, request)
	require.NoError(t, err)
	payload, err := json.Marshal(converted)
	require.NoError(t, err)
	var chat OllamaChatRequest
	require.NoError(t, json.Unmarshal(payload, &chat))
	assert.Equal(t, "llama3.1", chat.Model)
	assert.False(t, chat.Stream)
	// Native requests retain Claude's system field and Claude message shape;
	// conversion to Ollama options is reserved for the legacy fallback.
	var native dto.ClaudeRequest
	require.NoError(t, common.Unmarshal(payload, &native))
	assert.Equal(t, "be concise", native.GetStringSystem())
	require.Len(t, native.Messages, 1)
	assert.Equal(t, "hello", native.Messages[0].GetStringContent())
	assert.Nil(t, chat.Options["num_predict"])
}

func TestOllamaClaudeResponseIsAnthropicMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolLegacy)
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"llama3.1","message":{"role":"assistant","content":"hello"},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2}`))}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3.1"}}
	usage, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	assert.Equal(t, 5, usage.(*dto.Usage).TotalTokens)
	var out dto.ClaudeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, "message", out.Type)
	assert.Equal(t, "assistant", out.Role)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "text", out.Content[0].Type)
	assert.Equal(t, "hello", out.Content[0].GetText())
	assert.Equal(t, "end_turn", out.StopReason)
	assert.Equal(t, 3, out.Usage.InputTokens)
	assert.Equal(t, 2, out.Usage.OutputTokens)
}

func TestOllamaClaudeStreamIsAnthropicSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ollamaClaudeProtocolContextKey, ollamaClaudeProtocolLegacy)
	stream := "{" + `"model":"llama3.1","message":{"role":"assistant","content":"hello"},"done":false` + "}\n" + "{" + `"model":"llama3.1","done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2` + "}\n"
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude, IsStream: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "llama3.1"}}
	_, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
	require.Nil(t, apiErr)
	body := w.Body.String()
	assert.Contains(t, body, "event: message_start")
	assert.Contains(t, body, "event: content_block_start")
	assert.Contains(t, body, "event: content_block_delta")
	assert.Contains(t, body, "event: message_delta")
	assert.Contains(t, body, "event: message_stop")
	assert.NotContains(t, body, "[DONE]")
}

func TestOllamaChatHandlerNonStreamToolCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		raw    string
		wantID string
	}{
		{
			name:   "compact json per-line parse path",
			raw:    `{"model":"llama3.1","created_at":"2026-05-27T12:00:00Z","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_upstream","function":{"name":"get_weather","arguments":{"city":"Paris","days":0}}}]},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":7}`,
			wantID: "call_upstream",
		},
		{
			name: "pretty json fallback parse path",
			raw: `{
  "model": "llama3.1",
  "created_at": "2026-05-27T12:00:00Z",
  "message": {
    "role": "assistant",
    "content": "",
    "tool_calls": [
      {
        "function": {
          "name": "get_weather",
          "arguments": {
            "city": "Paris",
            "days": 0
          }
        }
      }
    ]
  },
  "done": true,
  "done_reason": "stop",
  "prompt_eval_count": 5,
  "eval_count": 7
}`,
			wantID: "call_0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.raw)),
			}

			usage, apiErr := ollamaChatHandler(c, &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "fallback-model"},
			}, resp)
			require.Nil(t, apiErr)
			require.NotNil(t, usage)
			assert.Equal(t, 12, usage.TotalTokens)

			var out dto.OpenAITextResponse
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &out))
			require.Len(t, out.Choices, 1)
			assert.Equal(t, constant.FinishReasonToolCalls, out.Choices[0].FinishReason)

			var toolCalls []dto.ToolCallResponse
			require.NoError(t, common.Unmarshal(out.Choices[0].Message.ToolCalls, &toolCalls))
			require.Len(t, toolCalls, 1)
			assert.Equal(t, tt.wantID, toolCalls[0].ID)
			assert.Equal(t, "function", toolCalls[0].Type)
			assert.Equal(t, "get_weather", toolCalls[0].Function.Name)
			assert.Nil(t, toolCalls[0].Index)

			var args map[string]any
			require.NoError(t, common.Unmarshal([]byte(toolCalls[0].Function.Arguments), &args))
			assert.Equal(t, "Paris", args["city"])
			assert.Equal(t, float64(0), args["days"])
		})
	}
}
