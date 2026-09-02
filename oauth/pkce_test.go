package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenericOAuthProviderSendsPKCEVerifier(t *testing.T) {
	type requestCapture struct {
		method string
		form   url.Values
	}
	captured := make(chan requestCapture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		captured <- requestCapture{method: r.Method, form: r.PostForm}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-token","token_type":"Bearer"}`))
	}))
	defer server.Close()

	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://dashboard.example.com"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	provider := NewGenericOAuthProvider(&model.CustomOAuthProvider{
		Name:                  "Test Provider",
		Slug:                  "test-provider",
		ClientId:              "client-id",
		ClientSecret:          "client-secret",
		TokenEndpoint:         server.URL,
		UserInfoEndpoint:      server.URL,
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		AuthStyle:             AuthStyleInParams,
	})

	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	const verifier = "flow-cookie-verifier"
	_, err := provider.ExchangeTokenWithVerifier(context.Background(), "authorization-code", verifier, ginContext)
	require.NoError(t, err)

	request := <-captured
	assert.Equal(t, http.MethodPost, request.method)
	assert.Equal(t, "authorization-code", request.form.Get("code"))
	assert.Equal(t, verifier, request.form.Get("code_verifier"))
	assert.Equal(t, "https://dashboard.example.com/oauth/test-provider", request.form.Get("redirect_uri"))
}

func TestGenericOAuthProviderUsesFlowBoundRedirectURI(t *testing.T) {
	captured := make(chan url.Values, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		captured <- r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-token"}`))
	}))
	defer server.Close()

	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://api.example.com"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	provider := NewGenericOAuthProvider(&model.CustomOAuthProvider{
		Name:                  "Test Provider",
		Slug:                  "test-provider",
		ClientId:              "client-id",
		ClientSecret:          "client-secret",
		TokenEndpoint:         server.URL,
		UserInfoEndpoint:      server.URL,
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		AuthStyle:             AuthStyleInParams,
	})
	ctx := WithRedirectURI(context.Background(), "https://panel.example.com/oauth/test-provider")
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	_, err := provider.ExchangeTokenWithVerifier(ctx, "authorization-code", "verifier", ginContext)
	require.NoError(t, err)
	request := <-captured
	assert.Equal(t, "https://panel.example.com/oauth/test-provider", request.Get("redirect_uri"))
}

func TestGenericOAuthProviderLegacyExchangeOmitsPKCEVerifier(t *testing.T) {
	captured := make(chan url.Values, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		captured <- r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-token"}`))
	}))
	defer server.Close()

	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://dashboard.example.com"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	provider := NewGenericOAuthProvider(&model.CustomOAuthProvider{
		Name:                  "Test Provider",
		Slug:                  "test-provider",
		ClientId:              "client-id",
		ClientSecret:          "client-secret",
		TokenEndpoint:         server.URL,
		UserInfoEndpoint:      server.URL,
		AuthorizationEndpoint: "https://idp.example.com/authorize",
	})
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	_, err := provider.ExchangeToken(context.Background(), "authorization-code", ginContext)
	require.NoError(t, err)
	request := <-captured
	assert.Empty(t, request.Get("code_verifier"))
}

func TestLinuxDOProviderUsesFrontendRedirectAndPKCE(t *testing.T) {
	captured := make(chan url.Values, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		captured <- r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-token"}`))
	}))
	defer server.Close()
	t.Setenv("LINUX_DO_TOKEN_ENDPOINT", server.URL)

	previousClientID := common.LinuxDOClientId
	previousClientSecret := common.LinuxDOClientSecret
	common.LinuxDOClientId = "linux-client"
	common.LinuxDOClientSecret = "linux-secret"
	t.Cleanup(func() {
		common.LinuxDOClientId = previousClientID
		common.LinuxDOClientSecret = previousClientSecret
	})

	request := httptest.NewRequest(http.MethodGet, "https://panel.example.com/oauth/linuxdo", nil)
	request.Host = "panel.example.com"
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = request
	const verifier = "linuxdo-verifier"
	_, err := (&LinuxDOProvider{}).ExchangeTokenWithVerifier(context.Background(), "authorization-code", verifier, ginContext)
	require.NoError(t, err)

	form := <-captured
	assert.Equal(t, verifier, form.Get("code_verifier"))
	assert.Equal(t, "https://panel.example.com/oauth/linuxdo", form.Get("redirect_uri"))
}
