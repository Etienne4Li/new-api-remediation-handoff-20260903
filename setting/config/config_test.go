package config

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAtomicConfig models a registered module whose live object cannot be
// safely mutated by ConfigManager's reflection path. The updater validates an
// isolated map, then publishes all supplied fields under one lock.
type testAtomicConfig struct {
	mu sync.RWMutex
	A  int `json:"a"`
	B  int `json:"b"`
}

// testCommitFailureConfig intentionally mutates before returning an error so
// ConfigManager's rollback path is exercised for both the failing module and
// modules committed earlier in the sorted order.
type testCommitFailureConfig struct {
	mu   sync.RWMutex
	A    string `json:"a"`
	B    string `json:"b"`
	fail bool
}

func (c *testCommitFailureConfig) ConfigSnapshot() interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return struct {
		A string `json:"a"`
		B string `json:"b"`
	}{A: c.A, B: c.B}
}

func (c *testCommitFailureConfig) ValidateConfigMap(values map[string]string) error {
	return nil
}

func (c *testCommitFailureConfig) RestoreConfigSnapshot(snapshot interface{}) error {
	value, ok := snapshot.(struct {
		A string `json:"a"`
		B string `json:"b"`
	})
	if !ok {
		return errors.New("unexpected snapshot type")
	}
	c.mu.Lock()
	c.A, c.B = value.A, value.B
	c.mu.Unlock()
	return nil
}

func (c *testCommitFailureConfig) UpdateConfigMap(values map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value, ok := values["a"]; ok {
		c.A = value
	}
	if value, ok := values["b"]; ok {
		if c.fail && value == "fail" {
			return assertErrCommitFailure
		}
		c.B = value
	}
	return nil
}

var assertErrCommitFailure = errors.New("intentional commit failure")

func (c *testAtomicConfig) ConfigSnapshot() interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return struct {
		A int `json:"a"`
		B int `json:"b"`
	}{A: c.A, B: c.B}
}

func (c *testAtomicConfig) ValidateConfigMap(values map[string]string) error {
	for key, raw := range values {
		if key != "a" && key != "b" {
			continue
		}
		if _, err := strconv.Atoi(raw); err != nil {
			return err
		}
	}
	return nil
}

func (c *testAtomicConfig) UpdateConfigMap(values map[string]string) error {
	if err := c.ValidateConfigMap(values); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	nextA, nextB := c.A, c.B
	if raw, ok := values["a"]; ok {
		nextA, _ = strconv.Atoi(raw)
	}
	if raw, ok := values["b"]; ok {
		nextB, _ = strconv.Atoi(raw)
	}
	c.A, c.B = nextA, nextB
	return nil
}

type testConfigWithMap struct {
	Modes map[string]string `json:"modes"`
	Exprs map[string]string `json:"exprs"`
	Name  string            `json:"name"`
}

func TestUpdateConfigFromMap_MapReplacement(t *testing.T) {
	cfg := &testConfigWithMap{
		Modes: map[string]string{
			"model-a": "tiered_expr",
			"model-b": "tiered_expr",
		},
		Exprs: map[string]string{
			"model-a": "p * 5 + c * 25",
			"model-b": "p * 10 + c * 50",
		},
		Name: "billing",
	}

	// Simulate removing model-a: new value only has model-b
	err := UpdateConfigFromMap(cfg, map[string]string{
		"modes": `{"model-b": "tiered_expr"}`,
		"exprs": `{"model-b": "p * 10 + c * 50"}`,
	})
	if err != nil {
		t.Fatalf("UpdateConfigFromMap failed: %v", err)
	}

	if _, ok := cfg.Modes["model-a"]; ok {
		t.Errorf("Modes still contains model-a after it was removed from the update; got %v", cfg.Modes)
	}
	if _, ok := cfg.Exprs["model-a"]; ok {
		t.Errorf("Exprs still contains model-a after it was removed from the update; got %v", cfg.Exprs)
	}

	if cfg.Modes["model-b"] != "tiered_expr" {
		t.Errorf("Modes[model-b] = %q, want %q", cfg.Modes["model-b"], "tiered_expr")
	}
	if cfg.Exprs["model-b"] != "p * 10 + c * 50" {
		t.Errorf("Exprs[model-b] = %q, want %q", cfg.Exprs["model-b"], "p * 10 + c * 50")
	}
}

func TestUpdateConfigFromMap_EmptyMapClearsAll(t *testing.T) {
	cfg := &testConfigWithMap{
		Modes: map[string]string{
			"model-a": "tiered_expr",
		},
		Exprs: map[string]string{
			"model-a": "p * 5 + c * 25",
		},
	}

	err := UpdateConfigFromMap(cfg, map[string]string{
		"modes": `{}`,
		"exprs": `{}`,
	})
	if err != nil {
		t.Fatalf("UpdateConfigFromMap failed: %v", err)
	}

	if len(cfg.Modes) != 0 {
		t.Errorf("Modes should be empty after updating with {}, got %v", cfg.Modes)
	}
	if len(cfg.Exprs) != 0 {
		t.Errorf("Exprs should be empty after updating with {}, got %v", cfg.Exprs)
	}
}

func TestUpdateConfigFromMap_ScalarFieldsUnchanged(t *testing.T) {
	cfg := &testConfigWithMap{
		Modes: map[string]string{"m": "v"},
		Name:  "old",
	}

	err := UpdateConfigFromMap(cfg, map[string]string{
		"name": "new",
	})
	if err != nil {
		t.Fatalf("UpdateConfigFromMap failed: %v", err)
	}

	if cfg.Name != "new" {
		t.Errorf("Name = %q, want %q", cfg.Name, "new")
	}
	// modes was not in configMap, should remain unchanged
	if cfg.Modes["m"] != "v" {
		t.Errorf("Modes should be unchanged, got %v", cfg.Modes)
	}
}

func TestUpdateConfigFromMapIsAtomicWhenOneFieldIsInvalid(t *testing.T) {
	cfg := &testConfigWithMap{
		Modes: map[string]string{"model-a": "ratio"},
		Exprs: map[string]string{"model-a": "p * 5"},
		Name:  "before",
	}

	err := UpdateConfigFromMap(cfg, map[string]string{
		"name":  "after",
		"modes": `{"model-b":"tiered_expr"}`,
		"exprs": `{invalid`,
	})
	require.Error(t, err)
	assert.Equal(t, "before", cfg.Name)
	assert.Equal(t, map[string]string{"model-a": "ratio"}, cfg.Modes)
	assert.Equal(t, map[string]string{"model-a": "p * 5"}, cfg.Exprs)
}

func TestConfigManagerLoadFromDBReportsInvalidValues(t *testing.T) {
	cfg := &testConfigWithMap{Name: "before"}
	manager := NewConfigManager()
	manager.Register("test", cfg)

	err := manager.LoadFromDB(map[string]string{
		"test.name":  "after",
		"test.modes": `{invalid`,
	})
	require.Error(t, err)
	assert.Equal(t, "before", cfg.Name)
}

func TestConfigManagerCustomUpdaterPublishesAtomicSnapshot(t *testing.T) {
	cfg := &testAtomicConfig{A: 1, B: 2}
	manager := NewConfigManager()
	manager.Register("atomic", cfg)

	exported := manager.ExportAllConfigs()
	assert.Equal(t, "1", exported["atomic.a"])
	assert.Equal(t, "2", exported["atomic.b"])

	require.NoError(t, manager.LoadFromDB(map[string]string{
		"atomic.a": "3",
		"atomic.b": "4",
	}))
	cfg.mu.RLock()
	assert.Equal(t, 3, cfg.A)
	assert.Equal(t, 4, cfg.B)
	cfg.mu.RUnlock()

	// Validation happens before publication, so one malformed field leaves both
	// previously published values untouched.
	require.Error(t, manager.LoadFromDB(map[string]string{
		"atomic.a": "5",
		"atomic.b": "not-an-int",
	}))
	cfg.mu.RLock()
	assert.Equal(t, 3, cfg.A)
	assert.Equal(t, 4, cfg.B)
	cfg.mu.RUnlock()
}

func TestConfigManagerLoadFromDBRollsBackCommittedModulesOnCommitFailure(t *testing.T) {
	first := &testAtomicConfig{A: 1, B: 2}
	second := &testCommitFailureConfig{A: "old-a", B: "old-b", fail: true}
	manager := NewConfigManager()
	// Sorted module order commits "first" before "second".
	manager.Register("first", first)
	manager.Register("second", second)

	err := manager.LoadFromDB(map[string]string{
		"first.a":  "10",
		"first.b":  "20",
		"second.a": "new-a",
		"second.b": "fail",
	})
	require.ErrorIs(t, err, assertErrCommitFailure)

	first.mu.RLock()
	assert.Equal(t, 1, first.A)
	assert.Equal(t, 2, first.B)
	first.mu.RUnlock()
	second.mu.RLock()
	assert.Equal(t, "old-a", second.A)
	assert.Equal(t, "old-b", second.B)
	second.mu.RUnlock()
}
