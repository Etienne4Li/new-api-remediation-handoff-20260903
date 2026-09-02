package setting

import "sync"

// The legacy exported flags remain for compatibility. Runtime readers should
// use GetMidjourneyConfig so option hot-reloads publish a coherent set.
var MjNotifyEnabled = false
var MjAccountFilterEnabled = false
var MjModeClearEnabled = false
var MjForwardUrlEnabled = true
var MjActionCheckSuccessEnabled = true

type MidjourneyConfig struct {
	NotifyEnabled             bool
	AccountFilterEnabled      bool
	ModeClearEnabled          bool
	ForwardURLEnabled         bool
	ActionCheckSuccessEnabled bool
}

var midjourneyConfigMu sync.RWMutex

func midjourneyConfigFromLegacy() MidjourneyConfig {
	return MidjourneyConfig{
		NotifyEnabled:             MjNotifyEnabled,
		AccountFilterEnabled:      MjAccountFilterEnabled,
		ModeClearEnabled:          MjModeClearEnabled,
		ForwardURLEnabled:         MjForwardUrlEnabled,
		ActionCheckSuccessEnabled: MjActionCheckSuccessEnabled,
	}
}

func applyMidjourneyConfigToLegacy(cfg MidjourneyConfig) {
	MjNotifyEnabled = cfg.NotifyEnabled
	MjAccountFilterEnabled = cfg.AccountFilterEnabled
	MjModeClearEnabled = cfg.ModeClearEnabled
	MjForwardUrlEnabled = cfg.ForwardURLEnabled
	MjActionCheckSuccessEnabled = cfg.ActionCheckSuccessEnabled
}

func GetMidjourneyConfig() MidjourneyConfig {
	midjourneyConfigMu.RLock()
	defer midjourneyConfigMu.RUnlock()
	return midjourneyConfigFromLegacy()
}

func UpdateMidjourneyConfig(update func(*MidjourneyConfig)) {
	if update == nil {
		return
	}
	midjourneyConfigMu.Lock()
	defer midjourneyConfigMu.Unlock()
	cfg := midjourneyConfigFromLegacy()
	update(&cfg)
	applyMidjourneyConfigToLegacy(cfg)
}
