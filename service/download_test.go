package service

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

func TestBoundedDownloadRequestTimeout(t *testing.T) {
	tests := []struct {
		name   string
		relay  int
		expect time.Duration
	}{
		{name: "zero uses finite default", relay: 0, expect: defaultDownloadRequestTimeout},
		{name: "negative uses finite default", relay: -1, expect: defaultDownloadRequestTimeout},
		{name: "positive relay timeout is respected", relay: 7, expect: 7 * time.Second},
		{name: "extreme relay timeout is capped", relay: int(^uint(0) >> 1), expect: maxDownloadRequestTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expect, boundedDownloadRequestTimeout(test.relay))
		})
	}
}

func TestCancelOnCloseBodyCancelsRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &cancelOnCloseBody{ReadCloser: io.NopCloser(strings.NewReader("ok")), cancel: cancel}
	require.NoError(t, body.Close())
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("closing response body did not cancel request context")
	}
}

func TestDoWorkerRequestRejectsNonHTTPURLWhenSSRFDisabled(t *testing.T) {
	previous := system_setting.GetRuntimeConfig()
	t.Cleanup(func() {
		system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) { *cfg = previous })
	})
	// The syntax boundary must remain active even when the optional SSRF
	// policy is disabled. No network request is needed for this rejection.
	system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) {
		cfg.WorkerURL = "https://worker.example.test"
		cfg.WorkerAllowHttpImageRequestEnabled = true
	})
	err := func() error {
		_, err := DoWorkerRequest(&WorkerRequest{URL: "httpsomething://target.example/asset"})
		return err
	}()
	require.Error(t, err)
	require.Contains(t, err.Error(), "only http/https allowed")
}
