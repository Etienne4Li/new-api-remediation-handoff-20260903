package common

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialEncryptionRoundTripAndRandomNonce(t *testing.T) {
	previousSecret := CryptoSecret
	previousConfigured := CredentialSecretConfigured
	previousReady := CredentialSecretRuntimeReady
	t.Cleanup(func() {
		CryptoSecret = previousSecret
		CredentialSecretConfigured = previousConfigured
		CredentialSecretRuntimeReady = previousReady
	})
	CryptoSecret = "credential-test-secret"
	CredentialSecretConfigured = true
	CredentialSecretRuntimeReady = true

	plaintext := "{" + `"access_token":"secret value","account_id":"acct"` + "}"
	first, err := EncryptCredential(plaintext)
	require.NoError(t, err)
	second, err := EncryptCredential(plaintext)
	require.NoError(t, err)
	assert.True(t, IsCredentialCiphertext(first))
	assert.NotEqual(t, first, second, "GCM must use a fresh nonce")
	decoded, err := DecryptCredential(first)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decoded)
	assert.NotContains(t, first, plaintext)
}

func TestCredentialDecryptionRejectsTamperingAndLegacyPlaintext(t *testing.T) {
	previousSecret := CryptoSecret
	previousConfigured := CredentialSecretConfigured
	previousReady := CredentialSecretRuntimeReady
	t.Cleanup(func() {
		CryptoSecret = previousSecret
		CredentialSecretConfigured = previousConfigured
		CredentialSecretRuntimeReady = previousReady
	})
	CryptoSecret = "credential-test-secret"
	CredentialSecretConfigured = true
	CredentialSecretRuntimeReady = true

	ciphertext, err := EncryptCredential("secret")
	require.NoError(t, err)
	last := ciphertext[len(ciphertext)-1]
	replacement := byte('A')
	if last == replacement {
		replacement = 'B'
	}
	tampered := ciphertext[:len(ciphertext)-1] + string(replacement)
	_, err = DecryptCredential(tampered)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialCiphertextInvalid))

	_, err = DecryptCredential("legacy-secret")
	assert.ErrorIs(t, err, ErrCredentialCiphertextInvalid)
}

func TestCredentialEncryptionFailsClosedAfterStartupWithoutStableSecret(t *testing.T) {
	previousSecret := CryptoSecret
	previousConfigured := CredentialSecretConfigured
	previousReady := CredentialSecretRuntimeReady
	t.Cleanup(func() {
		CryptoSecret = previousSecret
		CredentialSecretConfigured = previousConfigured
		CredentialSecretRuntimeReady = previousReady
	})
	CryptoSecret = "process-only-secret"
	CredentialSecretConfigured = false
	CredentialSecretRuntimeReady = true

	_, err := EncryptCredential("secret")
	assert.ErrorIs(t, err, ErrCredentialSecretUnavailable)
	fingerprint := CredentialFingerprint("secret")
	assert.Len(t, fingerprint, 64)
	assert.NotEqual(t, "secret", fingerprint)
}
