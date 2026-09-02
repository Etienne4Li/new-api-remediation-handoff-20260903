package perf_metrics_setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

type PerfMetricsSetting struct {
	Enabled       bool   `json:"enabled"`
	FlushInterval int    `json:"flush_interval"`
	BucketTime    string `json:"bucket_time"`
	RetentionDays int    `json:"retention_days"`
}

const (
	DefaultFlushIntervalMinutes = 5
	MaxFlushIntervalMinutes     = 7 * 24 * 60
	MaxRetentionDays            = 3650
)

var perfMetricsSetting = PerfMetricsSetting{
	Enabled:       true,
	FlushInterval: DefaultFlushIntervalMinutes,
	BucketTime:    "hour",
	RetentionDays: 0,
}
var perfMetricsSettingMu sync.RWMutex

type perfMetricsSettingFields PerfMetricsSetting

func init() {
	config.GlobalConfig.Register("perf_metrics_setting", &perfMetricsSetting)
}

func GetSetting() PerfMetricsSetting {
	perfMetricsSettingMu.RLock()
	defer perfMetricsSettingMu.RUnlock()
	return perfMetricsSetting
}

func GetBucketSeconds() int64 {
	setting := GetSetting()
	switch setting.BucketTime {
	case "minute":
		return 60
	case "5min":
		return 300
	case "hour":
		return 3600
	default:
		return 3600
	}
}

func GetFlushIntervalMinutes() int {
	flushInterval := GetSetting().FlushInterval
	if flushInterval < 1 || flushInterval > MaxFlushIntervalMinutes {
		return DefaultFlushIntervalMinutes
	}
	return flushInterval
}

func (s *PerfMetricsSetting) ConfigSnapshot() interface{} {
	if s == nil {
		return PerfMetricsSetting{}
	}
	perfMetricsSettingMu.RLock()
	defer perfMetricsSettingMu.RUnlock()
	return *s
}

func (s *PerfMetricsSetting) ValidateConfigMap(values map[string]string) error {
	if s == nil {
		return config.ValidateConfigFromMap(&PerfMetricsSetting{}, values)
	}
	perfMetricsSettingMu.RLock()
	staged := perfMetricsSettingFields(*s)
	perfMetricsSettingMu.RUnlock()
	if err := config.ValidateConfigFromMap(&staged, values); err != nil {
		return err
	}
	return validatePerfMetricsSetting(PerfMetricsSetting(staged))
}

func (s *PerfMetricsSetting) UpdateConfigMap(values map[string]string) error {
	if s == nil {
		return fmt.Errorf("perf metrics setting must not be nil")
	}
	perfMetricsSettingMu.Lock()
	defer perfMetricsSettingMu.Unlock()
	staged := perfMetricsSettingFields(*s)
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	if err := validatePerfMetricsSetting(PerfMetricsSetting(staged)); err != nil {
		return err
	}
	*s = PerfMetricsSetting(staged)
	return nil
}

func validatePerfMetricsSetting(setting PerfMetricsSetting) error {
	if setting.FlushInterval < 1 || setting.FlushInterval > MaxFlushIntervalMinutes {
		return fmt.Errorf("flush_interval must be between 1 and %d minutes", MaxFlushIntervalMinutes)
	}
	if setting.RetentionDays < 0 || setting.RetentionDays > MaxRetentionDays {
		return fmt.Errorf("retention_days must be between 0 and %d", MaxRetentionDays)
	}
	switch setting.BucketTime {
	case "minute", "5min", "hour":
		return nil
	default:
		return fmt.Errorf("bucket_time must be one of minute, 5min, hour")
	}
}
