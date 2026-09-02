package mokaai

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatGemini))
}

func (a *Adaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatClaude))
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatOpenAIAudio))
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatOpenAIImage))
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	// M3E's endpoint accepts the legacy Moka wire shape: model plus a string
	// array.  The public OpenAI-compatible DTO deliberately permits either a
	// scalar or an array, so normalize it at this provider boundary instead of
	// relying on whichever concrete JSON value the decoder produced.
	return &dto.EmbeddingRequest{
		Model: request.Model,
		Input: request.ParseInput(),
	}, nil
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {

}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	// https://cloud.baidu.com/doc/WENXINWORKSHOP/s/clntwmv7t
	suffix := "chat/"
	if strings.HasPrefix(info.UpstreamModelName, "m3e") {
		suffix = "embeddings"
	}
	fullRequestURL := fmt.Sprintf("%s/%s", info.ChannelBaseUrl, suffix)
	return fullRequestURL, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", fmt.Sprintf("Bearer %s", info.ApiKey))
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	switch info.RelayMode {
	case constant.RelayModeEmbeddings:
		baiduEmbeddingRequest := embeddingRequestOpenAI2Moka(*request)
		return baiduEmbeddingRequest, nil
	default:
		return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatOpenAI))
	}
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatRerank))
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, types.NewUnsupportedEndpointError(ChannelName, string(types.RelayFormatOpenAIResponses))
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {

	switch info.RelayMode {
	case constant.RelayModeEmbeddings:
		return mokaEmbeddingHandler(c, info, resp)
	default:
		// err, usage = mokaHandler(c, resp)

	}
	return
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
