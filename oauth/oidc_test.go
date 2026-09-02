package oauth

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOIDCProvider_GetName(t *testing.T) {
	originalDisplayName := system_setting.GetOIDCSettings().DisplayName
	t.Cleanup(func() {
		_ = config.GlobalConfig.LoadFromDB(map[string]string{"oidc.display_name": originalDisplayName})
	})

	p := &OIDCProvider{}

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"oidc.display_name": ""}))
	assert.Equal(t, "OIDC", p.GetName())

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"oidc.display_name": "  Acme SSO  "}))
	assert.Equal(t, "Acme SSO", p.GetName())
}
