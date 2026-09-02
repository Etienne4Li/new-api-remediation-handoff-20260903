package service

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSmartDetectMimeTypeUsesBytesBeforeUntrustedHeader(t *testing.T) {
	response := &http.Response{Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}}
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("payload")...)

	require.Equal(t, "image/png", smartDetectMimeType(response, "https://example.test/file", png))
	// An HTML signature is never converted into an image by a misleading URL or
	// response header.
	require.Equal(t, "application/octet-stream", smartDetectMimeType(response, "https://example.test/file.png", []byte("<html><body>blocked</body></html>")))
}

func TestResolveFileMIMERecognizesSafeDocumentAndMediaSignatures(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "pdf", data: []byte("%PDF-1.7\n"), want: "application/pdf"},
		{name: "png", data: []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, want: "image/png"},
		{name: "wav", data: append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 4)...), want: "audio/wav"},
		{name: "mp3 id3", data: append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 8)...), want: "audio/mpeg"},
		{name: "mp4", data: append([]byte{0, 0, 0, 24}, []byte("ftypmp42\x00\x00\x00\x00mp42isom")...), want: "video/mp4"},
		{name: "heic", data: append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00heicmif1")...), want: "image/heic"},
		{name: "avif", data: append([]byte{0, 0, 0, 16}, []byte("ftypavif\x00\x00\x00\x00")...), want: "image/avif"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveFileMIME(test.data, "text/html")
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestResolveFileMIMEDoesNotManufactureMediaTypeFromDeclaration(t *testing.T) {
	got, err := resolveFileMIME([]byte{0x01, 0x02, 0x03}, "video/mp4", "application/octet-stream")
	require.NoError(t, err)
	require.Equal(t, "application/octet-stream", got)
}

func TestLoadFromBase64RejectsUnsafeDeclarationAndContent(t *testing.T) {
	_, err := loadFromBase64("data:text/html;base64,"+base64.StdEncoding.EncodeToString([]byte("<script>alert(1)</script>")), "")
	require.ErrorIs(t, err, errUnsupportedFileMIME)

	_, err = loadFromBase64(base64.StdEncoding.EncodeToString([]byte("payload")), "image/svg+xml")
	require.ErrorIs(t, err, errUnsupportedFileMIME)
}

func TestLoadFromBase64CanonicalizesSafeDataURLMIME(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0}, 8)...)
	cached, err := loadFromBase64("data:image/png;BASE64,"+base64.StdEncoding.EncodeToString(png), "")
	require.NoError(t, err)
	require.Equal(t, "image/png", cached.MimeType)
	require.Equal(t, int64(len(png)), cached.Size)
}

func TestResolveFileMIMEIgnoresUnsafeDeclarationWhenBytesAreInconclusive(t *testing.T) {
	got, err := resolveFileMIME([]byte{0x01, 0x02, 0x03}, "text/html", "application/octet-stream")
	require.NoError(t, err)
	require.Equal(t, "application/octet-stream", got)
}

func TestResolveAudioMIMEUsesContainerBytes(t *testing.T) {
	wav := append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 4)...)
	got, err := ResolveAudioMIME(wav, "audio/mpeg")
	require.NoError(t, err)
	require.Equal(t, "audio/wav", got)

	ogg := make([]byte, 36)
	copy(ogg[:4], []byte("OggS"))
	ogg[26] = 1 // one segment
	ogg[27] = 8 // OpusHead payload length
	copy(ogg[28:], []byte("OpusHead"))
	got, err = ResolveAudioMIME(ogg, "audio/mpeg")
	require.NoError(t, err)
	require.Equal(t, "audio/ogg", got)

	_, err = ResolveAudioMIME([]byte("<html><script>alert(1)</script>"), "audio/mpeg")
	require.ErrorIs(t, err, errUnsupportedFileMIME)
}

func TestResolveAudioMIMERecognizesCommonContainers(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "ogg opus", data: minimalOggPage("OpusHead"), want: "audio/ogg"},
		{name: "flac", data: append([]byte("fLaC\x00\x00\x00\x22"), make([]byte, 34)...), want: "audio/flac"},
		{name: "aac adts", data: []byte{0xff, 0xf1, 0x50, 0x80, 0x00, 0xff, 0xfc}, want: "audio/aac"},
		{name: "m4a", data: append([]byte{0, 0, 0, 16}, []byte("ftypM4A \x00\x00\x00\x00")...), want: "audio/mp4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveAudioMIME(test.data)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}

	_, err := ResolveAudioMIME(minimalOggPage("video-packet"))
	require.ErrorIs(t, err, errUnsupportedFileMIME)
}

func minimalOggPage(packet string) []byte {
	page := make([]byte, 27+1+len(packet))
	copy(page[:4], "OggS")
	page[26] = 1
	page[27] = byte(len(packet))
	copy(page[28:], packet)
	return page
}
