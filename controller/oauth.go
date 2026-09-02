package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const oauthAuthFlowTTL = 10 * time.Minute

// OAuth login flows are bound to a short-lived, browser-only nonce.  The
// cookie name is derived from the state token so several login tabs can be
// active at once without overwriting one another's nonce.
const (
	oauthFlowCookiePrefix = "new_api_oauth_flow_"
	oauthFlowCookiePath   = "/api/oauth"
	oauthFlowCookieBytes  = 32
)

type oauthStateRequest struct {
	Provider string `json:"provider"`
	Intent   string `json:"intent"`
	Aff      string `json:"aff,omitempty"`
}

type oauthFlowPayload struct {
	AffiliateCode string `json:"affiliate_code,omitempty"`
}

// oauthRedirectOrigin returns the browser origin that is safe to use for an
// OAuth callback.  The value is selected once, when the flow is created, and
// persisted with that flow so token exchange never re-reads a mutable global
// ServerAddress.  An Origin header is accepted only when it belongs to an
// explicitly configured origin; this prevents a forged Host/Origin from
// turning the callback into an arbitrary external URL.
func oauthRedirectOrigin(c *gin.Context) (string, error) {
	allowed := make(map[string]struct{})
	configuredOrder := make([]string, 0, 4)
	cookieConfig := common.GetSessionCookieConfig()
	addAllowed := func(raw string) {
		for _, candidate := range strings.Split(raw, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if normalized, err := common.NormalizeOrigin(candidate); err == nil {
				allowed[normalized] = struct{}{}
				configuredOrder = append(configuredOrder, normalized)
			}
		}
	}
	addAllowed(system_setting.GetServerAddress())
	for _, trusted := range cookieConfig.TrustedURLs {
		addAllowed(trusted)
	}
	// These settings are also the documented allowlists for a separately
	// hosted frontend.  They are read here rather than trusting an arbitrary
	// request Host header.
	addAllowed(os.Getenv("FRONTEND_BASE_URL"))
	addAllowed(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if len(allowed) == 0 {
		return "", fmt.Errorf("no valid OAuth callback origin is configured")
	}

	if c != nil && c.Request != nil {
		origins := c.Request.Header.Values("Origin")
		if len(origins) > 1 || (len(origins) == 1 && strings.Contains(origins[0], ",")) {
			return "", fmt.Errorf("ambiguous OAuth Origin header")
		}
		if len(origins) == 1 {
			normalized, err := common.NormalizeOrigin(origins[0])
			if err != nil {
				return "", fmt.Errorf("invalid OAuth Origin header: %w", err)
			}
			if _, ok := allowed[normalized]; !ok {
				return "", fmt.Errorf("OAuth Origin is not configured")
			}
			return normalized, nil
		}
		// Some same-origin user agents omit Origin on a form-style POST.  A
		// single, syntactically valid Referer is an equivalent browser-origin
		// signal here; it is still required to match the explicit allowlist.
		if referers := c.Request.Header.Values("Referer"); len(referers) == 1 {
			parsed, parseErr := url.Parse(strings.TrimSpace(referers[0]))
			if parseErr == nil && parsed.Scheme != "" && parsed.Host != "" && parsed.User == nil {
				normalized, normalizeErr := common.NormalizeOrigin(parsed.Scheme + "://" + parsed.Host)
				if normalizeErr == nil {
					if _, ok := allowed[normalized]; ok {
						return normalized, nil
					}
				}
			}
		}
	}

	// Same-origin form submissions may omit Origin.  Use the canonical
	// configured address in that case; do not derive a callback from an
	// attacker-controlled Host header.
	if normalized, err := common.NormalizeOrigin(system_setting.GetServerAddress()); err == nil {
		return normalized, nil
	}
	for _, origin := range configuredOrder {
		return origin, nil
	}
	return "", fmt.Errorf("no valid OAuth callback origin is configured")
}

func oauthRedirectURI(c *gin.Context, provider string) (string, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" || strings.ContainsAny(provider, "/\\?#") {
		return "", fmt.Errorf("invalid OAuth provider slug")
	}
	origin, err := oauthRedirectOrigin(c)
	if err != nil {
		return "", err
	}
	return origin + "/oauth/" + provider, nil
}

// providerParams returns map with Provider key for i18n templates
func providerParams(name string) map[string]any {
	return map[string]any{"Provider": name}
}

func oauthFlowCookieName(state string) string {
	nameDigest := common.GenerateHMACWithKey(
		[]byte("oauth-flow-cookie-name-v1:"+common.SessionSecret), state,
	)
	// Keep the per-flow cookie name compact.  128 bits is ample for collision
	// resistance while avoiding needless request-header growth when a user has
	// several OAuth tabs open.
	return oauthFlowCookiePrefix + nameDigest[:32]
}

func oauthFlowCookieHash(value string) string {
	return common.GenerateHMACWithKey(
		[]byte("oauth-flow-cookie-v1:"+common.SessionSecret), value,
	)
}

func oauthCodeVerifierHash(value string) string {
	return common.GenerateHMACWithKey(
		[]byte("oauth-pkce-verifier-v1:"+common.SessionSecret), value,
	)
}

func oauthCodeVerifierMatches(storedHash, verifier string) bool {
	expectedHash := oauthCodeVerifierHash(verifier)
	storedBytes := make([]byte, len(expectedHash))
	copy(storedBytes, storedHash)
	matched := subtle.ConstantTimeCompare([]byte(expectedHash), storedBytes) == 1
	return verifier != "" && len(storedHash) == len(expectedHash) && matched
}

// oauthFlowCookieValue returns the raw verifier/nonce carried by the
// flow-scoped HttpOnly cookie. It is intentionally kept server-side at the
// callback boundary; callers should never echo it to a response or log.
func oauthFlowCookieValue(c *gin.Context, state string) string {
	if c == nil || strings.TrimSpace(state) == "" {
		return ""
	}
	value, err := c.Cookie(oauthFlowCookieName(state))
	if err != nil {
		return ""
	}
	return value
}

func newOAuthFlowCookieValue() (string, error) {
	randomValue := make([]byte, oauthFlowCookieBytes)
	if _, err := rand.Read(randomValue); err != nil {
		return "", fmt.Errorf("generate oauth flow cookie: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(randomValue), nil
}

// oauthCodeChallenge returns the RFC 7636 S256 challenge for a verifier.
// The verifier itself remains in the HttpOnly, flow-scoped browser cookie.
func oauthCodeChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// oauthBindCodeVerifier deterministically derives a PKCE verifier for a bind
// flow from the state token and the authenticated dashboard session. The
// state token is opaque to the client and the session id is never sent to the
// OAuth provider, so an intercepted authorization code cannot be exchanged
// from another dashboard session.
func oauthBindCodeVerifier(state, sessionID string) string {
	return common.GenerateHMACWithKey(
		[]byte("oauth-pkce-bind-v1:"+common.SessionSecret),
		state+":"+sessionID,
	)
}

func writeOAuthFlowCookie(c *gin.Context, state, value string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthFlowCookieName(state),
		Value:    value,
		Path:     oauthFlowCookiePath,
		MaxAge:   maxAge,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   common.IsSessionCookieSecure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearOAuthFlowCookie(c *gin.Context, state string) {
	if strings.TrimSpace(state) == "" {
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthFlowCookieName(state),
		Value:    "",
		Path:     oauthFlowCookiePath,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   common.IsSessionCookieSecure(),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearOAuthFlowCookieIfPresent avoids emitting a meaningless Set-Cookie for
// bind flows (which deliberately do not use a browser verifier cookie), while
// still retiring a stale login-flow cookie on replay/error paths.
func clearOAuthFlowCookieIfPresent(c *gin.Context, state string) {
	if oauthFlowCookieValue(c, state) != "" {
		clearOAuthFlowCookie(c, state)
	}
}

// oauthFlowCookieMatches always compares fixed-length values.  This keeps the
// secret comparison constant-time even when a malformed/legacy SessionId is
// supplied from storage; the explicit length check is applied after compare.
func oauthFlowCookieMatches(storedHash, cookieValue string) bool {
	expectedHash := oauthFlowCookieHash(cookieValue)
	storedBytes := make([]byte, len(expectedHash))
	copy(storedBytes, storedHash)
	matched := subtle.ConstantTimeCompare([]byte(expectedHash), storedBytes) == 1
	return cookieValue != "" && len(storedHash) == len(expectedHash) && matched
}

func validateOAuthLoginBrowserBinding(c *gin.Context, state string, flow *model.AuthFlow) (string, bool) {
	// An empty SessionId identifies a flow created before browser binding was
	// introduced.  Treat it as invalid rather than silently accepting it.
	if flow == nil || flow.SessionId == "" {
		return "", false
	}
	cookieValue := oauthFlowCookieValue(c, state)
	if !oauthFlowCookieMatches(flow.SessionId, cookieValue) {
		return "", false
	}
	// Flows created before PKCE was introduced have no separate verifier hash;
	// their existing browser nonce still provides the legacy binding guarantee.
	if flow.CodeVerifierHash == "" {
		return cookieValue, cookieValue != ""
	}
	return cookieValue, oauthCodeVerifierMatches(flow.CodeVerifierHash, cookieValue)
}

// exchangeOAuthToken uses the optional PKCE extension when a verifier is
// available, while retaining compatibility with third-party Provider
// implementations that predate RFC 7636 support. Built-in and generic
// providers implement PKCEProvider and therefore always receive the verifier.

func exchangeOAuthToken(provider oauth.Provider, code, codeVerifier, redirectURI string, c *gin.Context) (*oauth.OAuthToken, error) {
	ctx := c.Request.Context()
	if strings.TrimSpace(redirectURI) != "" {
		ctx = oauth.WithRedirectURI(ctx, redirectURI)
	}
	if pkceProvider, ok := provider.(oauth.PKCEProvider); ok && codeVerifier != "" {
		return pkceProvider.ExchangeTokenWithVerifier(ctx, code, codeVerifier, c)
	}
	return provider.ExchangeToken(ctx, code, c)
}

// GenerateOAuthCode generates a state code for OAuth CSRF protection
func GenerateOAuthCode(c *gin.Context) {
	var request oauthStateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	request.Provider = strings.TrimSpace(request.Provider)
	request.Intent = strings.TrimSpace(request.Intent)
	request.Aff = strings.TrimSpace(request.Aff)
	if oauth.GetProvider(request.Provider) == nil ||
		(request.Intent != model.AuthFlowIntentLogin && request.Intent != model.AuthFlowIntentBind) ||
		len(request.Aff) > 32 ||
		(request.Intent == model.AuthFlowIntentBind && request.Aff != "") {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	redirectURI, err := oauthRedirectURI(c, request.Provider)
	if err != nil {
		// Do not create a flow whose callback would later be exchanged against a
		// different origin. Keep the reason in server logs, not in the response.
		common.SysError(fmt.Sprintf("reject OAuth flow callback origin: provider=%s error=%v", request.Provider, err))
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	userID := 0
	sessionID := ""
	cookieValue := ""
	if request.Intent == model.AuthFlowIntentBind {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "绑定操作需要登录"})
			return
		}
		userID = identity.UserID
		sessionID = identity.SessionID
	} else {
		// For login flows SessionId carries an HMAC of a random, flow-scoped
		// browser nonce. The raw nonce is kept only in an HttpOnly cookie and is
		// also used as the RFC 7636 PKCE verifier.
		var err error
		cookieValue, err = newOAuthFlowCookieValue()
		if err != nil {
			common.ApiError(c, err)
			return
		}
		sessionID = oauthFlowCookieHash(cookieValue)
	}
	payload, err := common.Marshal(oauthFlowPayload{AffiliateCode: request.Aff})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	expiresAt := time.Now().Add(oauthAuthFlowTTL)
	flowInput := model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  request.Provider,
		Intent:    request.Intent,
		UserId:    userID,
		SessionId: sessionID,
		CodeVerifierHash: func() string {
			if request.Intent == model.AuthFlowIntentLogin {
				return oauthCodeVerifierHash(cookieValue)
			}
			return ""
		}(),
		RedirectURI: redirectURI,
		Payload:     string(payload),
		ExpiresAt:   expiresAt,
	}
	var state string
	if request.Intent == model.AuthFlowIntentBind {
		// The verifier includes the opaque state token, which is generated inside
		// CreateAuthFlow. Set its HMAC in the same transaction so a partially
		// initialized bind flow can never be observed or consumed.
		state, _, err = model.CreateAuthFlowWithAction(flowInput,
			func(tx *gorm.DB, generatedState string, created *model.AuthFlow) error {
				verifierHash := oauthCodeVerifierHash(
					oauthBindCodeVerifier(generatedState, sessionID),
				)
				created.CodeVerifierHash = verifierHash
				result := tx.Model(&model.AuthFlow{}).
					Where("id = ?", created.Id).
					Update("code_verifier_hash", verifierHash)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return fmt.Errorf("initialize oauth bind verifier: affected %d rows", result.RowsAffected)
				}
				return nil
			})
	} else {
		state, _, err = model.CreateAuthFlow(flowInput)
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	codeChallenge := ""
	if request.Intent == model.AuthFlowIntentLogin {
		writeOAuthFlowCookie(c, state, cookieValue, expiresAt)
		codeChallenge = oauthCodeChallenge(cookieValue)
	} else {
		// Bind flows do not need another browser cookie: the authenticated
		// dashboard session is the verifier's source of entropy. The transaction
		// above persisted only an HMAC marker; the raw verifier is never stored.
		codeVerifier := oauthBindCodeVerifier(state, sessionID)
		codeChallenge = oauthCodeChallenge(codeVerifier)
	}
	data := gin.H{
		"flow_token":            state,
		"code_challenge":        codeChallenge,
		"code_challenge_method": "S256",
		"redirect_uri":          redirectURI,
		"expires_at":            expiresAt.Unix(),
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

// HandleOAuth handles OAuth callback for all standard OAuth providers
func HandleOAuth(c *gin.Context) {
	providerName := c.Param("provider")
	provider := oauth.GetProvider(providerName)
	if provider == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgOAuthUnknownProvider),
		})
		return
	}

	// 1. Validate state (CSRF protection)
	state := c.Query("state")
	pendingFlow, err := model.GetAuthFlow(state, model.AuthFlowMatch{
		Purpose:  model.AuthFlowPurposeOAuth,
		Provider: providerName,
	})
	if err != nil {
		// A replayed callback must retire the browser nonce as well.  The
		// lookup intentionally happens before cookie validation, so only a
		// state that was actually consumed gets this cleanup behavior.
		if errors.Is(err, model.ErrAuthFlowConsumed) {
			clearOAuthFlowCookieIfPresent(c, state)
		}
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
		})
		return
	}

	consumeMatch := model.AuthFlowMatch{
		Purpose:  model.AuthFlowPurposeOAuth,
		Provider: providerName,
		Intent:   pendingFlow.Intent,
	}
	codeVerifier := ""
	// 2. Bind flows are bound to the live dashboard Session that created them.
	if pendingFlow.Intent == model.AuthFlowIntentBind {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok || identity.UserID != pendingFlow.UserId || identity.SessionID != pendingFlow.SessionId {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
			})
			return
		}
		if pendingFlow.CodeVerifierHash != "" {
			// New bind flows derive the verifier from the state and live dashboard
			// session. Legacy rows have no hash and use the pre-PKCE exchange path
			// for the remainder of their short lifetime.
			codeVerifier = oauthBindCodeVerifier(state, pendingFlow.SessionId)
			if !oauthCodeVerifierMatches(pendingFlow.CodeVerifierHash, codeVerifier) {
				c.JSON(http.StatusForbidden, gin.H{
					"success": false,
					"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
				})
				return
			}
		}
		consumeMatch.UserId = identity.UserID
		consumeMatch.SessionId = identity.SessionID
	} else if pendingFlow.Intent != model.AuthFlowIntentLogin {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	} else {
		var valid bool
		codeVerifier, valid = validateOAuthLoginBrowserBinding(c, state, pendingFlow)
		if !valid {
			// Do not consume a flow when the callback did not originate from the
			// browser that started it.  The legitimate browser can retry while
			// an attacker learns nothing about the provider exchange.
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": i18n.T(c, i18n.MsgOAuthStateInvalid),
			})
			return
		}
		consumeMatch.SessionId = pendingFlow.SessionId
	}

	// 3. Check if provider is enabled
	if !provider.IsEnabled() {
		common.ApiErrorI18n(c, i18n.MsgOAuthNotEnabled, providerParams(provider.GetName()))
		return
	}

	// 4. Handle error from provider
	errorCode := c.Query("error")
	if errorCode != "" {
		if _, err := model.ConsumeAuthFlow(state, consumeMatch); err != nil {
			if errors.Is(err, model.ErrAuthFlowConsumed) {
				clearOAuthFlowCookieIfPresent(c, state)
			}
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": i18n.T(c, i18n.MsgOAuthStateInvalid)})
			return
		}
		if pendingFlow.Intent == model.AuthFlowIntentLogin {
			clearOAuthFlowCookie(c, state)
		}
		errorDescription := c.Query("error_description")
		if errorDescription == "" {
			errorDescription = errorCode
		}
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": errorDescription,
		})
		return
	}
	if pendingFlow.Intent == model.AuthFlowIntentBind {
		handleOAuthBind(c, provider, pendingFlow, state, codeVerifier)
		return
	}

	// 5. Exchange code for token
	code := c.Query("code")
	// For login flows the browser-bound nonce is also the RFC 7636 PKCE
	// verifier. The server stores only its HMAC in SessionId, so an intercepted
	// authorization code cannot be exchanged without the initiating browser's
	// HttpOnly cookie.
	token, err := exchangeOAuthToken(provider, code, codeVerifier, pendingFlow.RedirectURI, c)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// 6. Get user info
	oauthUser, err := provider.GetUserInfo(c.Request.Context(), token)
	if err != nil {
		handleOAuthError(c, err)
		return
	}
	flow, err := model.ConsumeAuthFlow(state, consumeMatch)
	if err != nil {
		if errors.Is(err, model.ErrAuthFlowConsumed) {
			clearOAuthFlowCookieIfPresent(c, state)
		}
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": i18n.T(c, i18n.MsgOAuthStateInvalid)})
		return
	}
	clearOAuthFlowCookie(c, state)

	// 7. Find or create user
	var payload oauthFlowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		common.ApiError(c, err)
		return
	}
	user, err := findOrCreateOAuthUser(c, provider, oauthUser, payload.AffiliateCode)
	if err != nil {
		if errors.Is(err, model.ErrEmailAlreadyTaken) {
			common.ApiErrorI18n(c, i18n.MsgUserEmailAlreadyTaken)
			return
		}
		switch err.(type) {
		case *OAuthUserDeletedError:
			common.ApiErrorI18n(c, i18n.MsgOAuthUserDeleted)
		case *OAuthRegistrationDisabledError:
			common.ApiErrorI18n(c, i18n.MsgUserRegisterDisabled)
		case *OAuthEmailAlreadyTakenError:
			common.ApiErrorI18n(c, i18n.MsgUserEmailAlreadyTaken)
		default:
			common.ApiError(c, err)
		}
		return
	}

	// 8. Check user status
	if user.Status != common.UserStatusEnabled {
		common.ApiErrorI18n(c, i18n.MsgOAuthUserBanned)
		return
	}

	// 9. Setup login
	setupLogin(user, c)
}

// handleOAuthBind handles binding OAuth account to existing user
func handleOAuthBind(c *gin.Context, provider oauth.Provider, pendingFlow *model.AuthFlow, flowToken, codeVerifier string) {
	// Exchange code for token
	code := c.Query("code")
	token, err := exchangeOAuthToken(provider, code, codeVerifier, pendingFlow.RedirectURI, c)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// Get user info
	oauthUser, err := provider.GetUserInfo(c.Request.Context(), token)
	if err != nil {
		handleOAuthError(c, err)
		return
	}

	// Check if this OAuth account is already bound (check both new ID and legacy ID)
	if provider.IsUserIDTaken(oauthUser.ProviderUserID) {
		common.ApiErrorI18n(c, i18n.MsgOAuthAlreadyBound, providerParams(provider.GetName()))
		return
	}
	// Also check legacy ID to prevent duplicate bindings during migration period
	if legacyID, ok := oauthUser.Extra["legacy_id"].(string); ok && legacyID != "" {
		if provider.IsUserIDTaken(legacyID) {
			common.ApiErrorI18n(c, i18n.MsgOAuthAlreadyBound, providerParams(provider.GetName()))
			return
		}
	}

	if _, err := model.ConsumeAuthFlow(flowToken, model.AuthFlowMatch{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  pendingFlow.Provider,
		Intent:    model.AuthFlowIntentBind,
		UserId:    pendingFlow.UserId,
		SessionId: pendingFlow.SessionId,
	}); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": i18n.T(c, i18n.MsgOAuthStateInvalid)})
		return
	}
	clearOAuthFlowCookieIfPresent(c, flowToken)

	userId := pendingFlow.UserId

	// Handle binding based on provider type
	if genericProvider, ok := provider.(*oauth.GenericOAuthProvider); ok {
		// Custom provider: use user_oauth_bindings table
		err = model.UpdateUserOAuthBinding(userId, genericProvider.GetProviderId(), oauthUser.ProviderUserID)
		if err != nil {
			common.ApiError(c, err)
			return
		}
	} else {
		// Built-in providers use a durable claim and the users-table binding in
		// one transaction. The claim's unique subject and (provider,user) keys
		// close both the duplicate-account and same-user rebind races.
		column := provider.ProviderUserIDColumn()
		identityProvider, claimable := model.ExternalIdentityProviderForColumn(column)
		err = model.DB.Transaction(func(tx *gorm.DB) error {
			if claimable {
				// Rebinding replaces this user's previous subject. Release it
				// inside the same transaction so a failed claim leaves the old
				// binding intact.
				if err := model.ReleaseExternalIdentityWithTx(tx, identityProvider, userId); err != nil {
					return err
				}
				if err := model.ClaimExternalIdentityWithTx(tx, identityProvider, oauthUser.ProviderUserID, userId); err != nil {
					return err
				}
			}
			// Binding operations must only write the binding column. A complete
			// user snapshot could restore a concurrently changed role/status/group.
			return model.UpdateUserBindColumnWithTx(tx, userId, column, oauthUser.ProviderUserID)
		})
		if err != nil {
			if errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) {
				common.ApiErrorI18n(c, i18n.MsgOAuthAlreadyBound, providerParams(provider.GetName()))
				return
			}
			common.ApiError(c, err)
			return
		}
	}

	common.ApiSuccessI18n(c, i18n.MsgOAuthBindSuccess, gin.H{
		"action": "bind",
	})
}

// findOrCreateOAuthUser finds existing user or creates new user
func findOrCreateOAuthUser(c *gin.Context, provider oauth.Provider, oauthUser *oauth.OAuthUser, affiliateCode string) (*model.User, error) {
	user := &model.User{}

	// Check if user already exists with new ID
	if provider.IsUserIDTaken(oauthUser.ProviderUserID) {
		err := provider.FillUserByProviderID(user, oauthUser.ProviderUserID)
		if err != nil {
			return nil, err
		}
		// Check if user has been deleted
		if user.Id == 0 {
			return nil, &OAuthUserDeletedError{}
		}
		return user, nil
	}

	// Try to find user with legacy ID (for GitHub migration from login to numeric ID)
	if legacyID, ok := oauthUser.Extra["legacy_id"].(string); ok && legacyID != "" {
		if provider.IsUserIDTaken(legacyID) {
			err := provider.FillUserByProviderID(user, legacyID)
			if err != nil {
				return nil, err
			}
			if user.Id != 0 {
				// Found user with legacy ID, migrate to new ID
				common.SysLog(fmt.Sprintf("[OAuth] Migrating user %d from legacy_id=%s to new_id=%s",
					user.Id, legacyID, oauthUser.ProviderUserID))
				migrationErr := model.DB.Transaction(func(tx *gorm.DB) error {
					// Move the durable claim before taking the new subject. The
					// transaction rolls back the release if the new subject is owned
					// by another account.
					if err := model.ReleaseExternalIdentityWithTx(tx, model.ExternalIdentityProviderGitHub, user.Id); err != nil {
						return err
					}
					if err := model.ClaimExternalIdentityWithTx(tx, model.ExternalIdentityProviderGitHub, oauthUser.ProviderUserID, user.Id); err != nil {
						return err
					}
					result := tx.Model(&model.User{}).Where("id = ?", user.Id).Update("github_id", oauthUser.ProviderUserID)
					if result.Error != nil {
						return result.Error
					}
					if result.RowsAffected != 1 {
						return gorm.ErrRecordNotFound
					}
					return nil
				})
				if migrationErr != nil {
					common.SysError(fmt.Sprintf("[OAuth] Failed to migrate user %d: %s", user.Id, migrationErr.Error()))
					// Continue with login even if migration fails
				} else {
					user.GitHubId = oauthUser.ProviderUserID
				}
				return user, nil
			}
		}
	}

	// User doesn't exist, create new user if registration is enabled
	if !common.GetSecurityRuntimeConfig().RegisterEnabled {
		return nil, &OAuthRegistrationDisabledError{}
	}

	// Set up new user
	user.Username = provider.GetProviderPrefix() + strconv.Itoa(model.GetMaxUserId()+1)

	if oauthUser.Username != "" {
		if exists, err := model.CheckUserExistOrDeleted(oauthUser.Username, ""); err == nil && !exists {
			// 防止索引退化
			if len(oauthUser.Username) <= model.UserNameMaxLength {
				user.Username = oauthUser.Username
			}
		}
	}

	if oauthUser.DisplayName != "" {
		user.DisplayName = oauthUser.DisplayName
	} else if oauthUser.Username != "" {
		user.DisplayName = oauthUser.Username
	} else {
		user.DisplayName = provider.GetName() + " User"
	}
	if oauthUser.Email != "" {
		user.Email = model.NormalizeEmail(oauthUser.Email)
		if err := model.EnsureEmailAvailable(user.Email, 0); err != nil {
			if errors.Is(err, model.ErrEmailAlreadyTaken) {
				return nil, &OAuthEmailAlreadyTakenError{}
			}
			return nil, err
		}
	}
	user.Role = common.RoleCommonUser
	user.Status = common.UserStatusEnabled

	// Handle affiliate code
	inviterId := 0
	if strings.TrimSpace(affiliateCode) != "" {
		var lookupErr error
		inviterId, lookupErr = model.GetUserIdByAffCode(strings.TrimSpace(affiliateCode))
		if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("lookup affiliate code: %w", lookupErr)
		}
		if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			inviterId = 0
		}
	}

	// Use transaction to ensure user creation and OAuth binding are atomic
	if genericProvider, ok := provider.(*oauth.GenericOAuthProvider); ok {
		// Custom provider: create user and binding in a transaction
		err := model.DB.Transaction(func(tx *gorm.DB) error {
			// Create user
			if err := user.InsertWithTx(tx, inviterId); err != nil {
				return err
			}

			// Create OAuth binding
			binding := &model.UserOAuthBinding{
				UserId:         user.Id,
				ProviderId:     genericProvider.GetProviderId(),
				ProviderUserId: oauthUser.ProviderUserID,
			}
			if err := model.CreateUserOAuthBindingWithTx(tx, binding); err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			return nil, err
		}

		// Perform post-transaction tasks (logs, sidebar config, inviter rewards)
		user.FinalizeOAuthUserCreation(inviterId)
	} else {
		// Built-in provider: persist the provider column, claim, and user row in
		// one transaction. Custom implementations with no users-table column keep
		// the historical behavior and simply skip the durable claim.
		identityColumn := provider.ProviderUserIDColumn()
		identityProvider, claimable := model.ExternalIdentityProviderForColumn(identityColumn)
		provider.SetProviderUserID(user, oauthUser.ProviderUserID)
		err := model.DB.Transaction(func(tx *gorm.DB) error {
			// Create user
			if err := user.InsertWithTx(tx, inviterId); err != nil {
				return err
			}
			if claimable {
				if err := model.ClaimExternalIdentityWithTx(tx, identityProvider, oauthUser.ProviderUserID, user.Id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			// A concurrent callback may have won the claim after the initial
			// users-table lookup. Re-read that owner and complete login instead
			// of creating a second account or returning a transient failure.
			if claimable && errors.Is(err, model.ErrExternalIdentityAlreadyClaimed) {
				existing := &model.User{}
				if fillErr := provider.FillUserByProviderID(existing, oauthUser.ProviderUserID); fillErr == nil {
					if existing.Id == 0 {
						return nil, &OAuthUserDeletedError{}
					}
					return existing, nil
				}
			}
			return nil, err
		}

		// Perform post-transaction tasks
		user.FinalizeOAuthUserCreation(inviterId)
	}

	return user, nil
}

// Error types for OAuth
type OAuthUserDeletedError struct{}

func (e *OAuthUserDeletedError) Error() string {
	return "user has been deleted"
}

type OAuthRegistrationDisabledError struct{}

func (e *OAuthRegistrationDisabledError) Error() string {
	return "registration is disabled"
}

type OAuthEmailAlreadyTakenError struct{}

func (e *OAuthEmailAlreadyTakenError) Error() string {
	return "email is already in use"
}

// handleOAuthError handles OAuth errors and returns translated message
func handleOAuthError(c *gin.Context, err error) {
	switch e := err.(type) {
	case *oauth.OAuthError:
		if e.Params != nil {
			common.ApiErrorI18n(c, e.MsgKey, e.Params)
		} else {
			common.ApiErrorI18n(c, e.MsgKey)
		}
	case *oauth.AccessDeniedError:
		common.ApiErrorMsg(c, e.Message)
	case *oauth.TrustLevelError:
		common.ApiErrorI18n(c, i18n.MsgOAuthTrustLevelLow)
	default:
		common.ApiError(c, err)
	}
}
