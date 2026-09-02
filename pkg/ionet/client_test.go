package ionet

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultHTTPClientDoesNotForwardAPIKeyAcrossRedirect(t *testing.T) {
	forwarded := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "" {
			forwarded <- struct{}{}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	client := NewDefaultHTTPClient(time.Second)
	resp, err := client.Do(&HTTPRequest{
		Method: http.MethodGet,
		URL:    redirect.URL,
		Headers: map[string]string{
			"X-API-KEY": "io-secret",
		},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)

	select {
	case <-forwarded:
		t.Fatal("redirect target received the IO.NET API key")
	default:
	}
}
