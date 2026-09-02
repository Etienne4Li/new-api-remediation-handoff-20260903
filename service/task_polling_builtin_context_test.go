package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/task/ali"
	"github.com/QuantumNous/new-api/relay/channel/task/doubao"
	"github.com/QuantumNous/new-api/relay/channel/task/jimeng"
	"github.com/QuantumNous/new-api/relay/channel/task/kling"
	"github.com/QuantumNous/new-api/relay/channel/task/sora"
	"github.com/QuantumNous/new-api/relay/channel/task/suno"
	"github.com/QuantumNous/new-api/relay/channel/task/vidu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type taskFetchWithContext func(context.Context, string, string, map[string]any, string) (*http.Response, error)

func TestBuiltinTaskAdaptorsCancelInFlightPollingRequest(t *testing.T) {
	tests := []struct {
		name  string
		fetch taskFetchWithContext
		key   string
		body  map[string]any
	}{
		{name: "ali", fetch: (&ali.TaskAdaptor{}).FetchTaskWithContext, key: "sk-test", body: map[string]any{"task_id": "task-1"}},
		{name: "doubao", fetch: (&doubao.TaskAdaptor{}).FetchTaskWithContext, key: "sk-test", body: map[string]any{"task_id": "task-1"}},
		{name: "jimeng", fetch: (&jimeng.TaskAdaptor{}).FetchTaskWithContext, key: "access|secret", body: map[string]any{"task_id": "task-1"}},
		{name: "kling", fetch: (&kling.TaskAdaptor{}).FetchTaskWithContext, key: "sk-test", body: map[string]any{"task_id": "task-1", "action": constant.TaskActionGenerate}},
		{name: "sora", fetch: (&sora.TaskAdaptor{}).FetchTaskWithContext, key: "sk-test", body: map[string]any{"task_id": "task-1"}},
		{name: "suno", fetch: (&suno.TaskAdaptor{}).FetchTaskWithContext, key: "sk-test", body: map[string]any{"ids": []string{"task-1"}}},
		{name: "vidu", fetch: (&vidu.TaskAdaptor{}).FetchTaskWithContext, key: "sk-test", body: map[string]any{"task_id": "task-1"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
				close(started)
				select {
				case <-req.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)

			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				resp, err := tc.fetch(ctx, server.URL, tc.key, tc.body, "")
				if resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
				result <- err
			}()

			select {
			case <-started:
			case err := <-result:
				require.Failf(t, "request did not reach provider", "%s returned early: %v", tc.name, err)
			case <-time.After(time.Second):
				require.Fail(t, "request did not reach provider before timeout")
			}

			cancel()
			select {
			case err := <-result:
				require.Error(t, err)
				assert.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				require.Fail(t, "in-flight provider request ignored cancellation")
			}
		})
	}
}
