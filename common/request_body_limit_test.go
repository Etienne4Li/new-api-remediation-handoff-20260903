package common

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestGetAnonymousRequestBodyLimitBytesUsesDefaultForNonPositiveValues(t *testing.T) {
	previous := constant.AnonymousRequestBodyLimitKB
	t.Cleanup(func() { constant.AnonymousRequestBodyLimitKB = previous })

	for _, value := range []int{0, -1, -1024} {
		constant.AnonymousRequestBodyLimitKB = value
		require.Equal(t, int64(defaultAnonymousRequestBodyLimitKB)<<10,
			GetAnonymousRequestBodyLimitBytes(), "limit=%d", value)
	}
}

func TestReadBodyLimitedRejectsDeclaredAndChunkedOversize(t *testing.T) {
	const limit = int64(4)
	_, err := ReadBodyLimited(bytes.NewBufferString("small"), 5, limit)
	require.ErrorIs(t, err, ErrRequestBodyTooLarge)

	_, err = ReadBodyLimited(bytes.NewBufferString("12345"), -1, limit)
	require.ErrorIs(t, err, ErrRequestBodyTooLarge)
}

func TestReadBodyLimitedAcceptsBodyAtLimitAndRejectsNil(t *testing.T) {
	data, err := ReadBodyLimited(bytes.NewBufferString("1234"), -1, 4)
	require.NoError(t, err)
	require.Equal(t, []byte("1234"), data)

	_, err = ReadBodyLimited(nil, 0, 4)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrRequestBodyTooLarge))
}

func TestGetAnonymousRequestBodyLimitBytesHonorsPositiveValue(t *testing.T) {
	previous := constant.AnonymousRequestBodyLimitKB
	t.Cleanup(func() { constant.AnonymousRequestBodyLimitKB = previous })

	constant.AnonymousRequestBodyLimitKB = 64
	require.Equal(t, int64(64)<<10, GetAnonymousRequestBodyLimitBytes())
}

func TestCopyBodyLimitedRejectsChunkedOversizeAndAcceptsExactLimit(t *testing.T) {
	var dst bytes.Buffer
	n, err := CopyBodyLimited(&dst, bytes.NewBufferString("1234"), 4)
	require.NoError(t, err)
	require.Equal(t, int64(4), n)
	require.Equal(t, "1234", dst.String())

	dst.Reset()
	n, err = CopyBodyLimited(&dst, bytes.NewBufferString("12345"), 4)
	require.ErrorIs(t, err, ErrRequestBodyTooLarge)
	require.Equal(t, int64(5), n)
}

func TestDecodeBase64LimitedChecksExactDecodedLength(t *testing.T) {
	// 1,048,576 and 1,048,577 bytes can have the same padded base64 length;
	// the decoded-length check must still reject the latter.
	payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, (1<<20)+1))
	_, err := DecodeBase64Limited(payload, 1<<20)
	require.Error(t, err)
}

func TestMaxConfiguredByteLimitsFallBackOnOverflow(t *testing.T) {
	previousFile := constant.MaxFileDownloadMB
	previousRequest := constant.MaxRequestBodyMB
	t.Cleanup(func() {
		constant.MaxFileDownloadMB = previousFile
		constant.MaxRequestBodyMB = previousRequest
	})

	constant.MaxFileDownloadMB = int(^uint(0) >> 1)
	constant.MaxRequestBodyMB = int(^uint(0) >> 1)
	require.Equal(t, DefaultMaxFileDownloadBytes, GetMaxFileDownloadBytes())
	require.Equal(t, DefaultMaxRequestBodyBytes, GetMaxRequestBodyBytes())
}
