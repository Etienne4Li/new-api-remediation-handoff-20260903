package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type userSettingColumnUpdate struct {
	Setting string `gorm:"column:setting"`
}

func setupUserSettingSecretStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	previousRedis := common.RedisEnabled
	common.CryptoSecret = "user-setting-secret-storage-test-key"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	common.RedisEnabled = false
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
		common.RedisEnabled = previousRedis
	})
	return db
}

func rawUserSetting(t *testing.T, db *gorm.DB, id int) string {
	t.Helper()
	var row userSettingSecretMigrationRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").
		Select("id, setting").Where("id = ?", id).Take(&row).Error)
	return row.Setting
}

func TestUpdateUserSettingEncryptsCredentialBearingFields(t *testing.T) {
	db := setupUserSettingSecretStorageTest(t)
	user := User{Username: "setting-secret-user", Password: "password", AffCode: "setting-secret-aff"}
	require.NoError(t, db.Create(&user).Error)
	want := dto.UserSetting{
		NotifyType:    dto.NotifyTypeWebhook,
		WebhookUrl:    "https://hooks.example/secret-path?signature=one",
		WebhookSecret: "webhook-signing-secret",
		BarkUrl:       "https://api.day.app/device-secret/{{title}}/{{content}}",
		GotifyUrl:     "https://gotify.example/private-path",
		GotifyToken:   "gotify-application-token",
		Language:      "zh",
	}
	require.NoError(t, UpdateUserSetting(user.Id, want))

	raw := rawUserSetting(t, db, user.Id)
	for _, plaintext := range []string{want.WebhookUrl, want.WebhookSecret, want.BarkUrl, want.GotifyUrl, want.GotifyToken} {
		assert.NotContains(t, raw, plaintext)
	}
	assert.Equal(t, 5, strings.Count(raw, common.CredentialCiphertextPrefix))
	assert.Contains(t, raw, `"language":"zh"`)

	got, err := GetUserSetting(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, want.WebhookUrl, got.WebhookUrl)
	assert.Equal(t, want.WebhookSecret, got.WebhookSecret)
	assert.Equal(t, want.BarkUrl, got.BarkUrl)
	assert.Equal(t, want.GotifyUrl, got.GotifyUrl)
	assert.Equal(t, want.GotifyToken, got.GotifyToken)
	assert.Equal(t, "zh", got.Language)
}

func TestUserBeforeSaveProtectsDirectStructAndMapSettingWrites(t *testing.T) {
	db := setupUserSettingSecretStorageTest(t)
	structValue := `{"webhook_secret":"direct-struct-secret","future":{"keep":true},"language":"en"}`
	user := User{
		Username: "direct-setting-struct", Password: "password",
		AffCode: "direct-setting-struct-aff", Setting: structValue,
	}
	require.NoError(t, db.Create(&user).Error)
	raw := rawUserSetting(t, db, user.Id)
	assert.NotContains(t, raw, "direct-struct-secret")
	assert.Contains(t, raw, common.CredentialCiphertextPrefix)
	assert.Contains(t, raw, `"future"`)

	mapValue := `{"gotify_token":"direct-map-token","future":{"version":2},"language":"zh"}`
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("setting", mapValue).Error)
	raw = rawUserSetting(t, db, user.Id)
	assert.NotContains(t, raw, "direct-map-token")
	assert.Equal(t, 1, strings.Count(raw, common.CredentialCiphertextPrefix))
	assert.Contains(t, raw, `"future"`)

	setting, err := GetUserSetting(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "direct-map-token", setting.GotifyToken)
	assert.Equal(t, "zh", setting.Language)

	updateValue := `{"webhook_secret":"struct-update-secret","future":{"version":3},"language":"fr"}`
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Updates(User{Setting: updateValue}).Error)
	raw = rawUserSetting(t, db, user.Id)
	assert.NotContains(t, raw, "struct-update-secret")
	assert.Equal(t, 1, strings.Count(raw, common.CredentialCiphertextPrefix))
	assert.Contains(t, raw, `"future"`)

	setting, err = GetUserSetting(user.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "struct-update-secret", setting.WebhookSecret)
	assert.Equal(t, "fr", setting.Language)
}

func TestUserBeforeSaveRejectsUnsupportedSettingStructDestination(t *testing.T) {
	db := setupUserSettingSecretStorageTest(t)
	user := User{
		Username: "unsupported-setting-struct", Password: "password",
		AffCode: "unsupported-setting-struct-aff",
	}
	require.NoError(t, db.Create(&user).Error)

	err := db.Model(&User{}).Where("id = ?", user.Id).Updates(userSettingColumnUpdate{
		Setting: `{"webhook_secret":"must-not-persist"}`,
	}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected column setting")
	assert.NotContains(t, rawUserSetting(t, db, user.Id), "must-not-persist")
}

func TestMigrateLegacyUserSettingSecretsPreservesUnknownFieldsAndIsIdempotent(t *testing.T) {
	db := setupUserSettingSecretStorageTest(t)
	legacy := `{"webhook_secret":"legacy-webhook","gotify_token":"legacy-gotify","future":{"keep":true},"language":"en"}`
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": "legacy-setting-user", "password": "password", "aff_code": "legacy-setting-aff", "setting": legacy,
	}).Error)
	var row userSettingSecretMigrationRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").
		Select("id, setting").Where("username = ?", "legacy-setting-user").Take(&row).Error)

	require.NoError(t, MigrateLegacyUserSettingSecrets(db))
	first := rawUserSetting(t, db, row.ID)
	assert.NotContains(t, first, "legacy-webhook")
	assert.NotContains(t, first, "legacy-gotify")
	assert.Contains(t, first, `"future"`)
	require.NoError(t, MigrateLegacyUserSettingSecrets(db))
	assert.Equal(t, first, rawUserSetting(t, db, row.ID))

	setting, err := unmarshalUserSettingFromStorage(first)
	require.NoError(t, err)
	assert.Equal(t, "legacy-webhook", setting.WebhookSecret)
	assert.Equal(t, "legacy-gotify", setting.GotifyToken)
	assert.Equal(t, "en", setting.Language)
}

func TestUserSettingSecretsFailClosedForWrongOrUnstableKey(t *testing.T) {
	db := setupUserSettingSecretStorageTest(t)
	stored, err := marshalUserSettingForStorage(dto.UserSetting{WebhookSecret: "secret"})
	require.NoError(t, err)
	common.CryptoSecret = "wrong-user-setting-key"
	_, err = unmarshalUserSettingFromStorage(stored)
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)

	common.CryptoSecret = "user-setting-secret-storage-test-key"
	common.CredentialSecretConfigured = false
	_, err = marshalUserSettingForStorage(dto.UserSetting{GotifyToken: "must-not-persist"})
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)

	legacy := `{"webhook_secret":"legacy-plaintext"}`
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": "unstable-setting-user", "password": "password", "aff_code": "unstable-setting-aff", "setting": legacy,
	}).Error)
	err = MigrateLegacyUserSettingSecrets(db)
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)
}
