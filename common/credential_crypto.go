package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// CredentialCiphertextPrefix is deliberately versioned so a future key
// rotation/algorithm can coexist with already persisted credentials.  The
// database stores only this envelope (never the plaintext credential).
const CredentialCiphertextPrefix = "enc:v1:"

var (
	ErrCredentialSecretUnavailable = errors.New("credential encryption secret is not configured")
	ErrCredentialCiphertextInvalid = errors.New("credential ciphertext is invalid")
)

// credentialKey derives a fixed-size AES key from the deployment secret.  A
// domain separator prevents this key from being reused for unrelated HMACs or
// session material while keeping the derivation portable across SQLite,
// MySQL, and PostgreSQL deployments.
func credentialKey() ([]byte, error) {
	secret := strings.TrimSpace(CryptoSecret)
	if secret == "" {
		return nil, ErrCredentialSecretUnavailable
	}
	digest := sha256.Sum256([]byte("newapi-credential-encryption-v1\x00" + secret))
	return digest[:], nil
}

// CredentialEncryptionReady reports whether startup has loaded a stable
// operator-controlled secret.  Before InitEnv runs, package/unit-test
// fixtures are allowed to use the generated process key; production startup
// sets CredentialSecretRuntimeReady and then fails closed when the secret was
// not explicitly configured.
func CredentialEncryptionReady() bool {
	return !CredentialSecretRuntimeReady || CredentialSecretConfigured
}

// CredentialFingerprint returns a deterministic, secret-backed lookup value.
// It is safe to persist/index and is not reversible.  Callers should compare
// the decrypted value after a hash hit before granting access.
func CredentialFingerprint(plaintext string) string {
	secret := strings.TrimSpace(CryptoSecret)
	// CryptoSecret is initialized to a process-local UUID, so this branch is
	// only reachable in a catastrophically uninitialised process.  Returning a
	// deterministic digest still lets callers produce a bounded diagnostic key;
	// write paths reject the missing secret before persisting it.
	if secret == "" {
		digest := sha256.Sum256([]byte("newapi-credential-hash-v1\x00" + plaintext))
		return fmt.Sprintf("%x", digest[:])
	}
	return GenerateHMACWithKey([]byte("newapi-credential-hash-v1\x00"+secret), plaintext)
}

// IsCredentialCiphertext reports whether value uses this package's envelope
// format.  It intentionally does not attempt decryption, allowing model hooks
// to distinguish a legacy plaintext row from a corrupt/new ciphertext row.
func IsCredentialCiphertext(value string) bool {
	return strings.HasPrefix(value, CredentialCiphertextPrefix)
}

// EncryptCredential seals plaintext with AES-256-GCM and a fresh nonce.  The
// returned URL-safe base64 envelope is suitable for TEXT/VARCHAR columns and
// contains no line breaks.
func EncryptCredential(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if !CredentialEncryptionReady() {
		return "", ErrCredentialSecretUnavailable
	}
	key, err := credentialKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return CredentialCiphertextPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// DecryptCredential opens an envelope produced by EncryptCredential.  Legacy
// plaintext is intentionally not accepted here; model compatibility code
// handles it explicitly so accidental plaintext writes cannot be mistaken for
// valid ciphertext.
func DecryptCredential(envelope string) (string, error) {
	if !IsCredentialCiphertext(envelope) {
		return "", ErrCredentialCiphertextInvalid
	}
	if !CredentialEncryptionReady() {
		return "", ErrCredentialSecretUnavailable
	}
	key, err := credentialKey()
	if err != nil {
		return "", err
	}
	encoded := strings.TrimPrefix(envelope, CredentialCiphertextPrefix)
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCredentialCiphertextInvalid, err)
	}
	// Raw (unpadded) base64 decoding accepts non-zero bits in the final
	// partially-filled quantum.  Without a canonicality check, changing the
	// final character can decode to the exact same bytes and silently bypass a
	// tamper check.  Re-encoding makes the envelope representation unique.
	if encoded == "" || base64.RawURLEncoding.EncodeToString(sealed) != encoded {
		return "", ErrCredentialCiphertextInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return "", ErrCredentialCiphertextInvalid
	}
	nonce := sealed[:gcm.NonceSize()]
	plaintext, err := gcm.Open(nil, nonce, sealed[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("%w: authentication failed", ErrCredentialCiphertextInvalid)
	}
	return string(plaintext), nil
}
