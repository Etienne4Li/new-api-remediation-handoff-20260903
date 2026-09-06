package gemini

import (
	"bytes"
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
	"github.com/stretchr/testify/require"
)

// Feeds the fragmented upstream stream observed on 2026-09-06 through the
// OpenAI-compatible stream handler and checks the client sees one complete,
// named tool call instead of a named-but-empty call plus a nameless one.
func TestGeminiChatStreamHandlerRepairsFragmentedToolCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	oldStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 300
	t.Cleanup(func() {
		constant.StreamingTimeout = oldStreamingTimeout
	})

	info := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "gemini-3.8-flash",
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gemini-3.8-flash",
		},
	}

	streamBody := []byte("data: " + fragmentedChunk1 + "\n\n" +
		"data: " + fragmentedChunk2 + "\n\n" +
		"data: " + fragmentedChunk3 + "\n\n")
	resp := &http.Response{Body: io.NopCloser(bytes.NewReader(streamBody))}

	_, newAPIError := GeminiChatStreamHandler(c, info, resp)
	require.Nil(t, newAPIError)
	t.Logf("client stream:\n%s", recorder.Body.String())

	type agg struct {
		name string
		args strings.Builder
	}
	calls := map[int]*agg{}
	sawToolCallsFinish := false
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.HasSuffix(line, "[DONE]") {
			continue
		}
		var chunk dto.ChatCompletionsStreamResponse
		require.NoError(t, common.UnmarshalJsonStr(strings.TrimPrefix(line, "data: "), &chunk), line)
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && *choice.FinishReason == "tool_calls" {
				sawToolCallsFinish = true
			}
			for _, tc := range choice.Delta.ToolCalls {
				require.NotNil(t, tc.Index, "stream tool calls must carry an index: %s", line)
				a := calls[*tc.Index]
				if a == nil {
					a = &agg{}
					calls[*tc.Index] = a
				}
				if tc.ID != "" {
					require.NotEmpty(t, tc.Function.Name, "a tool call header must carry a name: %s", line)
					a.name = tc.Function.Name
				}
				a.args.WriteString(tc.Function.Arguments)
			}
		}
	}

	require.True(t, sawToolCallsFinish, "stream must finish with tool_calls")
	require.Len(t, calls, 1, "exactly one tool call expected, got %d", len(calls))
	only := calls[0]
	require.NotNil(t, only)
	require.Equal(t, "web_fetch", only.name)
	require.JSONEq(t, `{"url":"https://example.com"}`, only.args.String())
}
