package setting

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

const DefaultMaxTokenAutoGroups = 5

var autoGroups = []string{
	"default",
}
var autoGroupsMu sync.RWMutex

var DefaultUseAutoGroup = false
var defaultUseAutoGroupMu sync.RWMutex

func GetDefaultUseAutoGroup() bool {
	defaultUseAutoGroupMu.RLock()
	defer defaultUseAutoGroupMu.RUnlock()
	return DefaultUseAutoGroup
}

func SetDefaultUseAutoGroup(enabled bool) {
	defaultUseAutoGroupMu.Lock()
	DefaultUseAutoGroup = enabled
	defaultUseAutoGroupMu.Unlock()
}

var maxTokenAutoGroups atomic.Int64

func init() {
	maxTokenAutoGroups.Store(DefaultMaxTokenAutoGroups)
}

func ContainsAutoGroup(group string) bool {
	autoGroupsMu.RLock()
	defer autoGroupsMu.RUnlock()
	for _, autoGroup := range autoGroups {
		if autoGroup == group {
			return true
		}
	}
	return false
}

func UpdateAutoGroupsByJsonString(jsonString string) error {
	var decoded []string
	if err := common.Unmarshal([]byte(jsonString), &decoded); err != nil {
		return err
	}
	if decoded == nil {
		decoded = make([]string, 0)
	}
	decoded = append([]string(nil), decoded...)
	autoGroupsMu.Lock()
	autoGroups = decoded
	autoGroupsMu.Unlock()
	return nil
}

func AutoGroups2JsonString() string {
	autoGroupsMu.RLock()
	snapshot := append([]string(nil), autoGroups...)
	autoGroupsMu.RUnlock()
	jsonBytes, err := common.Marshal(snapshot)
	if err != nil {
		return "[]"
	}
	return string(jsonBytes)
}

func GetAutoGroups() []string {
	autoGroupsMu.RLock()
	defer autoGroupsMu.RUnlock()
	return append([]string(nil), autoGroups...)
}

func GetMaxTokenAutoGroups() int {
	return int(maxTokenAutoGroups.Load())
}

func ValidateMaxTokenAutoGroups(value string) error {
	maxCount, err := strconv.Atoi(value)
	if err != nil || maxCount <= 0 {
		return fmt.Errorf("MaxTokenAutoGroups must be a positive integer")
	}
	return nil
}

func UpdateMaxTokenAutoGroups(value string) error {
	if err := ValidateMaxTokenAutoGroups(value); err != nil {
		return err
	}
	maxCount, _ := strconv.Atoi(value)
	maxTokenAutoGroups.Store(int64(maxCount))
	return nil
}
