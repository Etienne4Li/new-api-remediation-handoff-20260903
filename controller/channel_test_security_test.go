package controller

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestDetectErrorFromTestResponseBodyDoesNotExposeProviderMessage(t *testing.T) {
	secret := "provider-secret-and-possibly-a-signed-url"
	err := detectErrorFromTestResponseBody([]byte(`{"error":{"message":"` + secret + `"}}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
	require.Contains(t, err.Error(), "message_meta=")
	require.Contains(t, err.Error(), "len=")
	require.Equal(t, "upstream error: message_meta="+common.SensitiveLogMeta(secret), err.Error())
}

func TestDetectErrorFromTestResponseBodySSEDoesNotExposeProviderMessage(t *testing.T) {
	secret := "sse-provider-secret"
	err := detectErrorFromTestResponseBody([]byte("data: {\"error\":{\"message\":\"" + secret + "\"}}\n\ndata: [DONE]\n"))
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
}

func TestChannelTestSafeErrorMessageMasksTransportCredentials(t *testing.T) {
	secret := "eyJhbGciOiJIUzI1NiJ9.payload.signature"
	err := errors.New("request failed Authorization: Bearer " + secret)
	message := channelTestSafeErrorMessage(err)
	require.NotContains(t, message, secret)
	require.Contains(t, message, "Bearer ***")
}
