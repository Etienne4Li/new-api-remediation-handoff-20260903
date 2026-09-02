package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeNotificationURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "trims valid https", input: "  https://notify.example.test/hook  ", want: "https://notify.example.test/hook"},
		{name: "accepts http", input: "http://notify.example.test/hook", want: "http://notify.example.test/hook"},
		{name: "rejects file scheme", input: "file:///tmp/notify", wantErr: "http or https"},
		{name: "rejects gopher scheme", input: "gopher://127.0.0.1:6379/", wantErr: "http or https"},
		{name: "rejects relative URL", input: "//notify.example.test/hook", wantErr: "http or https"},
		{name: "rejects missing host", input: "https://", wantErr: "host"},
		{name: "rejects userinfo", input: "https://user:pass@notify.example.test/hook", wantErr: "userinfo"},
		{name: "rejects template origin", input: "https://{{host}}/hook", wantErr: "invalid notification URL"},
		{name: "allows bark template path", input: "https://notify.example.test/{{title}}/{{content}}", want: "https://notify.example.test/{{title}}/{{content}}"},
		{name: "rejects fragment", input: "https://notify.example.test/hook#secret", wantErr: "fragment"},
		{name: "rejects empty port", input: "https://notify.example.test:", wantErr: "port"},
		{name: "rejects zero port", input: "https://notify.example.test:0", wantErr: "port"},
		{name: "rejects oversized port", input: "https://notify.example.test:65536", wantErr: "port"},
		{name: "rejects control character", input: "https://notify.example.test/hook\x7f", wantErr: "control"},
		{name: "rejects oversized URL", input: "https://notify.example.test/" + strings.Repeat("a", MaxNotificationURLLength), wantErr: "too long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeNotificationURL(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeNotificationCredential(t *testing.T) {
	got, err := NormalizeNotificationCredential("  webhook-secret  ", MaxNotificationSecretLength)
	require.NoError(t, err)
	require.Equal(t, "webhook-secret", got)

	_, err = NormalizeNotificationCredential(strings.Repeat("x", MaxNotificationTokenLength+1), MaxNotificationTokenLength)
	require.ErrorContains(t, err, "too long")
	_, err = NormalizeNotificationCredential("token\nvalue", MaxNotificationTokenLength)
	require.ErrorContains(t, err, "control")
}
