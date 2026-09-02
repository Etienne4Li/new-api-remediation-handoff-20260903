package model

import (
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// TestRuntimeConfigReadersUseTheOptionPublicationFence exercises the same
// updateOptionMap seam used by the admin option endpoint and the periodic
// database synchronizer.  Before the runtime snapshots were introduced,
// updateOptionMap wrote the exported globals while request code read them
// directly; -race reported a conflict as soon as an option was hot-reloaded.
func TestRuntimeConfigReadersUseTheOptionPublicationFence(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{
		"SMTPServer": "before",
		"RetryTimes": "0",
	}
	common.OptionMapRWMutex.Unlock()
	previousSMTP := common.GetSMTPConfig()
	previousRetry := common.GetRetryTimes()
	previousQuota := common.GetQuotaConfig()
	t.Cleanup(func() {
		common.UpdateSMTPConfig(func(cfg *common.SMTPConfig) { *cfg = previousSMTP })
		common.SetRetryTimes(previousRetry)
		common.SetQuotaPerUnit(previousQuota.QuotaPerUnit)
		common.SetPreConsumedQuota(previousQuota.PreConsumedQuota)
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for _, value := range []string{"smtp-a", "smtp-b", "smtp-a", "smtp-b"} {
			if err := updateOptionMap("SMTPServer", value); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for _, value := range []string{"500000", "600000", "500000", "600000"} {
			if err := updateOptionMap("QuotaPerUnit", value); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for _, value := range []string{"500", "700", "500", "700"} {
			if err := updateOptionMap("PreConsumedQuota", value); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for _, value := range []string{"1", "2", "1", "2"} {
			if err := updateOptionMap("RetryTimes", value); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for range []int{0, 1, 2, 3} {
			_ = common.GetSMTPConfig()
			_ = common.GetRetryTimes()
			_ = common.GetQuotaConfig()
		}
	}()

	close(start)
	wg.Wait()
	select {
	case err := <-errs:
		require.NoError(t, err)
	default:
	}

	cfg := common.GetSMTPConfig()
	require.Contains(t, []string{"smtp-a", "smtp-b"}, cfg.Server)
	require.Contains(t, []int{1, 2}, common.GetRetryTimes())
	quota := common.GetQuotaConfig()
	require.Contains(t, []float64{500000, 600000}, quota.QuotaPerUnit)
	require.Contains(t, []int{500, 700}, quota.PreConsumedQuota)
}

func TestLogConsumeEnabledUsesSynchronizedHotReload(t *testing.T) {
	previousFlag := common.IsLogConsumeEnabled()
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.SetLogConsumeEnabled(previousFlag)
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			value := "false"
			if i%2 == 1 {
				value = "true"
			}
			require.NoError(t, updateOptionMap("LogConsumeEnabled", value))
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			_ = common.IsLogConsumeEnabled()
		}
	}()
	close(start)
	wg.Wait()
}

func TestRecordLogHonorsHotReloadedConsumeLogFlag(t *testing.T) {
	previousFlag := common.IsLogConsumeEnabled()
	t.Cleanup(func() { common.SetLogConsumeEnabled(previousFlag) })

	content := "runtime-log-consume-flag-regression"
	common.SetLogConsumeEnabled(false)
	RecordLog(0, LogTypeConsume, content)
	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("type = ? AND content = ?", LogTypeConsume, content).Count(&count).Error)
	if count != 0 {
		t.Fatalf("consume log recorded while disabled: count=%d", count)
	}

	common.SetLogConsumeEnabled(true)
	RecordLog(0, LogTypeConsume, content)
	require.Eventually(t, func() bool {
		var current int64
		if err := LOG_DB.Model(&Log{}).Where("type = ? AND content = ?", LogTypeConsume, content).Count(&current).Error; err != nil {
			return false
		}
		return current == 1
	}, 2*time.Second, 10*time.Millisecond)
}
