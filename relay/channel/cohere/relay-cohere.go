package cohere

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func requestOpenAI2Cohere(textRequest dto.GeneralOpenAIRequest) *CohereRequest {
	cohereReq := CohereRequest{
		Model:       textRequest.Model,
		ChatHistory: []ChatHistory{},
		Message:     "",
		Stream:      lo.FromPtrOr(textRequest.Stream, false),
		MaxTokens:   textRequest.GetMaxTokens(),
	}
	if common.CohereSafetySetting != "NONE" {
		cohereReq.SafetyMode = common.CohereSafetySetting
	}
	if cohereReq.MaxTokens == 0 {
		cohereReq.MaxTokens = 4000
	}
	for _, msg := range textRequest.Messages {
		if msg.Role == "user" {
			cohereReq.Message = msg.StringContent()
		} else {
			var role string
			if msg.Role == "assistant" {
				role = "CHATBOT"
			} else if msg.Role == "system" {
				role = "SYSTEM"
			} else {
				role = "USER"
			}
			cohereReq.ChatHistory = append(cohereReq.ChatHistory, ChatHistory{
				Role:    role,
				Message: msg.StringContent(),
			})
		}
	}

	return &cohereReq
}

func requestConvertRerank2Cohere(rerankRequest dto.RerankRequest) *CohereRerankRequest {
	topN := lo.FromPtrOr(rerankRequest.TopN, 1)
	if topN <= 0 {
		topN = 1
	}
	cohereReq := CohereRerankRequest{
		Query:           rerankRequest.Query,
		Documents:       rerankRequest.Documents,
		Model:           rerankRequest.Model,
		TopN:            topN,
		ReturnDocuments: true,
	}
	return &cohereReq
}

func stopReasonCohere2OpenAI(reason string) string {
	switch reason {
	case "COMPLETE":
		return "stop"
	case "MAX_TOKENS":
		return "max_tokens"
	default:
		return reason
	}
}

func cohereStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	responseId := helper.GetResponseID(c)
	createdTime := common.GetTimestamp()
	usage := &dto.Usage{}
	var responseText strings.Builder
	var finished bool
	var hasUpstreamUsage bool

	helper.StreamScannerHandlerWithEventsAndDrain(c, resp, info, func(event, data string, sr *helper.StreamResult) {
		var cohereResp CohereResponse
		if err := common.UnmarshalJsonStr(data, &cohereResp); err != nil {
			common.SysLog("error unmarshalling Cohere stream response: error_meta=" + common.SensitiveLogMeta(err.Error()))
			sr.Error(err)
			return
		}

		text, terminal, finishReason, billedUnits := cohereStreamEvent(event, cohereResp)
		if billedUnits != nil {
			hasUpstreamUsage = true
			usage.PromptTokens = common.SaturatingAddNonNegativeInt(billedUnits.InputTokens)
			usage.CompletionTokens = common.SaturatingAddNonNegativeInt(billedUnits.OutputTokens)
			usage.TotalTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokens, usage.CompletionTokens)
		}
		if finished || sr.IsDraining() {
			return
		}

		var openaiResp *dto.ChatCompletionsStreamResponse
		if text != "" {
			responseText.WriteString(text)
			openaiResp = &dto.ChatCompletionsStreamResponse{
				Id:      responseId,
				Created: createdTime,
				Object:  "chat.completion.chunk",
				Model:   info.UpstreamModelName,
				Choices: []dto.ChatCompletionsStreamResponseChoice{{
					Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant", Content: &text},
					Index: 0,
				}},
			}
		}
		if terminal {
			finished = true
			finishReason = stopReasonCohere2OpenAI(finishReason)
			openaiResp = &dto.ChatCompletionsStreamResponse{
				Id:      responseId,
				Created: createdTime,
				Object:  "chat.completion.chunk",
				Model:   info.UpstreamModelName,
				Choices: []dto.ChatCompletionsStreamResponseChoice{{
					Delta:        dto.ChatCompletionsStreamResponseChoiceDelta{},
					Index:        0,
					FinishReason: &finishReason,
				}},
			}
		}
		if openaiResp == nil {
			return
		}
		if err := helper.ObjectData(c, openaiResp); err != nil {
			common.SysLog("failed to send Cohere stream response: error_meta=" + common.SensitiveLogMeta(err.Error()))
			if !requestContextDone(c) {
				sr.Stop(err)
			}
		}
		if terminal && !sr.IsDraining() {
			sr.Done()
		}
	}, &helper.StreamScannerDrainOptions{})

	if !finished && info.StreamStatus != nil && info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() && !requestContextDone(c) {
		finishReason := "stop"
		finalResponse := &dto.ChatCompletionsStreamResponse{
			Id:      responseId,
			Created: createdTime,
			Object:  "chat.completion.chunk",
			Model:   info.UpstreamModelName,
			Choices: []dto.ChatCompletionsStreamResponseChoice{{
				Delta:        dto.ChatCompletionsStreamResponseChoiceDelta{},
				Index:        0,
				FinishReason: &finishReason,
			}},
		}
		if err := helper.ObjectData(c, finalResponse); err != nil {
			common.SysLog("failed to send synthesized Cohere terminal response: error_meta=" + common.SensitiveLogMeta(err.Error()))
		}
	}
	if !requestContextDone(c) {
		helper.Done(c)
	}
	if !hasUpstreamUsage {
		usage = service.ResponseText2Usage(c, responseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}
	return usage, nil
}

func cohereStreamEvent(event string, response CohereResponse) (text string, terminal bool, finishReason string, billedUnits *CohereBilledUnits) {
	if response.Text != "" {
		text = response.Text
	}
	if response.Delta.Message.Content.Text != "" {
		text = response.Delta.Message.Content.Text
	}
	if response.Response != nil {
		billedUnits = &response.Response.Meta.BilledUnits
	}
	if response.Delta.Usage != nil {
		billedUnits = &response.Delta.Usage.BilledUnits
	}
	terminal = response.IsFinished || response.EventType == "stream-end" || event == "stream-end" || response.Type == "message-end" || event == "message-end"
	if !terminal {
		return text, false, "", billedUnits
	}
	finishReason = response.FinishReason
	if finishReason == "" {
		finishReason = response.Delta.FinishReason
	}
	if finishReason == "" && response.Response != nil {
		finishReason = response.Response.FinishReason
	}
	if finishReason == "" {
		finishReason = "COMPLETE"
	}
	return text, true, finishReason, billedUnits
}

func requestContextDone(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.Context().Err() != nil
}

func cohereHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	createdTime := common.GetTimestamp()
	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	service.CloseResponseBodyGracefully(resp)
	var cohereResp CohereResponseResult
	err = common.Unmarshal(responseBody, &cohereResp)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	usage := dto.Usage{}
	usage.PromptTokens = common.SaturatingAddNonNegativeInt(cohereResp.Meta.BilledUnits.InputTokens)
	usage.CompletionTokens = common.SaturatingAddNonNegativeInt(cohereResp.Meta.BilledUnits.OutputTokens)
	usage.TotalTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokens, usage.CompletionTokens)

	var openaiResp dto.TextResponse
	openaiResp.Id = cohereResp.ResponseId
	openaiResp.Created = createdTime
	openaiResp.Object = "chat.completion"
	openaiResp.Model = info.UpstreamModelName
	openaiResp.Usage = usage

	openaiResp.Choices = []dto.OpenAITextResponseChoice{
		{
			Index:        0,
			Message:      dto.Message{Content: cohereResp.Text, Role: "assistant"},
			FinishReason: stopReasonCohere2OpenAI(cohereResp.FinishReason),
		},
	}

	jsonResponse, err := common.Marshal(openaiResp)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = c.Writer.Write(jsonResponse)
	return &usage, nil
}

func cohereRerankHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	service.CloseResponseBodyGracefully(resp)
	var cohereResp CohereRerankResponseResult
	err = common.Unmarshal(responseBody, &cohereResp)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	usage := dto.Usage{}
	if cohereResp.Meta.BilledUnits.InputTokens == 0 {
		usage.PromptTokens = common.SaturatingAddNonNegativeInt(info.GetEstimatePromptTokens())
		usage.CompletionTokens = 0
		usage.TotalTokens = usage.PromptTokens
	} else {
		usage.PromptTokens = common.SaturatingAddNonNegativeInt(cohereResp.Meta.BilledUnits.InputTokens)
		usage.CompletionTokens = common.SaturatingAddNonNegativeInt(cohereResp.Meta.BilledUnits.OutputTokens)
		usage.TotalTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokens, usage.CompletionTokens)
	}

	var rerankResp dto.RerankResponse
	rerankResp.Results = cohereResp.Results
	rerankResp.Usage = usage

	jsonResponse, err := common.Marshal(rerankResp)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = c.Writer.Write(jsonResponse)
	return &usage, nil
}
