package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPServerTimeoutsFromEnvProtectSlowRequestsAndPreserveSSE(t *testing.T) {
	t.Setenv("HTTP_READ_HEADER_TIMEOUT_SECONDS", "7")
	t.Setenv("HTTP_READ_TIMEOUT_SECONDS", "31")
	t.Setenv("HTTP_WRITE_TIMEOUT_SECONDS", "0")
	t.Setenv("HTTP_IDLE_TIMEOUT_SECONDS", "43")

	timeouts := httpServerTimeoutsFromEnv()
	require.Equal(t, 7*time.Second, timeouts.ReadHeader)
	require.Equal(t, 31*time.Second, timeouts.Read)
	require.Zero(t, timeouts.Write)
	require.Equal(t, 43*time.Second, timeouts.Idle)

	server := newHTTPServer(":0", http.NotFoundHandler(), timeouts)
	require.Equal(t, timeouts.ReadHeader, server.ReadHeaderTimeout)
	require.Equal(t, timeouts.Read, server.ReadTimeout)
	require.Equal(t, timeouts.Write, server.WriteTimeout)
	require.Equal(t, timeouts.Idle, server.IdleTimeout)
	require.Equal(t, 1<<20, server.MaxHeaderBytes)
}

func TestHTTPServerTimeoutsRejectInvalidValues(t *testing.T) {
	t.Setenv("HTTP_READ_HEADER_TIMEOUT_SECONDS", "-1")
	t.Setenv("HTTP_READ_TIMEOUT_SECONDS", "bad")
	t.Setenv("HTTP_WRITE_TIMEOUT_SECONDS", "-2")
	t.Setenv("HTTP_IDLE_TIMEOUT_SECONDS", "0")

	timeouts := httpServerTimeoutsFromEnv()
	require.Equal(t, 15*time.Second, timeouts.ReadHeader)
	require.Equal(t, 120*time.Second, timeouts.Read)
	require.Zero(t, timeouts.Write)
	require.Equal(t, 120*time.Second, timeouts.Idle)
}

func TestHTTPServerTimeoutsRejectHugeValues(t *testing.T) {
	t.Setenv("HTTP_READ_HEADER_TIMEOUT_SECONDS", "9223372036854775807")
	t.Setenv("HTTP_READ_TIMEOUT_SECONDS", "999999999999")
	t.Setenv("HTTP_WRITE_TIMEOUT_SECONDS", "999999999999")
	t.Setenv("HTTP_IDLE_TIMEOUT_SECONDS", "-9223372036854775808")

	timeouts := httpServerTimeoutsFromEnv()
	require.Equal(t, 15*time.Second, timeouts.ReadHeader)
	require.Equal(t, 120*time.Second, timeouts.Read)
	require.Equal(t, 0*time.Second, timeouts.Write)
	require.Equal(t, 120*time.Second, timeouts.Idle)
}

func TestChannelUpdateFrequencyRejectsNonPositiveAndHugeValues(t *testing.T) {
	for _, value := range []string{"0", "-1", "9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CHANNEL_UPDATE_FREQUENCY", value)
			require.Equal(t, 30, channelUpdateFrequencyFromEnv())
		})
	}
	t.Setenv("CHANNEL_UPDATE_FREQUENCY", "45")
	require.Equal(t, 45, channelUpdateFrequencyFromEnv())
}
