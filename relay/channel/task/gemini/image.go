package gemini

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

const maxVeoImageSize = 20 * 1024 * 1024 // 20 MB

func detectVeoImageMIME(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	detected := strings.ToLower(strings.TrimSpace(strings.SplitN(http.DetectContentType(data), ";", 2)[0]))
	if !strings.HasPrefix(detected, "image/") {
		return ""
	}
	switch detected {
	case "image/png", "image/jpeg", "image/webp":
		return detected
	default:
		return ""
	}
}

// ExtractMultipartImage reads the first `input_reference` file from a multipart
// form upload and returns a VeoImageInput. Returns nil if no file is present.
func ExtractMultipartImage(c *gin.Context, info *relaycommon.RelayInfo) *VeoImageInput {
	if c == nil {
		return nil
	}
	mf, err := c.MultipartForm()
	if err != nil {
		return nil
	}
	files, exists := mf.File["input_reference"]
	if !exists || len(files) == 0 {
		return nil
	}
	fh := files[0]
	if fh.Size > maxVeoImageSize {
		return nil
	}
	file, err := fh.Open()
	if err != nil {
		return nil
	}
	defer file.Close()

	fileBytes, err := common.ReadBodyLimited(file, fh.Size, maxVeoImageSize)
	if err != nil {
		return nil
	}

	mimeType := detectVeoImageMIME(fileBytes)
	if mimeType == "" {
		return nil
	}

	if info != nil {
		info.Action = constant.TaskActionGenerate
	}
	return &VeoImageInput{
		BytesBase64Encoded: base64.StdEncoding.EncodeToString(fileBytes),
		MimeType:           mimeType,
	}
}

// ParseImageInput parses an image string (data URI or raw base64) into a
// VeoImageInput. Returns nil if the input is empty or invalid.
// TODO: support downloading HTTP URL images and converting to base64
func ParseImageInput(imageStr string) *VeoImageInput {
	imageStr = strings.TrimSpace(imageStr)
	if imageStr == "" {
		return nil
	}

	if strings.HasPrefix(imageStr, "data:") {
		return parseDataURI(imageStr)
	}

	raw, err := common.DecodeBase64Limited(imageStr, maxVeoImageSize)
	if err != nil {
		return nil
	}
	mimeType := detectVeoImageMIME(raw)
	if mimeType == "" {
		return nil
	}
	return &VeoImageInput{
		BytesBase64Encoded: imageStr,
		MimeType:           mimeType,
	}
}

func parseDataURI(uri string) *VeoImageInput {
	rest := uri[len("data:"):]
	idx := strings.Index(rest, ",")
	if idx < 0 {
		return nil
	}
	meta := rest[:idx]
	b64 := rest[idx+1:]
	if b64 == "" {
		return nil
	}

	parts := strings.Split(meta, ";")
	hasBase64 := false
	for _, part := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			hasBase64 = true
		}
	}
	if !hasBase64 {
		return nil
	}
	raw, err := common.DecodeBase64Limited(b64, maxVeoImageSize)
	if err != nil {
		return nil
	}
	mimeType := detectVeoImageMIME(raw)
	if mimeType == "" {
		return nil
	}

	return &VeoImageInput{
		BytesBase64Encoded: b64,
		MimeType:           mimeType,
	}
}
