package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupOptionSecretStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.CryptoSecret = "option-secret-storage-test-key"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	return db
}

func rawOptionValue(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	var row optionSecretMigrationRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("options").
		Select([]string{"key", "value"}).Where("key = ?", key).Take(&row).Error)
	return row.Value
}

func TestSensitiveOptionKeyPolicy(t *testing.T) {
	for _, key := range []string{
		"StripeApiSecret", "WaffoSandboxPrivateKey", "LinuxDOClientSecret",
		"oidc.client_secret", "provider.api_key", "provider.private-key",
	} {
		assert.True(t, IsSensitiveOptionKey(key), key)
	}
	for _, key := range []string{
		"TurnstileSiteKey", "WaffoPublicCert", "MaxTokenAutoGroups",
		"oidc.token_endpoint", "provider.public_key",
	} {
		assert.False(t, IsSensitiveOptionKey(key), key)
	}
}

func TestOptionSecretNewWritesAreEncryptedAndLoadAsPlaintext(t *testing.T) {
	db := setupOptionSecretStorageTest(t)
	secret := "whsec_option_super_secret"
	require.NoError(t, upsertOption(db, "StripeWebhookSecret", secret))

	raw := rawOptionValue(t, db, "StripeWebhookSecret")
	assert.True(t, common.IsCredentialCiphertext(raw))
	assert.NotContains(t, raw, secret)

	options, err := AllOption()
	require.NoError(t, err)
	require.Len(t, options, 1)
	assert.Equal(t, secret, options[0].Value)
}

func TestOptionSecretDirectCreateAndLongPrivateKeyUseTextStorage(t *testing.T) {
	db := setupOptionSecretStorageTest(t)
	privateKey := "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("long-private-key-material\n", 2000) + "-----END PRIVATE KEY-----"
	require.NoError(t, db.Create(&Option{Key: "WaffoPrivateKey", Value: privateKey}).Error)

	raw := rawOptionValue(t, db, "WaffoPrivateKey")
	assert.True(t, common.IsCredentialCiphertext(raw))
	var loaded Option
	require.NoError(t, db.First(&loaded, "key = ?", "WaffoPrivateKey").Error)
	assert.Equal(t, privateKey, loaded.Value)

	columnTypes, err := db.Migrator().ColumnTypes(&Option{})
	require.NoError(t, err)
	for _, columnType := range columnTypes {
		if columnType.Name() == "value" {
			assert.Equal(t, "TEXT", strings.ToUpper(columnType.DatabaseTypeName()))
			return
		}
	}
	t.Fatal("options.value column was not found")
}

func TestOptionSecretStructAndMapUpdatesAreEncrypted(t *testing.T) {
	db := setupOptionSecretStorageTest(t)
	const key = "StripeApiSecret"
	require.NoError(t, db.Create(&Option{Key: key, Value: "initial-secret"}).Error)

	structSecret := "struct-option-secret"
	patch := &Option{Value: structSecret}
	require.NoError(t, db.Model(&Option{Key: key}).Updates(patch).Error)
	assert.Equal(t, structSecret, patch.Value)
	assert.NotContains(t, rawOptionValue(t, db, key), structSecret)
	var loaded Option
	require.NoError(t, db.First(&loaded, "key = ?", key).Error)
	assert.Equal(t, structSecret, loaded.Value)

	mapSecret := "map-option-secret"
	require.NoError(t, db.Model(&Option{Key: key}).Update("value", mapSecret).Error)
	assert.NotContains(t, rawOptionValue(t, db, key), mapSecret)
	require.NoError(t, db.First(&loaded, "key = ?", key).Error)
	assert.Equal(t, mapSecret, loaded.Value)
}

func TestOptionSecretUpdateWithoutConcreteKeyFailsClosed(t *testing.T) {
	db := setupOptionSecretStorageTest(t)
	const key = "SMTPToken"
	require.NoError(t, db.Create(&Option{Key: key, Value: "initial-secret"}).Error)
	original := rawOptionValue(t, db, key)

	err := db.Model(&Option{}).Where("key = ?", key).Update("value", "must-not-persist").Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concrete option key")
	assert.Equal(t, original, rawOptionValue(t, db, key))

	err = db.Model(&Option{}).Where("key = ?", key).Updates(Option{Value: "also-must-not-persist"}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concrete option key")
	assert.Equal(t, original, rawOptionValue(t, db, key))
}

func TestMigrateLegacyOptionSecretsIsIdempotent(t *testing.T) {
	db := setupOptionSecretStorageTest(t)
	legacy := "legacy-oidc-client-secret"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("options").Create(map[string]interface{}{
		"key": "oidc.client_secret", "value": legacy,
	}).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("options").Create(map[string]interface{}{
		"key": "oidc.token_endpoint", "value": "https://issuer.example/token",
	}).Error)

	require.NoError(t, MigrateLegacyOptionSecrets(db))
	firstCiphertext := rawOptionValue(t, db, "oidc.client_secret")
	assert.True(t, common.IsCredentialCiphertext(firstCiphertext))
	assert.Equal(t, "https://issuer.example/token", rawOptionValue(t, db, "oidc.token_endpoint"))
	require.NoError(t, MigrateLegacyOptionSecrets(db))
	assert.Equal(t, firstCiphertext, rawOptionValue(t, db, "oidc.client_secret"))

	var loaded Option
	require.NoError(t, db.First(&loaded, "key = ?", "oidc.client_secret").Error)
	assert.Equal(t, legacy, loaded.Value)
}

func TestOptionSecretWrongKeyAndCorruptEnvelopeFailClosed(t *testing.T) {
	db := setupOptionSecretStorageTest(t)
	require.NoError(t, upsertOption(db, "SMTPToken", "smtp-password"))
	common.CryptoSecret = "wrong-option-secret-storage-key"
	_, err := AllOption()
	assert.ErrorIs(t, err, ErrOptionSecretStorageCorrupt)

	common.CryptoSecret = "option-secret-storage-test-key"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("options").
		Where("key = ?", "SMTPToken").Update("value", "enc:v1:not-valid-base64!").Error)
	err = MigrateLegacyOptionSecrets(db)
	assert.ErrorIs(t, err, ErrOptionSecretStorageCorrupt)
}

func TestOptionSecretWriteRejectsUnstableRuntimeKey(t *testing.T) {
	db := setupOptionSecretStorageTest(t)
	common.CredentialSecretConfigured = false
	err := upsertOption(db, "EpayKey", "must-not-persist")
	assert.ErrorIs(t, err, ErrOptionSecretStorageUnavailable)
	var count int64
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)

	// Public/non-secret options remain writable without an encryption key.
	require.NoError(t, upsertOption(db, "TurnstileSiteKey", "public-site-key"))
	assert.Equal(t, "public-site-key", rawOptionValue(t, db, "TurnstileSiteKey"))

	_, err = openOptionSecretValue("EpayKey", "enc:v2:future-envelope")
	assert.True(t, errors.Is(err, ErrOptionSecretStorageCorrupt))
}

func TestOptionSecretMigrationRejectsUnstableRuntimeKeyWithoutChangingPlaintext(t *testing.T) {
	db := setupOptionSecretStorageTest(t)
	legacy := "legacy-worker-secret"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("options").Create(map[string]interface{}{
		"key": "WorkerValidKey", "value": legacy,
	}).Error)
	common.CredentialSecretConfigured = false

	err := MigrateLegacyOptionSecrets(db)
	assert.ErrorIs(t, err, ErrOptionSecretStorageUnavailable)
	assert.Equal(t, legacy, rawOptionValue(t, db, "WorkerValidKey"))
}
