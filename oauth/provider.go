package oauth

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// redirectURIContextKey keeps the callback URI selected when an OAuth flow is
// started attached to the request that exchanges the authorization code.  It
// avoids making the Provider interface breaking while still allowing
// deployments with a separately hosted frontend to use their actual origin.
type redirectURIContextKey struct{}

// WithRedirectURI returns a context carrying the exact callback URI recorded
// for an OAuth flow.  Empty values are ignored so legacy callers retain each
// provider's historical fallback behavior.
func WithRedirectURI(ctx context.Context, redirectURI string) context.Context {
	if ctx == nil || strings.TrimSpace(redirectURI) == "" {
		return ctx
	}
	return context.WithValue(ctx, redirectURIContextKey{}, strings.TrimSpace(redirectURI))
}

// RedirectURIFromContext returns a flow-bound callback URI, if one was
// supplied by the controller.  Providers must treat an empty result as a
// request to use their legacy configured callback.
func RedirectURIFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(redirectURIContextKey{}).(string)
	return strings.TrimSpace(value)
}

// Provider defines the interface for OAuth providers
type Provider interface {
	// GetName returns the display name of the provider (e.g., "GitHub", "Discord")
	GetName() string

	// IsEnabled returns whether this OAuth provider is enabled
	IsEnabled() bool

	// ExchangeToken exchanges the authorization code for an access token. The
	// gin.Context is passed for providers that need request info (e.g., for
	// redirect_uri).
	ExchangeToken(ctx context.Context, code string, c *gin.Context) (*OAuthToken, error)

	// GetUserInfo retrieves user information using the access token
	GetUserInfo(ctx context.Context, token *OAuthToken) (*OAuthUser, error)

	// IsUserIDTaken checks if the provider user ID is already associated with an account
	IsUserIDTaken(providerUserID string) bool

	// FillUserByProviderID fills the user model by provider user ID
	FillUserByProviderID(user *model.User, providerUserID string) error

	// SetProviderUserID sets the provider user ID on the user model
	SetProviderUserID(user *model.User, providerUserID string)

	// GetProviderPrefix returns the prefix for auto-generated usernames (e.g., "github_")
	GetProviderPrefix() string

	// ProviderUserIDColumn returns the users-table column that stores this provider's
	// user ID, used by bind flows to update only the binding column instead of
	// writing back a full user snapshot. Providers that persist bindings elsewhere
	// (e.g. the user_oauth_bindings table) return an empty string.
	ProviderUserIDColumn() string
}

// PKCEProvider is an optional extension implemented by providers that accept
// RFC 7636 verifiers. Keeping it separate from Provider preserves source
// compatibility for external/custom provider implementations.
type PKCEProvider interface {
	ExchangeTokenWithVerifier(ctx context.Context, code, codeVerifier string, c *gin.Context) (*OAuthToken, error)
}
