package siliconflow

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func siliconflowRerankHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)
	var siliconflowResp SFRerankResponse
	err = common.Unmarshal(responseBody, &siliconflowResp)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	usage := &dto.Usage{
		PromptTokens:     common.SaturatingAddNonNegativeInt(siliconflowResp.Meta.Tokens.InputTokens),
		CompletionTokens: common.SaturatingAddNonNegativeInt(siliconflowResp.Meta.Tokens.OutputTokens),
	}
	usage.TotalTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokens, usage.CompletionTokens)
	rerankResp := &dto.RerankResponse{
		Results: siliconflowResp.Results,
		Usage:   *usage,
	}

	jsonResponse, err := common.Marshal(rerankResp)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	service.IOCopyBytesGracefully(c, resp, jsonResponse)
	return usage, nil
}
