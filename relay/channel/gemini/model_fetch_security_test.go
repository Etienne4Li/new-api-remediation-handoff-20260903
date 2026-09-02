package gemini

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchGeminiModelsDoesNotExposeUpstreamBody(t *testing.T) {
	const secret = "provider-error-with-sensitive-details"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(secret))
	}))
	defer server.Close()

	_, err := FetchGeminiModels(server.URL, "google-api-key-secret", "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
	require.Contains(t, err.Error(), "body_meta=")
	// The key is sent as a header and must never be copied into an error either.
	require.NotContains(t, err.Error(), "google-api-key-secret")
	// Keep this assertion explicit so a future implementation cannot silently
	// fall back to returning the raw response body.
	require.True(t, strings.Contains(err.Error(), "服务器返回错误"))
}
