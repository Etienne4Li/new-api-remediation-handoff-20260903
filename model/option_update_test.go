package model

import (
	"errors"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errOptionRollbackTest = errors.New("option rollback test failure")

func TestUpdateOptionMapValidatesStripeAccountID(t *testing.T) {
	original := setting.GetStripeConfig()
	t.Cleanup(func() {
		setting.UpdateStripeConfig(func(cfg *setting.StripeConfig) { *cfg = original })
	})

	require.Error(t, updateOptionMap("StripeAccountId", "platform-account"))
	assert.Equal(t, original.AccountID, setting.GetStripeConfig().AccountID)

	require.NoError(t, updateOptionMap("StripeAccountId", " acct_test_platform "))
	assert.Equal(t, "acct_test_platform", setting.GetStripeConfig().AccountID)
}

// optionRollbackConfig deliberately mutates its live state before returning an
// error for the second field. It models a registered runtime setting whose
// publication can fail after an earlier field in the same bulk update has
// already been applied.
type optionRollbackConfig struct {
	First  string `json:"first"`
	Second string `json:"second"`
	Fail   bool   `json:"-"`
	mu     sync.Mutex
}

func (c *optionRollbackConfig) ConfigSnapshot() interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return struct {
		First  string `json:"first"`
		Second string `json:"second"`
	}{First: c.First, Second: c.Second}
}

func (c *optionRollbackConfig) ValidateConfigMap(map[string]string) error {
	return nil
}

func (c *optionRollbackConfig) UpdateConfigMap(values map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value, ok := values["first"]; ok {
		c.First = value
	}
	if value, ok := values["second"]; ok {
		if c.Fail && value == "new-second" {
			return errOptionRollbackTest
		}
		c.Second = value
	}
	return nil
}

func (c *optionRollbackConfig) snapshotValues() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.First, c.Second
}

type optionFailingGeneralSetting struct {
	QuotaDisplayType string `json:"quota_display_type"`
}

func (s *optionFailingGeneralSetting) ConfigSnapshot() interface{} { return *s }

func (s *optionFailingGeneralSetting) ValidateConfigMap(map[string]string) error {
	return nil
}

func (s *optionFailingGeneralSetting) UpdateConfigMap(map[string]string) error {
	return errOptionRollbackTest
}

func TestUpdateOptionReturnsDatabaseErrorBeforePublishing(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousRetryTimes := common.RetryTimes
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousMap
		common.RetryTimes = previousRetryTimes
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// Deliberately do not migrate Option: every write must fail and must not
	// leak into the in-memory option map.
	DB = db
	common.OptionMap = map[string]string{"RetryTimes": "7"}
	common.RetryTimes = 7

	err = UpdateOption("RetryTimes", "9")
	require.Error(t, err)
	assert.Equal(t, "7", common.OptionMap["RetryTimes"])
	assert.Equal(t, 7, common.RetryTimes)
}

func TestLoadOptionsFromDatabaseReturnsReadErrorWithoutPublishing(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousRetryTimes := common.RetryTimes
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousMap
		common.RetryTimes = previousRetryTimes
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	require.NoError(t, db.Create(&Option{Key: "RetryTimes", Value: "9"}).Error)
	DB = db
	common.OptionMap = map[string]string{"RetryTimes": "7"}
	common.RetryTimes = 7

	// Force the read side to fail after the old runtime value has been
	// established. A failed sync must never turn into a partial publish.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	err = loadOptionsFromDatabase()
	require.Error(t, err)
	assert.Equal(t, "7", common.OptionMap["RetryTimes"])
	assert.Equal(t, 7, common.RetryTimes)
}

func TestInitOptionMapReturnsReadErrorWithoutReplacingSnapshot(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousRetryTimes := common.GetRetryTimes()
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
		common.SetRetryTimes(previousRetryTimes)
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	oldMap := map[string]string{"sentinel": "old", "RetryTimes": "7"}
	common.OptionMapRWMutex.Lock()
	common.OptionMap = oldMap
	common.OptionMapRWMutex.Unlock()
	common.SetRetryTimes(7)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	err = InitOptionMap()
	require.Error(t, err)
	assert.ErrorContains(t, err, "load options from database")
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, oldMap, common.OptionMap)
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, 7, common.GetRetryTimes())
}

func TestInitOptionMapRejectsMalformedSnapshotBeforeReplacingDefaults(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousRetryTimes := common.GetRetryTimes()
	previousPayMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
		common.SetRetryTimes(previousRetryTimes)
		operation_setting.PayMethods = previousPayMethods
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	require.NoError(t, db.Create([]Option{
		{Key: "RetryTimes", Value: "9"},
		{Key: "PayMethods", Value: "[{invalid"},
	}).Error)
	DB = db
	oldMap := map[string]string{"sentinel": "old", "RetryTimes": "7", "PayMethods": "old"}
	common.OptionMapRWMutex.Lock()
	common.OptionMap = oldMap
	common.OptionMapRWMutex.Unlock()
	common.SetRetryTimes(7)
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}

	err = InitOptionMap()
	require.Error(t, err)
	assert.ErrorContains(t, err, "PayMethods")
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, oldMap, common.OptionMap)
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, 7, common.GetRetryTimes())
	assert.Equal(t, []map[string]string{{"type": "alipay"}}, operation_setting.PayMethods)
}

func TestLoadOptionsFromDatabasePreflightsAllValuesBeforePublishing(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousRetryTimes := common.RetryTimes
	previousPayMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousMap
		common.RetryTimes = previousRetryTimes
		operation_setting.PayMethods = previousPayMethods
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	// RetryTimes is valid, but PayMethods is malformed. The valid option must
	// not publish before the complete snapshot passes validation.
	require.NoError(t, db.Create([]Option{
		{Key: "RetryTimes", Value: "9"},
		{Key: "PayMethods", Value: "[{invalid"},
	}).Error)
	DB = db
	common.OptionMap = map[string]string{"RetryTimes": "7", "PayMethods": "old"}
	common.RetryTimes = 7
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}

	err = loadOptionsFromDatabase()
	require.Error(t, err)
	assert.Equal(t, "7", common.OptionMap["RetryTimes"])
	assert.Equal(t, "old", common.OptionMap["PayMethods"])
	assert.Equal(t, 7, common.RetryTimes)
	assert.Equal(t, []map[string]string{{"type": "alipay"}}, operation_setting.PayMethods)
}

func TestLoadOptionsFromDatabaseSanitizesLegacyChatsWithoutBlockingOtherOptions(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousRetryTimes := common.RetryTimes
	previousChats := setting.GetChats()
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousMap
		common.RetryTimes = previousRetryTimes
		_ = setting.UpdateChatsByJsonString(settingChatsJSON(previousChats))
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	legacyChats := `[{"safe":"https://example.com"},{"unsafe":"https://example.com/?apiKey=leaked"},{"broken":"javascript:alert(1)"}]`
	require.NoError(t, db.Create([]Option{
		{Key: "Chats", Value: legacyChats},
		{Key: "RetryTimes", Value: "9"},
	}).Error)
	DB = db
	common.OptionMap = map[string]string{"Chats": legacyChats, "RetryTimes": "7"}
	common.RetryTimes = 7
	setting.Chats = []map[string]string{{"old": "https://old.example"}}

	require.NoError(t, loadOptionsFromDatabase())
	assert.Equal(t, 9, common.RetryTimes)
	assert.Equal(t, []map[string]string{{"safe": "https://example.com"}}, setting.GetChats())
	assert.JSONEq(t, `[{"safe":"https://example.com"}]`, common.OptionMap["Chats"])
}

func settingChatsJSON(chats []map[string]string) string {
	if chats == nil {
		return "[]"
	}
	bytesValue, err := common.Marshal(chats)
	if err != nil {
		return "[]"
	}
	return string(bytesValue)
}

func TestUpdateOptionRejectsInvalidValueBeforeDatabaseWrite(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousPayMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousMap
		operation_setting.PayMethods = previousPayMethods
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	oldValue := `[{"type":"alipay"}]`
	require.NoError(t, db.Create(&Option{Key: "PayMethods", Value: oldValue}).Error)
	common.OptionMap = map[string]string{"PayMethods": oldValue}
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}

	err = UpdateOption("PayMethods", `[{invalid`)
	require.Error(t, err)

	var persisted Option
	require.NoError(t, db.First(&persisted, "key = ?", "PayMethods").Error)
	assert.Equal(t, oldValue, persisted.Value)
	assert.Equal(t, oldValue, common.OptionMap["PayMethods"])
	assert.Equal(t, []map[string]string{{"type": "alipay"}}, operation_setting.PayMethods)
}

func TestUpdateOptionsBulkRejectsInvalidValueBeforeDatabaseWrite(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousChats := setting.Chats
	previousPayMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousMap
		setting.Chats = previousChats
		operation_setting.PayMethods = previousPayMethods
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	oldChats := `[{"old":"cherrystudio://old"}]`
	oldPayMethods := `[{"type":"alipay"}]`
	require.NoError(t, db.Create(&Option{Key: "Chats", Value: oldChats}).Error)
	require.NoError(t, db.Create(&Option{Key: "PayMethods", Value: oldPayMethods}).Error)
	common.OptionMap = map[string]string{"Chats": oldChats, "PayMethods": oldPayMethods}
	setting.Chats = []map[string]string{{"old": "cherrystudio://old"}}
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}

	err = UpdateOptionsBulk(map[string]string{
		"Chats":      `[{"new":"cherrystudio://new"}]`,
		"PayMethods": `[{invalid`,
	})
	require.Error(t, err)

	var persisted []Option
	require.NoError(t, db.Order("key asc").Find(&persisted).Error)
	require.Len(t, persisted, 2)
	assert.Equal(t, oldChats, persisted[0].Value)
	assert.Equal(t, oldPayMethods, persisted[1].Value)
	assert.Equal(t, oldChats, common.OptionMap["Chats"])
	assert.Equal(t, oldPayMethods, common.OptionMap["PayMethods"])
	assert.Equal(t, []map[string]string{{"old": "cherrystudio://old"}}, setting.Chats)
	assert.Equal(t, []map[string]string{{"type": "alipay"}}, operation_setting.PayMethods)
}

func TestUpdateOptionPublishesOnlyAfterSuccessfulDatabaseWrite(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousRetryTimes := common.RetryTimes
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousMap
		common.RetryTimes = previousRetryTimes
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	common.OptionMap = map[string]string{"RetryTimes": "7"}
	common.RetryTimes = 7

	require.NoError(t, UpdateOption("RetryTimes", "9"))
	assert.Equal(t, "9", common.OptionMap["RetryTimes"])
	assert.Equal(t, 9, common.RetryTimes)
	var persisted Option
	require.NoError(t, db.First(&persisted, "key = ?", "RetryTimes").Error)
	assert.Equal(t, "9", persisted.Value)
}

func TestUpdateOptionSanitizesFooterBeforePersistingAndPublishing(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousFooter := common.Footer
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousMap
		common.Footer = previousFooter
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	common.OptionMap = map[string]string{}
	common.Footer = ""

	require.NoError(t, UpdateOption("Footer", `<p onclick="alert(1)">Welcome</p><script>alert(2)</script><a href="javascript:alert(3)">bad</a>`))

	expected := `<p>Welcome</p><a>bad</a>`
	assert.Equal(t, expected, common.Footer)
	assert.Equal(t, expected, common.OptionMap["Footer"])
	var persisted Option
	require.NoError(t, db.First(&persisted, "key = ?", "Footer").Error)
	assert.Equal(t, expected, persisted.Value)
}

func TestUpdateOptionsBulkRollsBackExternalConfigWhenOptionMapWasMissing(t *testing.T) {
	previousDB := DB
	previousMap := common.OptionMap
	previousConfig := config.GlobalConfig.Get("general_setting")
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
		config.GlobalConfig.Register("general_setting", previousConfig)
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	require.NoError(t, db.Create([]Option{
		{Key: "general_setting.first", Value: "old-first"},
		{Key: "general_setting.second", Value: "old-second"},
	}).Error)
	DB = db

	// Keep the map intentionally sparse. This is possible during bootstrap or
	// after a newly registered setting; rollback must still restore the live
	// registered object, not merely delete the temporary map entries.
	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	settingState := &optionRollbackConfig{First: "old-first", Second: "old-second", Fail: true}
	config.GlobalConfig.Register("general_setting", settingState)

	err = UpdateOptionsBulk(map[string]string{
		"general_setting.first":  "new-first",
		"general_setting.second": "new-second",
	})
	require.ErrorIs(t, err, errOptionRollbackTest)

	first, second := settingState.snapshotValues()
	assert.Equal(t, "old-first", first)
	assert.Equal(t, "old-second", second)
	common.OptionMapRWMutex.RLock()
	_, firstPublished := common.OptionMap["general_setting.first"]
	_, secondPublished := common.OptionMap["general_setting.second"]
	common.OptionMapRWMutex.RUnlock()
	assert.False(t, firstPublished)
	assert.False(t, secondPublished)

	var persisted []Option
	require.NoError(t, db.Order("key asc").Find(&persisted).Error)
	require.Len(t, persisted, 2)
	assert.Equal(t, "old-first", persisted[0].Value)
	assert.Equal(t, "old-second", persisted[1].Value)
}

func TestUpdateOptionMapDisplayInCurrencyPropagatesConfigError(t *testing.T) {
	previousMap := common.OptionMap
	previousLegacy := common.DisplayInCurrencyEnabled
	previousConfig := config.GlobalConfig.Get("general_setting")
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
		common.DisplayInCurrencyEnabled = previousLegacy
		config.GlobalConfig.Register("general_setting", previousConfig)
	})

	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{"DisplayInCurrencyEnabled": "true"}
	common.OptionMapRWMutex.Unlock()
	common.DisplayInCurrencyEnabled = true
	config.GlobalConfig.Register("general_setting", &optionFailingGeneralSetting{QuotaDisplayType: operation_setting.QuotaDisplayTypeUSD})

	err := updateOptionMap("DisplayInCurrencyEnabled", "false")
	require.ErrorIs(t, err, errOptionRollbackTest)
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "true", common.OptionMap["DisplayInCurrencyEnabled"])
	common.OptionMapRWMutex.RUnlock()
	assert.True(t, common.DisplayInCurrencyEnabled)
}

func TestUpdateOptionMapDisplayInCurrencySynchronizesLegacyFlag(t *testing.T) {
	previousMap := common.OptionMap
	previousLegacy := common.DisplayInCurrencyEnabled
	previousGeneral := operation_setting.GetGeneralSettingSnapshot()
	t.Cleanup(func() {
		operation_setting.UpdateGeneralSetting(func(current *operation_setting.GeneralSetting) {
			*current = previousGeneral
		})
		common.DisplayInCurrencyEnabled = previousLegacy
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})

	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{"DisplayInCurrencyEnabled": "true"}
	common.OptionMapRWMutex.Unlock()
	common.DisplayInCurrencyEnabled = true

	require.NoError(t, updateOptionMap("DisplayInCurrencyEnabled", "false"))
	assert.False(t, common.DisplayInCurrencyEnabled)
	assert.Equal(t, operation_setting.QuotaDisplayTypeTokens, operation_setting.GetQuotaDisplayType())
	common.OptionMapRWMutex.RLock()
	assert.Equal(t, "false", common.OptionMap["DisplayInCurrencyEnabled"])
	common.OptionMapRWMutex.RUnlock()
}
