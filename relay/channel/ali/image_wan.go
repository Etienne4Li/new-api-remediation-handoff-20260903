package ali

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func oaiFormEdit2WanxImageEdit(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (*AliImageRequest, error) {
	var err error
	var imageRequest AliImageRequest
	imageRequest.Model = request.Model
	imageRequest.ResponseFormat = request.ResponseFormat
	wanInput := WanImageInput{
		Prompt: request.Prompt,
	}

	if err := common.UnmarshalBodyReusable(c, &wanInput); err != nil {
		return nil, err
	}
	if wanInput.Images, err = getImageBase64sFromForm(c, "image"); err != nil {
		return nil, fmt.Errorf("get image base64s from form failed: %w", err)
	}
	//wanParams := WanImageParameters{
	//	N: int(request.N),
	//}
	imageRequest.Input = wanInput
	imageN, ok := dto.BoundedImageN(request.N)
	if !ok {
		return nil, fmt.Errorf("n must be an integer between 1 and %d", dto.MaxImageN)
	}
	imageRequest.Parameters = AliImageParameters{
		N: int(imageN),
	}
	info.PriceData.AddOtherRatio("n", float64(imageRequest.Parameters.N))

	return &imageRequest, nil
}

func isOldWanModel(modelName string) bool {
	return strings.Contains(modelName, "wan") &&
		!lo.SomeBy([]string{"wan2.6", "wan2.7"}, func(v string) bool { return strings.Contains(modelName, v) })
}

func isWanModel(modelName string) bool {
	return strings.Contains(modelName, "wan")
}
