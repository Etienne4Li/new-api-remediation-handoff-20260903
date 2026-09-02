package ali

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func oaiImage2AliImageRequest(info *relaycommon.RelayInfo, request dto.ImageRequest, isSync bool) (*AliImageRequest, error) {
	var imageRequest AliImageRequest
	imageRequest.Model = request.Model
	imageRequest.ResponseFormat = request.ResponseFormat
	if request.Extra != nil {
		if val, ok := request.Extra["parameters"]; ok {
			err := common.Unmarshal(val, &imageRequest.Parameters)
			if err != nil {
				return nil, fmt.Errorf("invalid parameters field: %w", err)
			}
		} else {
			// 兼容没有parameters字段的情况，从openai标准字段中提取参数
			imageN, ok := dto.BoundedImageN(request.N)
			if !ok {
				return nil, fmt.Errorf("n must be an integer between 1 and %d", dto.MaxImageN)
			}
			imageRequest.Parameters = AliImageParameters{
				Size:      strings.Replace(request.Size, "x", "*", -1),
				N:         int(imageN),
				Watermark: request.Watermark,
			}
		}
		if val, ok := request.Extra["input"]; ok {
			err := common.Unmarshal(val, &imageRequest.Input)
			if err != nil {
				return nil, fmt.Errorf("invalid input field: %w", err)
			}
		}
	}

	if strings.Contains(request.Model, "z-image") {
		// z-image 开启prompt_extend后，按2倍计费
		if imageRequest.Parameters.PromptExtendValue() {
			info.PriceData.AddOtherRatio("prompt_extend", 2)
		}
	}

	// Parameters may come from Extra["parameters"], bypassing the standard
	// top-level n validation; enforce the same bound before it becomes a
	// billing multiplier.
	if imageRequest.Parameters.N < 0 || imageRequest.Parameters.N > dto.MaxImageN {
		return nil, fmt.Errorf("parameters.n must be an integer between 1 and %d", dto.MaxImageN)
	}
	if imageRequest.Parameters.N != 0 {
		info.PriceData.AddOtherRatio("n", float64(imageRequest.Parameters.N))
	}

	// 同步图片模型和异步图片模型请求格式不一样
	if isSync {
		if imageRequest.Input == nil {
			imageRequest.Input = AliImageInput{
				Messages: []AliMessage{
					{
						Role: "user",
						Content: []AliMediaContent{
							{
								Text: request.Prompt,
							},
						},
					},
				},
			}
		}
	} else {
		if imageRequest.Input == nil {
			imageRequest.Input = AliImageInput{
				Prompt: request.Prompt,
			}
		}
	}

	return &imageRequest, nil
}
func getImageBase64sFromForm(c *gin.Context, fieldName string) ([]string, error) {
	mf := c.Request.MultipartForm
	if mf == nil {
		if _, err := c.MultipartForm(); err != nil {
			return nil, fmt.Errorf("failed to parse image edit form request: %w", err)
		}
		mf = c.Request.MultipartForm
	}

	var imageFiles []*multipart.FileHeader
	var exists bool

	// First check for standard "image" field
	if imageFiles, exists = mf.File["image"]; !exists || len(imageFiles) == 0 {
		// If not found, check for "image[]" field
		if imageFiles, exists = mf.File["image[]"]; !exists || len(imageFiles) == 0 {
			// If still not found, iterate through all fields to find any that start with "image["
			foundArrayImages := false
			for fieldName, files := range mf.File {
				if strings.HasPrefix(fieldName, "image[") && len(files) > 0 {
					foundArrayImages = true
					imageFiles = append(imageFiles, files...)
				}
			}

			// If no image fields found at all
			if !foundArrayImages && (len(imageFiles) == 0) {
				return nil, errors.New("image is required")
			}
		}
	}

	if len(imageFiles) == 0 {
		return nil, errors.New("image is required")
	}
	if len(imageFiles) > dto.MaxImageN {
		return nil, fmt.Errorf("too many image files: maximum is %d", dto.MaxImageN)
	}

	//if len(imageFiles) > 1 {
	//	return nil, errors.New("only one image is supported for qwen edit")
	//}

	// 获取base64编码的图片
	var imageBase64s []string
	maxImageBytes := common.GetMaxFileDownloadBytes()
	maxTotalImageBytes := common.GetMaxRequestBodyBytes()
	var totalImageBytes int64
	for _, file := range imageFiles {
		if file == nil {
			return nil, errors.New("image is required")
		}
		if file.Size < 0 || file.Size > maxImageBytes {
			return nil, fmt.Errorf("image file exceeds maximum allowed size of %d bytes", maxImageBytes)
		}
		if file.Size > maxTotalImageBytes-totalImageBytes {
			return nil, fmt.Errorf("image files exceed maximum total size of %d bytes", maxTotalImageBytes)
		}
		totalImageBytes += file.Size
		image, err := file.Open()
		if err != nil {
			return nil, errors.New("failed to open image file")
		}

		// 读取文件内容
		imageData, err := common.ReadBodyLimited(image, file.Size, maxImageBytes)
		closeErr := image.Close()
		if err != nil {
			if closeErr != nil {
				return nil, fmt.Errorf("failed to read image file: %w (close: %v)", err, closeErr)
			}
			return nil, errors.New("failed to read image file")
		}
		if closeErr != nil {
			return nil, fmt.Errorf("failed to close image file: %w", closeErr)
		}

		// Determine the type from the bytes and reject non-image/active content.
		// The multipart header and filename are client-controlled; never forward
		// an arbitrary declaration in a data URL.
		mimeType, err := service.ResolveImageMIME(imageData)
		if err != nil {
			return nil, fmt.Errorf("unsupported image content: %w", err)
		}

		// 编码为base64
		base64Data := base64.StdEncoding.EncodeToString(imageData)

		// 构造data URL格式
		dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data)
		imageBase64s = append(imageBase64s, dataURL)
	}
	return imageBase64s, nil
}

func oaiFormEdit2AliImageEdit(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (*AliImageRequest, error) {
	var imageRequest AliImageRequest
	imageRequest.Model = request.Model
	imageRequest.ResponseFormat = request.ResponseFormat

	imageBase64s, err := getImageBase64sFromForm(c, "image")
	if err != nil {
		return nil, fmt.Errorf("get image base64s from form failed: %w", err)
	}
	//dto.MediaContent{}
	mediaContents := make([]AliMediaContent, len(imageBase64s))
	for i, b64 := range imageBase64s {
		mediaContents[i] = AliMediaContent{
			Image: b64,
		}
	}
	mediaContents = append(mediaContents, AliMediaContent{
		Text: request.Prompt,
	})
	imageRequest.Input = AliImageInput{
		Messages: []AliMessage{
			{
				Role:    "user",
				Content: mediaContents,
			},
		},
	}
	imageN, ok := dto.BoundedImageN(request.N)
	if !ok {
		return nil, fmt.Errorf("n must be an integer between 1 and %d", dto.MaxImageN)
	}
	imageRequest.Parameters = AliImageParameters{
		N:         int(imageN),
		Watermark: request.Watermark,
	}
	return &imageRequest, nil
}

func updateTask(info *relaycommon.RelayInfo, taskID string) (*AliResponse, error, []byte) {
	url := fmt.Sprintf("%s/api/v1/tasks/%s", info.ChannelBaseUrl, taskID)

	var aliResponse AliResponse

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return &aliResponse, err, nil
	}

	req.Header.Set("Authorization", "Bearer "+info.ApiKey)

	client, err := service.GetHttpClientWithProxy("")
	if err != nil {
		return &aliResponse, err, nil
	}
	resp, err := client.Do(req)
	if err != nil {
		common.SysLog("updateTask client.Do failed error_meta=" + common.SensitiveLogMeta(err.Error()))
		return &aliResponse, err, nil
	}
	defer resp.Body.Close()

	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)

	var response AliResponse
	err = common.Unmarshal(responseBody, &response)
	if err != nil {
		common.SysLog("updateTask response decode failed error_meta=" + common.SensitiveLogMeta(err.Error()))
		return &aliResponse, err, nil
	}

	return &response, nil, responseBody
}

func asyncTaskWait(c *gin.Context, info *relaycommon.RelayInfo, taskID string) (*AliResponse, []byte, error) {
	waitSeconds := 10
	step := 0
	maxStep := 20

	var taskResponse AliResponse
	var responseBody []byte

	time.Sleep(time.Duration(5) * time.Second)

	for {
		logger.LogDebug(c, "asyncTaskWait step %d/%d, wait %d seconds", step, maxStep, waitSeconds)
		step++
		rsp, err, body := updateTask(info, taskID)
		responseBody = body
		if err != nil {
			logger.LogWarn(c, "asyncTaskWait UpdateTask failed error_meta="+common.SensitiveLogMeta(err.Error()))
			time.Sleep(time.Duration(waitSeconds) * time.Second)
			continue
		}

		if rsp.Output.TaskStatus == "" {
			return &taskResponse, responseBody, nil
		}

		switch rsp.Output.TaskStatus {
		case "FAILED":
			fallthrough
		case "CANCELED":
			fallthrough
		case "SUCCEEDED":
			fallthrough
		case "UNKNOWN":
			return rsp, responseBody, nil
		}
		if step >= maxStep {
			break
		}
		time.Sleep(time.Duration(waitSeconds) * time.Second)
	}

	return nil, nil, fmt.Errorf("aliAsyncTaskWait timeout")
}

func responseAli2OpenAIImage(c *gin.Context, response *AliResponse, originBody []byte, info *relaycommon.RelayInfo, responseFormat string) *dto.ImageResponse {
	imageResponse := dto.ImageResponse{
		Created: info.StartTime.Unix(),
	}

	if len(response.Output.Results) > 0 {
		imageResponse.Data = response.Output.ResultToOpenAIImageDate(c, responseFormat)
	} else if len(response.Output.Choices) > 0 {
		imageResponse.Data = response.Output.ChoicesToOpenAIImageDate(c, responseFormat)
	}

	// The provider envelope can contain signed media URLs and diagnostic fields.
	// Expose only the bounded, credential-safe projection used by task APIs.
	imageResponse.Metadata = service.RedactTaskResponseBody(originBody)
	return &imageResponse
}

func aliImageHandler(a *Adaptor, c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.Usage) {
	responseFormat := c.GetString("response_format")

	var aliTaskResponse AliResponse
	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError), nil
	}
	service.CloseResponseBodyGracefully(resp)
	err = common.Unmarshal(responseBody, &aliTaskResponse)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError), nil
	}

	if aliTaskResponse.Message != "" {
		logger.LogError(c, "ali_async_task_failed message_meta="+common.SensitiveLogMeta(aliTaskResponse.Message))
		return types.NewError(errors.New(aliTaskResponse.Message), types.ErrorCodeBadResponse), nil
	}

	var (
		aliResponse    *AliResponse
		originRespBody []byte
	)

	if a.IsSyncImageModel {
		aliResponse = &aliTaskResponse
		originRespBody = responseBody
	} else {
		// 异步图片模型需要轮询任务结果
		aliResponse, originRespBody, err = asyncTaskWait(c, info, aliTaskResponse.Output.TaskId)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponse), nil
		}
		if aliResponse.Output.TaskStatus != "SUCCEEDED" {
			return types.WithOpenAIError(types.OpenAIError{
				Message: aliResponse.Output.Message,
				Type:    "ali_error",
				Param:   "",
				Code:    aliResponse.Output.Code,
			}, resp.StatusCode), nil
		}
	}

	if a.IsSyncImageModel {
		logger.LogDebug(c, "ali_sync_image_result_meta=%s", common.SensitiveLogBody(originRespBody))
	} else {
		logger.LogDebug(c, "ali_async_image_result_meta=%s", common.SensitiveLogBody(originRespBody))
	}

	imageResponses := responseAli2OpenAIImage(c, aliResponse, originRespBody, info, responseFormat)
	if aliResponse.Usage.ImageCount != 0 {
		info.PriceData.AddOtherRatio("n", float64(aliResponse.Usage.ImageCount))
	} else if len(imageResponses.Data) != 0 {
		info.PriceData.AddOtherRatio("n", float64(len(imageResponses.Data)))
	}
	jsonResponse, err := common.Marshal(imageResponses)
	if err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody), nil
	}
	service.IOCopyBytesGracefully(c, resp, jsonResponse)

	return nil, &dto.Usage{}
}
