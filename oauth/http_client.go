package oauth

import (
	"net/http"
	"time"
)

// newOAuthHTTPClient deliberately refuses redirects. OAuth token requests can
// carry client_secret in the POST body (or credentials in Authorization), and
// following a 307/308 redirect could resend those credentials to a different
// origin. Provider endpoints should be explicit and do not need transparent
// redirect handling; callers receive the redirect response and can fail it.
func newOAuthHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
