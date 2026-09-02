package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

func parseAudio(audioBase64 string, format string) (duration float64, err error) {
	payload, _, parseErr := parseBase64DataURL(audioBase64)
	if parseErr != nil {
		return 0, parseErr
	}
	audioData, err := common.DecodeBase64Limited(payload, common.GetMaxRequestBodyBytes())
	if err != nil {
		return 0, fmt.Errorf("base64 decode error: %v", err)
	}

	format = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(format, "-", "_")))
	var bytesPerSample int
	var sampleRate int
	switch format {
	case "", "pcm16":
		// Realtime sessions default to 24 kHz signed 16-bit PCM.
		bytesPerSample = 2
		sampleRate = 24000
	case "g711_ulaw", "g711_alaw":
		bytesPerSample = 1
		sampleRate = 8000
	default:
		return 0, fmt.Errorf("unsupported audio format %q", format)
	}

	samplesCount := len(audioData) / bytesPerSample
	duration = float64(samplesCount) / float64(sampleRate)
	return duration, nil
}

func DecodeBase64AudioData(audioBase64 string) (string, error) {
	payload, declaredMIME, err := parseBase64DataURL(audioBase64)
	if err != nil {
		return "", err
	}
	if declaredMIME != "" && declaredMIME != "application/octet-stream" && !strings.HasPrefix(declaredMIME, "audio/") {
		return "", fmt.Errorf("%w: declared type %s is not audio", errUnsupportedFileMIME, declaredMIME)
	}

	// Decode Base64 data with a finite bound. The value is client-controlled and
	// must never be handed to an unbounded decoder.
	decoded, err := common.DecodeBase64Limited(payload, common.GetMaxRequestBodyBytes())
	if err != nil {
		return "", fmt.Errorf("base64 decode error: %v", err)
	}
	// If the bytes have a recognizable signature, require it to be audio. Raw
	// PCM has no magic bytes and therefore remains an allowed opaque payload;
	// HTML/XML and other active content are rejected even when mislabeled as
	// audio by a data URL.
	detected := sniffFileMIME(decoded)
	if detected != "" && detected != "application/octet-stream" && !strings.HasPrefix(detected, "audio/") {
		return "", fmt.Errorf("%w: detected type %s is not audio", errUnsupportedFileMIME, detected)
	}

	return payload, nil
}
