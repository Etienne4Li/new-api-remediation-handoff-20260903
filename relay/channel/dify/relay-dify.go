package dify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
)

func uploadDifyFile(c *gin.Context, info *relaycommon.RelayInfo, user string, media dto.MediaContent) *DifyFile {
	if info == nil {
		common.SysLog("failed to upload Dify file: relay info is nil")
		return nil
	}
	uploadUrl := fmt.Sprintf("%s/v1/files/upload", info.ChannelBaseUrl)
	switch media.Type {
	case dto.ContentTypeImageURL:
		// Decode base64 data
		imageMedia := media.GetImageMedia()
		if imageMedia == nil {
			common.SysLog("failed to upload Dify file: image payload is missing")
			return nil
		}
		base64Data := imageMedia.Url
		// Remove base64 prefix if exists (e.g., "data:image/jpeg;base64,")
		if idx := strings.Index(base64Data, ","); idx != -1 {
			base64Data = base64Data[idx+1:]
		}

		// Decode base64 string with a finite image-size bound. The value comes
		// from the client request and may otherwise allocate without limit.
		decodedData, err := common.DecodeBase64Limited(base64Data, common.GetMaxFileDownloadBytes())
		if err != nil {
			common.SysLog("failed to decode base64: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}

		// The data URL declaration and multipart filename are client-controlled.
		// Validate the actual bytes before forwarding them to Dify so HTML/SVG or
		// arbitrary bytes cannot be uploaded as an image and later rendered as
		// active content by a downstream workflow.
		resolvedMIME, err := service.ResolveImageMIME(decodedData)
		if err != nil {
			common.SysLog("failed to resolve Dify image MIME: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}

		// Create temporary file
		tempFile, err := os.CreateTemp("", "dify-upload-*")
		if err != nil {
			common.SysLog("failed to create temp file: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}
		defer tempFile.Close()
		defer os.Remove(tempFile.Name())

		// Write decoded data to temp file
		if _, err := tempFile.Write(decodedData); err != nil {
			common.SysLog("failed to write to temp file: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}

		// Create multipart form
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)

		// Add user field
		if err := writer.WriteField("user", user); err != nil {
			common.SysLog("failed to add user field: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}

		// Derive a stable extension from the detected type; never interpolate the
		// caller's MIME declaration or filename into a multipart header. Use a
		// custom part so Dify also receives the canonical detected Content-Type
		// instead of multipart.Writer's application/octet-stream default.
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="image.%s"`, difyImageExtension(resolvedMIME)))
		h.Set("Content-Type", resolvedMIME)
		part, err := writer.CreatePart(h)
		if err != nil {
			common.SysLog("failed to create form file: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}

		// Copy file content to form
		if _, err = io.Copy(part, bytes.NewReader(decodedData)); err != nil {
			common.SysLog("failed to copy file content: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}
		if err := writer.Close(); err != nil {
			common.SysLog("failed to close multipart writer: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}

		// Create HTTP request
		req, err := http.NewRequest("POST", uploadUrl, body)
		if err != nil {
			common.SysLog("failed to create request: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}

		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", info.ApiKey))

		// Send request
		client := service.GetHttpClient()
		resp, err := client.Do(req)
		if err != nil {
			common.SysLog("failed to send request: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			common.SysLog(fmt.Sprintf("Dify file upload returned status %d", resp.StatusCode))
			return nil
		}

		// Parse response
		var result struct {
			Id string `json:"id"`
		}
		responseBody, readErr := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
		if readErr != nil {
			common.SysLog("failed to read response body: error_meta=" + common.SensitiveLogMeta(readErr.Error()))
			return nil
		}
		if err := common.Unmarshal(responseBody, &result); err != nil {
			common.SysLog("failed to decode response: error_meta=" + common.SensitiveLogMeta(err.Error()))
			return nil
		}
		if strings.TrimSpace(result.Id) == "" {
			common.SysLog("Dify file upload response did not contain an id")
			return nil
		}

		return &DifyFile{
			UploadFileId: result.Id,
			Type:         "image",
			TransferMode: "local_file",
		}
	}
	return nil
}

func difyImageExtension(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/bmp":
		return "bmp"
	case "image/tiff":
		return "tiff"
	case "image/heic":
		return "heic"
	case "image/heif":
		return "heif"
	case "image/avif":
		return "avif"
	case "image/x-icon":
		return "ico"
	default:
		// ResolveImageMIME only returns a finite allowlisted image type. Keep a
		// conservative fallback for future additions so the filename remains a
		// harmless constant even if this mapping is not updated immediately.
		return "img"
	}
}

func requestOpenAI2Dify(c *gin.Context, info *relaycommon.RelayInfo, request dto.GeneralOpenAIRequest) *DifyChatRequest {
	difyReq := DifyChatRequest{
		Inputs:           make(map[string]interface{}),
		AutoGenerateName: false,
	}

	user := request.User
	if len(user) == 0 {
		user = json.RawMessage(helper.GetResponseID(c))
	}
	var stringUser string
	err := common.Unmarshal(user, &stringUser)
	if err != nil {
		common.SysLog("failed to unmarshal user: error_meta=" + common.SensitiveLogMeta(err.Error()))
		stringUser = helper.GetResponseID(c)
	}
	difyReq.User = stringUser

	files := make([]DifyFile, 0)
	var content strings.Builder
	for _, message := range request.Messages {
		if message.Role == "system" {
			content.WriteString("SYSTEM: \n" + message.StringContent() + "\n")
		} else if message.Role == "assistant" {
			content.WriteString("ASSISTANT: \n" + message.StringContent() + "\n")
		} else {
			parseContent := message.ParseContent()
			for _, mediaContent := range parseContent {
				switch mediaContent.Type {
				case dto.ContentTypeText:
					content.WriteString("USER: \n" + mediaContent.Text + "\n")
				case dto.ContentTypeImageURL:
					media := mediaContent.GetImageMedia()
					if media == nil || strings.TrimSpace(media.Url) == "" {
						continue
					}
					var file *DifyFile
					if media.IsRemoteImage() {
						if !isAllowedDifyRemoteImageURL(media.Url) {
							common.SysLog("rejected Dify remote image URL by fetch policy")
							continue
						}
						// 修复 #2083: 远程图片分支此前未初始化 file，
						// 导致 file.Type = ... 触发 nil pointer dereference
						// 而 panic（500: "invalid memory address or nil pointer dereference"）。
						file = &DifyFile{
							// Dify expects a logical file category here, not an
							// arbitrary client MIME declaration. Keeping this fixed also
							// prevents active values such as image/svg+xml from being
							// reflected into the upstream JSON contract.
							Type:         "image",
							TransferMode: "remote_url",
							URL:          media.Url,
						}
					} else {
						file = uploadDifyFile(c, info, difyReq.User, mediaContent)
					}
					if file != nil {
						files = append(files, *file)
					}
				}
			}
		}
	}
	difyReq.Query = content.String()
	difyReq.Files = files
	mode := "blocking"
	if lo.FromPtrOr(request.Stream, false) {
		mode = "streaming"
	}
	difyReq.ResponseMode = mode
	return &difyReq
}

func isAllowedDifyRemoteImageURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	return service.ValidateSSRFProtectedFetchURL(parsed.String()) == nil
}

func streamResponseDify2OpenAI(difyResponse DifyChunkChatCompletionResponse) *dto.ChatCompletionsStreamResponse {
	response := dto.ChatCompletionsStreamResponse{
		Object:  "chat.completion.chunk",
		Created: common.GetTimestamp(),
		Model:   "dify",
	}
	var choice dto.ChatCompletionsStreamResponseChoice
	if strings.HasPrefix(difyResponse.Event, "workflow_") {
		if constant.DifyDebug {
			text := "Workflow: " + difyResponse.Data.WorkflowId
			if difyResponse.Event == "workflow_finished" {
				text += " " + difyResponse.Data.Status
			}
			choice.Delta.SetReasoningContent(text + "\n")
		}
	} else if strings.HasPrefix(difyResponse.Event, "node_") {
		if constant.DifyDebug {
			text := "Node: " + difyResponse.Data.NodeType
			if difyResponse.Event == "node_finished" {
				text += " " + difyResponse.Data.Status
			}
			choice.Delta.SetReasoningContent(text + "\n")
		}
	} else if difyResponse.Event == "message" || difyResponse.Event == "agent_message" {
		if difyResponse.Answer == "<details style=\"color:gray;background-color: #f8f8f8;padding: 8px;border-radius: 4px;\" open> <summary> Thinking... </summary>\n" {
			difyResponse.Answer = "<think>"
		} else if difyResponse.Answer == "</details>" {
			difyResponse.Answer = "</think>"
		}

		choice.Delta.SetContentString(difyResponse.Answer)
	}
	response.Choices = append(response.Choices, choice)
	return &response
}

func difyStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	var responseText string
	usage := &dto.Usage{}
	var nodeToken int
	helper.SetEventStreamHeaders(c)
	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var difyResponse DifyChunkChatCompletionResponse
		if err := common.Unmarshal([]byte(data), &difyResponse); err != nil {
			common.SysLog("error unmarshalling stream response: error_meta=" + common.SensitiveLogMeta(err.Error()))
			sr.Error(err)
			return
		}
		if difyResponse.Event == "message_end" {
			usage = &difyResponse.MetaData.Usage
			sr.Done()
			return
		} else if difyResponse.Event == "error" {
			sr.Stop(fmt.Errorf("dify error event"))
			return
		}
		openaiResponse := *streamResponseDify2OpenAI(difyResponse)
		if len(openaiResponse.Choices) != 0 {
			responseText += openaiResponse.Choices[0].Delta.GetContentString()
			if openaiResponse.Choices[0].Delta.ReasoningContent != nil {
				nodeToken = common.SaturatingAddNonNegativeInt(nodeToken, 1)
			}
		}
		if err := helper.ObjectData(c, openaiResponse); err != nil {
			common.SysLog("failed to send stream response: error_meta=" + common.SensitiveLogMeta(err.Error()))
			sr.Error(err)
		}
	})
	helper.Done(c)
	if usage.TotalTokens == 0 {
		usage = service.ResponseText2Usage(c, responseText, info.UpstreamModelName, info.GetEstimatePromptTokens())
	}
	usage.CompletionTokens = common.SaturatingAddNonNegativeInt(usage.CompletionTokens, nodeToken)
	if usage.TotalTokens != 0 {
		usage.TotalTokens = common.SaturatingAddNonNegativeInt(usage.PromptTokens, usage.CompletionTokens)
	}
	return usage, nil
}

func difyHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	var difyResponse DifyChatCompletionResponse
	responseBody, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)

	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	service.CloseResponseBodyGracefully(resp)
	err = common.Unmarshal(responseBody, &difyResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	fullTextResponse := dto.OpenAITextResponse{
		Id:      difyResponse.ConversationId,
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Usage:   difyResponse.MetaData.Usage,
	}
	choice := dto.OpenAITextResponseChoice{
		Index: 0,
		Message: dto.Message{
			Role:    "assistant",
			Content: difyResponse.Answer,
		},
		FinishReason: "stop",
	}
	fullTextResponse.Choices = append(fullTextResponse.Choices, choice)
	jsonResponse, err := common.Marshal(fullTextResponse)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	c.Writer.Write(jsonResponse)
	return &difyResponse.MetaData.Usage, nil
}
