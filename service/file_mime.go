package service

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
)

var errUnsupportedFileMIME = errors.New("unsupported or unsafe file MIME type")

// Keep this list deliberately finite. In particular, SVG and HTML are not
// accepted because browsers can execute content served with those types.
var safeFileMIMETypes = map[string]struct{}{
	"application/octet-stream":      {},
	"application/gzip":              {},
	"application/json":              {},
	"application/msword":            {},
	"application/ogg":               {},
	"application/pdf":               {},
	"application/vnd.ms-excel":      {},
	"application/vnd.ms-powerpoint": {},
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": {},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   {},
	"application/x-7z-compressed":                                               {},
	"application/x-gzip":                                                        {},
	"application/x-rar-compressed":                                              {},
	"application/zip":                                                           {},
	"audio/aac":                                                                 {},
	"audio/aiff":                                                                {},
	"audio/flac":                                                                {},
	"audio/m4a":                                                                 {},
	"audio/mp4":                                                                 {},
	"audio/mpeg":                                                                {},
	"audio/ogg":                                                                 {},
	"audio/opus":                                                                {},
	"audio/pcm":                                                                 {},
	"audio/wav":                                                                 {},
	"audio/webm":                                                                {},
	"audio/x-m4a":                                                               {},
	"audio/x-wav":                                                               {},
	"image/avif":                                                                {},
	"image/bmp":                                                                 {},
	"image/gif":                                                                 {},
	"image/heic":                                                                {},
	"image/heif":                                                                {},
	"image/jpeg":                                                                {},
	"image/png":                                                                 {},
	"image/tiff":                                                                {},
	"image/webp":                                                                {},
	"image/x-icon":                                                              {},
	"text/csv":                                                                  {},
	"text/plain":                                                                {},
	"video/3gpp":                                                                {},
	"video/3gpp2":                                                               {},
	"video/avi":                                                                 {},
	"video/flv":                                                                 {},
	"video/mp4":                                                                 {},
	"video/mpeg":                                                                {},
	"video/mpegps":                                                              {},
	"video/ogg":                                                                 {},
	"video/quicktime":                                                           {},
	"video/webm":                                                                {},
	"video/wmv":                                                                 {},
}

var fileMIMEAliases = map[string]string{
	"audio/mp3":       "audio/mpeg",
	"audio/mpeg3":     "audio/mpeg",
	"audio/wave":      "audio/wav",
	"audio/x-pn-wav":  "audio/wav",
	"audio/x-wav":     "audio/wav",
	"image/jpg":       "image/jpeg",
	"video/mov":       "video/quicktime",
	"video/mpg":       "video/mpeg",
	"video/x-flv":     "video/flv",
	"video/x-msvideo": "video/avi",
	"video/x-ms-wmv":  "video/wmv",
}

// parseBase64DataURL parses the subset of the data URL grammar accepted by
// the media decoders.  We intentionally require the explicit ;base64 marker;
// accepting a non-base64 data URL here would make callers accidentally treat
// URL-encoded active content as binary media.  A raw base64 payload (without a
// data URL prefix) remains supported for backwards compatibility.
func parseBase64DataURL(raw string) (payload string, mediaType string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("base64 payload is empty")
	}

	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return raw, "", nil
	}

	comma := strings.IndexByte(raw, ',')
	if comma <= len("data:") {
		return "", "", fmt.Errorf("invalid data URL")
	}
	metadata := raw[len("data:"):comma]
	parts := strings.Split(metadata, ";")
	hasBase64 := false
	mediaParts := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.EqualFold(part, "base64") {
			hasBase64 = true
			continue
		}
		mediaParts = append(mediaParts, part)
	}
	if !hasBase64 {
		return "", "", fmt.Errorf("data URL must use base64 encoding")
	}

	// A data URL may omit the media type (data:;base64,...).  Parameters are
	// parsed by mime.ParseMediaType so control characters and malformed values
	// cannot reach an upstream Content-Type field.
	mediaDeclaration := strings.TrimSpace(strings.Join(mediaParts, ";"))
	if mediaDeclaration != "" {
		mediaType, err = normalizeFileMIME(mediaDeclaration)
		if err != nil {
			return "", "", fmt.Errorf("invalid data URL MIME type: %w", err)
		}
		if !isSafeFileMIME(mediaType) {
			return "", "", fmt.Errorf("%w: %s", errUnsupportedFileMIME, mediaType)
		}
	}

	payload = raw[comma+1:]
	if payload == "" {
		return "", "", fmt.Errorf("base64 payload is empty")
	}
	return payload, mediaType, nil
}

func normalizeFileMIME(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", err
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if alias, ok := fileMIMEAliases[mediaType]; ok {
		mediaType = alias
	}
	return mediaType, nil
}

func isSafeFileMIME(mediaType string) bool {
	_, ok := safeFileMIMETypes[mediaType]
	return ok
}

// sniffFileMIME gives the bytes precedence over provider-controlled headers.
// The standard detector covers common raster, PDF, archive, audio, and video
// signatures; detectHEIF fills the one image family it cannot recognize.
func sniffFileMIME(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if detected, err := normalizeFileMIME(http.DetectContentType(data)); err == nil && detected != "" && detected != "application/octet-stream" {
		return detected
	}
	if heif := detectHEIF(data); heif != "" {
		return heif
	}
	return "application/octet-stream"
}

// resolveFileMIME uses a declaration only when the bytes are inconclusive.
// Invalid or unsafe declarations are ignored for remote files, while a
// concrete unsafe byte signature is rejected by callers that handle content.
func resolveFileMIME(data []byte, declared ...string) (string, error) {
	detected := sniffFileMIME(data)
	if detected != "" && detected != "application/octet-stream" {
		if !isSafeFileMIME(detected) {
			return "", fmt.Errorf("%w: %s", errUnsupportedFileMIME, detected)
		}
		return detected, nil
	}
	if len(data) == 0 {
		return "application/octet-stream", nil
	}
	for _, raw := range declared {
		mediaType, err := normalizeFileMIME(raw)
		if err != nil || !isSafeFileMIME(mediaType) {
			continue
		}
		// A provider declaration must not manufacture a media type when the
		// bytes are opaque.  Image/audio/video (and PDF, which is commonly
		// rendered by browsers) are accepted only when their signatures are
		// recognizable; otherwise an attacker can label arbitrary bytes as a
		// media resource and reach a downstream decoder or renderer.
		if mediaType != "application/octet-stream" &&
			(strings.HasPrefix(mediaType, "image/") || strings.HasPrefix(mediaType, "audio/") ||
				strings.HasPrefix(mediaType, "video/") || mediaType == "application/pdf") {
			continue
		}
		return mediaType, nil
	}
	return "application/octet-stream", nil
}

// resolveImageMIME is stricter than resolveFileMIME because image endpoints
// eventually hand the bytes to an image provider or a browser.  A declared
// image type is not enough: unknown bytes labelled image/png are rejected,
// while a concrete safe signature wins over a contradictory declaration.
func resolveImageMIME(data []byte, declared ...string) (string, error) {
	detected := sniffFileMIME(data)
	if detected == "" || detected == "application/octet-stream" {
		return "", fmt.Errorf("%w: image bytes are not recognizable", errUnsupportedFileMIME)
	}
	if !strings.HasPrefix(detected, "image/") || !isSafeFileMIME(detected) {
		return "", fmt.Errorf("%w: %s is not a safe image type", errUnsupportedFileMIME, detected)
	}
	// Declarations are deliberately ignored once a concrete image signature is
	// available.  Remote providers frequently send application/octet-stream (or
	// a stale text type) for valid images; using that declaration would reject
	// otherwise safe content.  Callers still pass declarations to
	// resolveFileMIME when they need a non-image fallback.
	return detected, nil
}

// ResolveImageMIME validates that data contains a recognized, safe raster
// image and returns its canonical MIME type. Declarations are only used as
// optional compatibility hints; the bytes always win and an unrecognized or
// active-content payload is rejected.
func ResolveImageMIME(data []byte, declared ...string) (string, error) {
	return resolveImageMIME(data, declared...)
}

// ResolveAudioMIME validates that data contains a recognized, safe audio
// container and returns its canonical MIME type.  Multipart upload headers
// and filenames are client-controlled, so callers must use the bytes rather
// than forwarding those declarations to an upstream transcription service.
// Raw PCM is intentionally not accepted here because it has no self-describing
// container; callers that explicitly support PCM should validate its framing
// and format separately before forwarding it.
func ResolveAudioMIME(data []byte, declared ...string) (string, error) {
	// net/http's detector intentionally returns generic video/application labels
	// for a few containers that are routinely used for audio (Ogg, WebM and
	// ISO-BMFF).  Check those signatures first and only then fall back to the
	// standard detector.  The declaration and filename are never trusted.
	detected := sniffAudioMIME(data)
	if detected == "" {
		return "", fmt.Errorf("%w: audio bytes are not recognizable", errUnsupportedFileMIME)
	}
	if !strings.HasPrefix(detected, "audio/") || !isSafeFileMIME(detected) {
		return "", fmt.Errorf("%w: %s is not a safe audio type", errUnsupportedFileMIME, detected)
	}
	return detected, nil
}

// sniffAudioMIME recognizes self-describing audio containers.  It is kept
// deliberately conservative: a caller-controlled MIME header or extension is
// never enough to classify opaque bytes as audio.
func sniffAudioMIME(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return "audio/wav"
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("FORM")) &&
		(bytes.Equal(data[8:12], []byte("AIFF")) || bytes.Equal(data[8:12], []byte("AIFC"))) {
		return "audio/aiff"
	}
	if len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")) {
		return "audio/mpeg"
	}
	if looksLikeMPEGAudioFrame(data) {
		return "audio/mpeg"
	}
	if looksLikeFLAC(data) {
		return "audio/flac"
	}
	if looksLikeADTSAAC(data) {
		return "audio/aac"
	}
	if looksLikeOgg(data) {
		return "audio/ogg"
	}
	if looksLikeWebMAudio(data) {
		return "audio/webm"
	}
	if looksLikeISOBMFFAudio(data) {
		return "audio/mp4"
	}

	if detected, err := normalizeFileMIME(http.DetectContentType(data)); err == nil &&
		strings.HasPrefix(detected, "audio/") && isSafeFileMIME(detected) {
		return detected
	}
	return ""
}

func looksLikeMPEGAudioFrame(data []byte) bool {
	if len(data) < 4 || data[0] != 0xff || data[1]&0xe0 != 0xe0 {
		return false
	}
	// Layer 0, bitrate index 0/15 and sample-rate index 3 are reserved.
	if data[1]&0x06 == 0 || data[2]>>4 == 0 || data[2]>>4 == 0x0f || (data[2]>>2)&0x03 == 0x03 {
		return false
	}
	return true
}

func looksLikeFLAC(data []byte) bool {
	if len(data) < 42 || !bytes.Equal(data[:4], []byte("fLaC")) {
		return false
	}
	// A valid FLAC stream starts with a STREAMINFO block of 34 bytes.
	if data[4]&0x7f != 0 {
		return false
	}
	blockLen := int(data[5])<<16 | int(data[6])<<8 | int(data[7])
	return blockLen >= 34 && len(data) >= 8+blockLen
}

func looksLikeADTSAAC(data []byte) bool {
	if len(data) < 7 || data[0] != 0xff || data[1]&0xf6 != 0xf0 {
		return false
	}
	// The sampling-frequency index 15 is reserved.  Ensure the first frame is
	// complete before accepting the payload as a self-describing stream.
	if (data[2]>>2)&0x0f == 0x0f {
		return false
	}
	frameLen := int(data[3]&0x03)<<11 | int(data[4])<<3 | int(data[5]>>5)
	return frameLen >= 7 && frameLen <= len(data)
}

func looksLikeOgg(data []byte) bool {
	if len(data) < 27 || !bytes.Equal(data[:4], []byte("OggS")) || data[4] != 0 {
		return false
	}
	segmentCount := int(data[26])
	if len(data) < 27+segmentCount {
		return false
	}
	bodyLen := 0
	for _, segmentLen := range data[27 : 27+segmentCount] {
		bodyLen += int(segmentLen)
	}
	if len(data) < 27+segmentCount+bodyLen {
		return false
	}
	// Ogg is also a video container.  Only accept streams whose first packet
	// advertises a known audio codec; a bare OggS marker is not enough.
	return bytes.Contains(data, []byte("OpusHead")) || bytes.Contains(data, []byte("vorbis")) ||
		bytes.Contains(data, []byte("Speex   ")) || bytes.Contains(data, []byte("fLaC"))
}

func looksLikeWebMAudio(data []byte) bool {
	if len(data) < 4 || !bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return false
	}
	// Codec IDs are stored as plain strings in Matroska TrackEntry elements.
	// Requiring an audio codec marker avoids treating an arbitrary video WebM as
	// a transcription upload while still supporting Opus/Vorbis WebM files.
	return bytes.Contains(data, []byte("A_OPUS")) || bytes.Contains(data, []byte("A_VORBIS")) ||
		bytes.Contains(data, []byte("A_AAC")) || bytes.Contains(data, []byte("A_FLAC"))
}

func looksLikeISOBMFFAudio(data []byte) bool {
	if len(data) < 12 || !bytes.Equal(data[4:8], []byte("ftyp")) {
		return false
	}
	boxSize := int64(data[0])<<24 | int64(data[1])<<16 | int64(data[2])<<8 | int64(data[3])
	if boxSize < 16 || boxSize > int64(len(data)) {
		return false
	}
	// M4A/M4B brands are unambiguously audio. Generic MP4 brands are accepted
	// only when the file advertises an audio track (the `soun` handler).
	brand := data[8:12]
	if bytes.Equal(brand, []byte("M4A ")) || bytes.Equal(brand, []byte("M4B ")) {
		return true
	}
	return bytes.Contains(data, []byte("soun"))
}

func validateFileMIMEDeclaration(raw string) (string, error) {
	mediaType, err := normalizeFileMIME(raw)
	if err != nil || !isSafeFileMIME(mediaType) {
		return "", fmt.Errorf("%w: %q", errUnsupportedFileMIME, strings.TrimSpace(raw))
	}
	return mediaType, nil
}

func mimeFromContentDisposition(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	filename := params["filename"]
	if filename == "" {
		filename = params["filename*"]
	}
	if dot := strings.LastIndex(filename, "."); dot >= 0 && dot+1 < len(filename) {
		return GetMimeTypeByExtension(filename[dot+1:])
	}
	return ""
}
