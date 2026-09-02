package service

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestDecodeBase64ImageDataRejectsOversizedPayloadBeforeDecode(t *testing.T) {
	previousLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = previousLimit })

	payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, (1<<20)+1))
	_, _, _, err := DecodeBase64ImageData(payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "maximum")
}

func TestDecodeBase64FileDataRejectsOversizedDataURLPayload(t *testing.T) {
	previousLimit := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 1
	t.Cleanup(func() { constant.MaxFileDownloadMB = previousLimit })

	payload := "data:image/png;base64," + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, (1<<20)+1))
	_, _, err := DecodeBase64FileData(payload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "maximum")
}

func TestDecodeBase64FileDataUsesImageBytesBeforeDeclaration(t *testing.T) {
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	require.NoError(t, png.Encode(&encoded, img))
	payload := base64.StdEncoding.EncodeToString(encoded.Bytes())

	mimeType, clean, err := DecodeBase64FileData("data:image/jpeg;charset=utf-8;BASE64," + payload)
	require.NoError(t, err)
	require.Equal(t, "image/png", mimeType)
	require.Equal(t, payload, clean)
}

func TestDecodeBase64FileDataRejectsDeclaredImageWithNonImageBytes(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("plain but not an image"))
	_, _, err := DecodeBase64FileData("data:image/png;base64," + payload)
	require.ErrorIs(t, err, errUnsupportedFileMIME)
}

func TestDecodeBase64FileDataRequiresBase64DataURL(t *testing.T) {
	_, _, err := DecodeBase64FileData("data:image/png,iVBORw0KGgo=")
	require.Error(t, err)
	require.Contains(t, err.Error(), "base64 encoding")
}

func TestFindISPEBoundsNestedMetadataDepth(t *testing.T) {
	box := make([]byte, 20)
	binary.BigEndian.PutUint32(box[:4], 20)
	copy(box[4:8], "ispe")
	binary.BigEndian.PutUint32(box[12:16], 1920)
	binary.BigEndian.PutUint32(box[16:20], 1080)
	for i := 0; i <= maxHEIFBoxDepth; i++ {
		wrapped := make([]byte, 8+len(box))
		binary.BigEndian.PutUint32(wrapped[:4], uint32(len(wrapped)))
		copy(wrapped[4:8], "ipco")
		copy(wrapped[8:], box)
		box = wrapped
	}
	_, _, ok := findISPE(box)
	require.False(t, ok, "excessively nested metadata must fail closed")
}
