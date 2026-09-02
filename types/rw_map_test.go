package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromJsonStringKeepsLiveMapOnDecodeError(t *testing.T) {
	m := NewRWMap[string, int]()
	m.Set("model-a", 7)

	err := LoadFromJsonString(m, `{"model-a":`)
	require.Error(t, err)
	assert.Equal(t, map[string]int{"model-a": 7}, m.ReadAll())
}

func TestLoadFromJsonStringWithCallbackRunsOnlyAfterAtomicSwap(t *testing.T) {
	m := NewRWMap[string, int]()
	m.Set("model-a", 7)
	called := false

	err := LoadFromJsonStringWithCallback(m, `{"model-b": 9}`, func() { called = true })
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, map[string]int{"model-b": 9}, m.ReadAll())

	called = false
	err = LoadFromJsonStringWithCallback(m, `[]`, func() { called = true })
	require.Error(t, err)
	assert.False(t, called)
	assert.Equal(t, map[string]int{"model-b": 9}, m.ReadAll())
}
