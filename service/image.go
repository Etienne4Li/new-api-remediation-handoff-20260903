package service

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"golang.org/x/image/webp"
)

const (
	// Image dimensions are carried in container headers.  A bounded prefix is
	// enough for the supported decoders and prevents malformed metadata from
	// forcing a full image allocation just to determine its dimensions.
	maxImageConfigBytes int64 = 1 << 20
	// Decoders only need the dimensions for token estimation.  Rejecting
	// implausibly large headers keeps subsequent area/tile arithmetic bounded
	// even when a compressed image advertises dimensions far beyond what the
	// download-size limit could realistically contain.
	maxImageDimension = 1 << 20
)

func validateImageDimensions(config image.Config) error {
	// Zero dimensions are used by callers to represent a non-image payload, so
	// leave that sentinel untouched. Negative dimensions, however, are never
	// valid and can poison token arithmetic.
	if config.Width < 0 || config.Height < 0 {
		return fmt.Errorf("image dimensions must be non-negative")
	}
	if config.Width > maxImageDimension || config.Height > maxImageDimension {
		return fmt.Errorf("image dimensions exceed maximum allowed size of %d", maxImageDimension)
	}
	return nil
}

func imageDataMaxBytes() int64 {
	return common.GetMaxFileDownloadBytes()
}

func decodeBase64ImagePayload(payload string) ([]byte, error) {
	return common.DecodeBase64Limited(payload, imageDataMaxBytes())
}

// return image.Config, format, clean base64 string, error
func DecodeBase64ImageData(base64String string) (image.Config, string, string, error) {
	cleanBase64, declaredMIME, err := parseBase64DataURL(base64String)
	if err != nil {
		return image.Config{}, "", "", err
	}
	if declaredMIME != "" && !strings.HasPrefix(declaredMIME, "image/") {
		return image.Config{}, "", "", fmt.Errorf("%w: declared type %s is not an image", errUnsupportedFileMIME, declaredMIME)
	}

	// 将base64字符串解码为字节切片。先限制编码后的长度，避免
	// DecodeString 为恶意输入分配不受控的内存。
	decodedData, err := decodeBase64ImagePayload(cleanBase64)
	if err != nil {
		return image.Config{}, "", "", fmt.Errorf("failed to decode base64 string: %w", err)
	}

	// 创建一个bytes.Buffer用于存储解码后的数据
	reader := bytes.NewReader(decodedData)
	config, format, err := getImageConfig(reader)
	return config, format, cleanBase64, err
}

func DecodeBase64FileData(base64String string) (string, string, error) {
	payload, declaredMIME, err := parseBase64DataURL(base64String)
	if err != nil {
		return "", "", err
	}
	decodedData, err := decodeBase64ImagePayload(payload)
	if err != nil {
		return "", "", fmt.Errorf("failed to decode base64 file data: %w", err)
	}
	// This helper is consumed by the Markdown-image conversion path. Require an
	// actual safe image signature so `data:image/png;base64,<arbitrary>` cannot
	// be forwarded as image content or used to smuggle active MIME.
	mimeType, err := resolveImageMIME(decodedData, declaredMIME)
	if err != nil {
		return "", "", err
	}
	return mimeType, payload, nil
}

// GetImageFromUrl 获取图片的类型和base64编码的数据
func GetImageFromUrl(url string) (mimeType string, data string, err error) {
	resp, err := DoDownloadRequest(url)
	if err != nil {
		return "", "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	// Check HTTP status code
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("failed to download image: HTTP %d", resp.StatusCode)
	}

	maxImageSize := imageDataMaxBytes()
	fileBytes, err := common.ReadBodyLimited(resp.Body, resp.ContentLength, maxImageSize)
	if err != nil {
		if errors.Is(err, common.ErrRequestBodyTooLarge) {
			return "", "", fmt.Errorf("image size exceeds maximum allowed size of %d bytes", maxImageSize)
		}
		return "", "", fmt.Errorf("failed to read image data: %w", err)
	}

	data = base64.StdEncoding.EncodeToString(fileBytes)
	mimeType, err = resolveImageMIME(fileBytes,
		resp.Header.Get("Content-Type"),
		mimeFromContentDisposition(resp.Header.Get("Content-Disposition")),
		guessMimeTypeFromURL(url),
	)
	if err != nil {
		return "", "", err
	}

	return mimeType, data, nil
}

func DecodeUrlImageData(imageUrl string) (image.Config, string, error) {
	response, err := DoDownloadRequest(imageUrl)
	if err != nil {
		common.SysLog(fmt.Sprintf("fail to get image from URL, error_meta=%s", common.SensitiveLogMeta(err.Error())))
		return image.Config{}, "", err
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		err = errors.New(fmt.Sprintf("fail to get image from url: %s", response.Status))
		return image.Config{}, "", err
	}

	var readData []byte
	for _, limit := range []int64{1024 * 8, 1024 * 24, 1024 * 64} {
		common.SysLog(fmt.Sprintf("try to decode image config with limit: %d", limit))

		// 从response.Body读取更多的数据直到达到当前的限制
		remaining := limit - int64(len(readData))
		if remaining > 0 {
			additionalData := make([]byte, remaining)
			n, readErr := io.ReadFull(response.Body, additionalData)
			if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
				return image.Config{}, "", fmt.Errorf("failed to read image data: %w", readErr)
			}
			readData = append(readData, additionalData[:n]...)
		}

		var config image.Config
		var format string
		config, format, err = getImageConfig(bytes.NewReader(readData))
		if err == nil {
			return config, format, nil
		}
	}

	return image.Config{}, "", err // 返回最后一个错误
}

func getImageConfig(reader io.Reader) (image.Config, string, error) {
	// Read only a bounded prefix. All supported formats expose dimensions in
	// their headers, and callers already retry with progressively larger
	// prefixes when a decoder needs more metadata.
	data, readErr := io.ReadAll(io.LimitReader(reader, maxImageConfigBytes+1))
	if readErr != nil {
		return image.Config{}, "", fmt.Errorf("failed to read image data: %w", readErr)
	}
	if int64(len(data)) > maxImageConfigBytes {
		return image.Config{}, "", fmt.Errorf("image config data exceeds maximum allowed size of %d bytes", maxImageConfigBytes)
	}

	// 读取图片的头部信息来获取图片尺寸
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err == nil {
		if dimensionErr := validateImageDimensions(config); dimensionErr != nil {
			return image.Config{}, "", dimensionErr
		}
		return config, format, nil
	}
	common.SysLog(fmt.Sprintf("fail to decode image config(gif, jpg, png): error_meta=%s", common.SensitiveLogMeta(err.Error())))

	config, err = webp.DecodeConfig(bytes.NewReader(data))
	if err == nil {
		if dimensionErr := validateImageDimensions(config); dimensionErr != nil {
			return image.Config{}, "", dimensionErr
		}
		return config, "webp", nil
	}
	common.SysLog(fmt.Sprintf("fail to decode image config(webp): error_meta=%s", common.SensitiveLogMeta(err.Error())))

	// Try HEIF/HEIC: parse ISOBMFF ispe box for dimensions
	if heifMime := detectHEIF(data); heifMime != "" {
		formatName := "heif"
		if heifMime == "image/heic" {
			formatName = "heic"
		}
		if w, h, ok := parseHEIFDimensions(data); ok {
			config := image.Config{Width: w, Height: h}
			if dimensionErr := validateImageDimensions(config); dimensionErr != nil {
				return image.Config{}, "", dimensionErr
			}
			return config, formatName, nil
		}
		return image.Config{}, "", fmt.Errorf("failed to decode HEIF/HEIC image dimensions")
	}

	return image.Config{}, "", err
}
