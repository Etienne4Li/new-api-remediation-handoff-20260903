package kling

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestConvertToRequestPayloadRejectsMetadataDurationOverflow(t *testing.T) {
	adaptor := &TaskAdaptor{}
	_, err := adaptor.convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt:   "a test video",
		Duration: 5,
		Metadata: map[string]any{"duration": "999999999999999999999"},
	}, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kling-v1"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duration")
}

func TestConvertToRequestPayloadRejectsMetadataModelOverride(t *testing.T) {
	for _, key := range []string{"model", "Model", "MODEL_NAME"} {
		t.Run(key, func(t *testing.T) {
			_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
				Prompt:   "a test video",
				Duration: 5,
				Metadata: map[string]any{key: "kling-v2-master"},
			}, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "kling-v1"}})
			require.Error(t, err)
			require.Contains(t, err.Error(), "model")
		})
	}
}

func TestParseTaskResultRejectsInvalidFinalUnitDeduction(t *testing.T) {
	for _, raw := range []string{"NaN", "Inf", "-Inf", "-1", "1e100"} {
		t.Run(raw, func(t *testing.T) {
			body := []byte(`{"code":0,"data":{"task_status":"succeed","final_unit_deduction":"` + raw + `"}}`)
			_, err := (&TaskAdaptor{}).ParseTaskResult(body)
			require.Error(t, err)
		})
	}
}

func TestParseTaskResultConvertsFiniteFinalUnitDeduction(t *testing.T) {
	body := []byte(`{"code":0,"data":{"task_status":"succeed","final_unit_deduction":"12.2"}}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.Equal(t, model.TaskStatusSuccess, result.Status)
	require.Equal(t, 13, result.TotalTokens)
}

func TestParseTaskResultKeepsEnvelopeErrorWhenNestedStatusSucceeds(t *testing.T) {
	body := []byte(`{"code":1001,"message":"upstream failed","data":{"task_id":"kling-task-42","task_status":"succeed","task_result":{"videos":[{"url":"https://attacker.example/video.mp4"}]}}}`)
	result, err := (&TaskAdaptor{}).ParseTaskResult(body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, string(model.TaskStatusFailure), result.Status)
	require.Equal(t, "upstream failed", result.Reason)
	require.Empty(t, result.Url)
}
