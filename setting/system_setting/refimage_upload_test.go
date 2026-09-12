package system_setting

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clearRefImageUploadEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		RefImageUploadEnabledEnv,
		RefImageUploadEndpointEnv,
		RefImageUploadTokenEnv,
		RefImageUploadTokenFileEnv,
		RefImageUploadTimeoutSecondsEnv,
	} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	ResetRefImageUploadConfigCache()
	t.Cleanup(ResetRefImageUploadConfigCache)
}

func TestLoadRefImageUploadConfigDefaultsToDisabled(t *testing.T) {
	clearRefImageUploadEnv(t)

	config := LoadRefImageUploadConfig()

	assert.False(t, config.Enabled)
	assert.False(t, config.TokenConfigured())
	assert.Equal(t, DefaultRefImageUploadEndpoint, config.Endpoint)
	assert.Equal(t, RefImageUploadPublicPrefix, config.PublicPrefix())
	assert.Equal(t, DefaultRefImageUploadTimeoutSeconds, config.TimeoutSeconds)
}

func TestLoadRefImageUploadConfigReadsEnvironmentCredential(t *testing.T) {
	clearRefImageUploadEnv(t)
	t.Setenv(RefImageUploadEnabledEnv, "true")
	t.Setenv(RefImageUploadTokenEnv, "token-from-env")

	config := LoadRefImageUploadConfig()

	require.True(t, config.Enabled)
	assert.True(t, config.TokenConfigured())
	assert.Equal(t, "env", config.TokenSource())
	assert.Equal(t, "token-from-env", config.Token())
}

func TestLoadRefImageUploadConfigReadsFileCredential(t *testing.T) {
	clearRefImageUploadEnv(t)
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("token-from-file\n"), 0o600))
	t.Setenv(RefImageUploadEnabledEnv, "true")
	t.Setenv(RefImageUploadTokenFileEnv, path)

	config := LoadRefImageUploadConfig()

	require.True(t, config.Enabled)
	assert.Equal(t, "file", config.TokenSource())
	assert.Equal(t, "token-from-file", config.Token())
}

// TestLoadRefImageUploadConfigNeverReturnsCredentialInErrors keeps the token
// out of every logged failure path.
func TestLoadRefImageUploadConfigNeverReturnsCredentialInErrors(t *testing.T) {
	clearRefImageUploadEnv(t)
	t.Setenv(RefImageUploadEnabledEnv, "true")
	t.Setenv(RefImageUploadTokenFileEnv, filepath.Join(t.TempDir(), "missing"))

	config := LoadRefImageUploadConfig()

	assert.False(t, config.Enabled, "an unreadable credential must disable the bridge")
	assert.Empty(t, config.Token())
}

func TestLoadRefImageUploadConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		timeout  string
	}{
		{name: "relative endpoint", endpoint: "/api/upload"},
		{name: "userinfo in endpoint", endpoint: "http://user:pass@host/api/upload"},
		{name: "unsupported scheme", endpoint: "ftp://host/api/upload"},
		{name: "fragment in endpoint", endpoint: "http://host/api/upload#x"},
		{name: "padded endpoint", endpoint: " http://host/api/upload"},
		{name: "zero timeout", endpoint: DefaultRefImageUploadEndpoint, timeout: "0"},
		{name: "over max timeout", endpoint: DefaultRefImageUploadEndpoint, timeout: "1000"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			clearRefImageUploadEnv(t)
			t.Setenv(RefImageUploadEnabledEnv, "true")
			t.Setenv(RefImageUploadTokenEnv, "token")
			t.Setenv(RefImageUploadEndpointEnv, testCase.endpoint)
			if testCase.timeout != "" {
				t.Setenv(RefImageUploadTimeoutSecondsEnv, testCase.timeout)
			}

			config := LoadRefImageUploadConfig()

			assert.False(t, config.Enabled)
		})
	}
}

func TestValidateRefImageUploadConfigAcceptsDefaultEndpoint(t *testing.T) {
	config := RefImageUploadConfig{
		Enabled:        true,
		Endpoint:       DefaultRefImageUploadEndpoint,
		TimeoutSeconds: DefaultRefImageUploadTimeoutSeconds,
	}
	assert.NoError(t, ValidateRefImageUploadConfig(config))
}

func TestRefImageUploadCredentialRefNamesBothSources(t *testing.T) {
	ref := RefImageUploadCredentialRef()
	assert.Contains(t, ref, "env:"+RefImageUploadTokenEnv)
	assert.Contains(t, ref, "file:"+RefImageUploadTokenFileEnv)
}
