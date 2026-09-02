package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTwoFASecretStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.CryptoSecret = "twofa-secret-storage-test-key"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TwoFA{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	return db
}

func rawTwoFASecret(t *testing.T, db *gorm.DB, id int) string {
	t.Helper()
	var row twoFASecretMigrationRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("two_fas").
		Select("id, secret").Where("id = ?", id).Take(&row).Error)
	return row.Secret
}

func TestTwoFASecretNewWritesAreEncryptedAndReadable(t *testing.T) {
	db := setupTwoFASecretStorageTest(t)
	secret := "JBSWY3DPEHPK3PXP"
	factor := TwoFA{UserId: 101, Secret: secret, IsEnabled: true}
	require.NoError(t, db.Create(&factor).Error)
	assert.Equal(t, secret, factor.Secret)

	raw := rawTwoFASecret(t, db, factor.Id)
	assert.True(t, common.IsCredentialCiphertext(raw))
	assert.NotContains(t, raw, secret)

	var loaded TwoFA
	require.NoError(t, db.First(&loaded, factor.Id).Error)
	assert.Equal(t, secret, loaded.Secret)
}

func TestTwoFASecretStructAndMapUpdatesAreEncrypted(t *testing.T) {
	db := setupTwoFASecretStorageTest(t)
	factor := TwoFA{UserId: 106, Secret: "ORIGINALTOTPSECRET", IsEnabled: true}
	require.NoError(t, db.Create(&factor).Error)

	structSecret := "STRUCTPATCHSECRET"
	patch := &TwoFA{Secret: structSecret, FailedAttempts: 2}
	require.NoError(t, db.Model(&TwoFA{}).Where("id = ?", factor.Id).Updates(patch).Error)
	assert.Equal(t, structSecret, patch.Secret)
	assert.True(t, common.IsCredentialCiphertext(rawTwoFASecret(t, db, factor.Id)))
	var loaded TwoFA
	require.NoError(t, db.First(&loaded, factor.Id).Error)
	assert.Equal(t, structSecret, loaded.Secret)
	assert.Equal(t, 2, loaded.FailedAttempts)

	mapSecret := "MAPPATCHSECRET"
	require.NoError(t, db.Model(&TwoFA{}).Where("id = ?", factor.Id).Updates(map[string]interface{}{
		"secret":          mapSecret,
		"failed_attempts": 3,
	}).Error)
	assert.NotContains(t, rawTwoFASecret(t, db, factor.Id), mapSecret)
	require.NoError(t, db.First(&loaded, factor.Id).Error)
	assert.Equal(t, mapSecret, loaded.Secret)
	assert.Equal(t, 3, loaded.FailedAttempts)
}

func TestMigrateLegacyTwoFASecretsIsIdempotent(t *testing.T) {
	db := setupTwoFASecretStorageTest(t)
	legacy := "KRSXG5DSNFXGOIDB"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("two_fas").Create(map[string]interface{}{
		"user_id": 102, "secret": legacy, "is_enabled": true,
	}).Error)
	var row twoFASecretMigrationRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("two_fas").
		Select("id, secret").Where("user_id = ?", 102).Take(&row).Error)

	require.NoError(t, MigrateLegacyTwoFASecrets(db))
	first := rawTwoFASecret(t, db, row.ID)
	assert.True(t, common.IsCredentialCiphertext(first))
	require.NoError(t, MigrateLegacyTwoFASecrets(db))
	assert.Equal(t, first, rawTwoFASecret(t, db, row.ID))

	var loaded TwoFA
	require.NoError(t, db.First(&loaded, row.ID).Error)
	assert.Equal(t, legacy, loaded.Secret)
}

func TestTwoFASecretWrongKeyAndUnstableKeyFailClosed(t *testing.T) {
	db := setupTwoFASecretStorageTest(t)
	factor := TwoFA{UserId: 103, Secret: "ONSWG4TFOQ======", IsEnabled: true}
	require.NoError(t, db.Create(&factor).Error)
	common.CryptoSecret = "wrong-twofa-storage-key"
	var loaded TwoFA
	err := db.First(&loaded, factor.Id).Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)

	common.CryptoSecret = "twofa-secret-storage-test-key"
	common.CredentialSecretConfigured = false
	err = db.Create(&TwoFA{UserId: 104, Secret: "MZXW6YTBOI======"}).Error
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)
}

func TestMigrateLegacyTwoFASecretRejectsUnstableKeyWithoutChangingRow(t *testing.T) {
	db := setupTwoFASecretStorageTest(t)
	legacy := "MFRGGZDFMZTWQ2LK"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("two_fas").Create(map[string]interface{}{
		"user_id": 105, "secret": legacy,
	}).Error)
	var row twoFASecretMigrationRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("two_fas").
		Select("id, secret").Where("user_id = ?", 105).Take(&row).Error)
	common.CredentialSecretConfigured = false

	err := MigrateLegacyTwoFASecrets(db)
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)
	assert.Equal(t, legacy, rawTwoFASecret(t, db, row.ID))
}
