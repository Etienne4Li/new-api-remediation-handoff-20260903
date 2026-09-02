package hailuo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func hailuoRelayInfo(model string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: model},
	}
}

func TestConvertToRequestPayloadRejectsMetadataDurationOverflow(t *testing.T) {
	_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt:   "test video",
		Duration: 6,
		Metadata: map[string]any{"duration": relaycommon.MaxTaskDurationSeconds + 1},
	}, hailuoRelayInfo("MiniMax-Hailuo-02"))

	require.Error(t, err)
	require.Contains(t, err.Error(), "duration")
}

func TestConvertToRequestPayloadRejectsCaseInsensitiveMetadataModel(t *testing.T) {
	for _, key := range []string{"model", "Model", "MODEL_NAME"} {
		t.Run(key, func(t *testing.T) {
			_, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
				Prompt:   "test video",
				Metadata: map[string]any{key: "T2V-01"},
			}, hailuoRelayInfo("MiniMax-Hailuo-02"))
			require.Error(t, err)
			require.Contains(t, err.Error(), "model")
		})
	}
}

func TestConvertToRequestPayloadUsesSeconds(t *testing.T) {
	payload, err := (&TaskAdaptor{}).convertToRequestPayload(&relaycommon.TaskSubmitReq{
		Prompt:  "test video",
		Seconds: "10",
	}, hailuoRelayInfo("MiniMax-Hailuo-02"))

	require.NoError(t, err)
	require.NotNil(t, payload.Duration)
	require.Equal(t, 10, *payload.Duration)
}

func TestParseTaskResultCarriesIdentityAndEscapesFileID(t *testing.T) {
	var gotPath, gotFileID, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFileID = r.URL.Query().Get("file_id")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"file":{"download_url":"https://cdn.example/video.mp4?sig=provider"},"base_resp":{"status_code":0}}`))
	}))
	defer server.Close()

	adaptor := &TaskAdaptor{baseURL: server.URL, apiKey: "test-key"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := adaptor.ParseTaskResultWithContext(ctx, []byte(`{"task_id":"hailuo-task-42","status":"Success","file_id":"file/id?x"}`), "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, model.TaskStatusSuccess, result.Status)
	require.Equal(t, "hailuo-task-42", result.TaskID)
	require.Equal(t, "/v1/files/retrieve", gotPath)
	require.Equal(t, "file/id?x", gotFileID)
	require.Equal(t, "Bearer test-key", gotAuth)
}

func TestFetchTaskWithContextHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&TaskAdaptor{}).FetchTaskWithContext(ctx, server.URL, "key", map[string]any{"task_id": "task"}, "")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestParseTaskResultKeepsEnvelopeErrorWhenStatusSaysSuccess(t *testing.T) {
	adaptor := &TaskAdaptor{baseURL: "https://provider.example", apiKey: "test-key"}
	result, err := adaptor.ParseTaskResultWithContext(context.Background(), []byte(`{"task_id":"hailuo-task-42","status":"Success","file_id":"file-42","base_resp":{"status_code":1001,"status_msg":"provider failed"}}`), "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, model.TaskStatusFailure, result.Status)
	require.Equal(t, 1001, result.Code)
	require.Equal(t, "provider failed", result.Reason)
}
