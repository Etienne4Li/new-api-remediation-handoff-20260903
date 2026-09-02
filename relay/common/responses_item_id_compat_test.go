package common

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeResponsesInputItemIDsRepairsKnownPrefixes(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"reasoning","id":"item_reasoning_1"},
			{"type":"message","id":"item_message_1"},
			{"role":"assistant","id":"item_message_2"},
			{"type":"message","id":"resp_abc123_msg"},
			{"type":"custom_tool_call","id":"fc_call_1"},
			{"type":"function_call","id":"ctc_call_2"},
			{"type":"custom_tool_call_output","id":"fco_output_1"},
			{"type":"function_call_output","id":"ctco_output_2"}
		],
		"preserved":true
	}`)

	got, report, err := NormalizeResponsesInputItemIDs(body)
	require.NoError(t, err)
	require.Equal(t, ResponsesInputItemIDNormalizationReport{
		Reasoning:          1,
		Message:            3,
		FunctionCall:       1,
		CustomToolCall:     1,
		FunctionCallOutput: 1,
		CustomToolOutput:   1,
	}, report)
	require.Equal(t, "rs_reasoning_1", gjson.GetBytes(got, "input.0.id").String())
	require.Equal(t, "msg_message_1", gjson.GetBytes(got, "input.1.id").String())
	require.Equal(t, "msg_message_2", gjson.GetBytes(got, "input.2.id").String())
	require.Equal(t, "msg_abc123_msg", gjson.GetBytes(got, "input.3.id").String())
	require.Equal(t, "ctc_fc_call_1", gjson.GetBytes(got, "input.4.id").String())
	require.Equal(t, "fc_ctc_call_2", gjson.GetBytes(got, "input.5.id").String())
	require.Equal(t, "ctco_fco_output_1", gjson.GetBytes(got, "input.6.id").String())
	require.Equal(t, "fco_ctco_output_2", gjson.GetBytes(got, "input.7.id").String())
	require.True(t, gjson.GetBytes(got, "preserved").Bool())
}

func TestNormalizeResponsesInputItemIDsLeavesUnknownIDsUntouched(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","id":"item_unknown"},{"type":"reasoning","id":"other_invalid"},{"type":"message","id":"resp_bad-id_msg"}]}`)

	got, report, err := NormalizeResponsesInputItemIDs(body)
	require.NoError(t, err)
	require.Equal(t, ResponsesInputItemIDNormalizationReport{}, report)
	require.Equal(t, string(body), string(got))
}

func TestNormalizeResponsesInputItemIDsLeavesNonArrayInputUntouched(t *testing.T) {
	body := []byte(`{"input":"hello"}`)

	got, report, err := NormalizeResponsesInputItemIDs(body)
	require.NoError(t, err)
	require.Equal(t, ResponsesInputItemIDNormalizationReport{}, report)
	require.Equal(t, string(body), string(got))
}
