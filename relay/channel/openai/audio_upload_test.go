package openai

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertAudioRequestSanitizesMultipartFilename(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "whisper-1"))
	part, err := writer.CreateFormFile("file", "../../evil\r\nX-Injected: yes.mp3")
	require.NoError(t, err)
	// A minimal self-describing WAV header is enough for the adaptor's
	// container check; the filename itself is intentionally hostile.
	_, err = part.Write(append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 16)...))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	defer common.CleanupBodyStorage(c)

	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeAudioTranscription}
	converted, err := (&Adaptor{}).ConvertAudioRequest(c, info, dto.AudioRequest{Model: "whisper-1"})
	require.NoError(t, err)
	require.NotNil(t, converted)
	convertedBytes, err := io.ReadAll(converted)
	require.NoError(t, err)

	replayed := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(convertedBytes))
	replayed.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	require.NoError(t, replayed.ParseMultipartForm(1<<20))
	files := replayed.MultipartForm.File["file"]
	require.Len(t, files, 1)
	require.Equal(t, "audio.wav", files[0].Filename)
	require.NotContains(t, files[0].Filename, "\r")
	require.NotContains(t, files[0].Filename, "\n")
}

func TestConvertAudioRequestRejectsOpaqueBytes(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "whisper-1"))
	part, err := writer.CreateFormFile("file", "recording.mp3")
	require.NoError(t, err)
	_, err = part.Write([]byte("<html><script>alert(1)</script></html>"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	defer common.CleanupBodyStorage(c)

	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeAudioTranscription}
	_, err = (&Adaptor{}).ConvertAudioRequest(c, info, dto.AudioRequest{Model: "whisper-1"})
	require.Error(t, err)
}
