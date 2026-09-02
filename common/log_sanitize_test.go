package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSensitiveLogMetaDoesNotRevealValue(t *testing.T) {
	secret := "whsec_test_signature_123456"
	meta := SensitiveLogMeta(secret)
	assert.NotContains(t, meta, secret)
	assert.NotContains(t, meta, "whsec")
	assert.Contains(t, meta, "len=")
	assert.Contains(t, meta, "hash=")
	assert.Equal(t, meta, SensitiveLogMeta(secret))
	assert.Equal(t, "empty", SensitiveLogMeta(""))
}

func TestSensitiveLogBodyUsesHashAndLengthOnly(t *testing.T) {
	body := []byte(`{"customer":{"email":"alice@example.com"},"signature":"secret"}`)
	meta := SensitiveLogBody(body)
	assert.NotContains(t, meta, string(body))
	assert.Contains(t, meta, "len=")
	assert.Contains(t, meta, "hash=")
}

func TestSanitizeRequestURIForLogMasksSensitiveQueries(t *testing.T) {
	raw := "/api/user/epay/notify?pid=merchant&key=secret-key&sign=signature&model=gpt-test&token=abc"
	got := SanitizeRequestURIForLog(raw)
	require.NotEqual(t, raw, got)
	assert.True(t, strings.HasPrefix(got, "/api/user/epay/notify?"))
	assert.NotContains(t, got, "secret-key")
	assert.NotContains(t, got, "signature")
	assert.NotContains(t, got, "abc")
	assert.Contains(t, got, "pid=merchant")
	assert.Contains(t, got, "model=gpt-test")
}

func TestSanitizeRequestURIForLogRejectsMalformedInputWithoutEcho(t *testing.T) {
	assert.Equal(t, "<invalid-uri>", SanitizeRequestURIForLog("/%zz?key=secret"))
	assert.Equal(t, "", SanitizeRequestURIForLog(""))
}
