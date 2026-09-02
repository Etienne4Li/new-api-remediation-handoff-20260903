package service

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// ErrUnsupportedVideoMIME indicates that a payload is not a recognized,
// browser-safe video container.  Callers can use errors.Is to distinguish a
// content validation failure from transport or JSON errors.
var ErrUnsupportedVideoMIME = errors.New("unsupported or unsafe video MIME type")

var safeVideoMIMETypes = map[string]struct{}{
	"video/3gpp":      {},
	"video/3gpp2":     {},
	"video/avi":       {},
	"video/flv":       {},
	"video/mpeg":      {},
	"video/mpegps":    {},
	"video/mp4":       {},
	"video/ogg":       {},
	"video/quicktime": {},
	"video/webm":      {},
	"video/wmv":       {},
}

var videoMIMEAliases = map[string]string{
	"video/mov":       "video/quicktime",
	"video/mpg":       "video/mpeg",
	"video/mpeg-ps":   "video/mpegps",
	"video/x-flv":     "video/flv",
	"video/x-msvideo": "video/avi",
	"video/x-ms-wmv":  "video/wmv",
}

// NormalizeVideoMIME parses and canonicalizes a provider-declared media type.
// Parameters are accepted for HTTP header compatibility but are never copied
// into a data URL or response header by callers.
func NormalizeVideoMIME(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", fmt.Errorf("invalid video MIME type: %w", err)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if alias, ok := videoMIMEAliases[mediaType]; ok {
		mediaType = alias
	}
	return mediaType, nil
}

// IsSafeVideoMIME reports whether a canonical MIME type is in the finite
// allowlist emitted by the video proxy.
func IsSafeVideoMIME(mediaType string) bool {
	_, ok := safeVideoMIMETypes[strings.ToLower(strings.TrimSpace(mediaType))]
	return ok
}

// SniffVideoMIME identifies a video from container bytes. Provider headers and
// extensions are intentionally not consulted.
func SniffVideoMIME(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if detected, err := NormalizeVideoMIME(http.DetectContentType(data)); err == nil && IsSafeVideoMIME(detected) {
		return detected
	}

	// The standard library deliberately recognizes only a small set of video
	// signatures. Cover the formats emitted by the async providers as well,
	// while requiring a real container marker rather than trusting an extension.
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("AVI ")) {
		return "video/avi"
	}
	if len(data) >= 9 && bytes.Equal(data[:3], []byte("FLV")) && data[3] == 1 &&
		(data[4]&0xfa) == 0 && binary.BigEndian.Uint32(data[5:9]) >= 9 {
		return "video/flv"
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS")) {
		return "video/ogg"
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return "video/webm"
	}
	if len(data) >= 4 && (bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xba}) ||
		bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xb3})) {
		return "video/mpeg"
	}
	if isMPEGTransportStream(data) {
		return "video/mpeg"
	}
	if isASFVideo(data) {
		return "video/wmv"
	}
	if isoType := sniffISOBaseMediaVideo(data); isoType != "" {
		return isoType
	}
	return ""
}

// ResolveVideoMIME validates a declaration against the actual container. A
// declaration can choose the canonical output label only after the bytes have
// independently identified as a safe video.
func ResolveVideoMIME(declaredRaw string, data []byte) (string, error) {
	declaredType := ""
	if strings.TrimSpace(declaredRaw) != "" {
		var err error
		declaredType, err = NormalizeVideoMIME(declaredRaw)
		if err != nil || !IsSafeVideoMIME(declaredType) {
			return "", fmt.Errorf("%w: declared type", ErrUnsupportedVideoMIME)
		}
	}
	detectedType := SniffVideoMIME(data)
	if !IsSafeVideoMIME(detectedType) {
		return "", fmt.Errorf("%w: container signature not recognized", ErrUnsupportedVideoMIME)
	}
	if declaredType != "" && !compatibleVideoMIME(declaredType, detectedType) {
		return "", fmt.Errorf("%w: declared and detected types differ", ErrUnsupportedVideoMIME)
	}
	if declaredType != "" {
		return declaredType, nil
	}
	return detectedType, nil
}

// DecodeBase64VideoData strictly decodes a raw base64 payload or a data URL,
// then validates the resulting bytes as a safe video. The returned bytes are
// suitable for writing to a response or re-encoding; no provider-controlled
// MIME string is ever interpolated into the result.
func DecodeBase64VideoData(raw string, maxBytes int64) (string, []byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, fmt.Errorf("%w: empty payload", ErrUnsupportedVideoMIME)
	}

	payload := raw
	declaredType := ""
	if len(raw) >= len("data:") && strings.EqualFold(raw[:len("data:")], "data:") {
		comma := strings.IndexByte(raw, ',')
		if comma <= len("data:") {
			return "", nil, fmt.Errorf("%w: invalid data URL", ErrUnsupportedVideoMIME)
		}
		metadata := raw[len("data:"):comma]
		parts := strings.Split(metadata, ";")
		declaredRaw := strings.TrimSpace(parts[0])
		seenBase64 := false
		for _, parameter := range parts[1:] {
			parameter = strings.TrimSpace(parameter)
			if strings.EqualFold(parameter, "base64") {
				if seenBase64 {
					return "", nil, fmt.Errorf("%w: duplicate base64 marker", ErrUnsupportedVideoMIME)
				}
				seenBase64 = true
				continue
			}
			// Arbitrary data URL parameters are not needed for video and can
			// smuggle a conflicting charset or active-content declaration.
			return "", nil, fmt.Errorf("%w: unsupported data URL parameter", ErrUnsupportedVideoMIME)
		}
		if !seenBase64 || strings.TrimSpace(raw[comma+1:]) == "" {
			return "", nil, fmt.Errorf("%w: data URL must contain base64 payload", ErrUnsupportedVideoMIME)
		}
		if declaredRaw != "" {
			var err error
			declaredType, err = NormalizeVideoMIME(declaredRaw)
			if err != nil || !IsSafeVideoMIME(declaredType) {
				return "", nil, fmt.Errorf("%w: declared type", ErrUnsupportedVideoMIME)
			}
		}
		payload = raw[comma+1:]
	}

	if strings.TrimSpace(payload) == "" {
		return "", nil, fmt.Errorf("%w: empty base64 payload", ErrUnsupportedVideoMIME)
	}
	if maxBytes <= 0 {
		maxBytes = common.GetMaxFileDownloadBytes()
	}
	if maxBytes <= 0 {
		return "", nil, fmt.Errorf("%w: invalid size limit", ErrUnsupportedVideoMIME)
	}
	encodedLimit := maxEncodedBase64Bytes(maxBytes)
	if int64(len(payload)) > encodedLimit {
		return "", nil, fmt.Errorf("%w: payload exceeds maximum size", ErrUnsupportedVideoMIME)
	}

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		// Keep compatibility with providers that omit padding, while retaining
		// the same pre-allocation bound and rejecting impossible lengths.
		if len(payload)%4 == 1 {
			return "", nil, fmt.Errorf("%w: invalid base64 payload: %v", ErrUnsupportedVideoMIME, err)
		}
		decoded, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return "", nil, fmt.Errorf("%w: invalid base64 payload: %v", ErrUnsupportedVideoMIME, err)
		}
	}
	if int64(len(decoded)) > maxBytes {
		return "", nil, fmt.Errorf("%w: decoded payload exceeds maximum size", ErrUnsupportedVideoMIME)
	}

	mimeType, err := ResolveVideoMIME(declaredType, decoded)
	if err != nil {
		return "", nil, err
	}
	return mimeType, decoded, nil
}

func maxEncodedBase64Bytes(maxBytes int64) int64 {
	maxInt64 := int64(^uint64(0) >> 1)
	if maxBytes <= 0 {
		return 0
	}
	if maxBytes > (maxInt64-4)/4*3 {
		return maxInt64
	}
	return ((maxBytes + 2) / 3 * 4) + 4
}

func compatibleVideoMIME(declared, detected string) bool {
	if declared == detected {
		return true
	}
	// QuickTime and MP4 are both ISO Base Media containers. Providers have
	// historically used either label for the same .mov/.mp4 payload.
	if (declared == "video/mp4" || declared == "video/quicktime") &&
		(detected == "video/mp4" || detected == "video/quicktime") {
		return true
	}
	if declared == "video/mpegps" && detected == "video/mpeg" {
		return true
	}
	return false
}

func sniffISOBaseMediaVideo(data []byte) string {
	if len(data) < 16 || !bytes.Equal(data[4:8], []byte("ftyp")) {
		return ""
	}
	boxSize := binary.BigEndian.Uint32(data[:4])
	if boxSize < 16 || boxSize%4 != 0 || uint64(boxSize) > uint64(len(data)) {
		return ""
	}
	end := int(boxSize)
	majorBrand := string(data[8:12])
	if isQuickTimeISOBrand(majorBrand) {
		return "video/quicktime"
	}
	if isoType := threeGPPISOType(majorBrand); isoType != "" {
		return isoType
	}
	if isVideoISOBrand(majorBrand) {
		return "video/mp4"
	}
	for offset := 16; offset+4 <= end; offset += 4 {
		brand := string(data[offset : offset+4])
		if isQuickTimeISOBrand(brand) {
			return "video/quicktime"
		}
		if isoType := threeGPPISOType(brand); isoType != "" {
			return isoType
		}
		if isVideoISOBrand(brand) {
			return "video/mp4"
		}
	}
	return ""
}

func isQuickTimeISOBrand(brand string) bool {
	return brand == "qt  " || brand == "quick"
}

func threeGPPISOType(brand string) string {
	if strings.HasPrefix(brand, "3g2") {
		return "video/3gpp2"
	}
	if strings.HasPrefix(brand, "3gp") {
		return "video/3gpp"
	}
	return ""
}

func isVideoISOBrand(brand string) bool {
	if strings.HasPrefix(brand, "mp4") || strings.HasPrefix(brand, "iso") {
		return true
	}
	switch brand {
	case "avc1", "av01", "dash", "f4v ", "hvc1", "hev1", "isml", "M4V ", "mp71", "MSNV", "cmfc", "cmfv":
		return true
	default:
		return false
	}
}

func isMPEGTransportStream(data []byte) bool {
	if len(data) < 376 || data[0] != 0x47 || data[188] != 0x47 {
		return false
	}
	return len(data) < 377 || data[376] == 0x47
}

func isASFVideo(data []byte) bool {
	// ASF/WMV file identifier GUID: 30 26 b2 75 8e 66 cf 11 a6 d9 00 aa 00 62 ce 6c.
	return len(data) >= 16 && bytes.Equal(data[:16], []byte{
		0x30, 0x26, 0xb2, 0x75, 0x8e, 0x66, 0xcf, 0x11, 0xa6, 0xd9, 0x00, 0xaa, 0x00, 0x62, 0xce, 0x6c,
	})
}
