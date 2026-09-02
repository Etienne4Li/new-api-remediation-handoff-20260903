package gemini

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTaskWithContextCancelsHungProviderRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	operation := "projects/test/locations/us-central1/models/veo-3.0-generate-001/operations/op-1"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	resp, err := (&TaskAdaptor{}).FetchTaskWithContext(ctx, server.URL, "test-key", map[string]any{
		"task_id": taskcommon.EncodeLocalTaskID(operation),
	}, "")
	assert.Nil(t, resp)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "request should honor caller deadline: %v", err)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider request was not started")
	}
}
