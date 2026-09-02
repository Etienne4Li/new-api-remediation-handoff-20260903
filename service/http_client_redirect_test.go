package service

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requestForRedirectTest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	for _, name := range sensitiveRedirectHeaders {
		req.Header.Set(name, "secret-value")
	}
	return req
}

func TestStripSensitiveRedirectHeadersUsesOriginSemantics(t *testing.T) {
	previous := requestForRedirectTest(t, "https://provider.example:443/status")
	next := requestForRedirectTest(t, "https://provider.example:8443/status")

	stripSensitiveRedirectHeaders(next, []*http.Request{previous})

	for _, name := range sensitiveRedirectHeaders {
		assert.Empty(t, next.Header.Values(name), name)
	}
}

func TestStripSensitiveRedirectHeadersPreservesSameOriginCredentials(t *testing.T) {
	previous := requestForRedirectTest(t, "https://PROVIDER.example/status")
	next := requestForRedirectTest(t, "https://provider.example:443/next")

	stripSensitiveRedirectHeaders(next, []*http.Request{previous})

	for _, name := range sensitiveRedirectHeaders {
		assert.Equal(t, []string{"secret-value"}, next.Header.Values(name), name)
	}
}

func TestStripSensitiveRedirectHeadersStripsSchemeAndHostChanges(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
	}{
		{name: "scheme", from: "https://provider.example/status", to: "http://provider.example/status"},
		{name: "host", from: "https://provider.example/status", to: "https://attacker.example/status"},
		{name: "userinfo", from: "https://provider.example/status", to: "https://user:pass@provider.example/status"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := requestForRedirectTest(t, test.from)
			next := requestForRedirectTest(t, test.to)
			stripSensitiveRedirectHeaders(next, []*http.Request{previous})
			for _, name := range sensitiveRedirectHeaders {
				assert.Empty(t, next.Header.Values(name), name)
			}
		})
	}
}

func TestSameHTTPOrigin(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "default port", left: "https://provider.example/a", right: "https://provider.example:443/b", want: true},
		{name: "case insensitive host", left: "HTTP://PROVIDER.EXAMPLE:80/a", right: "http://provider.example/b", want: true},
		{name: "different port", left: "https://provider.example:443/a", right: "https://provider.example:8443/b", want: false},
		{name: "different scheme", left: "https://provider.example/a", right: "http://provider.example/a", want: false},
		{name: "different host", left: "https://provider.example/a", right: "https://other.example/a", want: false},
		{name: "userinfo on left", left: "https://user:pass@provider.example/a", right: "https://provider.example/b", want: false},
		{name: "userinfo on right", left: "https://provider.example/a", right: "https://user:pass@provider.example/b", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := url.Parse(test.left)
			require.NoError(t, err)
			right, err := url.Parse(test.right)
			require.NoError(t, err)
			assert.Equal(t, test.want, sameHTTPOrigin(left, right))
		})
	}
}

func TestStripSensitiveRedirectHeadersHandlesMissingHistory(t *testing.T) {
	next := requestForRedirectTest(t, "https://provider.example/status")
	stripSensitiveRedirectHeaders(next, nil)
	for _, name := range sensitiveRedirectHeaders {
		assert.Equal(t, []string{"secret-value"}, next.Header.Values(name), name)
	}
}
