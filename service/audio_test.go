package service

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestParseAudioUsesRealtimeFormatRates(t *testing.T) {
	pcm := base64.StdEncoding.EncodeToString(make([]byte, 48000)) // one second at 24 kHz/16-bit
	duration, err := parseAudio(pcm, "pcm16")
	require.NoError(t, err)
	require.InDelta(t, 1.0, duration, 1e-9)

	ulaw := base64.StdEncoding.EncodeToString(make([]byte, 8000)) // one second at 8 kHz/8-bit
	duration, err = parseAudio(ulaw, "g711-ulaw")
	require.NoError(t, err)
	require.InDelta(t, 1.0, duration, 1e-9)
}

func TestParseAudioRejectsUnknownFormat(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("audio"))
	_, err := parseAudio(payload, "mp3")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported audio format")
}

func TestDecodeBase64AudioDataValidatesDataURLAndContent(t *testing.T) {
	previousLimit := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 1
	t.Cleanup(func() { constant.MaxRequestBodyMB = previousLimit })

	pcm := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0}, 32))
	clean, err := DecodeBase64AudioData("data:audio/pcm;BASE64," + pcm)
	require.NoError(t, err)
	require.Equal(t, pcm, clean)

	html := base64.StdEncoding.EncodeToString([]byte("<script>alert(1)</script>"))
	_, err = DecodeBase64AudioData("data:audio/mpeg;base64," + html)
	require.ErrorIs(t, err, errUnsupportedFileMIME)

	_, err = DecodeBase64AudioData("data:text/html," + html)
	require.Error(t, err)
	require.Contains(t, err.Error(), "base64 encoding")
}
