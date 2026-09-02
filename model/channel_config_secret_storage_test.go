package model

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type rawChannelConfigSecretRow struct {
	ID             int            `gorm:"column:id"`
	Setting        sql.NullString `gorm:"column:setting"`
	ParamOverride  sql.NullString `gorm:"column:param_override"`
	HeaderOverride sql.NullString `gorm:"column:header_override"`
	OtherSettings  sql.NullString `gorm:"column:settings"`
}

func setupChannelConfigSecretStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.CryptoSecret = "channel-config-secret-storage-test-key"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	return db
}

func rawChannelConfigSecrets(t *testing.T, db *gorm.DB, id int) rawChannelConfigSecretRow {
	t.Helper()
	var row rawChannelConfigSecretRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("channels").
		Select("id, setting, param_override, header_override, settings").
		Where("id = ?", id).Take(&row).Error)
	return row
}

func channelConfigSecretFixture(name string) Channel {
	setting := `{"proxy":"http://proxy-user:proxy-password@proxy.example:8080"}`
	paramOverride := `{"api_key":"body-secret"}`
	headerOverride := `{"Authorization":"Bearer header-secret"}`
	return Channel{
		Type: 1, Name: name, Key: "upstream-provider-key", Models: "test-model", Group: "default",
		Setting: &setting, ParamOverride: &paramOverride, HeaderOverride: &headerOverride,
		OtherSettings: `{"advanced_custom":{"advanced_routes":[{"incoming_path":"/v1/chat/completions","upstream_path":"/v1/chat/completions","auth":{"type":"header","name":"x-api-key","value":"route-secret"}}]}}`,
	}
}

func assertRawChannelConfigIsSealed(t *testing.T, row rawChannelConfigSecretRow) {
	t.Helper()
	for _, value := range []sql.NullString{row.Setting, row.ParamOverride, row.HeaderOverride, row.OtherSettings} {
		require.True(t, value.Valid)
		assert.True(t, common.IsCredentialCiphertext(value.String))
	}
	for _, plaintext := range []string{"proxy-password", "body-secret", "header-secret", "route-secret"} {
		assert.NotContains(t, row.Setting.String+row.ParamOverride.String+row.HeaderOverride.String+row.OtherSettings.String, plaintext)
	}
}

func TestChannelConfigSecretStructAndMapWritesAreEncrypted(t *testing.T) {
	db := setupChannelConfigSecretStorageTest(t)
	channel := channelConfigSecretFixture("encrypted-config")
	originalSetting := *channel.Setting
	originalOtherSettings := channel.OtherSettings
	require.NoError(t, db.Create(&channel).Error)
	assert.Equal(t, originalSetting, *channel.Setting)
	assert.Equal(t, originalOtherSettings, channel.OtherSettings)
	assertRawChannelConfigIsSealed(t, rawChannelConfigSecrets(t, db, channel.Id))

	var loaded Channel
	require.NoError(t, db.First(&loaded, channel.Id).Error)
	assert.Equal(t, originalSetting, *loaded.Setting)
	assert.Equal(t, originalOtherSettings, loaded.OtherSettings)

	rotatedHeader := `{"Authorization":"Bearer rotated-header-secret"}`
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]interface{}{
		"header_override": rotatedHeader,
		"used_quota":      gorm.Expr("used_quota + ?", 7),
	}).Error)
	assertRawChannelConfigIsSealed(t, rawChannelConfigSecrets(t, db, channel.Id))
	require.NoError(t, db.First(&loaded, channel.Id).Error)
	require.NotNil(t, loaded.HeaderOverride)
	assert.Equal(t, rotatedHeader, *loaded.HeaderOverride)
	assert.EqualValues(t, 7, loaded.UsedQuota)

	rotatedSetting := `{"proxy":"http://next-user:next-password@proxy.example:8080"}`
	structPatch := &Channel{Setting: &rotatedSetting, UsedQuota: 11}
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Updates(structPatch).Error)
	require.NotNil(t, structPatch.Setting)
	assert.Equal(t, rotatedSetting, *structPatch.Setting, "the caller's runtime patch must remain plaintext")
	assertRawChannelConfigIsSealed(t, rawChannelConfigSecrets(t, db, channel.Id))
	require.NoError(t, db.First(&loaded, channel.Id).Error)
	require.NotNil(t, loaded.Setting)
	assert.Equal(t, rotatedSetting, *loaded.Setting)
	assert.EqualValues(t, 11, loaded.UsedQuota)
}

func TestMigrateLegacyChannelConfigSecretsIsIdempotent(t *testing.T) {
	db := setupChannelConfigSecretStorageTest(t)
	legacy := channelConfigSecretFixture("legacy-config")
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("channels").Create(map[string]interface{}{
		"type": legacy.Type, "name": legacy.Name, "key": "legacy-key", "models": legacy.Models, "group": legacy.Group,
		"setting": *legacy.Setting, "param_override": *legacy.ParamOverride,
		"header_override": *legacy.HeaderOverride, "settings": legacy.OtherSettings,
	}).Error)
	var id int
	require.NoError(t, db.Table("channels").Where("name = ?", legacy.Name).Pluck("id", &id).Error)

	require.NoError(t, MigrateLegacyChannelConfigSecrets(db))
	first := rawChannelConfigSecrets(t, db, id)
	assertRawChannelConfigIsSealed(t, first)
	require.NoError(t, MigrateLegacyChannelConfigSecrets(db))
	second := rawChannelConfigSecrets(t, db, id)
	assert.Equal(t, first, second)

	var loaded Channel
	require.NoError(t, db.First(&loaded, id).Error)
	assert.Equal(t, *legacy.Setting, *loaded.Setting)
	assert.Equal(t, legacy.OtherSettings, loaded.OtherSettings)
}

func TestMigrateLegacyChannelConfigSecretsCASPreservesConcurrentEdit(t *testing.T) {
	db := setupChannelConfigSecretStorageTest(t)
	legacy := channelConfigSecretFixture("legacy-config-cas")
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("channels").Create(map[string]interface{}{
		"type": legacy.Type, "name": legacy.Name, "key": "legacy-key", "models": legacy.Models, "group": legacy.Group,
		"setting": *legacy.Setting, "param_override": *legacy.ParamOverride,
		"header_override": *legacy.HeaderOverride, "settings": legacy.OtherSettings,
	}).Error)
	var observed channelConfigSecretMigrationRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("channels").
		Select("id, setting, param_override, header_override, settings").
		Where("name = ?", legacy.Name).Take(&observed).Error)

	newerHeader := `{"Authorization":"Bearer concurrent-admin-secret"}`
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("channels").
		Where("id = ?", observed.ID).Update("header_override", newerHeader).Error)
	require.NoError(t, migrateChannelConfigSecretRow(db, observed))

	var loaded Channel
	require.NoError(t, db.First(&loaded, observed.ID).Error)
	require.NotNil(t, loaded.HeaderOverride)
	assert.Equal(t, newerHeader, *loaded.HeaderOverride)
	assertRawChannelConfigIsSealed(t, rawChannelConfigSecrets(t, db, observed.ID))
}

func TestChannelConfigSecretsFailClosedForWrongCorruptOrMissingKey(t *testing.T) {
	db := setupChannelConfigSecretStorageTest(t)
	channel := channelConfigSecretFixture("config-fail-closed")
	channel.Key = ""
	require.NoError(t, db.Create(&channel).Error)

	common.CryptoSecret = "wrong-channel-config-key"
	var loaded Channel
	err := db.First(&loaded, channel.Id).Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)

	common.CryptoSecret = "channel-config-secret-storage-test-key"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("channels").Where("id = ?", channel.Id).
		Update("header_override", "enc:v1:not-valid").Error)
	err = db.First(&loaded, channel.Id).Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)

	legacy := channelConfigSecretFixture("config-missing-key")
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("channels").Create(map[string]interface{}{
		"type": legacy.Type, "name": legacy.Name, "key": "", "models": legacy.Models, "group": legacy.Group,
		"setting": *legacy.Setting,
	}).Error)
	common.CredentialSecretConfigured = false
	err = MigrateLegacyChannelConfigSecrets(db)
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)
}
