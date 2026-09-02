package operation_setting

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentSettingSnapshotIsDeepCopyAndAtomic(t *testing.T) {
	original := GetPaymentSettingSnapshot()
	t.Cleanup(func() {
		UpdatePaymentSetting(func(cfg *PaymentSetting) {
			*cfg = clonePaymentSetting(&original)
		})
	})

	UpdatePaymentSetting(func(cfg *PaymentSetting) {
		cfg.AmountOptions = []int{10}
		cfg.AmountDiscount = map[int]float64{10: 0.1}
		cfg.ComplianceConfirmed = true
		cfg.ComplianceTermsVersion = "generation-0"
	})

	// A request-owned copy must not be able to mutate the published maps/slices.
	copy := GetPaymentSettingSnapshot()
	copy.AmountOptions[0] = 999
	copy.AmountDiscount[10] = 0.9
	copy.AmountOptions = append(copy.AmountOptions, 20)
	copy.AmountDiscount[20] = 0.8
	unchanged := GetPaymentSettingSnapshot()
	require.Equal(t, []int{10}, unchanged.AmountOptions)
	require.Equal(t, map[int]float64{10: 0.1}, unchanged.AmountDiscount)

	// Every publication changes related fields together. Readers must never see
	// a slice/map from different generations while a writer is hot-reloading.
	const iterations = 2000
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for generation := 1; generation <= iterations; generation++ {
			g := generation
			UpdatePaymentSetting(func(cfg *PaymentSetting) {
				cfg.AmountOptions = []int{g}
				cfg.AmountDiscount = map[int]float64{g: float64(g) / 100}
				cfg.ComplianceTermsVersion = fmt.Sprintf("generation-%d", g)
			})
		}
	}()
	for reader := 0; reader < 7; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				cfg := GetPaymentSettingSnapshot()
				if len(cfg.AmountOptions) != 1 || len(cfg.AmountDiscount) != 1 {
					errCh <- fmt.Errorf("mixed payment snapshot: options=%v discount=%v", cfg.AmountOptions, cfg.AmountDiscount)
					return
				}
				amount := cfg.AmountOptions[0]
				if cfg.AmountDiscount[amount] != float64(amount)/100 {
					errCh <- fmt.Errorf("mixed payment generation: options=%v discount=%v", cfg.AmountOptions, cfg.AmountDiscount)
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestPaymentRuntimeConfigSnapshotIsDeepCopyAndAtomic(t *testing.T) {
	original := GetPaymentRuntimeConfig()
	t.Cleanup(func() {
		UpdatePaymentRuntimeConfig(func(cfg *PaymentRuntimeConfig) {
			*cfg = original
		})
	})

	UpdatePaymentRuntimeConfig(func(cfg *PaymentRuntimeConfig) {
		cfg.PayAddress = "https://pay-0.example"
		cfg.EpayID = "merchant-0"
		cfg.EpayKey = "key-0"
		cfg.PayMethods = []map[string]string{{"type": "method-0"}}
	})
	copy := GetPaymentRuntimeConfig()
	copy.PayMethods[0]["type"] = "mutated"
	copy.PayMethods = append(copy.PayMethods, map[string]string{"type": "other"})
	unchanged := GetPaymentRuntimeConfig()
	require.Equal(t, "method-0", unchanged.PayMethods[0]["type"])
	require.Len(t, unchanged.PayMethods, 1)

	const iterations = 2000
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for generation := 1; generation <= iterations; generation++ {
			g := generation
			UpdatePaymentRuntimeConfig(func(cfg *PaymentRuntimeConfig) {
				cfg.PayAddress = fmt.Sprintf("https://pay-%d.example", g)
				cfg.EpayID = fmt.Sprintf("merchant-%d", g)
				cfg.EpayKey = fmt.Sprintf("key-%d", g)
				cfg.Price = float64(g)
				cfg.PayMethods = []map[string]string{{"type": fmt.Sprintf("method-%d", g)}}
			})
		}
	}()
	for reader := 0; reader < 7; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				cfg := GetPaymentRuntimeConfig()
				if len(cfg.PayMethods) != 1 {
					errCh <- fmt.Errorf("invalid payment runtime methods: %v", cfg.PayMethods)
					return
				}
				method := cfg.PayMethods[0]["type"]
				want := "method-" + strings.TrimPrefix(cfg.EpayID, "merchant-")
				if method != want || cfg.PayAddress != "https://pay-"+strings.TrimPrefix(cfg.EpayID, "merchant-")+".example" || cfg.EpayKey != "key-"+strings.TrimPrefix(cfg.EpayID, "merchant-") {
					errCh <- fmt.Errorf("mixed payment runtime snapshot: %+v", cfg)
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestGeneralSettingSnapshotIsAtomic(t *testing.T) {
	original := GetGeneralSettingSnapshot()
	t.Cleanup(func() {
		UpdateGeneralSetting(func(cfg *GeneralSetting) {
			*cfg = original
		})
	})

	const iterations = 2000
	UpdateGeneralSetting(func(cfg *GeneralSetting) {
		cfg.DocsLink = "https://docs-0.example"
		cfg.PingIntervalSeconds = 0
		cfg.CustomCurrencySymbol = "C0"
	})
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for generation := 1; generation <= iterations; generation++ {
			g := generation
			UpdateGeneralSetting(func(cfg *GeneralSetting) {
				cfg.DocsLink = fmt.Sprintf("https://docs-%d.example", g)
				cfg.PingIntervalSeconds = g
				cfg.CustomCurrencySymbol = fmt.Sprintf("C%d", g)
			})
		}
	}()
	for reader := 0; reader < 7; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				cfg := GetGeneralSettingSnapshot()
				generation := strings.TrimSuffix(strings.TrimPrefix(cfg.DocsLink, "https://docs-"), ".example")
				if generation == "" || fmt.Sprintf("C%s", generation) != cfg.CustomCurrencySymbol {
					errCh <- fmt.Errorf("mixed general snapshot: %+v", cfg)
					return
				}
				if strconv.Itoa(cfg.PingIntervalSeconds) != generation {
					errCh <- fmt.Errorf("mixed general generation: %+v", cfg)
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestScalarOperationSettingsConfigUpdatesAreAtomic(t *testing.T) {
	originalCheckin := GetCheckinSettingSnapshot()
	originalMonitor := GetMonitorSettingSnapshot()
	originalQuota := GetQuotaSettingSnapshot()
	originalToken := GetTokenSettingSnapshot()
	t.Cleanup(func() {
		UpdateCheckinSetting(func(cfg *CheckinSetting) { *cfg = originalCheckin })
		UpdateMonitorSetting(func(cfg *MonitorSetting) { *cfg = originalMonitor })
		UpdateQuotaSetting(func(cfg *QuotaSetting) { *cfg = originalQuota })
		UpdateTokenSetting(func(cfg *TokenSetting) { *cfg = originalToken })
	})

	// The custom ConfigMapUpdater path must update all fields as one logical
	// publication. In particular, an invalid sibling must leave the old value
	// untouched instead of committing whichever field reflection visited first.
	checkinCfg := config.GlobalConfig.Get("checkin_setting")
	require.NotNil(t, checkinCfg)
	require.NoError(t, config.UpdateConfigFromMap(checkinCfg, map[string]string{
		"enabled":   "true",
		"min_quota": "11",
		"max_quota": "22",
	}))
	checkin := GetCheckinSettingSnapshot()
	assert.True(t, checkin.Enabled)
	assert.Equal(t, 11, checkin.MinQuota)
	assert.Equal(t, 22, checkin.MaxQuota)
	require.Error(t, config.UpdateConfigFromMap(checkinCfg, map[string]string{
		"enabled":   "false",
		"min_quota": "not-an-int",
		"max_quota": "99",
	}))
	checkin = GetCheckinSettingSnapshot()
	assert.True(t, checkin.Enabled)
	assert.Equal(t, 11, checkin.MinQuota)
	assert.Equal(t, 22, checkin.MaxQuota)

	// Exercise the remaining scalar modules through their public atomic
	// updaters. Readers only ever receive a complete value copy.
	UpdateMonitorSetting(func(cfg *MonitorSetting) {
		cfg.AutoTestChannelMinutes = 7
		cfg.ChannelTestConcurrency = 4
		cfg.ChannelTestMode = ChannelTestModeAutoBanOnly
	})
	UpdateQuotaSetting(func(cfg *QuotaSetting) { cfg.EnableFreeModelPreConsume = false })
	UpdateTokenSetting(func(cfg *TokenSetting) { cfg.MaxUserTokens = 77 })
	monitor := GetMonitorSettingSnapshot()
	assert.Equal(t, 7.0, monitor.AutoTestChannelMinutes)
	assert.Equal(t, 4, monitor.ChannelTestConcurrency)
	assert.Equal(t, ChannelTestModeAutoBanOnly, monitor.ChannelTestMode)
	assert.False(t, GetQuotaSettingSnapshot().EnableFreeModelPreConsume)
	assert.Equal(t, 77, GetTokenSettingSnapshot().MaxUserTokens)
}

func TestChannelAffinitySettingSnapshotDeepCopy(t *testing.T) {
	original := GetChannelAffinitySettingSnapshot()
	t.Cleanup(func() {
		UpdateChannelAffinitySetting(func(cfg *ChannelAffinitySetting) { *cfg = original })
	})

	UpdateChannelAffinitySetting(func(cfg *ChannelAffinitySetting) {
		cfg.Rules = []ChannelAffinityRule{{
			Name:       "copy-test",
			ModelRegex: []string{"^model-"},
			KeySources: []ChannelAffinityKeySource{{Type: "gjson", Path: "meta.key"}},
			ParamOverrideTemplate: map[string]interface{}{
				"operations": []map[string]interface{}{{
					"mode":  "pass_headers",
					"value": []string{"X-Test"},
				}},
			},
		}}
	})

	snapshot := GetChannelAffinitySettingSnapshot()
	require.Len(t, snapshot.Rules, 1)
	snapshot.Rules[0].ModelRegex[0] = "mutated"
	snapshot.Rules[0].KeySources[0].Path = "mutated.path"
	ops := snapshot.Rules[0].ParamOverrideTemplate["operations"].([]map[string]interface{})
	ops[0]["mode"] = "mutated"
	ops[0]["value"].([]string)[0] = "mutated-header"

	unchanged := GetChannelAffinitySettingSnapshot()
	require.Len(t, unchanged.Rules, 1)
	assert.Equal(t, "^model-", unchanged.Rules[0].ModelRegex[0])
	assert.Equal(t, "meta.key", unchanged.Rules[0].KeySources[0].Path)
	unchangedOps := unchanged.Rules[0].ParamOverrideTemplate["operations"].([]map[string]interface{})
	assert.Equal(t, "pass_headers", unchangedOps[0]["mode"])
	assert.Equal(t, "X-Test", unchangedOps[0]["value"].([]string)[0])
}
