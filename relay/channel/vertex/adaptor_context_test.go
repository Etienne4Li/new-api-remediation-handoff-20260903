package vertex

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestAcquireAccessTokenWithContextCancelsBeforeNetwork(t *testing.T) {
	service.InitHttpClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	creds := Credentials{
		ProjectID:   "test-project",
		ClientEmail: "test@example.invalid",
		PrivateKey:  testPrivateKeyPEM(t),
	}
	_, err := AcquireAccessTokenWithContext(ctx, creds, "")
	// The canceled context must stop the OAuth exchange before any request can
	// block on DNS/network. The operation ID is intentionally valid so the
	// cancellation assertion reaches the exchange path.
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "expected context cancellation, got %v", err)
}
