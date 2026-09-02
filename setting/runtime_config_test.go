package setting

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

func TestRuntimeConfigPublishesCoherentSnapshots(t *testing.T) {
	previous := system_setting.GetRuntimeConfig()
	t.Cleanup(func() {
		system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) { *cfg = previous })
	})

	first := system_setting.RuntimeConfig{
		ServerAddress:                      "https://a.example",
		WorkerURL:                          "https://worker-a.example",
		WorkerValidKey:                     "worker-key-a",
		WorkerAllowHttpImageRequestEnabled: false,
	}
	second := system_setting.RuntimeConfig{
		ServerAddress:                      "https://b.example",
		WorkerURL:                          "https://worker-b.example",
		WorkerValidKey:                     "worker-key-b",
		WorkerAllowHttpImageRequestEnabled: true,
	}
	system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) { *cfg = first })

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			value := first
			if i%2 == 1 {
				value = second
			}
			system_setting.UpdateRuntimeConfig(func(cfg *system_setting.RuntimeConfig) { *cfg = value })
		}
	}()
	seen := make(chan system_setting.RuntimeConfig, 128)
	go func() {
		defer wg.Done()
		defer close(seen)
		<-start
		for i := 0; i < 128; i++ {
			seen <- system_setting.GetRuntimeConfig()
		}
	}()
	close(start)
	wg.Wait()
	for got := range seen {
		require.Truef(t, got == first || got == second, "mixed system runtime snapshot: %+v", got)
	}
}

func TestMidjourneyAndSensitiveSnapshotsPublishCoherently(t *testing.T) {
	previousMidjourney := GetMidjourneyConfig()
	previousSensitive := GetSensitiveConfig()
	t.Cleanup(func() {
		UpdateMidjourneyConfig(func(cfg *MidjourneyConfig) { *cfg = previousMidjourney })
		UpdateSensitiveConfig(func(cfg *SensitiveConfig) { *cfg = previousSensitive })
	})

	firstMidjourney := MidjourneyConfig{NotifyEnabled: true, AccountFilterEnabled: false, ModeClearEnabled: true, ForwardURLEnabled: false, ActionCheckSuccessEnabled: true}
	secondMidjourney := MidjourneyConfig{NotifyEnabled: false, AccountFilterEnabled: true, ModeClearEnabled: false, ForwardURLEnabled: true, ActionCheckSuccessEnabled: false}
	firstSensitive := SensitiveConfig{CheckEnabled: true, CheckOnPromptEnabled: true, StopOnSensitive: false, StreamCacheQueueLen: 1, SensitiveWords: []string{"alpha", "beta"}}
	secondSensitive := SensitiveConfig{CheckEnabled: false, CheckOnPromptEnabled: false, StopOnSensitive: true, StreamCacheQueueLen: 2, SensitiveWords: []string{"gamma", "delta"}}
	UpdateMidjourneyConfig(func(cfg *MidjourneyConfig) { *cfg = firstMidjourney })
	UpdateSensitiveConfig(func(cfg *SensitiveConfig) { *cfg = firstSensitive })

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 128; i++ {
			midjourney := firstMidjourney
			sensitive := firstSensitive
			if i%2 == 1 {
				midjourney = secondMidjourney
				sensitive = secondSensitive
			}
			UpdateMidjourneyConfig(func(cfg *MidjourneyConfig) { *cfg = midjourney })
			UpdateSensitiveConfig(func(cfg *SensitiveConfig) { *cfg = sensitive })
		}
	}()
	seen := make(chan struct {
		midjourney MidjourneyConfig
		sensitive  SensitiveConfig
	}, 128)
	go func() {
		defer wg.Done()
		defer close(seen)
		<-start
		for i := 0; i < 128; i++ {
			seen <- struct {
				midjourney MidjourneyConfig
				sensitive  SensitiveConfig
			}{GetMidjourneyConfig(), GetSensitiveConfig()}
		}
	}()
	close(start)
	wg.Wait()
	for got := range seen {
		require.Truef(t, got.midjourney == firstMidjourney || got.midjourney == secondMidjourney, "mixed Midjourney snapshot: %+v", got.midjourney)
		require.Len(t, got.sensitive.SensitiveWords, 2)
		require.Contains(t, []string{"alpha", "gamma"}, got.sensitive.SensitiveWords[0])
	}

	words := GetSensitiveConfig().SensitiveWords
	words[0] = "mutated-outside"
	require.NotEqual(t, "mutated-outside", GetSensitiveConfig().SensitiveWords[0])
}
