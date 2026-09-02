package common

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

import "github.com/QuantumNous/new-api/constant"

const defaultAnonymousRequestBodyLimitKB = 512
const maxInt64RequestLimit = int64(^uint64(0) >> 1)

const DefaultMaxFileDownloadBytes int64 = 64 << 20
const DefaultMaxRequestBodyBytes int64 = 128 << 20

// GetMaxFileDownloadBytes converts MAX_FILE_DOWNLOAD_MB to bytes while
// retaining a finite, safe default for unset or overflowing configuration.
func GetMaxFileDownloadBytes() int64 {
	maxMB := int64(constant.MaxFileDownloadMB)
	if maxMB <= 0 || maxMB > maxInt64RequestLimit>>20 {
		return DefaultMaxFileDownloadBytes
	}
	return maxMB << 20
}

// GetMaxRequestBodyBytes converts MAX_REQUEST_BODY_MB to bytes with a safe
// default.  It is shared by request middleware and adaptors that materialize
// a transformed copy of the request body.
func GetMaxRequestBodyBytes() int64 {
	maxMB := int64(constant.MaxRequestBodyMB)
	if maxMB <= 0 || maxMB > maxInt64RequestLimit>>20 {
		return DefaultMaxRequestBodyBytes
	}
	return maxMB << 20
}

func GetAnonymousRequestBodyLimitBytes() int64 {
	limitKB := constant.AnonymousRequestBodyLimitKB
	// A non-positive value must not disable the guard.  This limit is used on
	// unauthenticated endpoints (including payment webhooks), where an
	// operator typo such as ANONYMOUS_REQUEST_BODY_LIMIT_KB=0 would otherwise
	// turn an untrusted request body into an unbounded allocation.
	if limitKB <= 0 {
		limitKB = defaultAnonymousRequestBodyLimitKB
	}
	limitBytes := int64(limitKB)
	if limitBytes > maxInt64RequestLimit>>10 {
		return int64(defaultAnonymousRequestBodyLimitKB) << 10
	}
	return limitBytes << 10
}

// ReadBodyLimited consumes an untrusted request body without allowing an
// omitted/incorrect Content-Length header to turn it into an unbounded heap
// allocation. It reads one sentinel byte past the configured limit so both
// fixed-length and chunked requests are rejected consistently.
func ReadBodyLimited(body io.Reader, contentLength, maxBytes int64) ([]byte, error) {
	if body == nil {
		return nil, errors.New("request body is nil")
	}
	if maxBytes <= 0 {
		maxBytes = GetAnonymousRequestBodyLimitBytes()
	}
	// Leave room for the sentinel read below.  Callers normally use a much
	// smaller configured limit, but avoid wrapping maxBytes+1 if an extreme
	// value is supplied programmatically.
	if maxBytes >= maxInt64RequestLimit {
		maxBytes = maxInt64RequestLimit - 1
	}
	if contentLength > maxBytes {
		return nil, ErrRequestBodyTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrRequestBodyTooLarge
	}
	return data, nil
}

// CopyBodyLimited copies an untrusted stream while enforcing a hard byte
// limit.  The destination receives at most one sentinel byte beyond the
// limit; callers must discard the destination when ErrRequestBodyTooLarge is
// returned.  Wrapping the source in LimitReader also prevents an io.WriterTo
// implementation from bypassing the bound.
func CopyBodyLimited(dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	if dst == nil {
		return 0, errors.New("destination writer is nil")
	}
	if src == nil {
		return 0, errors.New("source reader is nil")
	}
	if maxBytes <= 0 {
		maxBytes = GetAnonymousRequestBodyLimitBytes()
	}
	if maxBytes >= maxInt64RequestLimit {
		maxBytes = maxInt64RequestLimit - 1
	}
	n, err := io.Copy(dst, io.LimitReader(src, maxBytes+1))
	if err != nil {
		return n, err
	}
	if n > maxBytes {
		return n, ErrRequestBodyTooLarge
	}
	return n, nil
}

// DecodeBase64Limited decodes a base64 payload only after checking both its
// encoded length and the exact decoded length.  The latter is necessary
// because adjacent decoded sizes can share the same padded encoded length.
func DecodeBase64Limited(payload string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = GetMaxFileDownloadBytes()
	}
	if maxBytes >= maxInt64RequestLimit {
		maxBytes = maxInt64RequestLimit - 1
	}
	if maxBytes <= 0 {
		return nil, errors.New("invalid base64 size limit")
	}
	if maxBytes > (maxInt64RequestLimit - 2) {
		return nil, errors.New("base64 size limit is too large")
	}
	groups := (maxBytes + 2) / 3
	if groups > maxInt64RequestLimit/4 || int64(len(payload)) > groups*4 {
		return nil, fmt.Errorf("base64 payload exceeds maximum decoded size of %d bytes", maxBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}
	if int64(len(decoded)) > maxBytes {
		return nil, fmt.Errorf("decoded payload exceeds maximum size of %d bytes", maxBytes)
	}
	return decoded, nil
}
