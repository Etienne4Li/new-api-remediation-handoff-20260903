package system_setting

import "sync"

// The exported variables are retained for source compatibility with older
// integrations. Runtime code must use RuntimeConfig snapshots so an admin
// hot-reload cannot race a request while it reads a URL and its credential.
var ServerAddress = "http://localhost:3000"
var WorkerUrl = ""
var WorkerValidKey = ""
var WorkerAllowHttpImageRequestEnabled = false

type RuntimeConfig struct {
	ServerAddress                      string
	WorkerURL                          string
	WorkerValidKey                     string
	WorkerAllowHttpImageRequestEnabled bool
}

var runtimeConfigMu sync.RWMutex

func runtimeConfigFromLegacy() RuntimeConfig {
	return RuntimeConfig{
		ServerAddress:                      ServerAddress,
		WorkerURL:                          WorkerUrl,
		WorkerValidKey:                     WorkerValidKey,
		WorkerAllowHttpImageRequestEnabled: WorkerAllowHttpImageRequestEnabled,
	}
}

func applyRuntimeConfigToLegacy(cfg RuntimeConfig) {
	ServerAddress = cfg.ServerAddress
	WorkerUrl = cfg.WorkerURL
	WorkerValidKey = cfg.WorkerValidKey
	WorkerAllowHttpImageRequestEnabled = cfg.WorkerAllowHttpImageRequestEnabled
}

func GetRuntimeConfig() RuntimeConfig {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	return runtimeConfigFromLegacy()
}

func UpdateRuntimeConfig(update func(*RuntimeConfig)) {
	if update == nil {
		return
	}
	runtimeConfigMu.Lock()
	defer runtimeConfigMu.Unlock()
	cfg := runtimeConfigFromLegacy()
	update(&cfg)
	applyRuntimeConfigToLegacy(cfg)
}

func GetServerAddress() string {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	return ServerAddress
}

func SetServerAddress(value string) {
	runtimeConfigMu.Lock()
	ServerAddress = value
	runtimeConfigMu.Unlock()
}

func GetWorkerURL() string {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	return WorkerUrl
}

func GetWorkerValidKey() string {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	return WorkerValidKey
}

func GetWorkerAllowHttpImageRequestEnabled() bool {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	return WorkerAllowHttpImageRequestEnabled
}

func SetWorkerAllowHttpImageRequestEnabled(value bool) {
	runtimeConfigMu.Lock()
	WorkerAllowHttpImageRequestEnabled = value
	runtimeConfigMu.Unlock()
}

func EnableWorker() bool {
	return GetWorkerURL() != ""
}
