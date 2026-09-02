package service

import (
	"fmt"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// GetFileTypeFromUrl 获取文件类型，返回 mime type， 例如 image/jpeg, image/png, image/gif, image/bmp, image/tiff, application/pdf
// 如果获取失败，返回 application/octet-stream
func GetFileTypeFromUrl(c *gin.Context, url string, reason ...string) (string, error) {
	response, err := DoDownloadRequest(url, []string{"get_mime_type", strings.Join(reason, ", ")}...)
	if err != nil {
		common.SysLog(fmt.Sprintf("fail to get file type from url_meta=%s, error: %s", common.SensitiveLogMeta(url), err.Error()))
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		message := fmt.Sprintf("failed to download file from url_meta=%s, status code: %d", common.SensitiveLogMeta(url), response.StatusCode)
		if c != nil {
			logger.LogError(c, message)
		} else {
			common.SysLog(message)
		}
		return "", fmt.Errorf("failed to download file, status code: %d", response.StatusCode)
	}

	// Read a bounded prefix before looking at any provider declaration. A
	// Content-Type header is untrusted input and must not override a concrete
	// signature (for example HTML labelled as image/png).
	const probeLimit int64 = 64 * 1024
	readData, readErr := io.ReadAll(io.LimitReader(response.Body, probeLimit))
	if readErr != nil {
		return "", fmt.Errorf("failed to inspect file content: %w", readErr)
	}
	if c != nil {
		logger.LogDebug(c, "Inspected %d bytes to determine file type", len(readData))
	}
	resolved, resolveErr := resolveFileMIME(readData,
		response.Header.Get("Content-Type"),
		mimeFromContentDisposition(response.Header.Get("Content-Disposition")),
		guessMimeTypeFromURL(url),
	)
	if resolveErr != nil {
		return "", resolveErr
	}
	return resolved, nil
}

// GetFileBase64FromUrl 从 URL 获取文件的 base64 编码数据
// Deprecated: 请使用 GetBase64Data 配合 types.NewURLFileSource 替代
// 此函数保留用于向后兼容，内部已重构为调用统一的文件服务
func GetFileBase64FromUrl(c *gin.Context, url string, reason ...string) (*types.LocalFileData, error) {
	source := types.NewURLFileSource(url)
	cachedData, err := LoadFileSource(c, source, reason...)
	if err != nil {
		return nil, err
	}

	// 转换为旧的 LocalFileData 格式以保持兼容
	base64Data, err := cachedData.GetBase64Data()
	if err != nil {
		return nil, err
	}
	return &types.LocalFileData{
		Base64Data: base64Data,
		MimeType:   cachedData.MimeType,
		Size:       cachedData.Size,
		Url:        url,
	}, nil
}

func GetMimeTypeByExtension(ext string) string {
	// Convert to lowercase for case-insensitive comparison
	ext = strings.ToLower(ext)
	switch ext {
	// Text files
	case "txt", "md", "markdown", "csv", "json", "xml", "html", "htm":
		return "text/plain"

	// Image files
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "jfif":
		return "image/jpeg"
	case "heic":
		return "image/heic"
	case "heif":
		return "image/heif"

	// Audio files
	case "mp3":
		return "audio/mp3"
	case "wav":
		return "audio/wav"
	case "mpeg":
		return "audio/mpeg"

	// Video files
	case "mp4":
		return "video/mp4"
	case "wmv":
		return "video/wmv"
	case "flv":
		return "video/flv"
	case "mov":
		return "video/mov"
	case "mpg":
		return "video/mpg"
	case "avi":
		return "video/avi"
	case "mpegps":
		return "video/mpegps"

	// Document files
	case "pdf":
		return "application/pdf"

	default:
		return "application/octet-stream" // Default for unknown types
	}
}
