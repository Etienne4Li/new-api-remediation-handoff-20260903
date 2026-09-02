package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadProviderResponseBodyRejectsDeclaredOversize(t *testing.T) {
	resp := &http.Response{
		ContentLength: DefaultProviderResponseBodyLimitBytes + 1,
		Body:          io.NopCloser(strings.NewReader("small")),
	}
	_, err := ReadProviderResponseBody(resp, 128)
	require.ErrorIs(t, err, ErrProviderResponseBodyTooLarge)
}

func TestReadProviderResponseBodyRejectsUnknownLengthOversize(t *testing.T) {
	resp := &http.Response{
		ContentLength: -1,
		Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", 129))),
	}
	_, err := ReadProviderResponseBody(resp, 128)
	require.ErrorIs(t, err, ErrProviderResponseBodyTooLarge)
}

func TestReadProviderResponseBodyAcceptsLimitAndNilErrors(t *testing.T) {
	resp := &http.Response{ContentLength: 3, Body: io.NopCloser(strings.NewReader("ok!"))}
	body, err := ReadProviderResponseBody(resp, 3)
	require.NoError(t, err)
	require.Equal(t, []byte("ok!"), body)

	_, err = ReadProviderResponseBody(nil, 3)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrProviderResponseBodyTooLarge))
}
