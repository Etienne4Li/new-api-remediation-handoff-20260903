package common

import "sync"

// SMTPConfig is the runtime SMTP configuration used by the mail sender.
//
// The legacy exported variables in constants.go are kept for compatibility
// with integrations and tests.  Runtime code must obtain a copy through
// GetSMTPConfig instead of reading those variables directly.  Configuration
// updates publish all fields while holding smtpConfigMu, so a request always
// observes one coherent set of credentials and transport flags.
type SMTPConfig struct {
	Server             string
	Port               int
	SSLEnabled         bool
	StartTLSEnabled    bool
	InsecureSkipVerify bool
	ForceAuthLogin     bool
	Account            string
	From               string
	Token              string
	SystemName         string
}

var smtpConfigMu sync.RWMutex

// logConsumeEnabledMu protects the legacy LogConsumeEnabled export while the
// admin option endpoint hot-reloads it. Runtime readers must use the accessor
// below; the exported variable remains for source compatibility only.
var logConsumeEnabledMu sync.RWMutex

// IsLogConsumeEnabled returns the currently published consume-log flag.
func IsLogConsumeEnabled() bool {
	logConsumeEnabledMu.RLock()
	defer logConsumeEnabledMu.RUnlock()
	return LogConsumeEnabled
}

// SetLogConsumeEnabled publishes the consume-log flag. It intentionally only
// takes its own lock so option publication can call it while holding
// OptionMapRWMutex without recursive lock acquisition.
func SetLogConsumeEnabled(value bool) {
	logConsumeEnabledMu.Lock()
	LogConsumeEnabled = value
	logConsumeEnabledMu.Unlock()
}

// QuotaConfig contains the process-wide billing unit and conservative
// pre-consumption baseline.  Both values are hot-reloadable options and are
// read on request/settlement paths, so callers must take a detached snapshot
// instead of reading the legacy exported variables directly.
type QuotaConfig struct {
	QuotaPerUnit     float64
	PreConsumedQuota int
}

var quotaConfigMu sync.RWMutex

// SecurityRuntimeConfig is the request-time snapshot for authentication and
// abuse-prevention settings.  These values are changed by the admin option
// endpoint while requests may be using them concurrently; returning one
// detached value prevents a request from combining an old credential with a
// new enable/disable flag (or from iterating a whitelist while it is replaced).
//
// The legacy exported variables in constants.go remain available for source
// compatibility.  Runtime updates must use UpdateSecurityRuntimeConfig (the
// option publisher does so below), and production readers must use
// GetSecurityRuntimeConfig.
type SecurityRuntimeConfig struct {
	PasswordLoginEnabled     bool
	PasswordRegisterEnabled  bool
	EmailVerificationEnabled bool
	RegisterEnabled          bool

	GitHubOAuthEnabled bool
	GitHubClientID     string
	GitHubClientSecret string

	LinuxDOOAuthEnabled      bool
	LinuxDOClientID          string
	LinuxDOClientSecret      string
	LinuxDOMinimumTrustLevel int

	TelegramOAuthEnabled bool
	TelegramBotToken     string
	TelegramBotName      string

	WeChatAuthEnabled           bool
	WeChatServerAddress         string
	WeChatServerToken           string
	WeChatAccountQRCodeImageURL string

	TurnstileCheckEnabled bool
	TurnstileSiteKey      string
	TurnstileSecretKey    string

	EmailDomainRestrictionEnabled bool
	EmailAliasRestrictionEnabled  bool
	EmailDomainWhitelist          []string
}

var securityRuntimeConfigMu sync.RWMutex

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func securityRuntimeConfigFromLegacyLocked() SecurityRuntimeConfig {
	return SecurityRuntimeConfig{
		PasswordLoginEnabled:          PasswordLoginEnabled,
		PasswordRegisterEnabled:       PasswordRegisterEnabled,
		EmailVerificationEnabled:      EmailVerificationEnabled,
		RegisterEnabled:               RegisterEnabled,
		GitHubOAuthEnabled:            GitHubOAuthEnabled,
		GitHubClientID:                GitHubClientId,
		GitHubClientSecret:            GitHubClientSecret,
		LinuxDOOAuthEnabled:           LinuxDOOAuthEnabled,
		LinuxDOClientID:               LinuxDOClientId,
		LinuxDOClientSecret:           LinuxDOClientSecret,
		LinuxDOMinimumTrustLevel:      LinuxDOMinimumTrustLevel,
		TelegramOAuthEnabled:          TelegramOAuthEnabled,
		TelegramBotToken:              TelegramBotToken,
		TelegramBotName:               TelegramBotName,
		WeChatAuthEnabled:             WeChatAuthEnabled,
		WeChatServerAddress:           WeChatServerAddress,
		WeChatServerToken:             WeChatServerToken,
		WeChatAccountQRCodeImageURL:   WeChatAccountQRCodeImageURL,
		TurnstileCheckEnabled:         TurnstileCheckEnabled,
		TurnstileSiteKey:              TurnstileSiteKey,
		TurnstileSecretKey:            TurnstileSecretKey,
		EmailDomainRestrictionEnabled: EmailDomainRestrictionEnabled,
		EmailAliasRestrictionEnabled:  EmailAliasRestrictionEnabled,
		EmailDomainWhitelist:          cloneStringSlice(EmailDomainWhitelist),
	}
}

func applySecurityRuntimeConfigToLegacyLocked(cfg SecurityRuntimeConfig) {
	PasswordLoginEnabled = cfg.PasswordLoginEnabled
	PasswordRegisterEnabled = cfg.PasswordRegisterEnabled
	EmailVerificationEnabled = cfg.EmailVerificationEnabled
	RegisterEnabled = cfg.RegisterEnabled
	GitHubOAuthEnabled = cfg.GitHubOAuthEnabled
	GitHubClientId = cfg.GitHubClientID
	GitHubClientSecret = cfg.GitHubClientSecret
	LinuxDOOAuthEnabled = cfg.LinuxDOOAuthEnabled
	LinuxDOClientId = cfg.LinuxDOClientID
	LinuxDOClientSecret = cfg.LinuxDOClientSecret
	LinuxDOMinimumTrustLevel = cfg.LinuxDOMinimumTrustLevel
	TelegramOAuthEnabled = cfg.TelegramOAuthEnabled
	TelegramBotToken = cfg.TelegramBotToken
	TelegramBotName = cfg.TelegramBotName
	WeChatAuthEnabled = cfg.WeChatAuthEnabled
	WeChatServerAddress = cfg.WeChatServerAddress
	WeChatServerToken = cfg.WeChatServerToken
	WeChatAccountQRCodeImageURL = cfg.WeChatAccountQRCodeImageURL
	TurnstileCheckEnabled = cfg.TurnstileCheckEnabled
	TurnstileSiteKey = cfg.TurnstileSiteKey
	TurnstileSecretKey = cfg.TurnstileSecretKey
	EmailDomainRestrictionEnabled = cfg.EmailDomainRestrictionEnabled
	EmailAliasRestrictionEnabled = cfg.EmailAliasRestrictionEnabled
	EmailDomainWhitelist = cloneStringSlice(cfg.EmailDomainWhitelist)
}

// GetSecurityRuntimeConfig returns a detached, coherent security snapshot.
// OptionMapRWMutex is acquired first to fence bulk option publication.  A
// lock-free variant is provided for bootstrap code that already holds the
// option writer lock.
func GetSecurityRuntimeConfig() SecurityRuntimeConfig {
	OptionMapRWMutex.RLock()
	defer OptionMapRWMutex.RUnlock()
	return getSecurityRuntimeConfigWithoutOptionLock()
}

func getSecurityRuntimeConfigWithoutOptionLock() SecurityRuntimeConfig {
	securityRuntimeConfigMu.RLock()
	defer securityRuntimeConfigMu.RUnlock()
	return securityRuntimeConfigFromLegacyLocked()
}

// UpdateSecurityRuntimeConfig publishes all fields changed by update as one
// unit.  The callback receives a private copy and the whitelist is deep-copied
// before publication, so readers can retain their snapshot safely.
func UpdateSecurityRuntimeConfig(update func(*SecurityRuntimeConfig)) {
	if update == nil {
		return
	}
	securityRuntimeConfigMu.Lock()
	defer securityRuntimeConfigMu.Unlock()
	cfg := securityRuntimeConfigFromLegacyLocked()
	update(&cfg)
	applySecurityRuntimeConfigToLegacyLocked(cfg)
}

func quotaConfigFromLegacy() QuotaConfig {
	return QuotaConfig{
		QuotaPerUnit:     QuotaPerUnit,
		PreConsumedQuota: PreConsumedQuota,
	}
}

// GetQuotaConfig returns one coherent billing snapshot.  OptionMapRWMutex is
// acquired first because updateOptionMap publishes all runtime options while
// holding that lock; this prevents a reader from observing a half-applied
// bulk update.  The returned value is detached and safe to retain.
func GetQuotaConfig() QuotaConfig {
	OptionMapRWMutex.RLock()
	defer OptionMapRWMutex.RUnlock()
	quotaConfigMu.RLock()
	defer quotaConfigMu.RUnlock()
	return quotaConfigFromLegacy()
}

func GetQuotaPerUnit() float64 {
	// Keep the scalar accessor independent from OptionMapRWMutex. Some status
	// handlers already hold that read lock while assembling a response; taking
	// it recursively can deadlock when a writer is queued on the RWMutex.
	quotaConfigMu.RLock()
	defer quotaConfigMu.RUnlock()
	return QuotaPerUnit
}

func GetPreConsumedQuota() int {
	quotaConfigMu.RLock()
	defer quotaConfigMu.RUnlock()
	return PreConsumedQuota
}

// SetQuotaPerUnit publishes one billing-unit value while retaining the legacy
// variable for source compatibility.  The option publication fence is held
// by model/updateOptionMap; this setter deliberately only takes the inner
// lock so it can also be used during a publication without recursive locking.
func SetQuotaPerUnit(value float64) {
	quotaConfigMu.Lock()
	QuotaPerUnit = value
	quotaConfigMu.Unlock()
}

// SetPreConsumedQuota publishes the conservative pre-consumption baseline.
func SetPreConsumedQuota(value int) {
	quotaConfigMu.Lock()
	PreConsumedQuota = value
	quotaConfigMu.Unlock()
}

func smtpConfigFromLegacy() SMTPConfig {
	return SMTPConfig{
		Server:             SMTPServer,
		Port:               SMTPPort,
		SSLEnabled:         SMTPSSLEnabled,
		StartTLSEnabled:    SMTPStartTLSEnabled,
		InsecureSkipVerify: SMTPInsecureSkipVerify,
		ForceAuthLogin:     SMTPForceAuthLogin,
		Account:            SMTPAccount,
		From:               SMTPFrom,
		Token:              SMTPToken,
		SystemName:         SystemName,
	}
}

func applySMTPConfigToLegacy(cfg SMTPConfig) {
	SMTPServer = cfg.Server
	SMTPPort = cfg.Port
	SMTPSSLEnabled = cfg.SSLEnabled
	SMTPStartTLSEnabled = cfg.StartTLSEnabled
	SMTPInsecureSkipVerify = cfg.InsecureSkipVerify
	SMTPForceAuthLogin = cfg.ForceAuthLogin
	SMTPAccount = cfg.Account
	SMTPFrom = cfg.From
	SMTPToken = cfg.Token
	SystemName = cfg.SystemName
}

// GetSMTPConfig returns an immutable copy of the currently published SMTP
// settings.  Callers may safely retain the copy for the lifetime of one
// request; subsequent hot updates cannot mutate it underneath the caller.
func GetSMTPConfig() SMTPConfig {
	// OptionMapRWMutex is the publication fence used by UpdateOption(s).  Take
	// it before the per-config lock so a bulk update cannot expose a mixture of
	// fields between successive UpdateSMTPConfig calls.
	OptionMapRWMutex.RLock()
	defer OptionMapRWMutex.RUnlock()
	smtpConfigMu.RLock()
	defer smtpConfigMu.RUnlock()
	return smtpConfigFromLegacy()
}

// UpdateSMTPConfig applies a related SMTP update atomically.  The callback
// receives a private copy and may mutate any fields; publication happens only
// after the callback returns.  This is used by the option hot-update path and
// by environment initialisation.
func UpdateSMTPConfig(update func(*SMTPConfig)) {
	if update == nil {
		return
	}
	smtpConfigMu.Lock()
	defer smtpConfigMu.Unlock()
	cfg := smtpConfigFromLegacy()
	update(&cfg)
	applySMTPConfigToLegacy(cfg)
}

// GetRetryTimes returns the retry limit under the same runtime publication
// discipline as other legacy options.
func GetRetryTimes() int {
	OptionMapRWMutex.RLock()
	defer OptionMapRWMutex.RUnlock()
	smtpConfigMu.RLock()
	defer smtpConfigMu.RUnlock()
	return RetryTimes
}

// GetSystemName returns the currently published site name.  Keeping this
// accessor synchronized matters because the admin option endpoint can change
// the name while status/email handlers are serving requests.
func GetSystemName() string {
	smtpConfigMu.RLock()
	defer smtpConfigMu.RUnlock()
	return SystemName
}

// SetSystemName publishes a new site name under the runtime configuration
// lock.  UpdateOption uses this through UpdateSMTPConfig when publishing a
// complete option snapshot.
func SetSystemName(value string) {
	smtpConfigMu.Lock()
	defer smtpConfigMu.Unlock()
	SystemName = value
}

// SetRetryTimes publishes a new retry limit.  It intentionally leaves the
// exported variable available for source compatibility while ensuring the
// production update path synchronizes with GetRetryTimes readers.
func SetRetryTimes(value int) {
	smtpConfigMu.Lock()
	defer smtpConfigMu.Unlock()
	RetryTimes = value
}
