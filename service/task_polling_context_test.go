package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type contextAwarePollingAdaptor struct {
	legacyCalled bool
	contextSeen  context.Context
}

func (a *contextAwarePollingAdaptor) Init(*relaycommon.RelayInfo) {}
func (a *contextAwarePollingAdaptor) FetchTask(string, string, map[string]any, string) (*http.Response, error) {
	a.legacyCalled = true
	return nil, nil
}
func (a *contextAwarePollingAdaptor) FetchTaskWithContext(ctx context.Context, _ string, _ string, _ map[string]any, _ string) (*http.Response, error) {
	a.contextSeen = ctx
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(http.NoBody)}, nil
}
func (a *contextAwarePollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return nil, nil
}
func (a *contextAwarePollingAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}

func TestFetchTaskWithContextPrefersCancellableAdaptor(t *testing.T) {
	adaptor := &contextAwarePollingAdaptor{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resp, err := FetchTaskWithContext(ctx, adaptor, "https://example.test", "key", map[string]any{"task_id": "id"}, "")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.False(t, adaptor.legacyCalled)
	require.NotNil(t, adaptor.contextSeen)
	_, hasDeadline := adaptor.contextSeen.Deadline()
	assert.True(t, hasDeadline)
	require.NoError(t, resp.Body.Close())
}

var _ TaskPollingAdaptor = (*contextAwarePollingAdaptor)(nil)
