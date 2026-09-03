package perf_metrics_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
)

func TestGroupOrderRoundTrip(t *testing.T) {
	original := perfMetricsSetting
	t.Cleanup(func() { perfMetricsSetting = original })

	if err := config.UpdateConfigFromMap(&perfMetricsSetting, map[string]string{"group_order": `["vip","default"]`}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	got := GetGroupOrder()
	if len(got) != 2 || got[0] != "vip" || got[1] != "default" {
		t.Fatalf("unexpected order: %v", got)
	}
	got[0] = "mutated"
	if perfMetricsSetting.GroupOrder[0] != "vip" {
		t.Fatalf("GetGroupOrder must return a copy")
	}

	if err := config.UpdateConfigFromMap(&perfMetricsSetting, map[string]string{"group_order": `[]`}); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if got := GetGroupOrder(); len(got) != 0 || got == nil {
		t.Fatalf("expected empty non-nil slice, got %v", got)
	}
}
