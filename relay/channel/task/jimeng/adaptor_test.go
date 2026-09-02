package jimeng

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jimengTestPNG(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	require.NoError(t, png.Encode(&data, img))
	return base64.StdEncoding.EncodeToString(data.Bytes())
}

func jimengRelayInfo(model string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model},
	}
}

func TestConvertToRequestPayloadRejectsMetadataFramesOverflow(t *testing.T) {
	_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt:   "test video",
		Duration: 5,
		Metadata: map[string]any{"frames": maxTaskFrames + 1},
	}, jimengRelayInfo("jimeng_vgfm_t2v_l20"))

	require.Error(t, err)
	require.Contains(t, err.Error(), "frames")
}

func TestConvertToRequestPayloadDoesNotAllowReqKeyOverride(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt:   "test video",
		Duration: 5,
		Metadata: map[string]any{"REQ_KEY": "different-provider-mode"},
	}, jimengRelayInfo("jimeng_vgfm_t2v_l20"))

	require.NoError(t, err)
	require.Equal(t, "jimeng_vgfm_t2v_l20", payload.ReqKey)
}

func TestConvertToRequestPayloadRejectsForgedImageBytes(t *testing.T) {
	for _, raw := range []string{
		base64.StdEncoding.EncodeToString([]byte("<html><script>alert(1)</script>")),
		"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not an image")),
	} {
		_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
			Prompt: "test video",
			Images: []string{raw},
		}, jimengRelayInfo("jimeng_vgfm_t2v_l20"))
		require.Error(t, err)
	}
}

func TestConvertToRequestPayloadCanonicalizesImageBase64AndURL(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt: "test video",
		Images: []string{jimengTestPNG(t)},
	}, jimengRelayInfo("jimeng_vgfm_t2v_l20"))
	require.NoError(t, err)
	require.Len(t, payload.BinaryDataBase64, 1)
	require.Empty(t, payload.ImageUrls)

	payload, err = (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt: "test video",
		Images: []string{"https://cdn.example.test/input.png"},
	}, jimengRelayInfo("jimeng_vgfm_t2v_l20"))
	require.NoError(t, err)
	require.Equal(t, []string{"https://cdn.example.test/input.png"}, payload.ImageUrls)
}

func TestConvertToRequestPayloadRejectsOversizedBase64Image(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(make([]byte, MaxFileSize+1))
	_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt: "test video",
		Images: []string{encoded},
	}, jimengRelayInfo("jimeng_vgfm_t2v_l20"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum")
}

func TestParseTaskResultKeepsEnvelopeErrorWhenNestedStatusSaysDone(t *testing.T) {
	body := []byte(`{"code":50001,"message":"upstream failed","data":{"status":"done","video_url":"https://attacker.example/video.mp4"}}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, relaycommon.TaskInfo{Code: 50001, Status: "FAILURE", Reason: "upstream failed", Progress: "100%"}, *result)
}

func TestParseTaskResultRejectsUnsafeSuccessURL(t *testing.T) {
	body := []byte(`{"code":10000,"data":{"status":"done","video_url":"file:///etc/passwd"}}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "video URL")
}
