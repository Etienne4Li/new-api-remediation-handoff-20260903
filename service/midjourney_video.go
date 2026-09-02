package service

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

const (
	MaxMidjourneyVideoURLsJSONBytes = 1 << 20
	midjourneyProxyTaskIDPrefix     = "~mj1~"
)

// ParseMidjourneyVideoURLs validates the persisted provider list before a
// public proxy URL is exposed or an indexed media fetch is attempted.
func ParseMidjourneyVideoURLs(raw string) ([]dto.ImgUrls, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if len([]byte(trimmed)) > MaxMidjourneyVideoURLsJSONBytes {
		return nil, errors.New("midjourney video URL list is too large")
	}
	var videoURLs []dto.ImgUrls
	if err := common.Unmarshal([]byte(trimmed), &videoURLs); err != nil {
		return nil, fmt.Errorf("invalid midjourney video URL list: %w", err)
	}
	if len(videoURLs) > MaxMidjourneyVideoURLCount {
		return nil, errors.New("too many midjourney video URLs")
	}
	for index := range videoURLs {
		videoURLs[index].Url = strings.TrimSpace(videoURLs[index].Url)
		if videoURLs[index].Url == "" {
			return nil, fmt.Errorf("midjourney video URL %d is empty", index)
		}
		if err := ValidateMidjourneyMediaURL(videoURLs[index].Url); err != nil {
			return nil, fmt.Errorf("midjourney video URL %d: %w", index, err)
		}
	}
	return videoURLs, nil
}

// EncodeMidjourneyProxyTaskID keeps common provider ids readable while using a
// route-safe representation for ids that Gin would otherwise split after URL
// path decoding (notably percent-encoded slashes).
func EncodeMidjourneyProxyTaskID(taskID string) string {
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, midjourneyProxyTaskIDPrefix) && url.PathEscape(trimmed) == trimmed {
		return trimmed
	}
	return midjourneyProxyTaskIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(trimmed))
}

// DecodeMidjourneyProxyTaskID accepts both the versioned route-safe form and
// legacy plain task ids. Re-encoding rejects non-canonical base64 spellings.
func DecodeMidjourneyProxyTaskID(routeID string) (string, error) {
	trimmed := strings.TrimSpace(routeID)
	if !strings.HasPrefix(trimmed, midjourneyProxyTaskIDPrefix) {
		return trimmed, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(trimmed, midjourneyProxyTaskIDPrefix))
	if err != nil || len(decoded) == 0 {
		return "", errors.New("invalid encoded midjourney task id")
	}
	taskID := string(decoded)
	if EncodeMidjourneyProxyTaskID(taskID) != trimmed {
		return "", errors.New("non-canonical encoded midjourney task id")
	}
	return taskID, nil
}

// BuildMidjourneyImageProxyURL returns the authenticated image route.
func BuildMidjourneyImageProxyURL(serverAddress, taskID string) string {
	return buildMidjourneyMediaProxyURL(serverAddress, "image", taskID, "")
}

// BuildMidjourneyVideoProxyURL returns the authenticated singular-video route.
func BuildMidjourneyVideoProxyURL(serverAddress, taskID string) string {
	return buildMidjourneyMediaProxyURL(serverAddress, "video", taskID, "")
}

// BuildMidjourneyIndexedVideoProxyURL returns an authenticated list-item route.
func BuildMidjourneyIndexedVideoProxyURL(serverAddress, taskID string, index int) string {
	return buildMidjourneyMediaProxyURL(serverAddress, "video", taskID, "/"+strconv.Itoa(index))
}

func buildMidjourneyMediaProxyURL(serverAddress, mediaType, taskID, suffix string) string {
	base := strings.TrimRight(strings.TrimSpace(serverAddress), "/")
	path := "/mj/" + mediaType + "/" + EncodeMidjourneyProxyTaskID(taskID) + suffix
	if base == "" {
		return path
	}
	return base + path
}
