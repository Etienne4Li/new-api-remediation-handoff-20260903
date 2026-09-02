package gemini

import (
	"bytes"
	"io"
	"mime/multipart"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
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

	var payload VeoRequestPayload
	require.NoError(t, common.Unmarshal(raw, &payload))
	require.NotNil(t, payload.Parameters)
	assert.Equal(t, 6, payload.Parameters.DurationSeconds)
}

func TestBuildRequestBodyRejectsUnsupportedImageReference(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "remote URL", value: "https://example.com/input.png"},
		{name: "malformed base64", value: "not-base64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
			c.Set("task_request", relaycommon.TaskSubmitReq{
				Model:  "veo-3.0-generate-001",
				Prompt: "a short test clip",
				Images: []string{tt.value},
			})
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}

			_, err := (&TaskAdaptor{}).BuildRequestBody(c, info)
			require.Error(t, err)
			require.Contains(t, err.Error(), "invalid image input")
		})
	}
}

func TestBuildRequestBodyAllowsValidImageWithoutRelayInfo(t *testing.T) {
	const pngDataURI = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{
		Model:  "veo-3.0-generate-001",
		Prompt: "a short test clip",
		Images: []string{pngDataURI},
	})

	body, err := (&TaskAdaptor{}).BuildRequestBody(c, nil)
	require.NoError(t, err)
	require.NotNil(t, body)
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
