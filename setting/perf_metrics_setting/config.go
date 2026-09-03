package perf_metrics_setting

import "github.com/QuantumNous/new-api/setting/config"

type PerfMetricsSetting struct {
	Enabled       bool     `json:"enabled"`
	FlushInterval int      `json:"flush_interval"`
	BucketTime    string   `json:"bucket_time"`
	RetentionDays int      `json:"retention_days"`
	GroupOrder    []string `json:"group_order"`
}

var perfMetricsSetting = PerfMetricsSetting{
	Enabled:       true,
	FlushInterval: 5,
	BucketTime:    "hour",
	RetentionDays: 0,
	GroupOrder:    []string{},
}

func init() {
	config.GlobalConfig.Register("perf_metrics_setting", &perfMetricsSetting)
}

func GetSetting() PerfMetricsSetting {
	return perfMetricsSetting
}

func GetGroupOrder() []string {
	if len(perfMetricsSetting.GroupOrder) == 0 {
		return []string{}
	}
	return append([]string(nil), perfMetricsSetting.GroupOrder...)
}

func GetBucketSeconds() int64 {
	switch perfMetricsSetting.BucketTime {
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
	if perfMetricsSetting.FlushInterval < 1 {
		return 1
	}
	return perfMetricsSetting.FlushInterval
}
