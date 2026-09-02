package vertex

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	geminitask "github.com/QuantumNous/new-api/relay/channel/task/gemini"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTaskResultCarriesOperationIdentityAllStates(t *testing.T) {
	operationName := "projects/test/locations/us-central1/models/veo-3.0-generate-001/operations/op-identity"
	tests := []struct {
		name           string
		done           bool
		errorMessage   string
		expectedStatus model.TaskStatus
	}{
		{name: "failure", done: true, errorMessage: "provider failure", expectedStatus: model.TaskStatusFailure},
		{name: "in progress", done: false, expectedStatus: model.TaskStatusInProgress},
		{name: "success", done: true, expectedStatus: model.TaskStatusSuccess},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := operationResponse{Name: " \t" + operationName + " \n", Done: tt.done}
			op.Error.Message = tt.errorMessage
			body, err := common.Marshal(op)
			require.NoError(t, err)

			result, err := (&TaskAdaptor{}).ParseTaskResult(body)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, string(tt.expectedStatus), result.Status)
			assert.Equal(t, taskcommon.EncodeLocalTaskID(operationName), result.TaskID)
		})
	}
}

func TestBuildRequestBodyUsesSecondsAsDuration(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{
		Model:   "veo-3.0-generate-001",
		Prompt:  "a short test clip",
		Seconds: "6",
	})
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}

	body, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.NoError(t, err)
	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	var payload geminitask.VeoRequestPayload
	require.NoError(t, common.Unmarshal(raw, &payload))
	require.NotNil(t, payload.Parameters)
	assert.Equal(t, 6, payload.Parameters.DurationSeconds)
}

func TestBuildRequestBodyRejectsUnsupportedImageReference(t *testing.T) {
	tests := []struct {
		name   string
		images []string
		want   string
	}{
		{
			name:   "remote URL",
			images: []string{"https://example.com/input.png"},
			want:   "invalid image input",
		},
		{
			name:   "malformed data URI",
			images: []string{"data:image/png;base64,not-base64!"},
			want:   "invalid image input",
		},
		{
			name:   "multiple images",
			images: []string{"https://example.com/first.png", "https://example.com/second.png"},
			want:   "only one image input is supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
			c.Set("task_request", relaycommon.TaskSubmitReq{
				Model:  "veo-3.0-generate-001",
				Prompt: "a short test clip",
				Images: tt.images,
			})
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}

			_, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestBuildRequestBodyRejectsUnexpectedTaskRequestType(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
	c.Set("task_request", "invalid")

	_, err := (&TaskAdaptor{}).BuildRequestBody(c, &relaycommon.RelayInfo{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected task_request type")
}

func TestBuildRequestBodyIncludesValidImageInput(t *testing.T) {
	const imageBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{
		Model:  "veo-3.0-generate-001",
		Prompt: "a short test clip",
		Images: []string{"data:image/png;base64," + imageBase64},
	})
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{},
	}

	body, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.NoError(t, err)
	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	var payload geminitask.VeoRequestPayload
	require.NoError(t, common.Unmarshal(raw, &payload))
	require.Len(t, payload.Instances, 1)
	require.NotNil(t, payload.Instances[0].Image)
	assert.Equal(t, imageBase64, payload.Instances[0].Image.BytesBase64Encoded)
	assert.Equal(t, "image/png", payload.Instances[0].Image.MimeType)
	assert.Equal(t, constant.TaskActionGenerate, info.Action)
}

func TestBuildRequestBodyRejectsInvalidMultipartImage(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("input_reference", "input.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("<html>not an image</html>"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	c.Set("task_request", relaycommon.TaskSubmitReq{
		Model:  "veo-3.0-generate-001",
		Prompt: "a short test clip",
	})
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}

	_, err = (&TaskAdaptor{}).BuildRequestBody(c, info)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid input_reference image")
}

func minimalVertexMP4() []byte {
	// A minimal ISO Base Media ftyp box. The parser only uses the container
	// signature; playback is outside the task-result parsing boundary.
	return append([]byte{0, 0, 0, 24}, []byte("ftypmp42\x00\x00\x00\x00mp42isom")...)
}

func TestParseTaskResultValidatesInlineVideoBytesAndCanonicalizesMIME(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(minimalVertexMP4())
	op := operationResponse{
		Name: "projects/test/locations/us-central1/models/veo-3.0-generate-001/operations/op-media",
		Done: true,
	}
	op.Response.Videos = []operationVideo{{
		MimeType:           "video/mp4; codecs=avc1",
		BytesBase64Encoded: encoded,
		Encoding:           "mp4",
	}}
	body, err := common.Marshal(op)
	require.NoError(t, err)

	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "data:video/mp4;base64,"+encoded, result.Url)
}

func TestParseTaskResultRejectsForgedOrMalformedInlineVideo(t *testing.T) {
	tests := []struct {
		name string
		data operationVideo
	}{
		{name: "html bytes", data: operationVideo{MimeType: "video/mp4", BytesBase64Encoded: base64.StdEncoding.EncodeToString([]byte("<html><script>alert(1)</script>"))}},
		{name: "bad base64", data: operationVideo{MimeType: "video/mp4", BytesBase64Encoded: "not-base64!"}},
		{name: "unsafe declaration", data: operationVideo{MimeType: "text/html", BytesBase64Encoded: base64.StdEncoding.EncodeToString(minimalVertexMP4())}},
		{name: "mismatched declaration", data: operationVideo{MimeType: "video/webm", BytesBase64Encoded: base64.StdEncoding.EncodeToString(minimalVertexMP4())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := operationResponse{
				Name: "projects/test/locations/us-central1/models/veo-3.0-generate-001/operations/op-media-invalid",
				Done: true,
			}
			op.Response.Videos = []operationVideo{tt.data}
			body, err := common.Marshal(op)
			require.NoError(t, err)
			result, parseErr := (&TaskAdaptor{}).ParseTaskResult(body)
			require.Error(t, parseErr)
			assert.Nil(t, result)
		})
	}
}

func TestParseTaskResultSupportsSafeEncodingDeclaration(t *testing.T) {
	op := operationResponse{
		Name: "projects/test/locations/us-central1/models/veo-3.0-generate-001/operations/op-encoding",
		Done: true,
	}
	op.Response.BytesBase64Encoded = base64.StdEncoding.EncodeToString(minimalVertexMP4())
	op.Response.Encoding = "mp4"
	body, err := common.Marshal(op)
	require.NoError(t, err)

	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Url, "data:video/mp4;base64,")
}
