package model

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/performance_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Option struct {
	Key   string `json:"key" gorm:"primaryKey"`
	Value string `json:"value" gorm:"type:text"`
}

// optionMemoryState captures the pre-update runtime value so a transaction
// can restore memory before releasing the publication fence when its publish
// or commit fails.
type optionMemoryState struct {
	// mapExists/mapValue preserve the exact OptionMap representation. The map
	// can be sparse during bootstrap, so this is intentionally separate from
	// the runtime snapshot below.
	mapExists bool
	mapValue  string

	// runtimeExists/runtimeValue preserve the external setting that an option
	// mutates (for example a registered config object or a payment snapshot).
	// Restoring only OptionMap is insufficient: a failed bulk publish can have
	// already changed the live object even when its map key was absent.
	runtimeExists bool
	runtimeValue  string
}

type optionPublication struct {
	key        string
	value      string
	config     interface{}
	configName string
	configMap  map[string]string
	configKeys []string
}

func AllOption() ([]*Option, error) {
	var options []*Option
	if DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	var err error
	err = DB.Find(&options).Error
	return options, err
}

var optionUpdateMutex sync.Mutex

func InitOptionMap() error {
	// Initialization and periodic/manual updates share the same writer lock.
	// Without this, a sync goroutine can publish a stale database snapshot
	// while the bootstrap map is still being built (or while an admin update is
	// in flight), leaving the process with a split configuration.
	optionUpdateMutex.Lock()
	defer optionUpdateMutex.Unlock()
	options, err := loadValidatedOptionsFromDatabase()
	if err != nil {
		return fmt.Errorf("load options from database: %w", err)
	}
	previous := captureOptionMemoryStates(options)
	stripeConfig := setting.GetStripeConfig()
	creemConfig := setting.GetCreemConfig()
	waffoConfig := setting.GetWaffoConfig()
	waffoPancakeConfig := setting.GetWaffoPancakeConfig()
	systemConfig := system_setting.GetRuntimeConfig()
	midjourneyConfig := setting.GetMidjourneyConfig()
	sensitiveConfig := setting.GetSensitiveConfig()
	paymentRuntimeConfig := operation_setting.GetPaymentRuntimeConfig()
	operationRuntimeConfig := operation_setting.GetOperationRuntimeConfig()
	rateLimitConfig := setting.GetModelRequestRateLimitConfig()
	smtpConfig := common.GetSMTPConfig()
	quotaConfig := common.GetQuotaConfig()
	securityConfig := common.GetSecurityRuntimeConfig()
	retryTimes := common.GetRetryTimes()
	generalRuntimeConfig := common.GetGeneralRuntimeConfig()

	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	previousOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)

	// 添加原有的系统配置
	common.OptionMap["FileUploadPermission"] = strconv.Itoa(generalRuntimeConfig.FileUploadPermission)
	common.OptionMap["FileDownloadPermission"] = strconv.Itoa(generalRuntimeConfig.FileDownloadPermission)
	common.OptionMap["ImageUploadPermission"] = strconv.Itoa(generalRuntimeConfig.ImageUploadPermission)
	common.OptionMap["ImageDownloadPermission"] = strconv.Itoa(generalRuntimeConfig.ImageDownloadPermission)
	common.OptionMap["PasswordLoginEnabled"] = strconv.FormatBool(securityConfig.PasswordLoginEnabled)
	common.OptionMap["PasswordRegisterEnabled"] = strconv.FormatBool(securityConfig.PasswordRegisterEnabled)
	common.OptionMap["EmailVerificationEnabled"] = strconv.FormatBool(securityConfig.EmailVerificationEnabled)
	common.OptionMap["GitHubOAuthEnabled"] = strconv.FormatBool(securityConfig.GitHubOAuthEnabled)
	common.OptionMap["LinuxDOOAuthEnabled"] = strconv.FormatBool(securityConfig.LinuxDOOAuthEnabled)
	common.OptionMap["TelegramOAuthEnabled"] = strconv.FormatBool(securityConfig.TelegramOAuthEnabled)
	common.OptionMap["WeChatAuthEnabled"] = strconv.FormatBool(securityConfig.WeChatAuthEnabled)
	common.OptionMap["TurnstileCheckEnabled"] = strconv.FormatBool(securityConfig.TurnstileCheckEnabled)
	common.OptionMap["RegisterEnabled"] = strconv.FormatBool(securityConfig.RegisterEnabled)
	common.OptionMap["AutomaticDisableChannelEnabled"] = strconv.FormatBool(generalRuntimeConfig.AutomaticDisableChannelEnabled)
	common.OptionMap["AutomaticEnableChannelEnabled"] = strconv.FormatBool(generalRuntimeConfig.AutomaticEnableChannelEnabled)
	common.OptionMap["LogConsumeEnabled"] = strconv.FormatBool(common.IsLogConsumeEnabled())
	common.OptionMap["DisplayInCurrencyEnabled"] = strconv.FormatBool(generalRuntimeConfig.DisplayInCurrencyEnabled)
	common.OptionMap["DisplayTokenStatEnabled"] = strconv.FormatBool(generalRuntimeConfig.DisplayTokenStatEnabled)
	common.OptionMap["DrawingEnabled"] = strconv.FormatBool(generalRuntimeConfig.DrawingEnabled)
	common.OptionMap["TaskEnabled"] = strconv.FormatBool(generalRuntimeConfig.TaskEnabled)
	common.OptionMap["DataExportEnabled"] = strconv.FormatBool(generalRuntimeConfig.DataExportEnabled)
	common.OptionMap["ChannelDisableThreshold"] = strconv.FormatFloat(generalRuntimeConfig.ChannelDisableThreshold, 'f', -1, 64)
	common.OptionMap["EmailDomainRestrictionEnabled"] = strconv.FormatBool(securityConfig.EmailDomainRestrictionEnabled)
	common.OptionMap["EmailAliasRestrictionEnabled"] = strconv.FormatBool(securityConfig.EmailAliasRestrictionEnabled)
	common.OptionMap["EmailDomainWhitelist"] = strings.Join(securityConfig.EmailDomainWhitelist, ",")
	common.OptionMap["SMTPServer"] = ""
	common.OptionMap["SMTPFrom"] = ""
	common.OptionMap["SMTPPort"] = strconv.Itoa(smtpConfig.Port)
	common.OptionMap["SMTPAccount"] = ""
	common.OptionMap["SMTPToken"] = ""
	common.OptionMap["SMTPSSLEnabled"] = strconv.FormatBool(smtpConfig.SSLEnabled)
	common.OptionMap["SMTPStartTLSEnabled"] = strconv.FormatBool(smtpConfig.StartTLSEnabled)
	common.OptionMap["SMTPInsecureSkipVerify"] = strconv.FormatBool(smtpConfig.InsecureSkipVerify)
	common.OptionMap["SMTPForceAuthLogin"] = strconv.FormatBool(smtpConfig.ForceAuthLogin)
	common.OptionMap["Notice"] = ""
	common.OptionMap["About"] = ""
	common.OptionMap["HomePageContent"] = ""
	common.OptionMap["Footer"] = generalRuntimeConfig.Footer
	common.OptionMap["SystemName"] = smtpConfig.SystemName
	common.OptionMap["Logo"] = generalRuntimeConfig.Logo
	common.OptionMap["ServerAddress"] = ""
	common.OptionMap["WorkerUrl"] = systemConfig.WorkerURL
	common.OptionMap["WorkerValidKey"] = systemConfig.WorkerValidKey
	common.OptionMap["WorkerAllowHttpImageRequestEnabled"] = strconv.FormatBool(systemConfig.WorkerAllowHttpImageRequestEnabled)
	common.OptionMap["PayAddress"] = ""
	common.OptionMap["CustomCallbackAddress"] = ""
	common.OptionMap["EpayId"] = ""
	common.OptionMap["EpayKey"] = ""
	common.OptionMap["Price"] = strconv.FormatFloat(paymentRuntimeConfig.Price, 'f', -1, 64)
	common.OptionMap["USDExchangeRate"] = strconv.FormatFloat(paymentRuntimeConfig.USDExchangeRate, 'f', -1, 64)
	common.OptionMap["MinTopUp"] = strconv.Itoa(paymentRuntimeConfig.MinTopUp)
	common.OptionMap["StripeMinTopUp"] = strconv.Itoa(stripeConfig.MinTopUp)
	common.OptionMap["StripeApiSecret"] = stripeConfig.ApiSecret
	common.OptionMap["StripeWebhookSecret"] = stripeConfig.WebhookSecret
	common.OptionMap["StripeAccountId"] = stripeConfig.AccountID
	common.OptionMap["StripePriceId"] = stripeConfig.PriceID
	common.OptionMap["StripeUnitPrice"] = strconv.FormatFloat(stripeConfig.UnitPrice, 'f', -1, 64)
	common.OptionMap["StripeCurrency"] = stripeConfig.Currency
	common.OptionMap["StripePromotionCodesEnabled"] = strconv.FormatBool(stripeConfig.PromotionCodesEnabled)
	common.OptionMap["CreemApiKey"] = creemConfig.ApiKey
	common.OptionMap["CreemProducts"] = creemConfig.Products
	common.OptionMap["CreemTestMode"] = strconv.FormatBool(creemConfig.TestMode)
	common.OptionMap["CreemWebhookSecret"] = creemConfig.WebhookSecret
	common.OptionMap["WaffoEnabled"] = strconv.FormatBool(waffoConfig.Enabled)
	common.OptionMap["WaffoApiKey"] = waffoConfig.ApiKey
	common.OptionMap["WaffoPrivateKey"] = waffoConfig.PrivateKey
	common.OptionMap["WaffoPublicCert"] = waffoConfig.PublicCert
	common.OptionMap["WaffoSandboxPublicCert"] = waffoConfig.SandboxPublicCert
	common.OptionMap["WaffoSandboxApiKey"] = waffoConfig.SandboxApiKey
	common.OptionMap["WaffoSandboxPrivateKey"] = waffoConfig.SandboxPrivateKey
	common.OptionMap["WaffoSandbox"] = strconv.FormatBool(waffoConfig.Sandbox)
	common.OptionMap["WaffoMerchantId"] = waffoConfig.MerchantID
	common.OptionMap["WaffoNotifyUrl"] = waffoConfig.NotifyURL
	common.OptionMap["WaffoReturnUrl"] = waffoConfig.ReturnURL
	common.OptionMap["WaffoSubscriptionReturnUrl"] = waffoConfig.SubscriptionReturnURL
	common.OptionMap["WaffoCurrency"] = waffoConfig.Currency
	common.OptionMap["WaffoUnitPrice"] = strconv.FormatFloat(waffoConfig.UnitPrice, 'f', -1, 64)
	common.OptionMap["WaffoMinTopUp"] = strconv.Itoa(waffoConfig.MinTopUp)
	common.OptionMap["WaffoPayMethods"] = setting.WaffoPayMethods2JsonString()
	common.OptionMap["WaffoPancakeMerchantID"] = waffoPancakeConfig.MerchantID
	common.OptionMap["WaffoPancakePrivateKey"] = waffoPancakeConfig.PrivateKey
	common.OptionMap["WaffoPancakeReturnURL"] = waffoPancakeConfig.ReturnURL
	common.OptionMap["WaffoPancakeUnitPrice"] = strconv.FormatFloat(waffoPancakeConfig.UnitPrice, 'f', -1, 64)
	common.OptionMap["WaffoPancakeMinTopUp"] = strconv.Itoa(waffoPancakeConfig.MinTopUp)
	common.OptionMap["WaffoPancakeStoreID"] = waffoPancakeConfig.StoreID
	common.OptionMap["WaffoPancakeProductID"] = waffoPancakeConfig.ProductID
	common.OptionMap["TopupGroupRatio"] = common.TopupGroupRatio2JSONString()
	common.OptionMap["Chats"] = setting.Chats2JsonString()
	common.OptionMap["AutoGroups"] = setting.AutoGroups2JsonString()
	common.OptionMap["DefaultUseAutoGroup"] = strconv.FormatBool(setting.GetDefaultUseAutoGroup())
	common.OptionMap["MaxTokenAutoGroups"] = strconv.Itoa(setting.GetMaxTokenAutoGroups())
	common.OptionMap["PayMethods"] = operation_setting.PayMethods2JsonString()
	common.OptionMap["GitHubClientId"] = ""
	common.OptionMap["GitHubClientSecret"] = ""
	common.OptionMap["TelegramBotToken"] = ""
	common.OptionMap["TelegramBotName"] = ""
	common.OptionMap["WeChatServerAddress"] = ""
	common.OptionMap["WeChatServerToken"] = ""
	common.OptionMap["WeChatAccountQRCodeImageURL"] = ""
	common.OptionMap["TurnstileSiteKey"] = ""
	common.OptionMap["TurnstileSecretKey"] = ""
	common.OptionMap["QuotaForNewUser"] = strconv.Itoa(generalRuntimeConfig.QuotaForNewUser)
	common.OptionMap["QuotaForInviter"] = strconv.Itoa(generalRuntimeConfig.QuotaForInviter)
	common.OptionMap["QuotaForInvitee"] = strconv.Itoa(generalRuntimeConfig.QuotaForInvitee)
	common.OptionMap["QuotaRemindThreshold"] = strconv.Itoa(generalRuntimeConfig.QuotaRemindThreshold)
	common.OptionMap["PreConsumedQuota"] = strconv.Itoa(quotaConfig.PreConsumedQuota)
	common.OptionMap["ModelRequestRateLimitCount"] = strconv.Itoa(rateLimitConfig.Count)
	common.OptionMap["ModelRequestRateLimitDurationMinutes"] = strconv.Itoa(rateLimitConfig.DurationMinutes)
	common.OptionMap["ModelRequestRateLimitSuccessCount"] = strconv.Itoa(rateLimitConfig.SuccessCount)
	common.OptionMap["ModelRequestRateLimitGroup"] = setting.ModelRequestRateLimitGroup2JSONString()
	common.OptionMap["ModelRatio"] = ratio_setting.ModelRatio2JSONString()
	common.OptionMap["ModelPrice"] = ratio_setting.ModelPrice2JSONString()
	common.OptionMap["CacheRatio"] = ratio_setting.CacheRatio2JSONString()
	common.OptionMap["CreateCacheRatio"] = ratio_setting.CreateCacheRatio2JSONString()
	common.OptionMap["GroupRatio"] = ratio_setting.GroupRatio2JSONString()
	common.OptionMap["GroupGroupRatio"] = ratio_setting.GroupGroupRatio2JSONString()
	common.OptionMap["UserUsableGroups"] = setting.UserUsableGroups2JSONString()
	common.OptionMap["CompletionRatio"] = ratio_setting.CompletionRatio2JSONString()
	common.OptionMap["ImageRatio"] = ratio_setting.ImageRatio2JSONString()
	common.OptionMap["AudioRatio"] = ratio_setting.AudioRatio2JSONString()
	common.OptionMap["AudioCompletionRatio"] = ratio_setting.AudioCompletionRatio2JSONString()
	common.OptionMap["TopUpLink"] = generalRuntimeConfig.TopUpLink
	//common.OptionMap["ChatLink"] = common.ChatLink
	//common.OptionMap["ChatLink2"] = common.ChatLink2
	common.OptionMap["QuotaPerUnit"] = strconv.FormatFloat(quotaConfig.QuotaPerUnit, 'f', -1, 64)
	common.OptionMap["RetryTimes"] = strconv.Itoa(retryTimes)
	common.OptionMap["DataExportInterval"] = strconv.Itoa(generalRuntimeConfig.DataExportInterval)
	common.OptionMap["DataExportDefaultTime"] = generalRuntimeConfig.DataExportDefaultTime
	common.OptionMap["DefaultCollapseSidebar"] = strconv.FormatBool(generalRuntimeConfig.DefaultCollapseSidebar)
	common.OptionMap["MjNotifyEnabled"] = strconv.FormatBool(midjourneyConfig.NotifyEnabled)
	common.OptionMap["MjAccountFilterEnabled"] = strconv.FormatBool(midjourneyConfig.AccountFilterEnabled)
	common.OptionMap["MjModeClearEnabled"] = strconv.FormatBool(midjourneyConfig.ModeClearEnabled)
	common.OptionMap["MjForwardUrlEnabled"] = strconv.FormatBool(midjourneyConfig.ForwardURLEnabled)
	common.OptionMap["MjActionCheckSuccessEnabled"] = strconv.FormatBool(midjourneyConfig.ActionCheckSuccessEnabled)
	common.OptionMap["CheckSensitiveEnabled"] = strconv.FormatBool(sensitiveConfig.CheckEnabled)
	common.OptionMap["DemoSiteEnabled"] = strconv.FormatBool(operationRuntimeConfig.DemoSiteEnabled)
	common.OptionMap["SelfUseModeEnabled"] = strconv.FormatBool(operationRuntimeConfig.SelfUseModeEnabled)
	common.OptionMap["ModelRequestRateLimitEnabled"] = strconv.FormatBool(rateLimitConfig.Enabled)
	common.OptionMap["CheckSensitiveOnPromptEnabled"] = strconv.FormatBool(sensitiveConfig.CheckOnPromptEnabled)
	common.OptionMap["StopOnSensitiveEnabled"] = strconv.FormatBool(sensitiveConfig.StopOnSensitive)
	common.OptionMap["SensitiveWords"] = strings.Join(sensitiveConfig.SensitiveWords, "\n")
	common.OptionMap["StreamCacheQueueLength"] = strconv.Itoa(sensitiveConfig.StreamCacheQueueLen)
	common.OptionMap["AutomaticDisableKeywords"] = strings.Join(operationRuntimeConfig.AutomaticDisableKeywords, "\n")
	common.OptionMap["AutomaticDisableStatusCodes"] = operation_setting.AutomaticDisableStatusCodesToStringWithoutOptionLock()
	common.OptionMap["AutomaticRetryStatusCodes"] = operation_setting.AutomaticRetryStatusCodesToStringWithoutOptionLock()
	common.OptionMap["ExposeRatioEnabled"] = strconv.FormatBool(ratio_setting.IsExposeRatioEnabled())

	// 自动添加所有注册的模型配置
	modelConfigs := config.GlobalConfig.ExportAllConfigs()
	for k, v := range modelConfigs {
		common.OptionMap[k] = v
	}

	if err := publishValidatedOptionsLocked(options, previous); err != nil {
		// The candidate map has never been visible to readers because the write
		// lock is still held. Restore the exact old map after rolling back any
		// runtime settings changed by the failed publication.
		common.OptionMap = previousOptionMap
		return fmt.Errorf("publish options from database: %w", err)
	}
	return nil
}

func loadOptionsFromDatabase() error {
	optionUpdateMutex.Lock()
	defer optionUpdateMutex.Unlock()
	return loadOptionsFromDatabaseLocked()
}

// loadOptionsFromDatabaseLocked reads and publishes the persisted options.
// The caller must hold optionUpdateMutex. Keeping the lock across the read
// and publish phases prevents a periodic sync from overwriting a newer
// UpdateOption call with an older snapshot.
func loadOptionsFromDatabaseLocked() error {
	options, err := loadValidatedOptionsFromDatabase()
	if err != nil {
		return err
	}
	previous := captureOptionMemoryStates(options)

	// Hold the map lock for the entire publication pass.  Readers that use
	// OptionMap therefore observe either the old snapshot or the fully applied
	// snapshot, never an intermediate mix of payment/auth settings.
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	return publishValidatedOptionsLocked(options, previous)
}

// loadValidatedOptionsFromDatabase builds the database half of an option
// snapshot without changing live runtime state. Callers may publish only after
// this function succeeds, so an unavailable database or one malformed row can
// never replace the current map with defaults.
func loadValidatedOptionsFromDatabase() ([]*Option, error) {
	options, err := AllOption()
	if err != nil {
		return nil, err
	}
	// Preflight every persisted option before publishing any of them. A single
	// malformed value must not leave a partially refreshed process: the next
	// request would otherwise observe a mixture of old and new pricing/auth
	// settings until the next restart.
	sort.Slice(options, func(i, j int) bool {
		return options[i].Key < options[j].Key
	})
	var validationErr error
	for _, option := range options {
		if option == nil {
			continue
		}
		// Chats is user-authored JSON and older installations may contain
		// malformed entries or templates that are no longer safe to publish.
		// Sanitize that one legacy collection in-memory before the all-options
		// preflight so it cannot prevent unrelated settings from loading. Do not
		// write the database from a slave (or blindly overwrite a concurrent
		// admin update); the next master-side option update can persist the clean
		// value through the normal CAS/upsert path.
		if option.Key == "Chats" {
			sanitizeLoadedChatsOption(option)
		}
		if err := validateOptionValue(option.Key, option.Value); err != nil {
			if validationErr == nil {
				validationErr = fmt.Errorf("%s: %w", option.Key, err)
			} else {
				validationErr = fmt.Errorf("%v; %s: %w", validationErr, option.Key, err)
			}
		}
	}
	if validationErr != nil {
		return nil, validationErr
	}
	return options, nil
}

func captureOptionMemoryStates(options []*Option) map[string]optionMemoryState {
	previous := make(map[string]optionMemoryState, len(options))
	for _, option := range options {
		if option == nil {
			continue
		}
		previous[option.Key] = captureOptionMemoryState(option.Key)
	}
	return previous
}

// publishValidatedOptionsLocked applies a fully preflighted snapshot and
// restores all touched runtime settings if a custom updater still fails during
// publication. The caller must hold common.OptionMapRWMutex for the whole call.
func publishValidatedOptionsLocked(options []*Option, previous map[string]optionMemoryState) error {
	// Preserve the first-seen order of options while collecting all fields for
	// one registered module. A module is therefore committed exactly once, but
	// unrelated legacy options retain their existing ordering semantics.
	publications := make([]optionPublication, 0, len(options))
	groupIndex := make(map[string]int)
	for _, option := range options {
		if option == nil {
			continue
		}
		key := option.Key
		value := normalizeOptionValue(key, option.Value)
		if key != operation_setting.ToolPriceOptionKey {
			if parts := strings.SplitN(key, ".", 2); len(parts) == 2 {
				if cfg := config.GlobalConfig.Get(parts[0]); cfg != nil {
					index, ok := groupIndex[parts[0]]
					if !ok {
						index = len(publications)
						groupIndex[parts[0]] = index
						publications = append(publications, optionPublication{
							config: cfg, configName: parts[0], configMap: make(map[string]string),
						})
					}
					publications[index].configMap[parts[1]] = value
					publications[index].configKeys = append(publications[index].configKeys, key)
					continue
				}
			}
		}
		publications = append(publications, optionPublication{key: key, value: value})
	}

	for _, item := range publications {
		var err error
		if item.config != nil {
			err = config.UpdateConfigFromMap(item.config, item.configMap)
			if err == nil {
				switch item.configName {
				case "performance_setting":
					performance_setting.UpdateAndSync()
				case "billing_setting":
					InvalidatePricingCache()
					ratio_setting.InvalidateExposedDataCache()
				}
			}
			if err == nil {
				for _, key := range item.configKeys {
					parts := strings.SplitN(key, ".", 2)
					common.OptionMap[key] = item.configMap[parts[1]]
				}
			}
		} else {
			err = updateOptionMapLocked(item.key, item.value)
		}
		if err == nil {
			continue
		}
		common.SysLog("failed to update option map: " + err.Error())
		// Restore in deterministic reverse order. The failed option/group is
		// included too: a custom setter may have changed state before returning.
		keys := make([]string, 0, len(previous))
		for key := range previous {
			keys = append(keys, key)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
		var rollbackErr error
		for _, key := range keys {
			state := previous[key]
			if restoreErr := restoreOptionMemoryStateLocked(key, state); restoreErr != nil && rollbackErr == nil {
				rollbackErr = restoreErr
			}
		}
		if rollbackErr != nil {
			return fmt.Errorf("%s: %w (runtime rollback: %v)", publicationErrorKey(item), err, rollbackErr)
		}
		return fmt.Errorf("%s: %w", publicationErrorKey(item), err)
	}
	return nil
}

func publicationErrorKey(item optionPublication) string {
	if item.configName != "" {
		return item.configName
	}
	return item.key
}

// sanitizeLoadedChatsOption applies the safe in-memory representation and,
// on a master node, opportunistically repairs the persisted row with a
// compare-and-swap predicate. A failed CAS never overwrites a concurrent
// administrator update; the latest value is re-read before publication.
func sanitizeLoadedChatsOption(option *Option) {
	if option == nil || option.Key != "Chats" {
		return
	}
	original := option.Value
	sanitized := setting.SanitizeChatsJSON(original)
	if sanitized == original {
		return
	}
	common.SysLog("sanitized legacy Chats option while loading")
	if common.IsMasterNode && DB != nil {
		result := DB.Model(&Option{Key: option.Key}).
			Where("key = ? AND value = ?", option.Key, original).
			Update("value", sanitized)
		if result.Error != nil {
			common.SysError("failed to persist sanitized Chats option: " + result.Error.Error())
		} else if result.RowsAffected == 0 {
			// Another process changed the option after AllOption read it. Keep
			// that writer's value authoritative rather than publishing stale data.
			var current Option
			if err := DB.First(&current, "key = ?", option.Key).Error; err == nil {
				option.Value = setting.SanitizeChatsJSON(current.Value)
				return
			} else {
				common.SysError("failed to re-read Chats after concurrent update: " + err.Error())
			}
		}
	}
	option.Value = sanitized
}

func SyncOptions(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		common.SysLog("syncing options from database")
		if err := loadOptionsFromDatabase(); err != nil {
			common.SysError("failed to sync options from database: " + err.Error())
		}
	}
}

func validateOptionValue(key string, value string) error {
	if key == operation_setting.ToolPriceOptionKey {
		return operation_setting.ValidateToolPricesJSON(value)
	}
	if key == operation_setting.ChannelTestConcurrencyOptionKey {
		return operation_setting.ValidateChannelTestConcurrency(value)
	}
	if key == "MaxTokenAutoGroups" {
		return setting.ValidateMaxTokenAutoGroups(value)
	}
	if strings.HasSuffix(key, "Enabled") || key == "DefaultCollapseSidebar" ||
		key == "DefaultUseAutoGroup" || key == "SMTPForceAuthLogin" || key == "SMTPInsecureSkipVerify" {
		if _, err := strconv.ParseBool(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("%s must be boolean: %w", key, err)
		}
	}
	// Validate registered, namespaced settings without touching the live
	// configuration. This is the preflight half of the DB→memory publish
	// protocol: a malformed value must be rejected before the DB transaction.
	if parts := strings.SplitN(key, ".", 2); len(parts) == 2 {
		if cfg := config.GlobalConfig.Get(parts[0]); cfg != nil {
			return config.ValidateConfigFromMap(cfg, map[string]string{parts[1]: value})
		}
	}

	// updateOptionMap historically ignored parse errors for legacy scalar
	// options (silently replacing a bad value with zero). Keep the wire format
	// compatible, but fail closed before persistence instead.
	switch key {
	case "FileUploadPermission", "FileDownloadPermission", "ImageUploadPermission", "ImageDownloadPermission",
		"SMTPPort", "MinTopUp", "StripeMinTopUp", "WaffoMinTopUp", "WaffoPancakeMinTopUp",
		"LinuxDOMinimumTrustLevel", "QuotaForNewUser", "QuotaForInviter", "QuotaForInvitee",
		"QuotaRemindThreshold", "PreConsumedQuota", "RetryTimes",
		"DataExportInterval", "StreamCacheQueueLength":
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return fmt.Errorf("%s must be an integer: %w", key, err)
		}
		minValue, maxValue := int64(math.MinInt64), int64(math.MaxInt64)
		switch key {
		case "SMTPPort":
			// Port zero is not a usable SMTP endpoint and can make the sender
			// silently dial an ephemeral service. Keep the protocol range explicit.
			minValue, maxValue = 1, 65535
		case "MinTopUp", "StripeMinTopUp", "WaffoMinTopUp", "WaffoPancakeMinTopUp":
			// Zero disables a minimum, which is useful for operators; negative
			// values would make negative requests pass the minimum check.
			minValue, maxValue = 0, int64(common.MaxWalletQuota)
		case "QuotaForNewUser", "QuotaForInviter", "QuotaForInvitee", "QuotaRemindThreshold":
			minValue, maxValue = 0, int64(common.MaxWalletQuota)
		case "PreConsumedQuota":
			minValue, maxValue = 0, int64(common.MaxQuota)
		case "RetryTimes":
			// A very large retry count turns a transient provider failure into
			// an unbounded request amplification loop.
			minValue, maxValue = 0, 1000
		case "DataExportInterval":
			minValue, maxValue = 0, 30*24*60
		case "StreamCacheQueueLength":
			// Queue entries retain request data; cap the operator-controlled
			// allocation while allowing substantially larger deployments than
			// the historical default.
			minValue, maxValue = 0, 1_000_000
		}
		if parsed < minValue || parsed > maxValue {
			return fmt.Errorf("%s must be between %d and %d", key, minValue, maxValue)
		}
	case "ModelRequestRateLimitCount", "ModelRequestRateLimitDurationMinutes", "ModelRequestRateLimitSuccessCount":
		intValue, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%s must be an integer: %w", key, err)
		}
		if err := setting.ValidateModelRequestRateLimitValue(key, intValue); err != nil {
			return err
		}
	case "Price", "USDExchangeRate", "StripeUnitPrice", "WaffoUnitPrice", "WaffoPancakeUnitPrice",
		"ChannelDisableThreshold", "QuotaPerUnit":
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			if err == nil {
				err = fmt.Errorf("value must be finite")
			}
			return fmt.Errorf("%s must be a finite number: %w", key, err)
		}
		switch key {
		case "QuotaPerUnit":
			if parsed <= 0 || parsed > float64(common.MaxWalletQuota) {
				return fmt.Errorf("%s must be greater than 0 and no more than %d", key, common.MaxWalletQuota)
			}
		case "ChannelDisableThreshold":
			if parsed < 0 || parsed > 7*24*60*60 {
				return fmt.Errorf("%s must be between 0 and %d seconds", key, 7*24*60*60)
			}
		default:
			// Monetary rates are multiplied by user-provided amounts. A zero or
			// negative rate can create free/negative orders, while an extreme
			// rate can overflow provider payloads. Keep a broad but finite
			// operator range.
			if parsed <= 0 || parsed > 1_000_000_000 {
				return fmt.Errorf("%s must be greater than 0 and no more than 1000000000", key)
			}
		}
	case "Chats":
		if err := setting.ValidateChatsJSON(value); err != nil {
			return fmt.Errorf("Chats: %w", err)
		}
	case "AutoGroups":
		var decoded []string
		if err := common.Unmarshal([]byte(value), &decoded); err != nil {
			return fmt.Errorf("AutoGroups: %w", err)
		}
	case "TopupGroupRatio":
		var decoded map[string]float64
		if err := common.Unmarshal([]byte(value), &decoded); err != nil {
			return fmt.Errorf("TopupGroupRatio: %w", err)
		}
		for name, ratio := range decoded {
			if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
				return fmt.Errorf("TopupGroupRatio[%s] must be a finite non-negative number", name)
			}
		}
	case "ModelRequestRateLimitGroup":
		if err := setting.CheckModelRequestRateLimitGroup(value); err != nil {
			return err
		}
	case "ModelRatio", "ModelPrice", "CompletionRatio", "CacheRatio", "CreateCacheRatio", "ImageRatio", "AudioRatio", "AudioCompletionRatio", "GroupRatio":
		var decoded map[string]float64
		if err := common.Unmarshal([]byte(value), &decoded); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		for name, ratio := range decoded {
			if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
				return fmt.Errorf("%s[%s] must be finite", key, name)
			}
		}
	case "GroupGroupRatio":
		var decoded map[string]map[string]float64
		if err := common.Unmarshal([]byte(value), &decoded); err != nil {
			return fmt.Errorf("GroupGroupRatio: %w", err)
		}
	case "UserUsableGroups":
		var decoded map[string]string
		if err := common.Unmarshal([]byte(value), &decoded); err != nil {
			return fmt.Errorf("UserUsableGroups: %w", err)
		}
	case "AutomaticDisableStatusCodes", "AutomaticRetryStatusCodes":
		if _, err := operation_setting.ParseHTTPStatusCodeRanges(value); err != nil {
			return err
		}
	case "PayMethods":
		var decoded []map[string]string
		if err := common.Unmarshal([]byte(value), &decoded); err != nil {
			return fmt.Errorf("PayMethods: %w", err)
		}
	}
	return nil
}

func UpdateOption(key string, value string) error {
	optionUpdateMutex.Lock()
	defer optionUpdateMutex.Unlock()
	if DB == nil {
		return fmt.Errorf("database is not initialized")
	}
	value = normalizeOptionValue(key, value)
	if err := validateOptionValue(key, value); err != nil {
		return err
	}
	previous := captureOptionMemoryState(key)
	return persistAndPublishOptions(
		[]string{key},
		map[string]string{key: value},
		map[string]optionMemoryState{key: previous},
	)
}

// UpdateOptionsBulk persists multiple key/value pairs in a single database
// transaction, then dispatches them through updateOptionMap in one pass. If
// any DB write fails the whole transaction rolls back and no in-memory state
// is touched — safe for callers that must commit a set of related options
// atomically (e.g. payment gateway binding).
func UpdateOptionsBulk(values map[string]string) error {
	optionUpdateMutex.Lock()
	defer optionUpdateMutex.Unlock()
	if DB == nil {
		return fmt.Errorf("database is not initialized")
	}
	if len(values) == 0 {
		return nil
	}
	normalizedValues := make(map[string]string, len(values))
	for key, value := range values {
		normalizedValues[key] = normalizeOptionValue(key, value)
	}
	values = normalizedValues
	for key, value := range values {
		if err := validateOptionValue(key, value); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	previous := make(map[string]optionMemoryState, len(keys))
	for _, key := range keys {
		previous[key] = captureOptionMemoryState(key)
	}
	return persistAndPublishOptions(keys, values, previous)
}

// persistAndPublishOptions keeps the database transaction open until the
// runtime snapshot has been published and the commit has succeeded. Holding
// OptionMapRWMutex across both phases prevents local readers from observing an
// uncommitted value. More importantly, a publish failure is handled by the
// transaction's rollback; it never issues a compensating write that could
// overwrite a newer value committed by another process.
func persistAndPublishOptions(keys []string, values map[string]string, previous map[string]optionMemoryState) error {
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()

	publicationStarted := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		for _, key := range keys {
			if err := upsertOption(tx, key, values[key]); err != nil {
				return err
			}
		}
		publicationStarted = true
		for _, key := range keys {
			if err := updateOptionMapLocked(key, values[key]); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || !publicationStarted {
		return err
	}
	if rollbackErr := restoreOptionMemoryStatesLocked(keys, previous); rollbackErr != nil {
		return fmt.Errorf("%w (runtime rollback: %v)", err, rollbackErr)
	}
	return err
}

// upsertOption stores one option without a read-before-write race.  Keep this
// helper in model/ so every supported SQL dialect gets the same semantics via
// GORM's clause builder.
func upsertOption(tx *gorm.DB, key, value string) error {
	if tx == nil {
		return errors.New("database is not initialized")
	}
	persistedValue, err := sealOptionSecretValue(key, value)
	if err != nil {
		return err
	}
	// persistedValue is already sealed. Skip the model hook so a future
	// envelope format cannot accidentally be interpreted as plaintext here.
	return tx.Session(&gorm.Session{SkipHooks: true}).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&Option{Key: key, Value: persistedValue}).Error
}

func readOptionMapValue(key string) (string, bool) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	if common.OptionMap == nil {
		return "", false
	}
	value, ok := common.OptionMap[key]
	return value, ok
}

// captureOptionMemoryState records both the map representation and the
// effective runtime value before a publication starts. OptionMap is a cache
// of the latter, but it can legitimately be sparse while bootstrapping (and
// can be stale after an older failed update), so never rely on map presence as
// proof that the external setting has not changed.
//
// This helper must be called before the caller acquires OptionMapRWMutex for
// publication. ConfigSnapshot providers are allowed to take that read lock.
func captureOptionMemoryState(key string) optionMemoryState {
	mapValue, mapExists := readOptionMapValue(key)
	runtimeValue, runtimeExists := readRuntimeOptionValue(key)
	if !runtimeExists && mapExists {
		// Unknown legacy settings still get a best-effort restoration from the
		// map. The map value is preferable to leaving the key permanently split.
		runtimeValue, runtimeExists = mapValue, true
	}
	return optionMemoryState{
		mapExists:     mapExists,
		mapValue:      mapValue,
		runtimeExists: runtimeExists,
		runtimeValue:  runtimeValue,
	}
}

// readRuntimeOptionValue returns a serialized snapshot of the setting an
// option updates. Namespaced settings are exported through ConfigManager so
// copy-on-write and custom ConfigMapUpdater implementations are handled by
// their own snapshot providers. Legacy scalar options fall back to OptionMap
// in captureOptionMemoryState when no dedicated snapshot is available.
func readRuntimeOptionValue(key string) (string, bool) {
	if key == "LogConsumeEnabled" {
		return strconv.FormatBool(common.IsLogConsumeEnabled()), true
	}
	if value, ok := systemRuntimeOptionValue(key, system_setting.GetRuntimeConfig()); ok {
		return value, true
	}
	if value, ok := midjourneyRuntimeOptionValue(key, setting.GetMidjourneyConfig()); ok {
		return value, true
	}
	if value, ok := sensitiveRuntimeOptionValue(key, setting.GetSensitiveConfig()); ok {
		return value, true
	}
	// Legacy security options are backed by one protected snapshot rather than
	// the sparse OptionMap representation. This matters for rollback: bootstrap
	// intentionally leaves secret keys blank in OptionMap, but a failed bulk
	// publish must still restore the live credential (and a sparse map must not
	// be treated as proof that the old runtime value did not exist).
	if value, ok := securityRuntimeOptionValue(key, common.GetSecurityRuntimeConfig()); ok {
		return value, true
	}
	if value, ok := generalRuntimeOptionValue(key, common.GetGeneralRuntimeConfig()); ok {
		return value, true
	}
	if !strings.Contains(key, ".") {
		return "", false
	}
	values := config.GlobalConfig.ExportAllConfigs()
	value, ok := values[key]
	return value, ok
}

func generalRuntimeOptionValue(key string, cfg common.GeneralRuntimeConfig) (string, bool) {
	switch key {
	case "QuotaForNewUser":
		return strconv.Itoa(cfg.QuotaForNewUser), true
	case "QuotaForInviter":
		return strconv.Itoa(cfg.QuotaForInviter), true
	case "QuotaForInvitee":
		return strconv.Itoa(cfg.QuotaForInvitee), true
	case "QuotaRemindThreshold":
		return strconv.Itoa(cfg.QuotaRemindThreshold), true
	case "ChannelDisableThreshold":
		return strconv.FormatFloat(cfg.ChannelDisableThreshold, 'f', -1, 64), true
	case "AutomaticDisableChannelEnabled":
		return strconv.FormatBool(cfg.AutomaticDisableChannelEnabled), true
	case "AutomaticEnableChannelEnabled":
		return strconv.FormatBool(cfg.AutomaticEnableChannelEnabled), true
	case "DrawingEnabled":
		return strconv.FormatBool(cfg.DrawingEnabled), true
	case "TaskEnabled":
		return strconv.FormatBool(cfg.TaskEnabled), true
	case "DataExportEnabled":
		return strconv.FormatBool(cfg.DataExportEnabled), true
	case "DataExportInterval":
		return strconv.Itoa(cfg.DataExportInterval), true
	case "DataExportDefaultTime":
		return cfg.DataExportDefaultTime, true
	case "TopUpLink":
		return cfg.TopUpLink, true
	case "Footer":
		return cfg.Footer, true
	case "Logo":
		return cfg.Logo, true
	case "FileUploadPermission":
		return strconv.Itoa(cfg.FileUploadPermission), true
	case "FileDownloadPermission":
		return strconv.Itoa(cfg.FileDownloadPermission), true
	case "ImageUploadPermission":
		return strconv.Itoa(cfg.ImageUploadPermission), true
	case "ImageDownloadPermission":
		return strconv.Itoa(cfg.ImageDownloadPermission), true
	case "DisplayInCurrencyEnabled":
		return strconv.FormatBool(cfg.DisplayInCurrencyEnabled), true
	case "DisplayTokenStatEnabled":
		return strconv.FormatBool(cfg.DisplayTokenStatEnabled), true
	case "DefaultCollapseSidebar":
		return strconv.FormatBool(cfg.DefaultCollapseSidebar), true
	default:
		return "", false
	}
}

func systemRuntimeOptionValue(key string, cfg system_setting.RuntimeConfig) (string, bool) {
	switch key {
	case "ServerAddress":
		return cfg.ServerAddress, true
	case "WorkerUrl":
		return cfg.WorkerURL, true
	case "WorkerValidKey":
		return cfg.WorkerValidKey, true
	case "WorkerAllowHttpImageRequestEnabled":
		return strconv.FormatBool(cfg.WorkerAllowHttpImageRequestEnabled), true
	default:
		return "", false
	}
}

func midjourneyRuntimeOptionValue(key string, cfg setting.MidjourneyConfig) (string, bool) {
	switch key {
	case "MjNotifyEnabled":
		return strconv.FormatBool(cfg.NotifyEnabled), true
	case "MjAccountFilterEnabled":
		return strconv.FormatBool(cfg.AccountFilterEnabled), true
	case "MjModeClearEnabled":
		return strconv.FormatBool(cfg.ModeClearEnabled), true
	case "MjForwardUrlEnabled":
		return strconv.FormatBool(cfg.ForwardURLEnabled), true
	case "MjActionCheckSuccessEnabled":
		return strconv.FormatBool(cfg.ActionCheckSuccessEnabled), true
	default:
		return "", false
	}
}

func sensitiveRuntimeOptionValue(key string, cfg setting.SensitiveConfig) (string, bool) {
	switch key {
	case "CheckSensitiveEnabled":
		return strconv.FormatBool(cfg.CheckEnabled), true
	case "CheckSensitiveOnPromptEnabled":
		return strconv.FormatBool(cfg.CheckOnPromptEnabled), true
	case "StopOnSensitiveEnabled":
		return strconv.FormatBool(cfg.StopOnSensitive), true
	case "StreamCacheQueueLength":
		return strconv.Itoa(cfg.StreamCacheQueueLen), true
	case "SensitiveWords":
		return strings.Join(cfg.SensitiveWords, "\n"), true
	default:
		return "", false
	}
}

func securityRuntimeOptionValue(key string, cfg common.SecurityRuntimeConfig) (string, bool) {
	switch key {
	case "PasswordLoginEnabled":
		return strconv.FormatBool(cfg.PasswordLoginEnabled), true
	case "PasswordRegisterEnabled":
		return strconv.FormatBool(cfg.PasswordRegisterEnabled), true
	case "EmailVerificationEnabled":
		return strconv.FormatBool(cfg.EmailVerificationEnabled), true
	case "RegisterEnabled":
		return strconv.FormatBool(cfg.RegisterEnabled), true
	case "GitHubOAuthEnabled":
		return strconv.FormatBool(cfg.GitHubOAuthEnabled), true
	case "GitHubClientId":
		return cfg.GitHubClientID, true
	case "GitHubClientSecret":
		return cfg.GitHubClientSecret, true
	case "LinuxDOOAuthEnabled":
		return strconv.FormatBool(cfg.LinuxDOOAuthEnabled), true
	case "LinuxDOClientId":
		return cfg.LinuxDOClientID, true
	case "LinuxDOClientSecret":
		return cfg.LinuxDOClientSecret, true
	case "LinuxDOMinimumTrustLevel":
		return strconv.Itoa(cfg.LinuxDOMinimumTrustLevel), true
	case "TelegramOAuthEnabled":
		return strconv.FormatBool(cfg.TelegramOAuthEnabled), true
	case "TelegramBotToken":
		return cfg.TelegramBotToken, true
	case "TelegramBotName":
		return cfg.TelegramBotName, true
	case "WeChatAuthEnabled":
		return strconv.FormatBool(cfg.WeChatAuthEnabled), true
	case "WeChatServerAddress":
		return cfg.WeChatServerAddress, true
	case "WeChatServerToken":
		return cfg.WeChatServerToken, true
	case "WeChatAccountQRCodeImageURL":
		return cfg.WeChatAccountQRCodeImageURL, true
	case "TurnstileCheckEnabled":
		return strconv.FormatBool(cfg.TurnstileCheckEnabled), true
	case "TurnstileSiteKey":
		return cfg.TurnstileSiteKey, true
	case "TurnstileSecretKey":
		return cfg.TurnstileSecretKey, true
	case "EmailDomainRestrictionEnabled":
		return strconv.FormatBool(cfg.EmailDomainRestrictionEnabled), true
	case "EmailAliasRestrictionEnabled":
		return strconv.FormatBool(cfg.EmailAliasRestrictionEnabled), true
	case "EmailDomainWhitelist":
		return strings.Join(cfg.EmailDomainWhitelist, ","), true
	default:
		return "", false
	}
}

func restoreOptionMemoryStatesLocked(keys []string, previous map[string]optionMemoryState) error {
	var firstErr error
	for index := len(keys) - 1; index >= 0; index-- {
		key := keys[index]
		if err := restoreOptionMemoryStateLocked(key, previous[key]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// restoreOptionMemoryStateLocked is the lock-free-internally variant used by
// multi-option publication rollback. The caller must hold OptionMapRWMutex.
func restoreOptionMemoryStateLocked(key string, state optionMemoryState) error {
	var restoreErr error
	if state.runtimeExists {
		restoreErr = updateOptionMapLocked(key, state.runtimeValue)
	} else if state.mapExists {
		// Unknown legacy options have no dedicated snapshot provider. Replaying
		// the map value is still safer than leaving the runtime at the new value.
		restoreErr = updateOptionMapLocked(key, state.mapValue)
	}

	// updateOptionMapLocked may return before assigning the map (a setter can
	// fail after changing its external object). Always restore the exact map
	// presence/value independently of that setter result.
	if state.mapExists {
		if common.OptionMap == nil {
			common.OptionMap = make(map[string]string)
		}
		common.OptionMap[key] = state.mapValue
	} else if common.OptionMap != nil {
		delete(common.OptionMap, key)
	}
	return restoreErr
}

func updateOptionMap(key string, value string) (err error) {
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	return updateOptionMapLocked(key, value)
}

// updateOptionMapLocked applies one option while OptionMapRWMutex is held.
// Keeping the implementation separate allows bulk/reload callers to publish
// a group atomically without recursive lock acquisition.
func updateOptionMapLocked(key string, value string) (err error) {
	if key == retiredThemeOptionKey {
		delete(common.OptionMap, key)
		return nil
	}
	value = normalizeOptionValue(key, value)
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	// 检查是否是模型配置 - 使用更规范的方式处理
	if handled, handleErr := handleConfigUpdate(key, value); handled {
		if handleErr != nil {
			return handleErr
		}
		common.OptionMap[key] = value
		return nil // 已由配置系统处理
	}

	// 处理传统配置项...
	if strings.HasSuffix(key, "Permission") {
		intValue, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("%s must be an integer: %w", key, parseErr)
		}
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) {
			switch key {
			case "FileUploadPermission":
				cfg.FileUploadPermission = intValue
			case "FileDownloadPermission":
				cfg.FileDownloadPermission = intValue
			case "ImageUploadPermission":
				cfg.ImageUploadPermission = intValue
			case "ImageDownloadPermission":
				cfg.ImageDownloadPermission = intValue
			}
		})
	}
	if strings.HasSuffix(key, "Enabled") || key == "DefaultCollapseSidebar" || key == "DefaultUseAutoGroup" || key == "SMTPForceAuthLogin" || key == "SMTPInsecureSkipVerify" {
		boolValue, parseErr := strconv.ParseBool(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("%s must be boolean: %w", key, parseErr)
		}
		switch key {
		case "PasswordRegisterEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.PasswordRegisterEnabled = boolValue })
		case "PasswordLoginEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.PasswordLoginEnabled = boolValue })
		case "EmailVerificationEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.EmailVerificationEnabled = boolValue })
		case "GitHubOAuthEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.GitHubOAuthEnabled = boolValue })
		case "LinuxDOOAuthEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.LinuxDOOAuthEnabled = boolValue })
		case "WeChatAuthEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.WeChatAuthEnabled = boolValue })
		case "TelegramOAuthEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.TelegramOAuthEnabled = boolValue })
		case "TurnstileCheckEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.TurnstileCheckEnabled = boolValue })
		case "RegisterEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.RegisterEnabled = boolValue })
		case "EmailDomainRestrictionEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.EmailDomainRestrictionEnabled = boolValue })
		case "EmailAliasRestrictionEnabled":
			common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.EmailAliasRestrictionEnabled = boolValue })
		case "AutomaticDisableChannelEnabled":
			common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.AutomaticDisableChannelEnabled = boolValue })
		case "AutomaticEnableChannelEnabled":
			common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.AutomaticEnableChannelEnabled = boolValue })
		case "LogConsumeEnabled":
			common.SetLogConsumeEnabled(boolValue)
		case "DisplayInCurrencyEnabled":
			// 兼容旧字段：同步到新配置 general_setting.quota_display_type（运行时生效）
			// true -> USD, false -> TOKENS
			newVal := "USD"
			if !boolValue {
				newVal = "TOKENS"
			}
			if cfg := config.GlobalConfig.Get("general_setting"); cfg != nil {
				if updateErr := config.UpdateConfigFromMap(cfg, map[string]string{"quota_display_type": newVal}); updateErr != nil {
					// Do not publish the compatibility flag or OptionMap entry when
					// the authoritative display-mode setting rejected the update.
					// The caller can then roll the persisted option back instead of
					// silently running with two different display modes.
					return updateErr
				}
			}
			common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.DisplayInCurrencyEnabled = boolValue })
		case "DisplayTokenStatEnabled":
			common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.DisplayTokenStatEnabled = boolValue })
		case "DrawingEnabled":
			common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.DrawingEnabled = boolValue })
		case "TaskEnabled":
			common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.TaskEnabled = boolValue })
		case "DataExportEnabled":
			common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.DataExportEnabled = boolValue })
		case "DefaultCollapseSidebar":
			common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.DefaultCollapseSidebar = boolValue })
		case "MjNotifyEnabled":
			setting.UpdateMidjourneyConfig(func(cfg *setting.MidjourneyConfig) { cfg.NotifyEnabled = boolValue })
		case "MjAccountFilterEnabled":
			setting.UpdateMidjourneyConfig(func(cfg *setting.MidjourneyConfig) { cfg.AccountFilterEnabled = boolValue })
		case "MjModeClearEnabled":
			setting.UpdateMidjourneyConfig(func(cfg *setting.MidjourneyConfig) { cfg.ModeClearEnabled = boolValue })
		case "MjForwardUrlEnabled":
			setting.UpdateMidjourneyConfig(func(cfg *setting.MidjourneyConfig) { cfg.ForwardURLEnabled = boolValue })
		case "MjActionCheckSuccessEnabled":
			setting.UpdateMidjourneyConfig(func(cfg *setting.MidjourneyConfig) { cfg.ActionCheckSuccessEnabled = boolValue })
		case "CheckSensitiveEnabled":
			setting.UpdateSensitiveConfig(func(cfg *setting.SensitiveConfig) { cfg.CheckEnabled = boolValue })
		case "DemoSiteEnabled":
			operation_setting.SetDemoSiteEnabled(boolValue)
		case "SelfUseModeEnabled":
			operation_setting.SetSelfUseModeEnabled(boolValue)
		case "CheckSensitiveOnPromptEnabled":
			setting.UpdateSensitiveConfig(func(cfg *setting.SensitiveConfig) { cfg.CheckOnPromptEnabled = boolValue })
		case "ModelRequestRateLimitEnabled":
			setting.SetModelRequestRateLimitEnabled(boolValue)
		case "StopOnSensitiveEnabled":
			setting.UpdateSensitiveConfig(func(cfg *setting.SensitiveConfig) { cfg.StopOnSensitive = boolValue })
		case "SMTPSSLEnabled":
			common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.SSLEnabled = boolValue })
		case "SMTPStartTLSEnabled":
			common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.StartTLSEnabled = boolValue })
		case "SMTPInsecureSkipVerify":
			common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.InsecureSkipVerify = boolValue })
		case "SMTPForceAuthLogin":
			common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.ForceAuthLogin = boolValue })
		case "WorkerAllowHttpImageRequestEnabled":
			system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) { cfg.WorkerAllowHttpImageRequestEnabled = boolValue })
		case "DefaultUseAutoGroup":
			setting.SetDefaultUseAutoGroup(boolValue)
		case "ExposeRatioEnabled":
			ratio_setting.SetExposeRatioEnabled(boolValue)
		}
	}
	switch key {
	case "EmailDomainWhitelist":
		domains := strings.Split(value, ",")
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.EmailDomainWhitelist = domains })
	case "SMTPServer":
		common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.Server = value })
	case "SMTPPort":
		intValue, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("SMTPPort must be an integer: %w", parseErr)
		}
		common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.Port = intValue })
	case "SMTPAccount":
		common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.Account = value })
	case "SMTPFrom":
		common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.From = value })
	case "SMTPToken":
		common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.Token = value })
	case "ServerAddress":
		system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) { cfg.ServerAddress = value })
	case "WorkerUrl":
		system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) { cfg.WorkerURL = value })
	case "WorkerValidKey":
		system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) { cfg.WorkerValidKey = value })
	case "PayAddress":
		operation_setting.UpdatePaymentRuntimeConfig(func(cfg *operation_setting.PaymentRuntimeConfig) {
			cfg.PayAddress = value
		})
	case "Chats":
		err = setting.UpdateChatsByJsonString(value)
	case "AutoGroups":
		err = setting.UpdateAutoGroupsByJsonString(value)
	case "MaxTokenAutoGroups":
		err = setting.UpdateMaxTokenAutoGroups(value)
	case "CustomCallbackAddress":
		operation_setting.UpdatePaymentRuntimeConfig(func(cfg *operation_setting.PaymentRuntimeConfig) {
			cfg.CustomCallbackAddress = value
		})
	case "EpayId":
		operation_setting.UpdatePaymentRuntimeConfig(func(cfg *operation_setting.PaymentRuntimeConfig) {
			cfg.EpayID = value
		})
	case "EpayKey":
		operation_setting.UpdatePaymentRuntimeConfig(func(cfg *operation_setting.PaymentRuntimeConfig) {
			cfg.EpayKey = value
		})
	case "Price":
		price, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || math.IsNaN(price) || math.IsInf(price, 0) {
			if parseErr == nil {
				parseErr = errors.New("value must be finite")
			}
			return fmt.Errorf("Price must be a finite number: %w", parseErr)
		}
		operation_setting.UpdatePaymentRuntimeConfig(func(cfg *operation_setting.PaymentRuntimeConfig) {
			cfg.Price = price
		})
	case "USDExchangeRate":
		exchangeRate, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || math.IsNaN(exchangeRate) || math.IsInf(exchangeRate, 0) {
			if parseErr == nil {
				parseErr = errors.New("value must be finite")
			}
			return fmt.Errorf("USDExchangeRate must be a finite number: %w", parseErr)
		}
		operation_setting.UpdatePaymentRuntimeConfig(func(cfg *operation_setting.PaymentRuntimeConfig) {
			cfg.USDExchangeRate = exchangeRate
		})
	case "MinTopUp":
		minTopUp, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("MinTopUp must be an integer: %w", parseErr)
		}
		operation_setting.UpdatePaymentRuntimeConfig(func(cfg *operation_setting.PaymentRuntimeConfig) {
			cfg.MinTopUp = minTopUp
		})
	case "StripeApiSecret":
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { cfg.ApiSecret = value })
	case "StripeWebhookSecret":
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { cfg.WebhookSecret = value })
	case "StripeAccountId":
		accountID := strings.TrimSpace(value)
		if accountID != "" && !strings.HasPrefix(accountID, "acct_") {
			return fmt.Errorf("StripeAccountId must start with acct_")
		}
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { cfg.AccountID = accountID })
	case "StripePriceId":
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { cfg.PriceID = value })
	case "StripeUnitPrice":
		unitPrice, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || math.IsNaN(unitPrice) || math.IsInf(unitPrice, 0) {
			if parseErr == nil {
				parseErr = errors.New("value must be finite")
			}
			return fmt.Errorf("StripeUnitPrice must be a finite number: %w", parseErr)
		}
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { cfg.UnitPrice = unitPrice })
	case "StripeCurrency":
		currency := strings.ToUpper(strings.TrimSpace(value))
		if len(currency) != 3 {
			return fmt.Errorf("StripeCurrency must be a 3-letter ISO currency code")
		}
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { cfg.Currency = currency })
	case "StripeMinTopUp":
		minTopUp, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("StripeMinTopUp must be an integer: %w", parseErr)
		}
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { cfg.MinTopUp = minTopUp })
	case "StripePromotionCodesEnabled":
		promotionCodesEnabled := value == "true"
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { cfg.PromotionCodesEnabled = promotionCodesEnabled })
	case "CreemApiKey":
		setting.UpdateCreemConfig(func(cfg *setting.CreemConfig) { cfg.ApiKey = value })
	case "CreemProducts":
		setting.UpdateCreemConfig(func(cfg *setting.CreemConfig) { cfg.Products = value })
	case "CreemTestMode":
		setting.UpdateCreemConfig(func(cfg *setting.CreemConfig) { cfg.TestMode = value == "true" })
	case "CreemWebhookSecret":
		setting.UpdateCreemConfig(func(cfg *setting.CreemConfig) { cfg.WebhookSecret = value })
	case "WaffoEnabled":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.Enabled = value == "true" })
	case "WaffoApiKey":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.ApiKey = value })
	case "WaffoPrivateKey":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.PrivateKey = value })
	case "WaffoPublicCert":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.PublicCert = value })
	case "WaffoSandboxPublicCert":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.SandboxPublicCert = value })
	case "WaffoSandboxApiKey":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.SandboxApiKey = value })
	case "WaffoSandboxPrivateKey":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.SandboxPrivateKey = value })
	case "WaffoSandbox":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.Sandbox = value == "true" })
	case "WaffoMerchantId":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.MerchantID = value })
	case "WaffoNotifyUrl":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.NotifyURL = value })
	case "WaffoReturnUrl":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.ReturnURL = value })
	case "WaffoSubscriptionReturnUrl":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.SubscriptionReturnURL = value })
	case "WaffoCurrency":
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.Currency = value })
	case "WaffoUnitPrice":
		unitPrice, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || math.IsNaN(unitPrice) || math.IsInf(unitPrice, 0) {
			if parseErr == nil {
				parseErr = errors.New("value must be finite")
			}
			return fmt.Errorf("WaffoUnitPrice must be a finite number: %w", parseErr)
		}
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.UnitPrice = unitPrice })
	case "WaffoMinTopUp":
		minTopUp, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("WaffoMinTopUp must be an integer: %w", parseErr)
		}
		setting.UpdateWaffoConfig(func(cfg *setting.WaffoConfig) { cfg.MinTopUp = minTopUp })
	case "WaffoPancakeMerchantID":
		setting.UpdateWaffoPancakeConfig(func(cfg *setting.WaffoPancakeConfig) { cfg.MerchantID = value })
	case "WaffoPancakePrivateKey":
		setting.UpdateWaffoPancakeConfig(func(cfg *setting.WaffoPancakeConfig) { cfg.PrivateKey = value })
	case "WaffoPancakeReturnURL":
		setting.UpdateWaffoPancakeConfig(func(cfg *setting.WaffoPancakeConfig) { cfg.ReturnURL = value })
	case "WaffoPancakeStoreID":
		setting.UpdateWaffoPancakeConfig(func(cfg *setting.WaffoPancakeConfig) { cfg.StoreID = value })
	case "WaffoPancakeProductID":
		setting.UpdateWaffoPancakeConfig(func(cfg *setting.WaffoPancakeConfig) { cfg.ProductID = value })
	case "WaffoPancakeUnitPrice":
		unitPrice, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || math.IsNaN(unitPrice) || math.IsInf(unitPrice, 0) {
			if parseErr == nil {
				parseErr = errors.New("value must be finite")
			}
			return fmt.Errorf("WaffoPancakeUnitPrice must be a finite number: %w", parseErr)
		}
		setting.UpdateWaffoPancakeConfig(func(cfg *setting.WaffoPancakeConfig) { cfg.UnitPrice = unitPrice })
	case "WaffoPancakeMinTopUp":
		minTopUp, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("WaffoPancakeMinTopUp must be an integer: %w", parseErr)
		}
		setting.UpdateWaffoPancakeConfig(func(cfg *setting.WaffoPancakeConfig) { cfg.MinTopUp = minTopUp })
	case "TopupGroupRatio":
		err = common.UpdateTopupGroupRatioByJSONString(value)
	case "GitHubClientId":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.GitHubClientID = value })
	case "GitHubClientSecret":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.GitHubClientSecret = value })
	case "LinuxDOClientId":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.LinuxDOClientID = value })
	case "LinuxDOClientSecret":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.LinuxDOClientSecret = value })
	case "LinuxDOMinimumTrustLevel":
		minimumTrustLevel, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("LinuxDOMinimumTrustLevel must be an integer: %w", parseErr)
		}
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.LinuxDOMinimumTrustLevel = minimumTrustLevel })
	case "Footer":
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.Footer = value })
	case "SystemName":
		common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { cfg.SystemName = value })
	case "Logo":
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.Logo = value })
	case "WeChatServerAddress":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.WeChatServerAddress = value })
	case "WeChatServerToken":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.WeChatServerToken = value })
	case "WeChatAccountQRCodeImageURL":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.WeChatAccountQRCodeImageURL = value })
	case "TelegramBotToken":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.TelegramBotToken = value })
	case "TelegramBotName":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.TelegramBotName = value })
	case "TurnstileSiteKey":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.TurnstileSiteKey = value })
	case "TurnstileSecretKey":
		common.UpdateSecurityRuntimeConfig(func(cfg *common.SecurityRuntimeConfig) { cfg.TurnstileSecretKey = value })
	case "QuotaForNewUser":
		quota, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("QuotaForNewUser must be an integer: %w", parseErr)
		}
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.QuotaForNewUser = quota })
	case "QuotaForInviter":
		quota, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("QuotaForInviter must be an integer: %w", parseErr)
		}
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.QuotaForInviter = quota })
	case "QuotaForInvitee":
		quota, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("QuotaForInvitee must be an integer: %w", parseErr)
		}
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.QuotaForInvitee = quota })
	case "QuotaRemindThreshold":
		threshold, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("QuotaRemindThreshold must be an integer: %w", parseErr)
		}
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.QuotaRemindThreshold = threshold })
	case "PreConsumedQuota":
		preConsumedQuota, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("PreConsumedQuota must be an integer: %w", parseErr)
		}
		common.SetPreConsumedQuota(preConsumedQuota)
	case "ModelRequestRateLimitCount":
		intValue, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("ModelRequestRateLimitCount must be an integer: %w", parseErr)
		}
		setting.SetModelRequestRateLimitCount(intValue)
	case "ModelRequestRateLimitDurationMinutes":
		intValue, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("ModelRequestRateLimitDurationMinutes must be an integer: %w", parseErr)
		}
		setting.SetModelRequestRateLimitDurationMinutes(intValue)
	case "ModelRequestRateLimitSuccessCount":
		intValue, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("ModelRequestRateLimitSuccessCount must be an integer: %w", parseErr)
		}
		setting.SetModelRequestRateLimitSuccessCount(intValue)
	case "ModelRequestRateLimitGroup":
		err = setting.UpdateModelRequestRateLimitGroupByJSONString(value)
	case "RetryTimes":
		intValue, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("RetryTimes must be an integer: %w", parseErr)
		}
		common.SetRetryTimes(intValue)
	case "DataExportInterval":
		interval, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("DataExportInterval must be an integer: %w", parseErr)
		}
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.DataExportInterval = interval })
	case "DataExportDefaultTime":
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.DataExportDefaultTime = value })
	case "ModelRatio":
		err = ratio_setting.UpdateModelRatioByJSONString(value)
	case "GroupRatio":
		err = ratio_setting.UpdateGroupRatioByJSONString(value)
	case "GroupGroupRatio":
		err = ratio_setting.UpdateGroupGroupRatioByJSONString(value)
	case "UserUsableGroups":
		err = setting.UpdateUserUsableGroupsByJSONString(value)
	case "CompletionRatio":
		err = ratio_setting.UpdateCompletionRatioByJSONString(value)
	case "ModelPrice":
		err = ratio_setting.UpdateModelPriceByJSONString(value)
	case "CacheRatio":
		err = ratio_setting.UpdateCacheRatioByJSONString(value)
	case "CreateCacheRatio":
		err = ratio_setting.UpdateCreateCacheRatioByJSONString(value)
	case "ImageRatio":
		err = ratio_setting.UpdateImageRatioByJSONString(value)
	case "AudioRatio":
		err = ratio_setting.UpdateAudioRatioByJSONString(value)
	case "AudioCompletionRatio":
		err = ratio_setting.UpdateAudioCompletionRatioByJSONString(value)
	case "TopUpLink":
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.TopUpLink = value })
	//case "ChatLink":
	//	common.ChatLink = value
	//case "ChatLink2":
	//	common.ChatLink2 = value
	case "ChannelDisableThreshold":
		threshold, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
			if parseErr == nil {
				parseErr = errors.New("value must be finite")
			}
			return fmt.Errorf("ChannelDisableThreshold must be a finite number: %w", parseErr)
		}
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { cfg.ChannelDisableThreshold = threshold })
	case "QuotaPerUnit":
		quotaPerUnit, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || math.IsNaN(quotaPerUnit) || math.IsInf(quotaPerUnit, 0) {
			if parseErr == nil {
				parseErr = errors.New("value must be finite")
			}
			return fmt.Errorf("QuotaPerUnit must be a finite number: %w", parseErr)
		}
		common.SetQuotaPerUnit(quotaPerUnit)
	case "SensitiveWords":
		setting.SensitiveWordsFromString(value)
	case "AutomaticDisableKeywords":
		operation_setting.AutomaticDisableKeywordsFromString(value)
	case "AutomaticDisableStatusCodes":
		err = operation_setting.AutomaticDisableStatusCodesFromString(value)
	case "AutomaticRetryStatusCodes":
		err = operation_setting.AutomaticRetryStatusCodesFromString(value)
	case "StreamCacheQueueLength":
		queueLength, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil {
			return fmt.Errorf("StreamCacheQueueLength must be an integer: %w", parseErr)
		}
		setting.UpdateSensitiveConfig(func(cfg *setting.SensitiveConfig) { cfg.StreamCacheQueueLen = queueLength })
	case "PayMethods":
		err = operation_setting.UpdatePayMethodsByJsonString(value)
	case "WaffoPayMethods":
		// WaffoPayMethods is read directly from OptionMap via setting.GetWaffoPayMethods().
		// The value is already stored in OptionMap at the top of this function (line: common.OptionMap[key] = value).
		// No additional in-memory variable to update.
	}
	if err != nil {
		return err
	}
	common.OptionMap[key] = value
	return err
}

// normalizeOptionValue keeps persisted and in-memory values subject to the
// same policy.  In particular, Footer is rendered as HTML on the public site,
// so sanitizing only in the React component would leave unsafe markup in the
// database and in status/config responses.
func normalizeOptionValue(key string, value string) string {
	if key == "Footer" {
		return common.SanitizeFooterHTML(value)
	}
	return value
}

// handleConfigUpdate 处理分层配置更新，返回是否已处理
func handleConfigUpdate(key, value string) (bool, error) {
	if key == operation_setting.ToolPriceOptionKey {
		operation_setting.LoadToolPricesFromJSONString(value)
		return true, nil
	}

	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return false, nil // 不是分层配置
	}

	configName := parts[0]
	configKey := parts[1]

	// 获取配置对象
	cfg := config.GlobalConfig.Get(configName)
	if cfg == nil {
		return false, nil // 未注册的配置
	}

	// 更新配置
	configMap := map[string]string{
		configKey: value,
	}
	if err := config.UpdateConfigFromMap(cfg, configMap); err != nil {
		return true, err
	}

	// 特定配置的后处理
	if configName == "performance_setting" {
		performance_setting.UpdateAndSync()
	} else if configName == "billing_setting" {
		InvalidatePricingCache()
		ratio_setting.InvalidateExposedDataCache()
	}

	return true, nil // 已处理
}
