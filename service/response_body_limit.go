package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultProviderResponseBodyLimitBytes bounds a non-streaming response from
// an upstream provider.  Provider error/JSON payloads are normally tiny, but
// a misbehaving endpoint must not be able to force an unbounded allocation in
// every relay handler that parses a response with io.ReadAll.
const DefaultProviderResponseBodyLimitBytes int64 = 16 << 20

var ErrProviderResponseBodyTooLarge = errors.New("provider response body too large")

const maxInt64ProviderResponseLimit = int64(^uint64(0) >> 1)

// ReadProviderResponseBody reads a provider response body with a hard limit.
// Check Content-Length first, then read one extra byte so chunked responses
// cannot bypass the limit by omitting the header.  The response body remains
// owned by the caller and is not closed here.
func ReadProviderResponseBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("provider response body is nil")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultProviderResponseBodyLimitBytes
	}
	if maxBytes >= maxInt64ProviderResponseLimit {
		maxBytes = maxInt64ProviderResponseLimit - 1
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d bytes", ErrProviderResponseBodyTooLarge, maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d bytes", ErrProviderResponseBodyTooLarge, maxBytes)
	}
	return body, nil
}
