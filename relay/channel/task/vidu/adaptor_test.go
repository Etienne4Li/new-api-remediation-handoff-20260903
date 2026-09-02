package vidu

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRelayInfo(model string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model},
	}
}

func TestParseTaskResultRejectsUnsafeSuccessURL(t *testing.T) {
	body := []byte(`{"state":"success","creations":[{"url":"file:///etc/passwd"}]}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "video URL")
}

func TestConvertToRequestPayloadRejectsMetadataDurationOverflow(t *testing.T) {
	_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt:   "test video",
		Duration: 5,
		Metadata: map[string]any{"duration": relaycommon.MaxTaskDurationSeconds + 1},
	}, testRelayInfo("viduq2"))

	require.Error(t, err)
	require.Contains(t, err.Error(), "duration")
}

func TestConvertToRequestPayloadRejectsCaseInsensitiveMetadataModel(t *testing.T) {
	for _, key := range []string{"model", "Model", "MODEL_NAME"} {
		t.Run(key, func(t *testing.T) {
			_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
				Prompt:   "test video",
				Metadata: map[string]any{key: "viduq1"},
			}, testRelayInfo("viduq2"))
			require.Error(t, err)
			require.Contains(t, err.Error(), "model")
		})
	}
}

func TestConvertToRequestPayloadUsesSeconds(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt:  "test video",
		Seconds: "7",
	}, testRelayInfo("viduq2"))

	require.NoError(t, err)
	require.Equal(t, 7, payload.Duration)
}

func TestConvertToRequestPayloadRejectsUnsafeImageURL(t *testing.T) {
	for _, value := range []string{
		"file:///etc/passwd",
		"https://user:secret@media.example.test/image.png",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
				Prompt: "test video",
				Images: []string{value},
			}, testRelayInfo("viduq2"))
			require.Error(t, err)
		})
	}
}

func TestConvertToRequestPayloadAllowsBase64Image(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt: "test video",
		Images: []string{"iVBORw0KGgo="},
	}, testRelayInfo("viduq2"))
	require.NoError(t, err)
	require.Equal(t, []string{"iVBORw0KGgo="}, payload.Images)
}
