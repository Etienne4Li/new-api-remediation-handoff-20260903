package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRefreshCodexOAuthTokenRejectsRedirect(t *testing.T) {
	forwarded := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	client, err := getCodexOAuthHTTPClient("")
	require.NoError(t, err)
	client.Timeout = time.Second
	response, err := refreshCodexOAuthToken(
		context.Background(), client, redirect.URL, "client", "refresh-secret",
	)
	require.Error(t, err)
	require.Nil(t, response)

	select {
	case <-forwarded:
		t.Fatal("redirect target received the Codex refresh token")
	case <-time.After(200 * time.Millisecond):
	}
}
