package oaichat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponseOpenAI2GeminiMapsTextToolFinishReasonAndUsage(t *testing.T) {
	msg := dto.Message{
		Role:    "assistant",
		Content: "hello",
	}
	msg.SetToolCalls([]dto.ToolCallRequest{
		{
			ID:   "call_1",
			Type: "function",
			Function: dto.FunctionRequest{
				Name:      "lookup",
				Arguments: `{"q":"x"}`,
			},
		},
	})

	resp := ResponseOpenAI2Gemini(&dto.OpenAITextResponse{
		Model: "gpt-test",
		Choices: []dto.OpenAITextResponseChoice{
			{
				Index:        2,
				Message:      msg,
				FinishReason: "length",
			},
		},
		Usage: dto.Usage{
			PromptTokens:     11,
			CompletionTokens: 5,
			TotalTokens:      16,
		},
	}, nil)

	assert.Equal(t, 11, resp.UsageMetadata.PromptTokenCount)
	assert.Equal(t, 5, resp.UsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 16, resp.UsageMetadata.TotalTokenCount)
	require.NotNil(t, resp.UsageMetadata.BillingUsage)
	require.NotNil(t, resp.UsageMetadata.BillingUsage.OpenAIUsage)
	assert.Equal(t, dto.BillingUsageSourceOAIChat, resp.UsageMetadata.BillingUsage.Source)
	assert.Equal(t, dto.BillingUsageSemanticOpenAI, resp.UsageMetadata.BillingUsage.Semantic)
	assert.Equal(t, 11, resp.UsageMetadata.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, 5, resp.UsageMetadata.BillingUsage.OpenAIUsage.CompletionTokens)
	assert.Equal(t, 16, resp.UsageMetadata.BillingUsage.OpenAIUsage.TotalTokens)
	assert.Nil(t, resp.UsageMetadata.BillingUsage.OpenAIUsage.BillingUsage)
	require.Len(t, resp.Candidates, 1)
	assert.Equal(t, int64(2), resp.Candidates[0].Index)
	require.NotNil(t, resp.Candidates[0].FinishReason)
	assert.Equal(t, "MAX_TOKENS", *resp.Candidates[0].FinishReason)
	require.Len(t, resp.Candidates[0].Content.Parts, 2)
	assert.Equal(t, "hello", resp.Candidates[0].Content.Parts[0].Text)
	require.NotNil(t, resp.Candidates[0].Content.Parts[1].FunctionCall)
	assert.Equal(t, "lookup", resp.Candidates[0].Content.Parts[1].FunctionCall.FunctionName)
	assert.Equal(t, map[string]interface{}{"q": "x"}, resp.Candidates[0].Content.Parts[1].FunctionCall.Arguments)
}

func TestResponseOpenAI2GeminiMapsMarkdownDataImageToInlineData(t *testing.T) {
	resp := ResponseOpenAI2Gemini(&dto.OpenAITextResponse{
		Choices: []dto.OpenAITextResponseChoice{
			{
				Message: dto.Message{
					Role:    "assistant",
					Content: "generated image\n![image](data:image/png;base64,aGVsbG8=)",
				},
				FinishReason: "stop",
			},
		},
	}, nil)

	require.Len(t, resp.Candidates, 1)
	require.Len(t, resp.Candidates[0].Content.Parts, 2)
	assert.Equal(t, "generated image\n", resp.Candidates[0].Content.Parts[0].Text)
	require.NotNil(t, resp.Candidates[0].Content.Parts[1].InlineData)
	assert.Equal(t, "image/png", resp.Candidates[0].Content.Parts[1].InlineData.MimeType)
	assert.Equal(t, "aGVsbG8=", resp.Candidates[0].Content.Parts[1].InlineData.Data)
}

func TestResponseOpenAI2GeminiKeepsInvalidDataImageAsText(t *testing.T) {
	tests := []string{
		"![image](data:image/svg+xml;base64,PHN2Zz4=)",
		"![image](data:image/png;base64,not_base64!)",
	}
	for _, content := range tests {
		t.Run(content, func(t *testing.T) {
			resp := ResponseOpenAI2Gemini(&dto.OpenAITextResponse{
				Choices: []dto.OpenAITextResponseChoice{{
					Message:      dto.Message{Role: "assistant", Content: content},
					FinishReason: "stop",
				}},
			}, nil)

			require.Len(t, resp.Candidates, 1)
			require.Len(t, resp.Candidates[0].Content.Parts, 1)
			assert.Equal(t, content, resp.Candidates[0].Content.Parts[0].Text)
			assert.Nil(t, resp.Candidates[0].Content.Parts[0].InlineData)
		})
	}
}

func TestResponseOpenAI2GeminiMapsMessageImagesToInlineAndFileData(t *testing.T) {
	var openAIResponse dto.OpenAITextResponse
	err := kitutil.Unmarshal([]byte(`{
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"",
				"images":[
					{"type":"image_url","image_url":{"url":"data:image/webp;base64,UklGRg=="}},
					{"type":"image_url","image_url":{"url":"https://example.com/result.png","mime_type":"image/png"}}
				]
			},
			"finish_reason":"stop"
		}]
	}`), &openAIResponse)
	require.NoError(t, err)
	require.Len(t, openAIResponse.Choices, 1)
	require.NotEmpty(t, openAIResponse.Choices[0].Message.Images)
	resp := ResponseOpenAI2Gemini(&openAIResponse, nil)

	require.Len(t, resp.Candidates, 1)
	require.Len(t, resp.Candidates[0].Content.Parts, 2)
	require.NotNil(t, resp.Candidates[0].Content.Parts[0].InlineData)
	assert.Equal(t, "image/webp", resp.Candidates[0].Content.Parts[0].InlineData.MimeType)
	assert.Equal(t, "UklGRg==", resp.Candidates[0].Content.Parts[0].InlineData.Data)
	require.NotNil(t, resp.Candidates[0].Content.Parts[1].FileData)
	assert.Equal(t, "image/png", resp.Candidates[0].Content.Parts[1].FileData.MimeType)
	assert.Equal(t, "https://example.com/result.png", resp.Candidates[0].Content.Parts[1].FileData.FileUri)
}

func TestOpenAIMessageImagesAreResponseOnly(t *testing.T) {
	var response dto.OpenAITextResponse
	err := kitutil.Unmarshal([]byte(`{
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"done",
				"images":[{"b64_json":"UklGRg==","mime_type":"image/webp"}]
			},
			"finish_reason":"stop"
		}]
	}`), &response)
	require.NoError(t, err)
	require.Len(t, response.Choices, 1)
	require.NotEmpty(t, response.Choices[0].Message.Images)

	encoded, err := json.Marshal(response.Choices[0].Message)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"images"`)
}

func TestStreamResponseOpenAI2GeminiKeepsPartialDataURLAsText(t *testing.T) {
	resp := StreamResponseOpenAI2Gemini(&dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
				Content: geminiRespPtr("data:image/png;base64,iVBOR"),
			},
		}},
	}, &convmeta.Values{})

	require.NotNil(t, resp)
	require.Len(t, resp.Candidates, 1)
	require.Len(t, resp.Candidates[0].Content.Parts, 1)
	assert.Equal(t, "data:image/png;base64,iVBOR", resp.Candidates[0].Content.Parts[0].Text)
	assert.Nil(t, resp.Candidates[0].Content.Parts[0].InlineData)
}

func TestStreamResponseOpenAI2GeminiMapsToolCallFinishReasonAndUsage(t *testing.T) {
	resp := StreamResponseOpenAI2Gemini(&dto.ChatCompletionsStreamResponse{
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Index:        1,
				FinishReason: geminiRespPtr("tool_calls"),
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					ToolCalls: []dto.ToolCallResponse{
						{
							Type: "function",
							Function: dto.FunctionResponse{
								Name:      "lookup",
								Arguments: `{"q":"x"}`,
							},
						},
					},
				},
			},
		},
		Usage: &dto.Usage{
			PromptTokens:     13,
			CompletionTokens: 8,
			TotalTokens:      21,
		},
	}, &convmeta.Values{})

	require.NotNil(t, resp)
	assert.Equal(t, 13, resp.UsageMetadata.PromptTokenCount)
	assert.Equal(t, 8, resp.UsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 21, resp.UsageMetadata.TotalTokenCount)
	require.NotNil(t, resp.UsageMetadata.BillingUsage)
	require.NotNil(t, resp.UsageMetadata.BillingUsage.OpenAIUsage)
	assert.Equal(t, 13, resp.UsageMetadata.BillingUsage.OpenAIUsage.PromptTokens)
	assert.Equal(t, 8, resp.UsageMetadata.BillingUsage.OpenAIUsage.CompletionTokens)
	require.Len(t, resp.Candidates, 1)
	assert.Equal(t, int64(1), resp.Candidates[0].Index)
	require.NotNil(t, resp.Candidates[0].FinishReason)
	assert.Equal(t, "STOP", *resp.Candidates[0].FinishReason)
	require.Len(t, resp.Candidates[0].Content.Parts, 1)
	require.NotNil(t, resp.Candidates[0].Content.Parts[0].FunctionCall)
	assert.Equal(t, "lookup", resp.Candidates[0].Content.Parts[0].FunctionCall.FunctionName)
	assert.Equal(t, map[string]interface{}{"q": "x"}, resp.Candidates[0].Content.Parts[0].FunctionCall.Arguments)
}

func geminiRespPtr[T any](value T) *T {
	return &value
}
