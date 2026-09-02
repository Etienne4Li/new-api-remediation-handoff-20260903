package kitutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaskSensitiveInfoRedactsBearerAndSecretFields(t *testing.T) {
	secret := "super-secret-token-value"
	input := `request failed Authorization: Bearer ` + secret + ` body={"api_key":"` + secret + `","access_token":"` + secret + `"}`

	got := MaskSensitiveInfo(input)
	require.NotContains(t, got, secret)
	require.Contains(t, got, "Bearer ***")
	require.Contains(t, got, `"api_key":"***"`)
	require.Contains(t, got, `"access_token":"***"`)
}

func TestMaskSensitiveInfoKeepsNonSecretValues(t *testing.T) {
	got := MaskSensitiveInfo(`model=gpt-test tokenization failed`)
	require.Contains(t, got, "model=gpt-test")
	require.Contains(t, got, "tokenization failed")
}

func TestMaskSensitiveInfoRedactsDottedAndBasicCredentials(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.payload.signature"
	basic := "dXNlcjpwYXNz"
	input := "Authorization: Bearer " + jwt + "\nProxy-Authorization: Basic " + basic

	got := MaskSensitiveInfo(input)
	require.NotContains(t, got, jwt)
	require.NotContains(t, got, basic)
	require.Contains(t, got, "Authorization: Bearer ***")
	require.Contains(t, got, "Proxy-Authorization: Basic ***")
}
