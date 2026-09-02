package oauth

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
)

// OAuth responses are controlled by an external identity provider.  They are
// expected to be small JSON (or URL-encoded) documents, so never let a broken
// or compromised provider make an OAuth callback allocate without a bound.
// Keep this generous enough for custom provider claims while protecting the
// request goroutine from an unbounded body.
const maxOAuthResponseBodyBytes int64 = 1 << 20

var ErrOAuthResponseTooLarge = errors.New("oauth provider response body too large")

// readOAuthResponseBody reads an OAuth provider response with a hard upper
// bound.  Content-Length is checked before allocation when available, and an
// extra byte is read for chunked/unknown-length responses so the limit cannot
// be bypassed by omitting the header.
func readOAuthResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("oauth provider response body is nil")
	}
	if resp.ContentLength > maxOAuthResponseBodyBytes {
		return nil, fmt.Errorf("%w: limit=%d bytes", ErrOAuthResponseTooLarge, maxOAuthResponseBodyBytes)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxOAuthResponseBodyBytes {
		return nil, fmt.Errorf("%w: limit=%d bytes", ErrOAuthResponseTooLarge, maxOAuthResponseBodyBytes)
	}
	return body, nil
}

// decodeOAuthJSONResponse is the bounded equivalent of json.Decoder.Decode
// used by the built-in OAuth providers.  Reading first also makes the bound
// effective for chunked responses where Content-Length is -1.
func decodeOAuthJSONResponse(resp *http.Response, destination any) error {
	body, err := readOAuthResponseBody(resp)
	if err != nil {
		return err
	}
	return common.Unmarshal(body, destination)
}
