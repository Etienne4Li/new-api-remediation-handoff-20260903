package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// sealProviderRuntimeValue encrypts provider data that must remain replayable
// after a restart, such as a signed media URL. Public response projections
// redact these values separately; destroying credentials here would break the
// authenticated media proxy.
func sealProviderRuntimeValue(field, value string, maxBytes int) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if common.IsCredentialCiphertext(trimmed) {
		plaintext, err := openProviderRuntimeValue(field, trimmed, maxBytes)
		if err != nil {
			return "", err
		}
		if plaintext == "" {
			return "", fmt.Errorf("%w: %s envelope is empty", ErrCredentialStorageCorrupt, field)
		}
		return trimmed, nil
	}
	if strings.HasPrefix(trimmed, "enc:") {
		return "", fmt.Errorf("%w: unsupported %s envelope", ErrCredentialStorageCorrupt, field)
	}
	if maxBytes <= 0 || len([]byte(value)) > maxBytes {
		return "", fmt.Errorf("%s exceeds storage limit", field)
	}
	if !common.CredentialEncryptionReady() {
		return "", fmt.Errorf("%w: %s", ErrCredentialStorageUnavailable, field)
	}
	sealed, err := common.EncryptCredential(value)
	if err != nil {
		if errors.Is(err, common.ErrCredentialSecretUnavailable) {
			return "", fmt.Errorf("%w: encrypt %s: %v", ErrCredentialStorageUnavailable, field, err)
		}
		return "", fmt.Errorf("encrypt %s: %w", field, err)
	}
	return sealed, nil
}

// openProviderRuntimeValue keeps legacy plaintext readable during a rolling
// upgrade while failing closed for corrupt or unknown encrypted envelopes.
func openProviderRuntimeValue(field, value string, maxBytes int) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if !common.IsCredentialCiphertext(trimmed) {
		if strings.HasPrefix(trimmed, "enc:") {
			return "", fmt.Errorf("%w: unsupported %s envelope", ErrCredentialStorageCorrupt, field)
		}
		if maxBytes <= 0 || len([]byte(value)) > maxBytes {
			return "", fmt.Errorf("%w: %s exceeds storage limit", ErrCredentialStorageCorrupt, field)
		}
		return value, nil
	}
	plaintext, err := common.DecryptCredential(trimmed)
	if err != nil {
		if errors.Is(err, common.ErrCredentialSecretUnavailable) {
			return "", fmt.Errorf("%w: decrypt %s: %v", ErrCredentialStorageUnavailable, field, err)
		}
		return "", fmt.Errorf("%w: decrypt %s: %v", ErrCredentialStorageCorrupt, field, err)
	}
	if maxBytes <= 0 || len([]byte(plaintext)) > maxBytes {
		return "", fmt.Errorf("%w: decrypted %s exceeds storage limit", ErrCredentialStorageCorrupt, field)
	}
	return plaintext, nil
}
