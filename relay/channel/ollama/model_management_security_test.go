package ollama

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchOllamaModelsDoesNotExposeUpstreamBody(t *testing.T) {
	const secret = "ollama-error-with-sensitive-details"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(secret))
	}))
	defer server.Close()

	_, err := FetchOllamaModels(server.URL, "ollama-api-key-secret")
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
	require.NotContains(t, err.Error(), "ollama-api-key-secret")
	require.Contains(t, err.Error(), "body_meta=")
}
