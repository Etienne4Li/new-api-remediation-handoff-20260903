package common

import (
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/constant"
)

var (
	Port         = flag.Int("port", 3000, "the listening port")
	PrintVersion = flag.Bool("version", false, "print version and exit")
	PrintHelp    = flag.Bool("help", false, "print help and exit")
	LogDir       = flag.String("log-dir", "./logs", "specify the log directory")
)

// Startup configuration limits keep malformed environment values from turning
// periodic workers into busy loops or from creating unbounded allocations.
// The limits are deliberately generous for normal deployments, while still
// being small enough to make duration and pool sizing predictable.
const (
	maxPollingIntervalSeconds      int64 = 60 * 60
	maxSyncFrequencySeconds        int   = 24 * 60 * 60
	maxBatchUpdateIntervalSeconds  int   = 60 * 60
	maxRelayTimeoutSeconds         int64 = 24 * 60 * 60
	maxRelayIdleConnTimeoutSeconds int64 = 24 * 60 * 60
	maxConnectionPoolEntries       int   = 10_000
	maxRateLimitCount              int   = 1_000_000
	maxRateLimitDurationSeconds    int   = 20 * 60
	maxStreamingTimeoutSeconds     int   = 24 * 60 * 60
	maxFileSizeMB                  int   = 4 * 1024
	maxStreamScannerBufferMB       int   = 1024
	maxAnonymousRequestBodyLimitKB int   = 64 * 1024
	maxNotifyLimitCount            int   = 1_000
	maxNotificationDurationMinutes int   = 24 * 60
	maxTaskQueryLimit              int   = 10_000
	maxTaskTimeoutMinutes          int   = 7 * 24 * 60
	maxUserSessionEntries          int   = 100_000
	maxUserSessionWindowSeconds    int   = 30 * 24 * 60 * 60
	maxUserSessionRetentionDays    int   = 365
	maxUserSessionAlertThreshold   int   = 1_000_000_000
)

func printHelp() {
	fmt.Println("NewAPI(Based OneAPI) " + Version + " - The next-generation LLM gateway and AI asset management system supports multiple languages.")
	fmt.Println("Original Project: OneAPI by JustSong - https://github.com/songquanpeng/one-api")
	fmt.Println("Maintainer: QuantumNous - https://github.com/QuantumNous/new-api")
	fmt.Println("Usage: newapi [--port <port>] [--log-dir <log directory>] [--version] [--help]")
}

func InitEnv() {
	flag.Parse()
	// Recompute this on every explicit bootstrap (tests and embedded callers
	// may invoke InitEnv more than once).  A generated UUID fallback is
	// process-local and cannot safely protect credentials across restarts.
	CredentialSecretConfigured = false
	CredentialSecretRuntimeReady = false

	envVersion := os.Getenv("VERSION")
	if envVersion != "" {
		Version = envVersion
	}

	if *PrintVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	if *PrintHelp {
		printHelp()
		os.Exit(0)
	}

	// Prefer *_FILE variants so production deployments can use Docker/K8s
	// secrets without exposing credentials through process environments or
	// `docker inspect`.  Keep the historical environment variables for local
	// development and backwards compatibility.
	sessionSecretConfigured := false
	if sessionSecret, err := GetEnvOrFile("SESSION_SECRET", "SESSION_SECRET_FILE"); err != nil {
		log.Fatal(err)
	} else if sessionSecret != "" {
		ss := sessionSecret
		if ss == "random_string" {
			log.Println("WARNING: SESSION_SECRET is set to the default value 'random_string', please change it to a random string.")
			log.Println("警告：SESSION_SECRET被设置为默认值'random_string'，请修改为随机字符串。")
			log.Fatal("Please set SESSION_SECRET to a random string.")
		} else {
			SessionSecret = ss
			sessionSecretConfigured = true
		}
	}
	if cryptoSecret, err := GetEnvOrFile("CRYPTO_SECRET", "CRYPTO_SECRET_FILE"); err != nil {
		log.Fatal(err)
	} else if cryptoSecret != "" {
		CryptoSecret = cryptoSecret
		CredentialSecretConfigured = true
	} else {
		CryptoSecret = SessionSecret
		CredentialSecretConfigured = sessionSecretConfigured
	}
	if err := InitSessionCookieSettings(); err != nil {
		log.Fatal(err)
	}
	initUserSessionSettings()
	if os.Getenv("SQLITE_PATH") != "" {
		SQLitePath = os.Getenv("SQLITE_PATH")
	}
	if *LogDir != "" {
		var err error
		*LogDir, err = filepath.Abs(*LogDir)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := os.Stat(*LogDir); os.IsNotExist(err) {
			err = os.Mkdir(*LogDir, 0700)
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	// Initialize variables from constants.go that were using environment variables
	DebugEnabled = os.Getenv("DEBUG") == "true"
	MemoryCacheEnabled = os.Getenv("MEMORY_CACHE_ENABLED") == "true"
	IsMasterNode = os.Getenv("NODE_TYPE") != "slave"
	initNodeNameIdentity()
	TLSInsecureSkipVerify = GetEnvOrDefaultBool("TLS_INSECURE_SKIP_VERIFY", false)
	if TLSInsecureSkipVerify {
		if tr, ok := http.DefaultTransport.(*http.Transport); ok && tr != nil {
			if tr.TLSClientConfig != nil {
				tr.TLSClientConfig.InsecureSkipVerify = true
			} else {
				tr.TLSClientConfig = InsecureTLSConfig
			}
		}
	}
	startTLSEnabled := GetEnvOrDefaultBool("SMTP_STARTTLS_ENABLE", GetEnvOrDefaultBool("SMTP_STARTTLS_ENABLED", false))
	insecureSkipVerify := GetEnvOrDefaultBool("SMTP_INSECURE_SKIP_VERIFY", GetEnvOrDefaultBool("SMTP_TLS_INSECURE_SKIP_VERIFY", false))
	UpdateSMTPConfig(func(cfg *SMTPConfig) {
		cfg.StartTLSEnabled = startTLSEnabled
		cfg.InsecureSkipVerify = insecureSkipVerify
	})

	// Parse requestInterval and set RequestInterval. Zero intentionally means
	// no delay for the legacy manual balance loop; all other values are bounded
	// before conversion to avoid duration overflow.
	RequestInterval = GetEnvOrDefaultDurationSeconds(
		"POLLING_INTERVAL", 0, 0, maxPollingIntervalSeconds,
	)
	requestInterval = int(RequestInterval / time.Second)

	// Initialize variables with GetEnvOrDefault
	SyncFrequency = GetEnvOrDefaultBounded("SYNC_FREQUENCY", 60, 1, maxSyncFrequencySeconds)
	BatchUpdateInterval = GetEnvOrDefaultBounded("BATCH_UPDATE_INTERVAL", 5, 1, maxBatchUpdateIntervalSeconds)
	// Publish the batch-mode choice before database-backed workers and request
	// handlers are started. Previously main assigned this flag only after
	// launching background goroutines, so an enabled deployment could process
	// an initial window with the wrong accounting mode and race with readers.
	BatchUpdateEnabled = GetEnvOrDefaultBool("BATCH_UPDATE_ENABLED", false)
	RelayTimeout = int(GetEnvOrDefaultDurationSeconds("RELAY_TIMEOUT", 0, 0, maxRelayTimeoutSeconds) / time.Second)
	RelayIdleConnTimeout = int(GetEnvOrDefaultDurationSeconds("RELAY_IDLE_CONN_TIMEOUT", 90, 0, maxRelayIdleConnTimeoutSeconds) / time.Second)
	RelayMaxIdleConns = GetEnvOrDefaultBounded("RELAY_MAX_IDLE_CONNS", 500, 0, maxConnectionPoolEntries)
	RelayMaxIdleConnsPerHost = GetEnvOrDefaultBounded("RELAY_MAX_IDLE_CONNS_PER_HOST", 100, 0, maxConnectionPoolEntries)

	// Initialize string variables with GetEnvOrDefaultString
	GeminiSafetySetting = GetEnvOrDefaultString("GEMINI_SAFETY_SETTING", "BLOCK_NONE")
	CohereSafetySetting = GetEnvOrDefaultString("COHERE_SAFETY_SETTING", "NONE")

	// Initialize rate limit variables
	GlobalApiRateLimitEnable = GetEnvOrDefaultBool("GLOBAL_API_RATE_LIMIT_ENABLE", true)
	GlobalApiRateLimitNum = GetEnvOrDefaultBounded("GLOBAL_API_RATE_LIMIT", 360, 1, maxRateLimitCount)
	GlobalApiRateLimitDuration = int64(GetEnvOrDefaultBounded("GLOBAL_API_RATE_LIMIT_DURATION", 180, 1, maxRateLimitDurationSeconds))

	GlobalWebRateLimitEnable = GetEnvOrDefaultBool("GLOBAL_WEB_RATE_LIMIT_ENABLE", true)
	GlobalWebRateLimitNum = GetEnvOrDefaultBounded("GLOBAL_WEB_RATE_LIMIT", 120, 1, maxRateLimitCount)
	GlobalWebRateLimitDuration = int64(GetEnvOrDefaultBounded("GLOBAL_WEB_RATE_LIMIT_DURATION", 180, 1, maxRateLimitDurationSeconds))

	CriticalRateLimitEnable = GetEnvOrDefaultBool("CRITICAL_RATE_LIMIT_ENABLE", true)
	CriticalRateLimitNum = GetEnvOrDefaultBounded("CRITICAL_RATE_LIMIT", 20, 1, maxRateLimitCount)
	CriticalRateLimitDuration = int64(GetEnvOrDefaultBounded("CRITICAL_RATE_LIMIT_DURATION", 20*60, 1, maxRateLimitDurationSeconds))

	SearchRateLimitEnable = GetEnvOrDefaultBool("SEARCH_RATE_LIMIT_ENABLE", true)
	SearchRateLimitNum = GetEnvOrDefaultBounded("SEARCH_RATE_LIMIT", 10, 1, maxRateLimitCount)
	SearchRateLimitDuration = int64(GetEnvOrDefaultBounded("SEARCH_RATE_LIMIT_DURATION", 60, 1, maxRateLimitDurationSeconds))
	initConstantEnv()
	CredentialSecretRuntimeReady = true
}

func initUserSessionSettings() {
	UserSessionActiveLimit = positiveUserSessionEnvBounded("USER_SESSION_ACTIVE_LIMIT", DefaultUserSessionActiveLimit, maxUserSessionEntries)
	UserSessionIssuanceLimit = positiveUserSessionEnvBounded("USER_SESSION_ISSUANCE_LIMIT", DefaultUserSessionIssuanceLimit, maxUserSessionEntries)
	UserSessionIssuanceWindowSeconds = int64(positiveUserSessionEnvBounded("USER_SESSION_ISSUANCE_WINDOW_SECONDS", DefaultUserSessionIssuanceWindowSeconds, maxUserSessionWindowSeconds))
	UserSessionRevokedRetentionDays = positiveUserSessionEnvBounded("USER_SESSION_REVOKED_RETENTION_DAYS", DefaultUserSessionRevokedRetentionDays, maxUserSessionRetentionDays)
	UserSessionHourlyAlertThreshold = positiveUserSessionEnvBounded("USER_SESSION_HOURLY_ALERT_THRESHOLD", DefaultUserSessionHourlyAlertThreshold, maxUserSessionAlertThreshold)

	const secondsPerDay = 24 * 60 * 60
	if int64(UserSessionRevokedRetentionDays) > math.MaxInt64/secondsPerDay {
		SysError(fmt.Sprintf(
			"USER_SESSION_REVOKED_RETENTION_DAYS is too large, using default value: %d",
			DefaultUserSessionRevokedRetentionDays,
		))
		UserSessionRevokedRetentionDays = DefaultUserSessionRevokedRetentionDays
	}
	retentionSeconds := int64(UserSessionRevokedRetentionDays) * secondsPerDay
	if UserSessionIssuanceWindowSeconds > retentionSeconds {
		configuredWindow := UserSessionIssuanceWindowSeconds
		UserSessionIssuanceWindowSeconds = retentionSeconds
		SysError(fmt.Sprintf(
			"USER_SESSION_ISSUANCE_WINDOW_SECONDS exceeds revoked retention; configured_window_seconds=%d revoked_retention_seconds=%d effective_window_seconds=%d",
			configuredWindow,
			retentionSeconds,
			UserSessionIssuanceWindowSeconds,
		))
	}
}

func positiveUserSessionEnv(name string, fallback int) int {
	return positiveUserSessionEnvBounded(name, fallback, int(^uint(0)>>1))
}

func positiveUserSessionEnvBounded(name string, fallback, maxValue int) int {
	value := GetEnvOrDefaultBounded(name, fallback, 1, maxValue)
	if value <= 0 {
		// Keep this branch for callers that pass an invalid fallback/range; normal
		// bounded calls already handle non-positive values above.
		SysError(fmt.Sprintf("%s must be positive, using default value: %d", name, fallback))
		return fallback
	}
	return value
}

func initConstantEnv() {
	constant.StreamingTimeout = GetEnvOrDefaultBounded("STREAMING_TIMEOUT", 300, 1, maxStreamingTimeoutSeconds)
	constant.DifyDebug = GetEnvOrDefaultBool("DIFY_DEBUG", true)
	constant.MaxFileDownloadMB = GetEnvOrDefaultBounded("MAX_FILE_DOWNLOAD_MB", 64, 1, maxFileSizeMB)
	constant.StreamScannerMaxBufferMB = GetEnvOrDefaultBounded("STREAM_SCANNER_MAX_BUFFER_MB", 128, 1, maxStreamScannerBufferMB)
	// MaxRequestBodyMB 请求体最大大小（解压后），用于防止超大请求/zip bomb导致内存暴涨
	constant.MaxRequestBodyMB = GetEnvOrDefaultBounded("MAX_REQUEST_BODY_MB", 128, 1, maxFileSizeMB)
	constant.AnonymousRequestBodyLimitKB = GetEnvOrDefaultBounded("ANONYMOUS_REQUEST_BODY_LIMIT_KB", 512, 1, maxAnonymousRequestBodyLimitKB)
	// ForceStreamOption 覆盖请求参数，强制返回usage信息
	constant.ForceStreamOption = GetEnvOrDefaultBool("FORCE_STREAM_OPTION", true)
	constant.CountToken = GetEnvOrDefaultBool("CountToken", true)
	constant.GetMediaToken = GetEnvOrDefaultBool("GET_MEDIA_TOKEN", true)
	constant.GetMediaTokenNotStream = GetEnvOrDefaultBool("GET_MEDIA_TOKEN_NOT_STREAM", false)
	constant.UpdateTask = GetEnvOrDefaultBool("UPDATE_TASK", true)
	constant.AzureDefaultAPIVersion = GetEnvOrDefaultString("AZURE_DEFAULT_API_VERSION", "2025-04-01-preview")
	constant.NotifyLimitCount = GetEnvOrDefaultBounded("NOTIFY_LIMIT_COUNT", 2, 1, maxNotifyLimitCount)
	constant.NotificationLimitDurationMinute = GetEnvOrDefaultBounded("NOTIFICATION_LIMIT_DURATION_MINUTE", 10, 1, maxNotificationDurationMinutes)
	// GenerateDefaultToken 是否生成初始令牌，默认关闭。
	constant.GenerateDefaultToken = GetEnvOrDefaultBool("GENERATE_DEFAULT_TOKEN", false)
	// 是否启用错误日志
	constant.ErrorLogEnabled = GetEnvOrDefaultBool("ERROR_LOG_ENABLED", false)
	// 任务轮询时查询的最大数量
	constant.TaskQueryLimit = GetEnvOrDefaultBounded("TASK_QUERY_LIMIT", 1000, 1, maxTaskQueryLimit)
	// 异步任务超时时间（分钟），超过此时间未完成的任务将被标记为失败并退款。0 表示禁用。
	constant.TaskTimeoutMinutes = GetEnvOrDefaultBounded("TASK_TIMEOUT_MINUTES", 1440, 0, maxTaskTimeoutMinutes)

	soraPatchStr := GetEnvOrDefaultString("TASK_PRICE_PATCH", "")
	if soraPatchStr != "" {
		var taskPricePatches []string
		soraPatches := strings.Split(soraPatchStr, ",")
		for _, patch := range soraPatches {
			trimmedPatch := strings.TrimSpace(patch)
			if trimmedPatch != "" {
				taskPricePatches = append(taskPricePatches, trimmedPatch)
			}
		}
		constant.TaskPricePatches = taskPricePatches
	}

	// Initialize trusted redirect domains for URL validation
	trustedDomainsStr := GetEnvOrDefaultString("TRUSTED_REDIRECT_DOMAINS", "")
	var trustedDomains []string
	domains := strings.Split(trustedDomainsStr, ",")
	for _, domain := range domains {
		trimmedDomain := strings.TrimSpace(domain)
		if trimmedDomain != "" {
			// Normalize domain to lowercase
			trustedDomains = append(trustedDomains, strings.ToLower(trimmedDomain))
		}
	}
	constant.TrustedRedirectDomains = trustedDomains
}
