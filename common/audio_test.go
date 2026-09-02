package common

import (
	"bytes"
	"context"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestGetAudioDurationAACRejectsOversizedInput(t *testing.T) {
	previousLimit := constant.MaxRequestBodyMB
	constant.MaxRequestBodyMB = 1
	t.Cleanup(func() { constant.MaxRequestBodyMB = previousLimit })

	data := bytes.Repeat([]byte{0}, (1<<20)+1)
	_, err := GetAudioDuration(context.Background(), bytes.NewReader(data), ".aac")
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum allowed size")
}

func TestGetAudioDurationAACRejectsMalformedFrameWithoutPanicking(t *testing.T) {
	// A valid ADTS sync word followed by a zero frame length used to reach the
	// third-party splitter's unchecked slice and could panic the request.
	data := []byte{0xff, 0xf1, 0x50, 0x80, 0x00, 0x00, 0x00}

	require.NotPanics(t, func() {
		_, err := GetAudioDuration(context.Background(), bytes.NewReader(data), ".aac")
		require.Error(t, err)
	})
}
