package xai

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func streamResponseXAI2OpenAI(xAIResp *dto.ChatCompletionsStreamResponse, usage *dto.Usage) *dto.ChatCompletionsStreamResponse {
	if xAIResp == nil {
		return nil
	}
	if xAIResp.Usage != nil && usage != nil {
		xAIResp.Usage.CompletionTokens = common.SaturatingAddNonNegativeInt(usage.CompletionTokens)
	}
	openAIResp := &dto.ChatCompletionsStreamResponse{
		Id:      xAIResp.Id,
		Object:  xAIResp.Object,
		Created: xAIResp.Created,
		Model:   xAIResp.Model,
		Choices: xAIResp.Choices,
		Usage:   xAIResp.Usage,
	}

	return openAIResp
}

func xAIStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	usage := &dto.Usage{}
	var responseTextBuilder strings.Builder
	var toolCount int
	var containStreamUsage bool

	helper.SetEventStreamHeaders(c)

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var xAIResp *dto.ChatCompletionsStreamResponse
		if err := common.UnmarshalJsonStr(data, &xAIResp); err != nil {
			common.SysLog("error unmarshalling stream response: error_meta=" + common.SensitiveLogMeta(err.Error()))
			sr.Error(err)
			return
		}

		// 把 xAI 的usage转换为 OpenAI 的usage
		if xAIResp.Usage != nil {
			containStreamUsage = true
			normalizeXAIUsage(xAIResp.Usage)
			usage.PromptTokens = xAIResp.Usage.PromptTokens
			usage.TotalTokens = xAIResp.Usage.TotalTokens
			usage.CompletionTokens = xAIResp.Usage.CompletionTokens
		}

		openaiResponse := streamResponseXAI2OpenAI(xAIResp, usage)
		_ = openai.ProcessStreamResponse(*openaiResponse, &responseTextBuilder, &toolCount)
		if err := helper.ObjectData(c, openaiResponse); err != nil {
			common.SysLog("failed to send stream response: error_meta=" + common.SensitiveLogMeta(err.Error()))
			sr.Error(err)
		}
	})

	if !containStreamUsage {
		usage = service.ResponseText2Usage(c, responseTextBuilder.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		usage.CompletionTokens = common.SaturatingAddNonNegativeInt(usage.CompletionTokens, common.SaturatingMulNonNegativeInt(toolCount, 7))
		usage.TotalTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokens, usage.CompletionTokens)
	}

	helper.Done(c)
	service.CloseResponseBodyGracefully(resp)
	return usage, nil
}

func xAIHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	var xaiResponse ChatCompletionResponse
	err = common.Unmarshal(responseBody, &xaiResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if xaiResponse.Usage != nil {
		normalizeXAIUsage(xaiResponse.Usage)
	}

	// new body
	encodeJson, err := common.Marshal(xaiResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}

	service.IOCopyBytesGracefully(c, resp, encodeJson)

	return xaiResponse.Usage, nil
}

// normalizeXAIUsage turns the provider's aggregate counters into a
// non-negative, internally consistent usage value. xAI sometimes omits one
// of prompt/completion/total; malformed values must not become a negative
// completion count or a wrapped bill when the relay derives the missing field.
func normalizeXAIUsage(usage *dto.Usage) {
	if usage == nil {
		return
	}
	usage.PromptTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokens)
	usage.CompletionTokens = common.SaturatingAddNonNegativeInt(usage.CompletionTokens)
	usage.TotalTokens = common.SaturatingAddNonNegativeInt(usage.TotalTokens)
	knownTotal := common.SaturatingAddNonNegativeInt(usage.PromptTokens, usage.CompletionTokens)
	if usage.TotalTokens < knownTotal {
		usage.TotalTokens = knownTotal
	}
	if usage.TotalTokens > usage.PromptTokens {
		usage.CompletionTokens = usage.TotalTokens - usage.PromptTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = knownTotal
	}

	usage.PromptTokensDetails.CachedTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokensDetails.CachedTokens)
	usage.PromptTokensDetails.CachedCreationTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokensDetails.CachedCreationTokens)
	usage.PromptTokensDetails.CacheWriteTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokensDetails.CacheWriteTokens)
	usage.PromptTokensDetails.TextTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokensDetails.TextTokens)
	usage.PromptTokensDetails.AudioTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokensDetails.AudioTokens)
	usage.PromptTokensDetails.ImageTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokensDetails.ImageTokens)
	usage.CompletionTokenDetails.ReasoningTokens = common.SaturatingAddNonNegativeInt(usage.CompletionTokenDetails.ReasoningTokens)
	usage.CompletionTokenDetails.TextTokens = common.SaturatingAddNonNegativeInt(usage.CompletionTokenDetails.TextTokens)
	usage.CompletionTokenDetails.AudioTokens = common.SaturatingAddNonNegativeInt(usage.CompletionTokenDetails.AudioTokens)
	usage.CompletionTokenDetails.ImageTokens = common.SaturatingAddNonNegativeInt(usage.CompletionTokenDetails.ImageTokens)
	if usage.CompletionTokens > usage.CompletionTokenDetails.ReasoningTokens {
		usage.CompletionTokenDetails.TextTokens = usage.CompletionTokens - usage.CompletionTokenDetails.ReasoningTokens
	} else {
		usage.CompletionTokenDetails.TextTokens = 0
	}
	if details := usage.InputTokensDetails; details != nil {
		details.CachedTokens = common.SaturatingAddNonNegativeInt(details.CachedTokens)
		details.CachedCreationTokens = common.SaturatingAddNonNegativeInt(details.CachedCreationTokens)
		details.CacheWriteTokens = common.SaturatingAddNonNegativeInt(details.CacheWriteTokens)
		details.TextTokens = common.SaturatingAddNonNegativeInt(details.TextTokens)
		details.AudioTokens = common.SaturatingAddNonNegativeInt(details.AudioTokens)
		details.ImageTokens = common.SaturatingAddNonNegativeInt(details.ImageTokens)
	}
}
