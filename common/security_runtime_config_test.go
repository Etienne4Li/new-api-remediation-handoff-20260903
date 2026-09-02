package common

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecurityRuntimeConfigPublishesCoherentSnapshots(t *testing.T) {
	previous := GetSecurityRuntimeConfig()
	t.Cleanup(func() {
		UpdateSecurityRuntimeConfig(func(cfg *SecurityRuntimeConfig) { *cfg = previous })
	})

	first := SecurityRuntimeConfig{
		PasswordLoginEnabled:          true,
		PasswordRegisterEnabled:       true,
		EmailVerificationEnabled:      false,
		RegisterEnabled:               true,
		GitHubOAuthEnabled:            true,
		GitHubClientID:                "github-a",
		GitHubClientSecret:            "github-secret-a",
		LinuxDOOAuthEnabled:           true,
		LinuxDOClientID:               "linux-a",
		LinuxDOClientSecret:           "linux-secret-a",
		LinuxDOMinimumTrustLevel:      1,
		TelegramOAuthEnabled:          true,
		TelegramBotToken:              "telegram-a",
		TelegramBotName:               "bot-a",
		WeChatAuthEnabled:             true,
		WeChatServerAddress:           "https://wechat-a.example",
		WeChatServerToken:             "wechat-token-a",
		TurnstileCheckEnabled:         true,
		TurnstileSiteKey:              "site-a",
		TurnstileSecretKey:            "secret-a",
		EmailDomainRestrictionEnabled: true,
		EmailAliasRestrictionEnabled:  false,
		EmailDomainWhitelist:          []string{"a.example", "b.example"},
	}
	second := first
	second.PasswordLoginEnabled = false
	second.PasswordRegisterEnabled = false
	second.EmailVerificationEnabled = true
	second.RegisterEnabled = false
	second.GitHubOAuthEnabled = false
	second.GitHubClientID = "github-b"
	second.GitHubClientSecret = "github-secret-b"
	second.LinuxDOClientID = "linux-b"
	second.LinuxDOClientSecret = "linux-secret-b"
	second.LinuxDOMinimumTrustLevel = 4
	second.TelegramBotToken = "telegram-b"
	second.TelegramBotName = "bot-b"
	second.WeChatServerAddress = "https://wechat-b.example"
	second.WeChatServerToken = "wechat-token-b"
	second.TurnstileCheckEnabled = false
	second.TurnstileSiteKey = "site-b"
	second.TurnstileSecretKey = "secret-b"
	second.EmailDomainRestrictionEnabled = false
	second.EmailAliasRestrictionEnabled = true
	second.EmailDomainWhitelist = []string{"c.example"}

	UpdateSecurityRuntimeConfig(func(cfg *SecurityRuntimeConfig) { *cfg = first })
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	seen := make(chan SecurityRuntimeConfig, 256)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 256; i++ {
			value := first
			if i%2 == 1 {
				value = second
			}
			UpdateSecurityRuntimeConfig(func(cfg *SecurityRuntimeConfig) { *cfg = value })
		}
	}()
	go func() {
		defer wg.Done()
		defer close(seen)
		<-start
		for i := 0; i < 256; i++ {
			seen <- GetSecurityRuntimeConfig()
		}
	}()
	close(start)
	wg.Wait()
	for got := range seen {
		// Compare the scalar tuple and whitelist together; a reader must never
		// observe credentials from one generation with flags from the other.
		require.Truef(t, securityConfigEqual(got, first) || securityConfigEqual(got, second), "mixed security snapshot: %+v", got)
	}

	snapshot := GetSecurityRuntimeConfig()
	if len(snapshot.EmailDomainWhitelist) > 0 {
		snapshot.EmailDomainWhitelist[0] = "mutated.example"
	}
	require.NotEqual(t, "mutated.example", GetSecurityRuntimeConfig().EmailDomainWhitelist[0])
}

func securityConfigEqual(a, b SecurityRuntimeConfig) bool {
	if a.PasswordLoginEnabled != b.PasswordLoginEnabled ||
		a.PasswordRegisterEnabled != b.PasswordRegisterEnabled ||
		a.EmailVerificationEnabled != b.EmailVerificationEnabled ||
		a.RegisterEnabled != b.RegisterEnabled ||
		a.GitHubOAuthEnabled != b.GitHubOAuthEnabled ||
		a.GitHubClientID != b.GitHubClientID ||
		a.GitHubClientSecret != b.GitHubClientSecret ||
		a.LinuxDOOAuthEnabled != b.LinuxDOOAuthEnabled ||
		a.LinuxDOClientID != b.LinuxDOClientID ||
		a.LinuxDOClientSecret != b.LinuxDOClientSecret ||
		a.LinuxDOMinimumTrustLevel != b.LinuxDOMinimumTrustLevel ||
		a.TelegramOAuthEnabled != b.TelegramOAuthEnabled ||
		a.TelegramBotToken != b.TelegramBotToken ||
		a.TelegramBotName != b.TelegramBotName ||
		a.WeChatAuthEnabled != b.WeChatAuthEnabled ||
		a.WeChatServerAddress != b.WeChatServerAddress ||
		a.WeChatServerToken != b.WeChatServerToken ||
		a.WeChatAccountQRCodeImageURL != b.WeChatAccountQRCodeImageURL ||
		a.TurnstileCheckEnabled != b.TurnstileCheckEnabled ||
		a.TurnstileSiteKey != b.TurnstileSiteKey ||
		a.TurnstileSecretKey != b.TurnstileSecretKey ||
		a.EmailDomainRestrictionEnabled != b.EmailDomainRestrictionEnabled ||
		a.EmailAliasRestrictionEnabled != b.EmailAliasRestrictionEnabled ||
		len(a.EmailDomainWhitelist) != len(b.EmailDomainWhitelist) {
		return false
	}
	for i := range a.EmailDomainWhitelist {
		if a.EmailDomainWhitelist[i] != b.EmailDomainWhitelist[i] {
			return false
		}
	}
	return true
}
