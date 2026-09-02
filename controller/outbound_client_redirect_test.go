package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelSyncHTTPClientRefusesRedirect(t *testing.T) {
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

	client := newHTTPClient()
	client.Timeout = time.Second
	resp, err := client.Get(redirect.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	select {
	case <-forwarded:
		t.Fatal("metadata redirect target received a request")
	default:
	}
}

func TestRatioSyncHTTPClientRefusesRedirect(t *testing.T) {
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

	client := newRatioSyncHTTPClient()
	client.Timeout = time.Second
	req, err := http.NewRequest(http.MethodGet, redirect.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
	require.NoError(t, resp.Body.Close())

	select {
	case <-forwarded:
		t.Fatal("ratio redirect target received the OpenRouter credential")
	default:
	}
}
