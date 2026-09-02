package oauth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuthHTTPClientRefusesRedirectBeforeResendingCredentials(t *testing.T) {
	type observedRequest struct {
		method string
		body   string
		auth   string
	}

	forwarded := make(chan observedRequest, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwarded <- observedRequest{method: r.Method, body: string(body), auth: r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	req, err := http.NewRequest(http.MethodPost, redirect.URL, strings.NewReader("client_secret=do-not-forward"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Basic do-not-forward")

	res, err := newOAuthHTTPClient(time.Second).Do(req)
	require.NoError(t, err)
	require.NotNil(t, res)
	defer res.Body.Close()
	require.Equal(t, http.StatusTemporaryRedirect, res.StatusCode)

	select {
	case got := <-forwarded:
		t.Fatalf("redirect target received credentials: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
}
