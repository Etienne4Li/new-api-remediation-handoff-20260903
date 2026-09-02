package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStatusReturnsEffectiveOIDCDisplayName(t *testing.T) {
	originalDisplayName := system_setting.GetOIDCSettings().DisplayName
	originalOptionMap := common.OptionMap
	t.Cleanup(func() {
		_ = config.GlobalConfig.LoadFromDB(map[string]string{"oidc.display_name": originalDisplayName})
		common.OptionMap = originalOptionMap
	})
	common.OptionMap = map[string]string{}

	tests := []struct {
		name        string
		displayName string
		want        string
	}{
		{
			name:        "custom name is trimmed",
			displayName: "  Acme SSO  ",
			want:        "Acme SSO",
		},
		{
			name:        "whitespace-only name falls back",
			displayName: "   ",
			want:        "OIDC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"oidc.display_name": tt.displayName}))
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)

			GetStatus(context)

			var payload struct {
				Success bool           `json:"success"`
				Data    map[string]any `json:"data"`
			}
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			require.True(t, payload.Success)
			assert.Equal(t, tt.want, payload.Data["oidc_display_name"])
		})
	}
}
