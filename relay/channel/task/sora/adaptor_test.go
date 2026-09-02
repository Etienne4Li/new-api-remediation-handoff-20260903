package sora

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func soraMultipartContext(t *testing.T, filename string, payload []byte) *gin.Context {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "make a clip"))
	part, err := writer.CreateFormFile("input_reference", filename)
	require.NoError(t, err)
	_, err = part.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	return c
}

func TestSoraMultipartBodyUsesDetectedMIMEAndSafeFilename(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("payload")...)
	c := soraMultipartContext(t, "../../evil\r\nX-Injected: yes.svg", png)
	defer common.CleanupBodyStorage(c)

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	info.ChannelMeta.UpstreamModelName = "sora-2"
	body, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.NoError(t, err)
	data, err := io.ReadAll(body)
	require.NoError(t, err)

	replayed := httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(data))
	replayed.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	require.NoError(t, replayed.ParseMultipartForm(1<<20))
	files := replayed.MultipartForm.File["input_reference"]
	require.Len(t, files, 1)
	require.Equal(t, "input_reference.png", files[0].Filename)
	require.Equal(t, "image/png", files[0].Header.Get("Content-Type"))
	file, err := files[0].Open()
	require.NoError(t, err)
	defer file.Close()
	got, err := io.ReadAll(file)
	require.NoError(t, err)
	require.Equal(t, png, got)
}

func TestSoraMultipartBodyRejectsForgedMedia(t *testing.T) {
	c := soraMultipartContext(t, "input.png", []byte("<html><script>alert(1)</script>"))
	defer common.CleanupBodyStorage(c)

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	info.ChannelMeta.UpstreamModelName = "sora-2"
	_, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.Error(t, err)
}

func TestSoraBuildRequestBodyReturnsReplayablePassThroughBody(t *testing.T) {
	payload := []byte("opaque-sora-request-body")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/octet-stream")
	defer common.CleanupBodyStorage(c)

	info := &relaycommon.RelayInfo{}
	body, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.NoError(t, err)
	replayable, ok := body.(common.ReplayableBody)
	require.True(t, ok)

	sent, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, payload, sent)
	assert.EqualValues(t, len(payload), replayable.Size())

	replayBody, err := replayable.NewReader()
	require.NoError(t, err)
	replay, err := io.ReadAll(replayBody)
	require.NoError(t, err)
	require.NoError(t, replayBody.Close())
	assert.Equal(t, payload, replay)
}

func TestSoraJSONBodyRejectsCallbackFields(t *testing.T) {
	payload := []byte(`{"prompt":"make a clip","callback_url":"https://attacker.example/a","callbackUrl":"https://attacker.example/b","CALLBACK-URL":"https://attacker.example/c","metadata":{"callback_url":"https://attacker.example/nested","style":"cinematic"}}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	defer common.CleanupBodyStorage(c)

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	info.ChannelMeta.UpstreamModelName = "sora-2"
	body, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.NoError(t, err)
	data, err := io.ReadAll(body)
	require.NoError(t, err)

	var got map[string]interface{}
	require.NoError(t, common.Unmarshal(data, &got))
	assert.Equal(t, "sora-2", got["model"])
	assert.Equal(t, "make a clip", got["prompt"])
	assert.NotContains(t, got, "callback_url")
	assert.NotContains(t, got, "callbackUrl")
	assert.NotContains(t, got, "CALLBACK-URL")
	nested, ok := got["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, nested, "callback_url")
	assert.Equal(t, "cinematic", nested["style"])
}

func TestSoraMultipartBodyRejectsCallbackFields(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "make a clip"))
	require.NoError(t, writer.WriteField("callback_url", "https://attacker.example/a"))
	require.NoError(t, writer.WriteField("callbackUrl", "https://attacker.example/b"))
	require.NoError(t, writer.WriteField("CALLBACK-URL", "https://attacker.example/c"))
	require.NoError(t, writer.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	defer common.CleanupBodyStorage(c)

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	info.ChannelMeta.UpstreamModelName = "sora-2"
	forwarded, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.NoError(t, err)
	data, err := io.ReadAll(forwarded)
	require.NoError(t, err)

	replayed := httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(data))
	replayed.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
	require.NoError(t, replayed.ParseMultipartForm(1<<20))
	assert.Equal(t, []string{"make a clip"}, replayed.MultipartForm.Value["prompt"])
	assert.Equal(t, []string{"sora-2"}, replayed.MultipartForm.Value["model"])
	assert.NotContains(t, replayed.MultipartForm.Value, "callback_url")
	assert.NotContains(t, replayed.MultipartForm.Value, "callbackUrl")
	assert.NotContains(t, replayed.MultipartForm.Value, "CALLBACK-URL")
}

func TestSoraMultipartBodyRejectsParseFailure(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader([]byte("--truncated")))
	c.Request.Header.Set("Content-Type", "multipart/form-data; boundary=truncated")
	defer common.CleanupBodyStorage(c)

	info := &relaycommon.RelayInfo{}
	_, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.Error(t, err)
}

func TestParseTaskResultCarriesProviderTaskID(t *testing.T) {
	body := []byte(`{"id":"sora-task-42","status":"processing"}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "sora-task-42", result.TaskID)
}
