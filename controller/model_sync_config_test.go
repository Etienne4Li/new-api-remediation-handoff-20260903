package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSyncHTTPTimeoutRejectsInvalidAndHugeValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "zero", value: "0", want: 15 * time.Second},
		{name: "negative", value: "-1", want: 15 * time.Second},
		{name: "malformed", value: "not-a-number", want: 15 * time.Second},
		{name: "overflow", value: "9223372036854775807", want: 15 * time.Second},
		{name: "valid", value: "23", want: 23 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SYNC_HTTP_TIMEOUT_SECONDS", test.value)
			require.Equal(t, test.want, syncHTTPTimeout(15))
		})
	}
}

func TestSyncRetryAndResponseLimitsFallback(t *testing.T) {
	t.Setenv("SYNC_HTTP_RETRY", "999999999999")
	require.Equal(t, 3, getSyncHTTPRetryCount())
	t.Setenv("SYNC_HTTP_RETRY", "0")
	require.Equal(t, 3, getSyncHTTPRetryCount())
	t.Setenv("SYNC_HTTP_MAX_MB", "999999999999")
	require.Equal(t, 10, getSyncHTTPResponseLimitMB())
	t.Setenv("SYNC_HTTP_MAX_MB", "0")
	require.Equal(t, 10, getSyncHTTPResponseLimitMB())
}

func TestWaitSyncRetryStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, waitSyncRetry(ctx, time.Hour, 0))
}
