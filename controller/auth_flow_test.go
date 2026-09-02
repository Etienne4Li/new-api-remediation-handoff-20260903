package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type authFlowTestOAuthProvider struct {
	exchangeErr     error
	userInfoErr     error
	exchangeCalls   int
	verifierCalls   int
	lastVerifier    string
	lastRedirectURI string
	userInfoCalls   int
}

func (*authFlowTestOAuthProvider) GetName() string { return "Auth Flow Test" }
func (*authFlowTestOAuthProvider) IsEnabled() bool { return true }
func (provider *authFlowTestOAuthProvider) ExchangeToken(context.Context, string, *gin.Context) (*oauth.OAuthToken, error) {
	provider.exchangeCalls++
	if provider.exchangeErr != nil {
		return nil, provider.exchangeErr
	}
	return &oauth.OAuthToken{}, nil
}
func (provider *authFlowTestOAuthProvider) ExchangeTokenWithVerifier(ctx context.Context, code, verifier string, c *gin.Context) (*oauth.OAuthToken, error) {
	provider.verifierCalls++
	provider.lastVerifier = verifier
	provider.lastRedirectURI = oauth.RedirectURIFromContext(ctx)
	return provider.ExchangeToken(ctx, code, c)
}

func TestOAuthFlowBindsRedirectURIToConfiguredBrowserOrigin(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://api.example.com"
	t.Setenv("FRONTEND_BASE_URL", "https://panel.example.com")
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	startRecorder := httptest.NewRecorder()
	startContext, _ := gin.CreateTestContext(startRecorder)
	startContext.Request = httptest.NewRequest(http.MethodPost, "https://api.example.com/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"login"}`))
	startContext.Request.Header.Set("Content-Type", "application/json")
	startContext.Request.Header.Set("Origin", "https://panel.example.com")
	GenerateOAuthCode(startContext)
	require.Equal(t, http.StatusOK, startRecorder.Code)
	var startResponse struct {
		Data struct {
			FlowToken   string `json:"flow_token"`
			RedirectURI string `json:"redirect_uri"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(startRecorder.Body.Bytes(), &startResponse))
	assert.Equal(t, "https://panel.example.com/oauth/auth-flow-test", startResponse.Data.RedirectURI)
	cookies := startRecorder.Result().Cookies()
	require.Len(t, cookies, 1)

	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	callback := httptest.NewRecorder()
	router.ServeHTTP(callback, authFlowTestCallbackRequest(startResponse.Data.FlowToken, "&code=test", cookies[0]))
	assert.Equal(t, http.StatusOK, callback.Code)
	assert.Equal(t, startResponse.Data.RedirectURI, provider.lastRedirectURI)
}

func TestGenerateOAuthCodeRejectsUnconfiguredRedirectOrigin(t *testing.T) {
	setupAuthFlowControllerTest(t)
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://api.example.com"
	t.Setenv("FRONTEND_BASE_URL", "https://panel.example.com")
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "https://api.example.com/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"login"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Origin", "https://evil.example.com")
	GenerateOAuthCode(c)

	assert.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	var count int64
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestOAuthFlowUsesAllowlistedRefererWhenOriginIsOmitted(t *testing.T) {
	setupAuthFlowControllerTest(t)
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://api.example.com"
	t.Setenv("FRONTEND_BASE_URL", "https://panel.example.com")
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "https://api.example.com/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"login"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Referer", "https://panel.example.com/sign-in")
	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			RedirectURI string `json:"redirect_uri"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "https://panel.example.com/oauth/auth-flow-test", response.Data.RedirectURI)
}
func (provider *authFlowTestOAuthProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	provider.userInfoCalls++
	if provider.userInfoErr != nil {
		return nil, provider.userInfoErr
	}
	return &oauth.OAuthUser{ProviderUserID: "external-user"}, nil
}
func (*authFlowTestOAuthProvider) IsUserIDTaken(string) bool                      { return false }
func (*authFlowTestOAuthProvider) FillUserByProviderID(*model.User, string) error { return nil }
func (*authFlowTestOAuthProvider) SetProviderUserID(*model.User, string)          {}
func (*authFlowTestOAuthProvider) GetProviderPrefix() string                      { return "flow_" }
func (*authFlowTestOAuthProvider) ProviderUserIDColumn() string                   { return "" }

type authFlowTestLoginFlow struct {
	state  string
	cookie *http.Cookie
}

func setupAuthFlowControllerTest(t *testing.T) *authFlowTestOAuthProvider {
	t.Helper()
	// HandleOAuth renders a translated state error for browser-binding
	// failures.  Initialize the bundle so those negative-path tests exercise
	// the handler rather than the test process's uninitialized global.
	require.NoError(t, i18n.Init())
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AuthFlow{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	provider := &authFlowTestOAuthProvider{}
	oauth.Register("auth-flow-test", provider)
	t.Cleanup(func() {
		oauth.Unregister("auth-flow-test")
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
	return provider
}

func createAuthFlowTestLogin(t *testing.T, payload string) authFlowTestLoginFlow {
	t.Helper()
	cookieValue, err := newOAuthFlowCookieValue()
	require.NoError(t, err)
	state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  "auth-flow-test",
		Intent:    model.AuthFlowIntentLogin,
		SessionId: oauthFlowCookieHash(cookieValue),
		Payload:   payload,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	return authFlowTestLoginFlow{
		state: state,
		cookie: &http.Cookie{
			Name:  oauthFlowCookieName(state),
			Value: cookieValue,
		},
	}
}

func authFlowTestCallbackRequest(state, query string, cookies ...*http.Cookie) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+state+query, nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	return request
}

func requireOAuthFlowCookieCleared(t *testing.T, recorder *httptest.ResponseRecorder, state string) {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == oauthFlowCookieName(state) {
			assert.Empty(t, cookie.Value)
			assert.Equal(t, -1, cookie.MaxAge)
			assert.Equal(t, oauthFlowCookiePath, cookie.Path)
			assert.True(t, cookie.HttpOnly)
			assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
			return
		}
	}
	require.Fail(t, "OAuth flow cookie was not cleared")
}

func TestGenerateOAuthCodeCarriesAffiliateInLoginFlow(t *testing.T) {
	setupAuthFlowControllerTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"login","aff":"invite-code"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
	})
	require.NoError(t, err)
	var payload oauthFlowPayload
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
	assert.Equal(t, "invite-code", payload.AffiliateCode)
	assert.Zero(t, flow.UserId)
	assert.Len(t, flow.SessionId, 64)
	cookies := recorder.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, oauthFlowCookieName(response.Data.FlowToken), cookies[0].Name)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
	assert.Equal(t, common.SessionCookieSecure, cookies[0].Secure)
	assert.Equal(t, oauthFlowCookiePath, cookies[0].Path)
	assert.NotEqual(t, cookies[0].Value, flow.SessionId)
	assert.True(t, oauthFlowCookieMatches(flow.SessionId, cookies[0].Value))
}

func TestGenerateOAuthCodeReturnsPKCEChallengeAndCallbackUsesVerifier(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"login"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	GenerateOAuthCode(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken           string `json:"flow_token"`
			CodeChallenge       string `json:"code_challenge"`
			CodeChallengeMethod string `json:"code_challenge_method"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.NotEmpty(t, response.Data.FlowToken)
	require.NotEmpty(t, response.Data.CodeChallenge)
	assert.Equal(t, "S256", response.Data.CodeChallengeMethod)
	cookies := recorder.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, response.Data.CodeChallenge, oauthCodeChallenge(cookies[0].Value))

	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	callback := httptest.NewRecorder()
	router.ServeHTTP(callback, authFlowTestCallbackRequest(response.Data.FlowToken, "&code=test", cookies[0]))
	assert.Equal(t, http.StatusOK, callback.Code)
	assert.Equal(t, 1, provider.verifierCalls)
	assert.Equal(t, cookies[0].Value, provider.lastVerifier)
}

func TestGenerateOAuthCodeBindReturnsSessionDerivedPKCEChallenge(t *testing.T) {
	setupAuthFlowControllerTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"bind"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 42)
	c.Set("session_id", "session-42")
	c.Set("auth_version", int64(3))
	c.Set("session_version", int64(2))

	GenerateOAuthCode(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			FlowToken     string `json:"flow_token"`
			CodeChallenge string `json:"code_challenge"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotEmpty(t, response.Data.FlowToken)
	require.NotEmpty(t, response.Data.CodeChallenge)
	assert.Empty(t, recorder.Result().Cookies())
	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  "auth-flow-test",
		Intent:    model.AuthFlowIntentBind,
		UserId:    42,
		SessionId: "session-42",
	})
	require.NoError(t, err)
	verifier := oauthBindCodeVerifier(response.Data.FlowToken, "session-42")
	assert.Equal(t, response.Data.CodeChallenge, oauthCodeChallenge(verifier))
	assert.Equal(t, oauthCodeVerifierHash(verifier), flow.CodeVerifierHash)
}

func TestGenerateOAuthCodeBindsFlowToAuthenticatedSession(t *testing.T) {
	setupAuthFlowControllerTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"bind"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 42)
	c.Set("session_id", "session-42")
	c.Set("auth_version", int64(3))
	c.Set("session_version", int64(2))

	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42",
	})
	require.NoError(t, err)
	assert.Equal(t, 42, flow.UserId)
	assert.Equal(t, "session-42", flow.SessionId)
	assert.Empty(t, recorder.Result().Cookies())
}

func TestOAuthLoginConsumesFlowOnlyAfterProviderIdentity(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)

	tests := []struct {
		name        string
		exchangeErr error
		userInfoErr error
	}{
		{name: "exchange failure", exchangeErr: errors.New("exchange failed")},
		{name: "user info failure", userInfoErr: errors.New("user info failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider.exchangeErr = test.exchangeErr
			provider.userInfoErr = test.userInfoErr
			flow := createAuthFlowTestLogin(t, `{}`)

			router := gin.New()
			router.GET("/api/oauth/:provider", HandleOAuth)
			request := authFlowTestCallbackRequest(flow.state, "&code=test", flow.cookie)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			storedFlow, err := model.GetAuthFlow(flow.state, model.AuthFlowMatch{
				Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
			})
			require.NoError(t, err)
			assert.Nil(t, storedFlow.ConsumedAt)
			assert.Empty(t, response.Result().Cookies())
		})
	}
}

func TestOAuthLoginConsumesFlowAfterProviderIdentityAndOnProviderError(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)

	provider.exchangeErr = nil
	provider.userInfoErr = nil
	successFlow := createAuthFlowTestLogin(t, `{invalid`)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := authFlowTestCallbackRequest(successFlow.state, "&code=test", successFlow.cookie)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_, err := model.GetAuthFlow(successFlow.state, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	requireOAuthFlowCookieCleared(t, response, successFlow.state)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, 1, provider.userInfoCalls)

	providerErrorFlow := createAuthFlowTestLogin(t, `{}`)
	request = authFlowTestCallbackRequest(providerErrorFlow.state, "&error=access_denied", providerErrorFlow.cookie)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_, err = model.GetAuthFlow(providerErrorFlow.state, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	requireOAuthFlowCookieCleared(t, response, providerErrorFlow.state)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, 1, provider.userInfoCalls)
}

func TestOAuthLoginRequiresMatchingBrowserCookie(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)

	tests := []struct {
		name             string
		cookie           *http.Cookie
		wantStatus       int
		wantConsumed     bool
		wantExchangeCall int
	}{
		{name: "missing cookie", wantStatus: http.StatusForbidden},
		{name: "wrong cookie", cookie: &http.Cookie{Value: "wrong"}, wantStatus: http.StatusForbidden},
		{name: "matching cookie", wantStatus: http.StatusOK, wantConsumed: true, wantExchangeCall: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider.exchangeErr = nil
			provider.userInfoErr = nil
			provider.exchangeCalls = 0
			provider.userInfoCalls = 0
			flow := createAuthFlowTestLogin(t, `{invalid`)
			var cookies []*http.Cookie
			switch test.name {
			case "wrong cookie":
				cookies = []*http.Cookie{{Name: flow.cookie.Name, Value: test.cookie.Value}}
			case "matching cookie":
				cookies = []*http.Cookie{flow.cookie}
			}
			request := authFlowTestCallbackRequest(flow.state, "&code=test", cookies...)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			assert.Equal(t, test.wantStatus, response.Code)
			storedFlow, err := model.GetAuthFlow(flow.state, model.AuthFlowMatch{
				Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
			})
			if test.wantConsumed {
				assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
				requireOAuthFlowCookieCleared(t, response, flow.state)
			} else {
				require.NoError(t, err)
				assert.Nil(t, storedFlow.ConsumedAt)
				assert.Empty(t, response.Result().Cookies())
			}
			assert.Equal(t, test.wantExchangeCall, provider.exchangeCalls)
			assert.Equal(t, test.wantExchangeCall, provider.userInfoCalls)
		})
	}
}

func TestOAuthLoginFlowsUseIndependentCookiesAcrossTabs(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	first := createAuthFlowTestLogin(t, `{invalid`)
	second := createAuthFlowTestLogin(t, `{invalid`)
	require.NotEqual(t, first.cookie.Name, second.cookie.Name)

	// A callback carrying the other tab's cookie cannot invoke the provider or
	// consume either flow.
	response := httptest.NewRecorder()
	router.ServeHTTP(response, authFlowTestCallbackRequest(first.state, "&code=test", second.cookie))
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Zero(t, provider.exchangeCalls)
	_, err := model.GetAuthFlow(first.state, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	require.NoError(t, err)

	// Each tab can then complete with its own nonce, independently.
	response = httptest.NewRecorder()
	router.ServeHTTP(response, authFlowTestCallbackRequest(first.state, "&code=test", first.cookie))
	assert.Equal(t, http.StatusOK, response.Code)
	requireOAuthFlowCookieCleared(t, response, first.state)
	assert.Equal(t, 1, provider.exchangeCalls)

	response = httptest.NewRecorder()
	router.ServeHTTP(response, authFlowTestCallbackRequest(second.state, "&code=test", second.cookie))
	assert.Equal(t, http.StatusOK, response.Code)
	requireOAuthFlowCookieCleared(t, response, second.state)
	assert.Equal(t, 2, provider.exchangeCalls)
}

func TestOAuthLoginRejectsLegacyUnboundFlow(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  "auth-flow-test",
		Intent:    model.AuthFlowIntentLogin,
		Payload:   `{}`,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	request := authFlowTestCallbackRequest(state, "&code=test", &http.Cookie{
		Name: oauthFlowCookieName(state), Value: "arbitrary",
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Zero(t, provider.exchangeCalls)
	_, err = model.GetAuthFlow(state, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	require.NoError(t, err)
}

func TestOAuthLoginReplayClearsBrowserCookie(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	flow := createAuthFlowTestLogin(t, `{invalid`)

	firstResponse := httptest.NewRecorder()
	router.ServeHTTP(firstResponse, authFlowTestCallbackRequest(flow.state, "&code=test", flow.cookie))
	requireOAuthFlowCookieCleared(t, firstResponse, flow.state)
	require.Equal(t, 1, provider.exchangeCalls)

	// A replayed state is rejected before provider exchange, but the stale
	// browser nonce is retired too so it cannot linger in the client.
	secondResponse := httptest.NewRecorder()
	router.ServeHTTP(secondResponse, authFlowTestCallbackRequest(flow.state, "&code=test", flow.cookie))
	assert.Equal(t, http.StatusForbidden, secondResponse.Code)
	requireOAuthFlowCookieCleared(t, secondResponse, flow.state)
	assert.Equal(t, 1, provider.exchangeCalls)
}

func TestGenerateOAuthCodeUsesSecureLaxCookieSetting(t *testing.T) {
	setupAuthFlowControllerTest(t)
	previousSecure := common.SessionCookieSecure
	common.SessionCookieSecure = true
	t.Cleanup(func() { common.SessionCookieSecure = previousSecure })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"login"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	cookies := recorder.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.True(t, cookies[0].Secure)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
}

func TestOAuthBindProviderErrorConsumesSessionBoundFlow(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	flowToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42", Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("session_id", "session-42")
		c.Set("auth_version", int64(1))
		c.Set("session_version", int64(1))
		c.Next()
	})
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+flowToken+"&error=access_denied&error_description=cancelled", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	_, err = model.GetAuthFlow(flowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Zero(t, provider.exchangeCalls)
	assert.Zero(t, provider.userInfoCalls)
}
