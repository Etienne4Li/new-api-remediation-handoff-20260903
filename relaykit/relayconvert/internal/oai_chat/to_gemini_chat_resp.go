package oaichat

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

var (
	geminiMarkdownImagePattern = regexp.MustCompile(`(?i)!\[[^\]]*\]\((data:image/[^;()\s]+;base64,[^)\s]+)\)`)
	geminiDataImagePattern     = regexp.MustCompile(`(?i)data:image/[^;,\s]+;base64,[A-Za-z0-9+/=_-]+`)
)

const maxGeminiInlineImageBytes = 20 << 20

type openAIImageOutput struct {
	Type      string          `json:"type,omitempty"`
	ImageURL  json.RawMessage `json:"image_url,omitempty"`
	URL       string          `json:"url,omitempty"`
	B64JSON   string          `json:"b64_json,omitempty"`
	MimeType  string          `json:"mime_type,omitempty"`
	MimeType2 string          `json:"mimeType,omitempty"`
}

type openAIImageURL struct {
	URL       string `json:"url,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	MimeType2 string `json:"mimeType,omitempty"`
}

func parseGeminiImageDataURL(value string) (*dto.GeminiInlineData, bool) {
	value = strings.TrimSpace(value)
	lowerValue := strings.ToLower(value)
	if !strings.HasPrefix(lowerValue, "data:image/") {
		return nil, false
	}
	comma := strings.IndexByte(value, ',')
	if comma < 0 || !strings.HasSuffix(lowerValue[:comma], ";base64") || comma == len(value)-1 {
		return nil, false
	}
	header := lowerValue[len("data:"):comma]
	semicolon := strings.IndexByte(header, ';')
	if semicolon <= 0 {
		return nil, false
	}
	mimeType := header[:semicolon]
	if !isSupportedGeminiImageMimeType(mimeType) {
		return nil, false
	}
	imageData := value[comma+1:]
	if !isValidGeminiImageBase64(imageData) {
		return nil, false
	}
	return &dto.GeminiInlineData{MimeType: mimeType, Data: imageData}, true
}

func isSupportedGeminiImageMimeType(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png", "image/jpeg", "image/webp", "image/gif", "image/avif":
		return true
	default:
		return false
	}
}

func isValidGeminiImageBase64(value string) bool {
	decodedLength := base64.StdEncoding.DecodedLen(len(value))
	if decodedLength == 0 || decodedLength > maxGeminiInlineImageBytes {
		return false
	}
	decoded := make([]byte, decodedLength)
	n, err := base64.StdEncoding.Strict().Decode(decoded, []byte(value))
	return err == nil && n > 0 && n <= maxGeminiInlineImageBytes
}

func appendGeminiImageReference(parts *[]dto.GeminiPart, value string, mimeType string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if inlineData, ok := parseGeminiImageDataURL(value); ok {
		*parts = append(*parts, dto.GeminiPart{InlineData: inlineData})
		return true
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		*parts = append(*parts, dto.GeminiPart{FileData: &dto.GeminiFileData{
			MimeType: mimeType,
			FileUri:  value,
		}})
		return true
	}
	if isSupportedGeminiImageMimeType(mimeType) && isValidGeminiImageBase64(value) {
		*parts = append(*parts, dto.GeminiPart{InlineData: &dto.GeminiInlineData{
			MimeType: strings.ToLower(strings.TrimSpace(mimeType)),
			Data:     value,
		}})
		return true
	}
	return false
}

func appendGeminiTextAndImages(parts *[]dto.GeminiPart, text string) {
	if text == "" {
		return
	}
	matches := geminiMarkdownImagePattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		rawMatches := geminiDataImagePattern.FindAllStringIndex(text, -1)
		if len(rawMatches) == 0 {
			*parts = append(*parts, dto.GeminiPart{Text: text})
			return
		}
		cursor := 0
		for _, match := range rawMatches {
			if prefix := text[cursor:match[0]]; prefix != "" {
				*parts = append(*parts, dto.GeminiPart{Text: prefix})
			}
			if !appendGeminiImageReference(parts, text[match[0]:match[1]], "") {
				*parts = append(*parts, dto.GeminiPart{Text: text[match[0]:match[1]]})
			}
			cursor = match[1]
		}
		if suffix := text[cursor:]; suffix != "" {
			*parts = append(*parts, dto.GeminiPart{Text: suffix})
		}
		return
	}

	cursor := 0
	for _, match := range matches {
		if prefix := text[cursor:match[0]]; prefix != "" {
			*parts = append(*parts, dto.GeminiPart{Text: prefix})
		}
		imageValue := text[match[2]:match[3]]
		if !appendGeminiImageReference(parts, imageValue, "") {
			*parts = append(*parts, dto.GeminiPart{Text: text[match[0]:match[1]]})
		}
		cursor = match[1]
	}
	if suffix := text[cursor:]; suffix != "" {
		*parts = append(*parts, dto.GeminiPart{Text: suffix})
	}
}

func appendOpenAIImageOutput(parts *[]dto.GeminiPart, output openAIImageOutput) {
	mimeType := output.MimeType
	if mimeType == "" {
		mimeType = output.MimeType2
	}
	if output.B64JSON != "" {
		appendGeminiImageReference(parts, output.B64JSON, mimeTypeOrDefault(mimeType, "image/png"))
		return
	}
	if output.URL != "" {
		appendGeminiImageReference(parts, output.URL, mimeType)
		return
	}
	if len(output.ImageURL) == 0 {
		return
	}
	var imageURL string
	if kitutil.Unmarshal(output.ImageURL, &imageURL) == nil && imageURL != "" {
		appendGeminiImageReference(parts, imageURL, mimeType)
		return
	}
	var imageObject openAIImageURL
	if kitutil.Unmarshal(output.ImageURL, &imageObject) != nil {
		return
	}
	if imageObject.MimeType == "" {
		imageObject.MimeType = imageObject.MimeType2
	}
	appendGeminiImageReference(parts, imageObject.URL, mimeTypeOrDefault(mimeType, imageObject.MimeType))
}

func appendOpenAIImages(parts *[]dto.GeminiPart, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var outputs []openAIImageOutput
	if kitutil.Unmarshal(raw, &outputs) == nil {
		for _, output := range outputs {
			appendOpenAIImageOutput(parts, output)
		}
		return
	}
	var output openAIImageOutput
	if kitutil.Unmarshal(raw, &output) == nil {
		appendOpenAIImageOutput(parts, output)
	}
}

func mimeTypeOrDefault(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func appendOpenAIMessageContent(parts *[]dto.GeminiPart, message dto.Message) {
	for _, content := range message.ParseContent() {
		switch content.Type {
		case dto.ContentTypeText:
			appendGeminiTextAndImages(parts, content.Text)
		case dto.ContentTypeImageURL:
			if image := content.GetImageMedia(); image != nil {
				appendGeminiImageReference(parts, image.Url, image.MimeType)
			}
		}
	}

	appendOpenAIImages(parts, message.Images)
}

// ResponseOpenAI2Gemini 将 OpenAI 响应转换为 Gemini 格式
func ResponseOpenAI2Gemini(openAIResponse *dto.OpenAITextResponse, info convmeta.Meta) *dto.GeminiChatResponse {
	totalTokens := openAIResponse.TotalTokens
	if totalTokens == 0 {
		totalTokens = openAIResponse.PromptTokens + openAIResponse.CompletionTokens
	}
	geminiResponse := &dto.GeminiChatResponse{
		Candidates:       make([]dto.GeminiChatCandidate, 0, len(openAIResponse.Choices)),
		HasUsageMetadata: true,
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     openAIResponse.PromptTokens,
			CandidatesTokenCount: openAIResponse.CompletionTokens,
			TotalTokenCount:      totalTokens,
			BillingUsage:         openAIBillingUsageFromUsage(&openAIResponse.Usage),
		},
	}
	if metadata, ok := geminiBillingMetadataFromOpenAIUsage(&openAIResponse.Usage); ok {
		geminiResponse.UsageMetadata = metadata
	}

	for _, choice := range openAIResponse.Choices {
		candidate := dto.GeminiChatCandidate{
			Index:         int64(choice.Index),
			SafetyRatings: []dto.GeminiChatSafetyRating{},
		}

		// 设置结束原因
		var finishReason string
		switch choice.FinishReason {
		case "stop":
			finishReason = "STOP"
		case "length":
			finishReason = "MAX_TOKENS"
		case "content_filter":
			finishReason = "SAFETY"
		case "tool_calls":
			finishReason = "STOP"
		default:
			finishReason = "STOP"
		}
		candidate.FinishReason = &finishReason

		// 转换消息内容
		content := dto.GeminiChatContent{
			Role:  "model",
			Parts: make([]dto.GeminiPart, 0),
		}

		appendOpenAIMessageContent(&content.Parts, choice.Message)

		toolCalls := choice.Message.ParseToolCalls()
		for _, toolCall := range toolCalls {
			var args map[string]interface{}
			if toolCall.Function.Arguments != "" {
				if err := kitutil.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
					args = map[string]interface{}{"arguments": toolCall.Function.Arguments}
				}
			} else {
				args = make(map[string]interface{})
			}

			part := dto.GeminiPart{
				FunctionCall: &dto.FunctionCall{
					FunctionName: toolCall.Function.Name,
					Arguments:    args,
				},
			}
			content.Parts = append(content.Parts, part)
		}

		candidate.Content = content
		geminiResponse.Candidates = append(geminiResponse.Candidates, candidate)
	}

	return geminiResponse
}

// StreamResponseOpenAI2Gemini 将 OpenAI 流式响应转换为 Gemini 格式
func StreamResponseOpenAI2Gemini(openAIResponse *dto.ChatCompletionsStreamResponse, info convmeta.Meta) *dto.GeminiChatResponse {
	// 检查是否有实际内容或结束标志
	hasContent := false
	hasFinishReason := false
	for _, choice := range openAIResponse.Choices {
		if len(choice.Delta.GetContentString()) > 0 || (choice.Delta.ToolCalls != nil && len(choice.Delta.ToolCalls) > 0) {
			hasContent = true
		}
		if choice.FinishReason != nil {
			hasFinishReason = true
		}
	}

	// 如果没有实际内容且没有结束标志，跳过。主要针对 openai 流响应开头的空数据
	if !hasContent && !hasFinishReason {
		return nil
	}

	estimatePromptTokens := 0
	if info != nil {
		estimatePromptTokens = info.GetEstimatePromptTokens()
	}
	geminiResponse := &dto.GeminiChatResponse{
		Candidates:       make([]dto.GeminiChatCandidate, 0, len(openAIResponse.Choices)),
		HasUsageMetadata: true,
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     estimatePromptTokens,
			CandidatesTokenCount: 0, // 流式响应中可能没有完整的 usage 信息
			TotalTokenCount:      estimatePromptTokens,
		},
	}

	if openAIResponse.Usage != nil {
		geminiResponse.UsageMetadata.PromptTokenCount = openAIResponse.Usage.PromptTokens
		geminiResponse.UsageMetadata.CandidatesTokenCount = openAIResponse.Usage.CompletionTokens
		geminiResponse.UsageMetadata.TotalTokenCount = openAIResponse.Usage.TotalTokens
		geminiResponse.UsageMetadata.BillingUsage = openAIBillingUsageFromUsage(openAIResponse.Usage)
		if metadata, ok := geminiBillingMetadataFromOpenAIUsage(openAIResponse.Usage); ok {
			geminiResponse.UsageMetadata = metadata
		}
	}

	for _, choice := range openAIResponse.Choices {
		candidate := dto.GeminiChatCandidate{
			Index:         int64(choice.Index),
			SafetyRatings: []dto.GeminiChatSafetyRating{},
		}

		// 设置结束原因
		if choice.FinishReason != nil {
			var finishReason string
			switch *choice.FinishReason {
			case "stop":
				finishReason = "STOP"
			case "length":
				finishReason = "MAX_TOKENS"
			case "content_filter":
				finishReason = "SAFETY"
			case "tool_calls":
				finishReason = "STOP"
			default:
				finishReason = "STOP"
			}
			candidate.FinishReason = &finishReason
		}

		// 转换消息内容
		content := dto.GeminiChatContent{
			Role:  "model",
			Parts: make([]dto.GeminiPart, 0),
		}

		// 处理工具调用
		if choice.Delta.ToolCalls != nil {
			for _, toolCall := range choice.Delta.ToolCalls {
				// 解析参数
				var args map[string]interface{}
				if toolCall.Function.Arguments != "" {
					if err := kitutil.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
						args = map[string]interface{}{"arguments": toolCall.Function.Arguments}
					}
				} else {
					args = make(map[string]interface{})
				}

				part := dto.GeminiPart{
					FunctionCall: &dto.FunctionCall{
						FunctionName: toolCall.Function.Name,
						Arguments:    args,
					},
				}
				content.Parts = append(content.Parts, part)
			}
		} else {
			// 流式文本可能在任意字节边界拆分，不能逐块解析 data URL。
			textContent := choice.Delta.GetContentString()
			if textContent != "" {
				content.Parts = append(content.Parts, dto.GeminiPart{Text: textContent})
			}
		}

		candidate.Content = content
		geminiResponse.Candidates = append(geminiResponse.Candidates, candidate)
	}

	return geminiResponse
}

func geminiBillingMetadataFromOpenAIUsage(usage *dto.Usage) (dto.GeminiUsageMetadata, bool) {
	if usage == nil || usage.BillingUsage == nil || usage.BillingUsage.GeminiUsageMetadata == nil {
		return dto.GeminiUsageMetadata{}, false
	}
	if usage.BillingUsage.Source != dto.BillingUsageSourceGeminiChat && usage.BillingUsage.Semantic != dto.BillingUsageSemanticGemini {
		return dto.GeminiUsageMetadata{}, false
	}
	billingUsage := dto.CloneBillingUsage(usage.BillingUsage)
	if billingUsage == nil || billingUsage.GeminiUsageMetadata == nil {
		return dto.GeminiUsageMetadata{}, false
	}
	return *billingUsage.GeminiUsageMetadata, true
}

func openAIBillingUsageFromUsage(usage *dto.Usage) *dto.BillingUsage {
	if usage == nil {
		return nil
	}
	if existingBillingUsage := dto.CloneBillingUsage(usage.BillingUsage); existingBillingUsage != nil && existingBillingUsage.OpenAIUsage != nil {
		if existingBillingUsage.Source == dto.BillingUsageSourceOAIChat ||
			existingBillingUsage.Source == dto.BillingUsageSourceOAIResponses ||
			existingBillingUsage.Semantic == dto.BillingUsageSemanticOpenAI {
			return existingBillingUsage
		}
	}
	return dto.NewOpenAIChatBillingUsage(usage)
}
