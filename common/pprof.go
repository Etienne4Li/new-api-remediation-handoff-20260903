package common

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/shirou/gopsutil/cpu"
)

const (
	defaultPPROFDirectory    = "./pprof"
	defaultPPROFThreshold    = 80.0
	pprofSampleDuration      = 10 * time.Second
	pprofPollInterval        = 30 * time.Second
	maxPPROFDirectoryNameLen = 4096
)

// Monitor 定时监控 CPU 使用率，超过阈值输出 pprof 文件。
//
// Profiling is an operational aid and must never be able to terminate the
// application.  A transient gopsutil/filesystem error is logged and retried
// on the next interval.  Profile files are private because they can contain
// request data and provider details.
func Monitor() {
	for {
		percent, err := cpu.Percent(time.Second, false)
		if err != nil {
			SysLog("读取 CPU 使用率失败 " + err.Error())
			time.Sleep(pprofPollInterval)
			continue
		}
		config := GetPerformanceMonitorConfig()
		threshold := config.CPUThreshold
		if threshold <= 0 {
			threshold = int(defaultPPROFThreshold)
		}
		if config.Enabled && len(percent) > 0 && percent[0] >= float64(threshold) {
			if err := captureCPUProfile(); err != nil {
				SysLog("采集 CPU pprof 失败 " + err.Error())
			}
		}
		time.Sleep(pprofPollInterval)
	}
}

func pprofDirectory() string {
	if raw := strings.TrimSpace(os.Getenv("PPROF_DIR")); raw != "" && len(raw) <= maxPPROFDirectoryNameLen {
		return raw
	}
	return defaultPPROFDirectory
}

// captureCPUProfile captures one bounded profile and closes all resources on
// every path, including StartCPUProfile failures.
func captureCPUProfile() error {
	directory := filepath.Clean(pprofDirectory())
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	path := filepath.Join(directory, fmt.Sprintf("cpu-%s.pprof", time.Now().UTC().Format("20060102150405.000000000")))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create profile file: %w", err)
	}
	started := false
	defer func() {
		if started {
			pprof.StopCPUProfile()
		}
		_ = f.Close()
	}()
	if err := pprof.StartCPUProfile(f); err != nil {
		return fmt.Errorf("start CPU profile: %w", err)
	}
	started = true
	time.Sleep(pprofSampleDuration)
	return nil
}
