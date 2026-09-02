package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWeChatHTTPClientRefusesRedirectBeforeResendingCredential(t *testing.T) {
	forwarded := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		forwarded <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	req, err := http.NewRequest(http.MethodGet, redirect.URL, strings.NewReader("unused"))
	require.NoError(t, err)
	req.Header.Set("Authorization", "wechat-secret")
	res, err := newWeChatHTTPClient(time.Second).Do(req)
	require.NoError(t, err)
	require.NotNil(t, res)
	defer res.Body.Close()
	require.Equal(t, http.StatusTemporaryRedirect, res.StatusCode)

	select {
	case <-forwarded:
		t.Fatal("redirect target received the WeChat credential")
	case <-time.After(200 * time.Millisecond):
	}
}
