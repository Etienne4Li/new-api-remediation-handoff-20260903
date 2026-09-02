package gemini

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func veoPNGBase64(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{G: 0xff, A: 0xff})
	require.NoError(t, png.Encode(&data, img))
	return base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestParseImageInputUsesActualImageMIME(t *testing.T) {
	payload := veoPNGBase64(t)
	input := ParseImageInput("data:image/jpeg;BASE64," + payload)
	require.NotNil(t, input)
	require.Equal(t, "image/png", input.MimeType)
	require.Equal(t, payload, input.BytesBase64Encoded)
}

func TestParseImageInputRejectsNonImageAndMalformedDataURLs(t *testing.T) {
	html := base64.StdEncoding.EncodeToString([]byte("<html><script>alert(1)</script></html>"))
	require.Nil(t, ParseImageInput("data:image/png;base64,"+html))
	require.Nil(t, ParseImageInput("data:image/png,"+veoPNGBase64(t)))
	require.Nil(t, ParseImageInput("data:image/png;base64,not_base64!"))
}

func TestParseImageInputRejectsOversizedEncodedPayloadBeforeDecode(t *testing.T) {
	oversized := strings.Repeat("A", ((maxVeoImageSize+2)/3)*4+1)
	require.Nil(t, ParseImageInput(oversized))
}
