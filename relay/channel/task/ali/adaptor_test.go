package ali

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRelayInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
}

func TestConvertToAliRequestWan27I2VBuildsMediaFromImage(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:    "wan2.7-i2v",
		Prompt:   "animate the first frame",
		Image:    "https://example.com/first.png",
		Size:     "720p",
		Duration: 10,
	}

	aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.NoError(t, err)
	require.Equal(t, "wan2.7-i2v", aliReq.Model)
	require.Equal(t, "720P", aliReq.Parameters.Resolution)
	require.Equal(t, 10, aliReq.Parameters.Duration)
	require.Equal(t, []AliVideoMedia{
		{Type: "first_frame", URL: "https://example.com/first.png"},
	}, aliReq.Input.Media)
	require.Empty(t, aliReq.Input.ImgURL)

	body, err := common.Marshal(aliReq)
	require.NoError(t, err)
	require.Contains(t, string(body), `"media"`)
	require.NotContains(t, string(body), `"img_url"`)
}

func TestConvertToAliRequestWan27I2VBuildsFirstAndLastFrameFromImages(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:  "wan2.7-i2v",
		Prompt: "interpolate between frames",
		Images: []string{
			"https://example.com/first.png",
			"https://example.com/last.png",
		},
	}

	aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.NoError(t, err)
	require.Equal(t, []AliVideoMedia{
		{Type: "first_frame", URL: "https://example.com/first.png"},
		{Type: "last_frame", URL: "https://example.com/last.png"},
	}, aliReq.Input.Media)
}

func TestConvertToAliRequestWan27I2VPrefersImageBeforeImagesAndInputReference(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:          "wan2.7-i2v",
		Prompt:         "use the direct image",
		Image:          " https://example.com/direct.png ",
		Images:         []string{"https://example.com/images-first.png", " https://example.com/images-last.png "},
		InputReference: "https://example.com/input-reference.png",
	}

	aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.NoError(t, err)
	require.Equal(t, []AliVideoMedia{
		{Type: "first_frame", URL: "https://example.com/direct.png"},
		{Type: "last_frame", URL: "https://example.com/images-last.png"},
	}, aliReq.Input.Media)
}

func TestConvertToAliRequestWan27I2VFallsBackToFirstNonEmptyImage(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:  "wan2.7-i2v",
		Prompt: "skip blank images",
		Image:  " ",
		Images: []string{
			" ",
			" https://example.com/first.png ",
			" https://example.com/last.png ",
		},
		InputReference: "https://example.com/input-reference.png",
	}

	aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.NoError(t, err)
	require.Equal(t, []AliVideoMedia{
		{Type: "first_frame", URL: "https://example.com/first.png"},
		{Type: "last_frame", URL: "https://example.com/last.png"},
	}, aliReq.Input.Media)
}

func TestConvertToAliRequestWan27I2VKeepsExplicitMetadataMedia(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:          "wan2.7-i2v",
		Prompt:         "continue the clip",
		Image:          "https://example.com/direct.png",
		Images:         []string{"https://example.com/images-first.png", "https://example.com/images-last.png"},
		InputReference: "https://example.com/input-reference.png",
		Metadata: map[string]interface{}{
			"input": map[string]interface{}{
				"media": []interface{}{
					map[string]interface{}{
						"type": "first_clip",
						"url":  "https://example.com/input.mp4",
					},
				},
			},
		},
	}

	aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.NoError(t, err)
	require.Equal(t, []AliVideoMedia{
		{Type: "first_clip", URL: "https://example.com/input.mp4"},
	}, aliReq.Input.Media)
	require.Empty(t, aliReq.Input.ImgURL)

	body, err := common.Marshal(aliReq)
	require.NoError(t, err)
	require.Contains(t, string(body), `"media"`)
	require.NotContains(t, string(body), `"img_url"`)
}

func TestConvertToAliRequestWan27I2VRequiresMedia(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:  "wan2.7-i2v",
		Prompt: "animate without a frame",
	}

	_, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "requires image"))
}

func TestConvertToAliRequestWan25I2VKeepsLegacyImgURL(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:  "wan2.5-i2v-preview",
		Prompt: "animate the first frame",
		Image:  "https://example.com/first.png",
	}

	aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.NoError(t, err)
	require.Equal(t, "https://example.com/first.png", aliReq.Input.ImgURL)
	require.Empty(t, aliReq.Input.Media)

	body, err := common.Marshal(aliReq)
	require.NoError(t, err)
	require.Contains(t, string(body), `"img_url"`)
	require.NotContains(t, string(body), `"media"`)
}

func TestConvertToAliRequestRejectsMetadataDurationOutsideBillingBounds(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:    "wan2.5-i2v-preview",
		Prompt:   "animate the first frame",
		Duration: 5,
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"duration": relaycommon.MaxTaskDurationSeconds + 1,
			},
		},
	}

	_, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.Error(t, err)
	require.Contains(t, err.Error(), "duration")
}

func TestProcessAliOtherRatiosHandlesNilParameters(t *testing.T) {
	require.NotPanics(t, func() {
		ratios, err := ProcessAliOtherRatios(&AliVideoRequest{
			Model:      "wan2.5-i2v-preview",
			Parameters: nil,
		})
		require.NoError(t, err)
		require.Empty(t, ratios)
	})
}

func TestConvertToAliRequestRejectsNonFiniteMetadataDuration(t *testing.T) {
	adaptor := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:  "wan2.5-i2v-preview",
		Prompt: "animate the first frame",
		Metadata: map[string]interface{}{
			"parameters": map[string]interface{}{
				"duration": math.Inf(1),
			},
		},
	}

	_, err := adaptor.convertToAliRequest(testRelayInfo(), req)

	require.Error(t, err)
}

func TestConvertToAliRequestRejectsUnsafeMediaURL(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		metadata map[string]interface{}
	}{
		{
			name:  "wan2.7 media scheme",
			model: "wan2.7-i2v",
			metadata: map[string]interface{}{
				"input": map[string]interface{}{
					"media": []interface{}{
						map[string]interface{}{"type": "first_frame", "url": "file:///etc/passwd"},
					},
				},
			},
		},
		{
			name:  "legacy audio userinfo",
			model: "wan2.5-i2v-preview",
			metadata: map[string]interface{}{
				"input": map[string]interface{}{
					"audio_url": "https://user:secret@media.example.test/audio.mp3",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (&TaskAdaptor{}).convertToAliRequest(testRelayInfo(), relaycommon.TaskSubmitReq{
				Model:    tt.model,
				Prompt:   "animate the frame",
				Image:    "https://example.com/first.png",
				Metadata: tt.metadata,
			})
			require.Error(t, err)
		})
	}
}

func TestConvertToAliRequestAllowsLegacyBase64ImageReference(t *testing.T) {
	aliReq, err := (&TaskAdaptor{}).convertToAliRequest(testRelayInfo(), relaycommon.TaskSubmitReq{
		Model:  "wan2.5-i2v-preview",
		Prompt: "animate the frame",
		Image:  "iVBORw0KGgo=",
	})
	require.NoError(t, err)
	require.Equal(t, "iVBORw0KGgo=", aliReq.Input.ImgURL)
}

func TestValidateRequestRejectsAliMetadataDurationBeforeBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", strings.NewReader(`{
		"model":"wan2.5-i2v-preview",
		"prompt":"animate the first frame",
		"metadata":{"parameters":{"duration":3601}}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{},
	}

	taskErr := (&TaskAdaptor{}).ValidateRequestAndSetAction(c, info)

	require.NotNil(t, taskErr)
	require.Equal(t, "invalid_request", taskErr.Code)
	require.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
	require.Equal(t, constant.TaskActionTextGenerate, info.Action)
}

func TestParseTaskResultCarriesProviderTaskID(t *testing.T) {
	body := []byte(`{"output":{"task_id":"ali-task-42","task_status":"RUNNING"}}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "ali-task-42", result.TaskID)
}

func TestParseTaskResultKeepsEnvelopeErrorWhenNestedStatusSucceeds(t *testing.T) {
	body := []byte(`{"code":"InvalidParameter","message":"upstream failed","output":{"task_id":"ali-task-42","task_status":"SUCCEEDED","video_url":"https://attacker.example/video.mp4"}}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, string(model.TaskStatusFailure), result.Status)
	assert.Equal(t, "upstream failed", result.Reason)
	assert.Empty(t, result.Url)
}
