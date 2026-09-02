package doubao

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertToRequestPayloadRejectsMetadataDurationOverflow(t *testing.T) {
	_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Model:   "doubao-seedance-1-0-pro-250528",
		Prompt:  "test video",
		Seconds: "5",
		Metadata: map[string]any{
			"duration": relaycommon.MaxTaskDurationSeconds + 1,
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "duration")
}

func TestConvertToRequestPayloadRejectsCaseInsensitiveMetadataModel(t *testing.T) {
	for _, key := range []string{"model", "Model", "MODEL_NAME"} {
		t.Run(key, func(t *testing.T) {
			_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
				Model:    "doubao-seedance-1-0-pro-250528",
				Prompt:   "test video",
				Metadata: map[string]any{key: "different-model"},
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), "model")
		})
	}
}

func TestConvertToRequestPayloadNormalizesDurationFields(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Model:    "doubao-seedance-1-0-pro-250528",
		Prompt:   "test video",
		Duration: 6,
	})
	require.NoError(t, err)
	require.NotNil(t, payload.Duration)
	require.Equal(t, 6, int(*payload.Duration))

	payload, err = (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Model:    "doubao-seedance-1-0-pro-250528",
		Prompt:   "test video",
		Duration: 6,
		Seconds:  "8",
	})
	require.NoError(t, err)
	require.NotNil(t, payload.Duration)
	require.Equal(t, 8, int(*payload.Duration))
}

func TestParseTaskResultCarriesProviderTaskID(t *testing.T) {
	body := []byte(`{"id":"doubao-task-42","status":"processing"}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "doubao-task-42", result.TaskID)
}

func TestParseTaskResultRejectsUnsafeSuccessURL(t *testing.T) {
	body := []byte(`{"id":"doubao-task-42","status":"succeeded","content":{"video_url":"file:///etc/passwd"}}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "video URL")
}
