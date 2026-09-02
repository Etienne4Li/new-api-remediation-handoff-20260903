package ollama

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// ollamaClaudeHandler adapts Ollama's chat JSON response to Anthropic Messages.
// The temporary recorder lets the existing Ollama parser remain the single
// source of truth for newline-delimited and single-frame responses.
func ollamaClaudeHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	recorder := httptest.NewRecorder()
	parsedContext, _ := gin.CreateTestContext(recorder)
	usage, apiErr := ollamaChatHandler(parsedContext, info, resp)
	if apiErr != nil {
		return usage, apiErr
	}
	var openAIResponse dto.OpenAITextResponse
	if err := common.Unmarshal(recorder.Body.Bytes(), &openAIResponse); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	claudeResponse := relayconvert.ResponseOpenAI2Claude(&openAIResponse, info)
	// The shared OpenAI→Claude converter intentionally focuses on text and
	// tool-use blocks.  Ollama's legacy endpoint can return a complete
	// `message.thinking` field, so preserve it as an Anthropic thinking block
	// instead of silently dropping billable reasoning content.
	if len(openAIResponse.Choices) > 0 {
		reasoning := openAIResponse.Choices[0].Message.GetReasoningContent()
		if reasoning != "" {
			thinking := dto.ClaudeMediaMessage{Type: "thinking", Thinking: &reasoning}
			claudeResponse.Content = append([]dto.ClaudeMediaMessage{thinking}, claudeResponse.Content...)
		}
	}
	out, err := common.Marshal(claudeResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, out)
	return usage, nil
}

func ollamaClaudeStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	recorder := httptest.NewRecorder()
	parsedContext, _ := gin.CreateTestContext(recorder)
	usage, apiErr := ollamaStreamHandler(parsedContext, info, resp)
	if apiErr != nil {
		return usage, apiErr
	}
	helper.SetEventStreamHeaders(c)
	scanner := bufio.NewScanner(bytes.NewReader(recorder.Body.Bytes()))
	// The Ollama parser emits OpenAI chunks without the host's usual response
	// counter bookkeeping. Mark the first converted chunk so the shared
	// OpenAI-to-Claude converter emits message_start exactly once.
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" || data == "" {
			continue
		}
		var chunk dto.ChatCompletionsStreamResponse
		if err := common.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
		}
		// The Claude stream converter uses SendResponseCount to decide which
		// lifecycle event to emit. Increment exactly once per upstream frame;
		// incrementing both before and after conversion made the first frame
		// appear as count=2 and caused message_start/message_delta drift.
		info.IncrSendResponseCount()
		claudeResponses := relayconvert.StreamResponseOpenAI2Claude(&chunk, info)
		for _, claudeResponse := range claudeResponses {
			if err := helper.ClaudeData(c, *claudeResponse); err != nil {
				return usage, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			}
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return usage, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	return usage, nil
}
