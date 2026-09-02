package common

import "sync"

// GeneralRuntimeConfig contains the legacy options that are consumed directly
// by request handlers and background workers.  These options can be changed
// through the admin option endpoint while requests are in flight; publishing
// and reading them as one snapshot prevents data races and mixed generations
// (for example, a new-user quota paired with an old inviter reward).
//
// The exported scalar variables in constants.go remain for source
// compatibility. Production code must use GetGeneralRuntimeConfig and option
// writers must use UpdateGeneralRuntimeConfig.
type GeneralRuntimeConfig struct {
	QuotaForNewUser      int
	QuotaForInviter      int
	QuotaForInvitee      int
	QuotaRemindThreshold int

	ChannelDisableThreshold        float64
	AutomaticDisableChannelEnabled bool
	AutomaticEnableChannelEnabled  bool

	DrawingEnabled        bool
	TaskEnabled           bool
	DataExportEnabled     bool
	DataExportInterval    int
	DataExportDefaultTime string

	TopUpLink string
	Footer    string
	Logo      string

	FileUploadPermission    int
	FileDownloadPermission  int
	ImageUploadPermission   int
	ImageDownloadPermission int

	// These compatibility display/sidebar flags are also hot-reloadable
	// options and therefore share the same publication lock.
	DisplayInCurrencyEnabled bool
	DisplayTokenStatEnabled  bool
	DefaultCollapseSidebar   bool
}

var generalRuntimeConfigMu sync.RWMutex

func generalRuntimeConfigFromLegacyLocked() GeneralRuntimeConfig {
	return GeneralRuntimeConfig{
		QuotaForNewUser:                QuotaForNewUser,
		QuotaForInviter:                QuotaForInviter,
		QuotaForInvitee:                QuotaForInvitee,
		QuotaRemindThreshold:           QuotaRemindThreshold,
		ChannelDisableThreshold:        ChannelDisableThreshold,
		AutomaticDisableChannelEnabled: AutomaticDisableChannelEnabled,
		AutomaticEnableChannelEnabled:  AutomaticEnableChannelEnabled,
		DrawingEnabled:                 DrawingEnabled,
		TaskEnabled:                    TaskEnabled,
		DataExportEnabled:              DataExportEnabled,
		DataExportInterval:             DataExportInterval,
		DataExportDefaultTime:          DataExportDefaultTime,
		TopUpLink:                      TopUpLink,
		Footer:                         Footer,
		Logo:                           Logo,
		FileUploadPermission:           FileUploadPermission,
		FileDownloadPermission:         FileDownloadPermission,
		ImageUploadPermission:          ImageUploadPermission,
		ImageDownloadPermission:        ImageDownloadPermission,
		DisplayInCurrencyEnabled:       DisplayInCurrencyEnabled,
		DisplayTokenStatEnabled:        DisplayTokenStatEnabled,
		DefaultCollapseSidebar:         DefaultCollapseSidebar,
	}
}

func applyGeneralRuntimeConfigToLegacyLocked(cfg GeneralRuntimeConfig) {
	QuotaForNewUser = cfg.QuotaForNewUser
	QuotaForInviter = cfg.QuotaForInviter
	QuotaForInvitee = cfg.QuotaForInvitee
	QuotaRemindThreshold = cfg.QuotaRemindThreshold
	ChannelDisableThreshold = cfg.ChannelDisableThreshold
	AutomaticDisableChannelEnabled = cfg.AutomaticDisableChannelEnabled
	AutomaticEnableChannelEnabled = cfg.AutomaticEnableChannelEnabled
	DrawingEnabled = cfg.DrawingEnabled
	TaskEnabled = cfg.TaskEnabled
	DataExportEnabled = cfg.DataExportEnabled
	DataExportInterval = cfg.DataExportInterval
	DataExportDefaultTime = cfg.DataExportDefaultTime
	TopUpLink = cfg.TopUpLink
	Footer = cfg.Footer
	Logo = cfg.Logo
	FileUploadPermission = cfg.FileUploadPermission
	FileDownloadPermission = cfg.FileDownloadPermission
	ImageUploadPermission = cfg.ImageUploadPermission
	ImageDownloadPermission = cfg.ImageDownloadPermission
	DisplayInCurrencyEnabled = cfg.DisplayInCurrencyEnabled
	DisplayTokenStatEnabled = cfg.DisplayTokenStatEnabled
	DefaultCollapseSidebar = cfg.DefaultCollapseSidebar
}

// GetGeneralRuntimeConfig returns a detached, coherent snapshot. Taking the
// option publication fence first means a bulk option refresh cannot expose a
// half-applied generation to request handlers. The internal lock-free variant
// is used by bootstrap code that already holds OptionMapRWMutex.
func GetGeneralRuntimeConfig() GeneralRuntimeConfig {
	OptionMapRWMutex.RLock()
	defer OptionMapRWMutex.RUnlock()
	return getGeneralRuntimeConfigWithoutOptionLock()
}

func getGeneralRuntimeConfigWithoutOptionLock() GeneralRuntimeConfig {
	generalRuntimeConfigMu.RLock()
	defer generalRuntimeConfigMu.RUnlock()
	return generalRuntimeConfigFromLegacyLocked()
}

// UpdateGeneralRuntimeConfig atomically publishes related legacy options. The
// callback mutates a private value and must not retain the pointer after it
// returns.
func UpdateGeneralRuntimeConfig(update func(*GeneralRuntimeConfig)) {
	if update == nil {
		return
	}
	generalRuntimeConfigMu.Lock()
	defer generalRuntimeConfigMu.Unlock()
	next := generalRuntimeConfigFromLegacyLocked()
	update(&next)
	applyGeneralRuntimeConfigToLegacyLocked(next)
}
