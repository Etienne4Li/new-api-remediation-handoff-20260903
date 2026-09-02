package common

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRedirectURL(t *testing.T) {
	// Save original trusted domains and restore after test
	originalDomains := constant.TrustedRedirectDomains
	defer func() {
		constant.TrustedRedirectDomains = originalDomains
	}()

	tests := []struct {
		name           string
		url            string
		trustedDomains []string
		wantErr        bool
		errContains    string
	}{
		// Valid cases
		{
			name:           "exact domain match with https",
			url:            "https://example.com/success",
			trustedDomains: []string{"example.com"},
			wantErr:        false,
		},
		{
			name:           "exact domain match with http",
			url:            "http://example.com/callback",
			trustedDomains: []string{"example.com"},
			wantErr:        false,
		},
		{
			name:           "subdomain match",
			url:            "https://sub.example.com/success",
			trustedDomains: []string{"example.com"},
			wantErr:        false,
		},
		{
			name:           "case insensitive domain",
			url:            "https://EXAMPLE.COM/success",
			trustedDomains: []string{"example.com"},
			wantErr:        false,
		},

		// Invalid cases - untrusted domain
		{
			name:           "untrusted domain",
			url:            "https://evil.com/phishing",
			trustedDomains: []string{"example.com"},
			wantErr:        true,
			errContains:    "not in the trusted domains list",
		},
		{
			name:           "suffix attack - fakeexample.com",
			url:            "https://fakeexample.com/success",
			trustedDomains: []string{"example.com"},
			wantErr:        true,
			errContains:    "not in the trusted domains list",
		},
		{
			name:           "empty trusted domains list",
			url:            "https://example.com/success",
			trustedDomains: []string{},
			wantErr:        true,
			errContains:    "not in the trusted domains list",
		},

		// Invalid cases - scheme
		{
			name:           "javascript scheme",
			url:            "javascript:alert('xss')",
			trustedDomains: []string{"example.com"},
			wantErr:        true,
			errContains:    "invalid URL scheme",
		},
		{
			name:           "data scheme",
			url:            "data:text/html,<script>alert('xss')</script>",
			trustedDomains: []string{"example.com"},
			wantErr:        true,
			errContains:    "invalid URL scheme",
		},

		// Edge cases
		{
			name:           "empty URL",
			url:            "",
			trustedDomains: []string{"example.com"},
			wantErr:        true,
			errContains:    "invalid URL scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up trusted domains for this test case
			constant.TrustedRedirectDomains = tt.trustedDomains

			err := ValidateRedirectURL(tt.url)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateRedirectURL(%q) expected error containing %q, got nil", tt.url, tt.errContains)
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("ValidateRedirectURL(%q) error = %q, want error containing %q", tt.url, err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateRedirectURL(%q) unexpected error: %v", tt.url, err)
				}
			}
		})
	}
}

func TestValidateHTTPURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "https", url: "https://cdn.example.test/image.png"},
		{name: "uppercase scheme", url: "HTTPS://cdn.example.test/image.png"},
		{name: "custom port", url: "http://cdn.example.test:8080/image.png"},
		{name: "missing host", url: "https:///image.png", wantErr: "host"},
		{name: "unsupported scheme", url: "file:///tmp/image.png", wantErr: "scheme"},
		{name: "userinfo", url: "https://user:secret@cdn.example.test/image.png", wantErr: "userinfo"},
		{name: "invalid port", url: "https://cdn.example.test:65536/image.png", wantErr: "port"},
		{name: "empty port", url: "https://cdn.example.test:", wantErr: "port"},
		{name: "fragment", url: "https://cdn.example.test/image.png#secret", wantErr: "fragment"},
		{name: "opaque URL", url: "https:cdn.example.test/image.png", wantErr: "host"},
		{name: "control character", url: "https://cdn.example.test/image.png\x7f", wantErr: "control"},
		{name: "oversized URL", url: "https://cdn.example.test/" + strings.Repeat("x", MaxHTTPURLLength), wantErr: "long"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHTTPURL(tt.url)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateHTTPURL(%q) unexpected error: %v", tt.url, err)
				}
				return
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantErr)) {
				t.Fatalf("ValidateHTTPURL(%q) error = %v, want substring %q", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestValidateCredentialFreeURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "ordinary query", url: "https://provider.example/v1?api-version=2024-02-01&alt=sse"},
		{name: "local Ollama endpoint", url: "http://127.0.0.1:11434/v1?api-version=local"},
		{name: "GLM endpoint identifier", url: "glm-coding-plan"},
		{name: "Kimi endpoint identifier", url: "kimi-coding-plan"},
		{name: "Doubao endpoint identifier", url: "doubao-coding-plan"},
		{name: "userinfo", url: "https://user:password@provider.example/v1", wantErr: "userinfo"},
		{name: "API key", url: "https://provider.example/v1?api_key=secret", wantErr: "credential query"},
		{name: "encoded API key name", url: "https://provider.example/v1?API%5FKEY=secret", wantErr: "credential query"},
		{name: "generic credential", url: "https://provider.example/v1?credential=secret", wantErr: "credential query"},
		{name: "subscription key", url: "https://provider.example/v1?subscription-key=secret", wantErr: "credential query"},
		{name: "Google API key", url: "https://provider.example/v1?X-Goog-API-Key=secret", wantErr: "credential query"},
		{name: "token variant", url: "https://provider.example/v1?custom-token=secret", wantErr: "credential query"},
		{name: "fragment", url: "https://provider.example/v1#access_token=secret", wantErr: "fragment"},
		{name: "malformed query", url: "https://provider.example/v1?api-version=1;alt=sse", wantErr: "query"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCredentialFreeURL(tt.url)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func resetSessionCookieSettingsAfterTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		SessionCookieSecure = false
		SessionCookieTrustedURLs = nil
	})
}

func TestInitSessionCookieSettingsDefaultsToInsecure(t *testing.T) {
	resetSessionCookieSettingsAfterTest(t)
	t.Setenv("SESSION_COOKIE_SECURE", "")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "")

	require.NoError(t, InitSessionCookieSettings())
	assert.False(t, SessionCookieSecure)
	assert.Empty(t, SessionCookieTrustedURLs)
}

func TestInitSessionCookieSettingsRequiresBothEnvVars(t *testing.T) {
	t.Run("secure without trusted url", func(t *testing.T) {
		resetSessionCookieSettingsAfterTest(t)
		t.Setenv("SESSION_COOKIE_SECURE", "true")
		t.Setenv("SESSION_COOKIE_TRUSTED_URL", "")

		require.Error(t, InitSessionCookieSettings())
	})

	t.Run("trusted url without secure", func(t *testing.T) {
		resetSessionCookieSettingsAfterTest(t)
		t.Setenv("SESSION_COOKIE_SECURE", "")
		t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://example.com")

		require.Error(t, InitSessionCookieSettings())
	})
}

func TestInitSessionCookieSettingsRequiresHTTPSURL(t *testing.T) {
	resetSessionCookieSettingsAfterTest(t)
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "http://example.com")

	require.Error(t, InitSessionCookieSettings())
}

func TestInitSessionCookieSettingsEnablesSecureCookie(t *testing.T) {
	resetSessionCookieSettingsAfterTest(t)
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://example.com")

	require.NoError(t, InitSessionCookieSettings())
	assert.True(t, SessionCookieSecure)
	assert.Equal(t, []string{"https://example.com"}, SessionCookieTrustedURLs)
}

func TestInitSessionCookieSettingsAllowsMultipleTrustedURLs(t *testing.T) {
	resetSessionCookieSettingsAfterTest(t)
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://example.com, https://admin.example.com")

	require.NoError(t, InitSessionCookieSettings())
	assert.True(t, SessionCookieSecure)
	assert.Equal(t, []string{"https://example.com", "https://admin.example.com"}, SessionCookieTrustedURLs)
}

func TestInitSessionCookieSettingsRejectsEmptyTrustedURLInList(t *testing.T) {
	resetSessionCookieSettingsAfterTest(t)
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://example.com,")

	require.Error(t, InitSessionCookieSettings())
}

func TestInitSessionCookieSettingsNormalizesExactOrigins(t *testing.T) {
	resetSessionCookieSettingsAfterTest(t)
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("SESSION_COOKIE_TRUSTED_URL", "https://EXAMPLE.com:443,https://admin.example.com:8443/")

	require.NoError(t, InitSessionCookieSettings())
	assert.Equal(t, []string{"https://example.com", "https://admin.example.com:8443"}, SessionCookieTrustedURLs)
}

func TestInitSessionCookieSettingsRejectsNonOriginURLs(t *testing.T) {
	for _, trustedURL := range []string{
		"https://*.example.com",
		"https://user@example.com",
		"https://example.com/admin",
		"https://example.com?next=admin",
		"https://example.com#admin",
	} {
		t.Run(trustedURL, func(t *testing.T) {
			resetSessionCookieSettingsAfterTest(t)
			t.Setenv("SESSION_COOKIE_SECURE", "true")
			t.Setenv("SESSION_COOKIE_TRUSTED_URL", trustedURL)
			require.Error(t, InitSessionCookieSettings())
		})
	}
}
