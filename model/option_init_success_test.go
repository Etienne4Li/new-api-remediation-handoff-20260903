package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// InitOptionMap publishes while holding OptionMapRWMutex.  Keep this success
// path under a watchdog because a lock-taking serializer called from inside
// that critical section would otherwise leave startup hung indefinitely.
func TestInitOptionMapSuccessfulPublicationDoesNotDeadlock(t *testing.T) {
	previousDB := DB
	previousOperationConfig := operation_setting.GetOperationRuntimeConfig()
	common.OptionMapRWMutex.RLock()
	previousMap := common.OptionMap
	common.OptionMapRWMutex.RUnlock()
	t.Cleanup(func() {
		DB = previousDB
		operation_setting.UpdateOperationRuntimeConfig(func(config *operation_setting.OperationRuntimeConfig) {
			*config = previousOperationConfig
		})
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	require.NoError(t, db.Create([]Option{
		{Key: "DemoSiteEnabled", Value: "true"},
		{Key: "SelfUseModeEnabled", Value: "false"},
		{Key: "AutomaticDisableKeywords", Value: "account disabled"},
		{Key: "AutomaticDisableStatusCodes", Value: "401"},
		{Key: "AutomaticRetryStatusCodes", Value: "429"},
	}).Error)
	DB = db
	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{"sentinel": "before"}
	common.OptionMapRWMutex.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- InitOptionMap()
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("InitOptionMap did not complete; possible recursive OptionMapRWMutex acquisition")
	}
}
