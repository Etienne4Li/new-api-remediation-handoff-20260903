package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func testChannelResponseContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return ctx
}

func TestChannelResponseForOrdinaryAdminRedactsCredentialBearingFields(t *testing.T) {
	baseURL := "https://user:password@provider.example/v1?api_key=top-secret"
	setting := `{"proxy":"http://proxy-user:proxy-secret@proxy.example:8080"}`
	paramOverride := `{"api_key":"param-secret"}`
	headerOverride := `{"Authorization":"Bearer header-secret"}`
	channel := &model.Channel{
		Id:             11,
		Key:            "channel-secret",
		BaseURL:        &baseURL,
		Setting:        &setting,
		ParamOverride:  &paramOverride,
		HeaderOverride: &headerOverride,
		OtherSettings:  `{"advanced_custom":{"advanced_routes":[{"auth":{"type":"header","name":"X-Key","value":"route-secret"}}]}}`,
		OtherInfo:      `{"provider_token":"other-secret"}`,
	}

	projected := channelResponseForActor(testChannelResponseContext(), channel)
	require.NotNil(t, projected)
	require.Empty(t, projected.Key)
	require.Nil(t, projected.BaseURL)
	require.Nil(t, projected.Setting)
	require.Nil(t, projected.ParamOverride)
	require.Nil(t, projected.HeaderOverride)
	require.Equal(t, "{}", projected.OtherSettings)
	require.Empty(t, projected.OtherInfo)

	body, err := common.Marshal(projected)
	require.NoError(t, err)
	require.NotContains(t, string(body), "channel-secret")
	require.NotContains(t, string(body), "proxy-secret")
	require.NotContains(t, string(body), "route-secret")
}

func TestChannelResponseForSensitiveAdminKeepsConfigurationButNotKey(t *testing.T) {
	baseURL := "https://provider.example/v1"
	setting := `{"proxy":"http://proxy.example:8080"}`
	channel := &model.Channel{Key: "channel-secret", BaseURL: &baseURL, Setting: &setting}
	ctx := testChannelResponseContext()
	ctx.Set("id", 1)
	ctx.Set("role", common.RoleRootUser)

	projected := channelResponseForActor(ctx, channel)
	require.NotNil(t, projected)
	require.Empty(t, projected.Key)
	require.Equal(t, channel.BaseURL, projected.BaseURL)
	require.Equal(t, channel.Setting, projected.Setting)
}

func TestChannelKeyPreviewDoesNotExposePrefix(t *testing.T) {
	secret := "sk-provider-secret"
	preview := channelKeyPreview(secret)
	require.NotEmpty(t, preview)
	require.NotContains(t, preview, "sk-provider")
	require.NotContains(t, preview, secret)
	require.Contains(t, preview, "hash:")
}
