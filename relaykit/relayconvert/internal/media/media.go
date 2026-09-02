package media

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"sync"

	"context"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type MediaResolver struct {
	GetBase64Data        func(c context.Context, source types.FileSource, reason ...string) (string, string, error)
	DecodeBase64FileData func(base64String string) (string, string, error)
}

const maxResolvedMediaMIMEBytes = 256
const maxResolvedMediaBytes int64 = 128 << 20

func normalizeResolvedMediaMIME(raw string) (string, error) {
	if len(raw) > maxResolvedMediaMIMEBytes {
		return "", errors.New("resolved media MIME type is too long")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err != nil || mediaType == "" {
		return "", fmt.Errorf("invalid resolved media MIME type %q", raw)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "image/svg+xml" || mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return "", fmt.Errorf("unsafe resolved media MIME type %q", mediaType)
	}
	switch {
	case strings.HasPrefix(mediaType, "image/"),
		strings.HasPrefix(mediaType, "audio/"),
		strings.HasPrefix(mediaType, "video/"),
		mediaType == "application/pdf",
		mediaType == "text/plain":
		return mediaType, nil
	default:
		return "", fmt.Errorf("unsupported resolved media MIME type %q", mediaType)
	}
}

func validateResolvedBase64(data string) error {
	if strings.TrimSpace(data) == "" {
		return errors.New("resolved base64 media data is empty")
	}
	maxEncoded := ((maxResolvedMediaBytes + 2) / 3) * 4
	if int64(len(data)) > maxEncoded {
		return fmt.Errorf("resolved base64 media data exceeds %d bytes", maxResolvedMediaBytes)
	}
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(data))
	n, err := io.Copy(io.Discard, io.LimitReader(decoder, maxResolvedMediaBytes+1))
	if err != nil {
		return fmt.Errorf("invalid resolved base64 media data: %w", err)
	}
	if n > maxResolvedMediaBytes {
		return fmt.Errorf("resolved base64 media data exceeds %d bytes", maxResolvedMediaBytes)
	}
	return nil
}

var (
	mediaResolverMu sync.RWMutex
	mediaResolver   MediaResolver
)

func SetMediaResolver(resolver MediaResolver) {
	mediaResolverMu.Lock()
	defer mediaResolverMu.Unlock()

	mediaResolver = resolver
}

func ResolveBase64Data(c context.Context, source types.FileSource, reason ...string) (string, string, error) {
	mediaResolverMu.RLock()
	resolver := mediaResolver.GetBase64Data
	mediaResolverMu.RUnlock()
	if resolver == nil {
		return "", "", errors.New("relayconvert media resolver is not configured")
	}
	data, mediaType, err := resolver(c, source, reason...)
	if err != nil {
		return "", "", err
	}
	if err := validateResolvedBase64(data); err != nil {
		return "", "", err
	}
	mediaType, err = normalizeResolvedMediaMIME(mediaType)
	if err != nil {
		return "", "", err
	}
	return data, mediaType, nil
}

func DecodeBase64FileData(base64String string) (string, string, error) {
	mediaResolverMu.RLock()
	resolver := mediaResolver.DecodeBase64FileData
	mediaResolverMu.RUnlock()
	if resolver == nil {
		return "", "", errors.New("relayconvert media resolver is not configured")
	}
	mediaType, data, err := resolver(base64String)
	if err != nil {
		return "", "", err
	}
	if err := validateResolvedBase64(data); err != nil {
		return "", "", err
	}
	mediaType, err = normalizeResolvedMediaMIME(mediaType)
	if err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(mediaType, "image/") {
		return "", "", fmt.Errorf("resolved markdown media is not an image: %q", mediaType)
	}
	return mediaType, data, nil
}
