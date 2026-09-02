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

type rawCustomOAuthSecretRow struct {
	ID           int    `gorm:"column:id"`
	ClientSecret string `gorm:"column:client_secret"`
}

func setupCustomOAuthSecretStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.CryptoSecret = "custom-oauth-secret-storage-test-key"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&CustomOAuthProvider{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	return db
}

func customOAuthProviderFixture(slug, secret string) CustomOAuthProvider {
	return CustomOAuthProvider{
		Name:                  "Custom OAuth " + slug,
		Slug:                  slug,
		ClientId:              "client-" + slug,
		ClientSecret:          secret,
		AuthorizationEndpoint: "https://issuer.example/authorize",
		TokenEndpoint:         "https://issuer.example/token",
		UserInfoEndpoint:      "https://issuer.example/userinfo",
	}
}

func rawCustomOAuthSecret(t *testing.T, db *gorm.DB, id int) string {
	t.Helper()
	var row rawCustomOAuthSecretRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Select("id, client_secret").Where("id = ?", id).Take(&row).Error)
	return row.ClientSecret
}

func TestCustomOAuthSecretNewWritesAreEncryptedAndReadable(t *testing.T) {
	db := setupCustomOAuthSecretStorageTest(t)
	secret := "oauth-client-secret-never-store-plaintext"
	provider := customOAuthProviderFixture("encrypted-write", secret)
	require.NoError(t, CreateCustomOAuthProvider(&provider))

	raw := rawCustomOAuthSecret(t, db, provider.Id)
	assert.True(t, common.IsCredentialCiphertext(raw))
	assert.NotContains(t, raw, secret)
	assert.Equal(t, secret, provider.ClientSecret)

	loaded, err := GetCustomOAuthProviderById(provider.Id)
	require.NoError(t, err)
	assert.Equal(t, secret, loaded.ClientSecret)

	rotated := "oauth-client-secret-after-rotation"
	loaded.ClientSecret = rotated
	require.NoError(t, UpdateCustomOAuthProvider(loaded))
	assert.Equal(t, rotated, loaded.ClientSecret)
	rotatedRaw := rawCustomOAuthSecret(t, db, provider.Id)
	assert.True(t, common.IsCredentialCiphertext(rotatedRaw))
	assert.NotContains(t, rotatedRaw, rotated)
	reloaded, err := GetCustomOAuthProviderById(provider.Id)
	require.NoError(t, err)
	assert.Equal(t, rotated, reloaded.ClientSecret)
}

func TestCustomOAuthSecretMapUpdatesAreEncrypted(t *testing.T) {
	db := setupCustomOAuthSecretStorageTest(t)
	provider := customOAuthProviderFixture("map-update", "initial-secret")
	require.NoError(t, CreateCustomOAuthProvider(&provider))

	rotated := "rotated-through-map"
	require.NoError(t, db.Model(&CustomOAuthProvider{}).Where("id = ?", provider.Id).
		Updates(map[string]interface{}{"client_secret": rotated}).Error)
	assert.NotContains(t, rawCustomOAuthSecret(t, db, provider.Id), rotated)

	loaded, err := GetCustomOAuthProviderById(provider.Id)
	require.NoError(t, err)
	assert.Equal(t, rotated, loaded.ClientSecret)
}

func TestCustomOAuthSecretStructUpdatesAreEncrypted(t *testing.T) {
	db := setupCustomOAuthSecretStorageTest(t)
	provider := customOAuthProviderFixture("struct-update", "initial-secret")
	require.NoError(t, CreateCustomOAuthProvider(&provider))

	rotated := "rotated-through-struct"
	patch := &CustomOAuthProvider{ClientSecret: rotated, AccessDeniedMessage: "denied"}
	require.NoError(t, db.Model(&CustomOAuthProvider{}).Where("id = ?", provider.Id).Updates(patch).Error)
	assert.Equal(t, rotated, patch.ClientSecret)
	assert.NotContains(t, rawCustomOAuthSecret(t, db, provider.Id), rotated)

	loaded, err := GetCustomOAuthProviderById(provider.Id)
	require.NoError(t, err)
	assert.Equal(t, rotated, loaded.ClientSecret)
	assert.Equal(t, "denied", loaded.AccessDeniedMessage)
}

func TestCustomOAuthSecretExistingEnvelopeIsNotDoubleEncrypted(t *testing.T) {
	db := setupCustomOAuthSecretStorageTest(t)
	secret := "already-sealed-custom-oauth-secret"
	envelope, err := common.EncryptCredential(secret)
	require.NoError(t, err)
	provider := customOAuthProviderFixture("existing-envelope", envelope)
	require.NoError(t, CreateCustomOAuthProvider(&provider))

	assert.Equal(t, envelope, rawCustomOAuthSecret(t, db, provider.Id))
	assert.Equal(t, secret, provider.ClientSecret)
}

func TestMigrateLegacyCustomOAuthSecretsIsIdempotent(t *testing.T) {
	db := setupCustomOAuthSecretStorageTest(t)
	legacy := "legacy-custom-oauth-secret"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Create(map[string]interface{}{
			"name": "Legacy OAuth", "slug": "legacy-oauth", "client_id": "legacy-client",
			"client_secret": legacy, "authorization_endpoint": "https://issuer.example/authorize",
			"token_endpoint": "https://issuer.example/token", "user_info_endpoint": "https://issuer.example/userinfo",
		}).Error)
	var row rawCustomOAuthSecretRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Select("id, client_secret").Where("slug = ?", "legacy-oauth").Take(&row).Error)

	// A rolling deployment can consume a legacy row before startup migration.
	loaded, err := GetCustomOAuthProviderById(row.ID)
	require.NoError(t, err)
	assert.Equal(t, legacy, loaded.ClientSecret)

	require.NoError(t, MigrateLegacyCustomOAuthSecrets(db))
	firstCiphertext := rawCustomOAuthSecret(t, db, row.ID)
	assert.True(t, common.IsCredentialCiphertext(firstCiphertext))
	assert.NotContains(t, firstCiphertext, legacy)
	require.NoError(t, MigrateLegacyCustomOAuthSecrets(db))
	assert.Equal(t, firstCiphertext, rawCustomOAuthSecret(t, db, row.ID))

	loaded, err = GetCustomOAuthProviderById(row.ID)
	require.NoError(t, err)
	assert.Equal(t, legacy, loaded.ClientSecret)
}

func TestMigrateLegacyCustomOAuthSecretCASPreservesNewerRotation(t *testing.T) {
	db := setupCustomOAuthSecretStorageTest(t)
	stale := "stale-secret-read-by-migrator"
	newer := "newer-secret-written-by-admin"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Create(map[string]interface{}{
			"name": "CAS OAuth", "slug": "cas-oauth", "client_id": "cas-client",
			"client_secret": stale, "authorization_endpoint": "https://issuer.example/authorize",
			"token_endpoint": "https://issuer.example/token", "user_info_endpoint": "https://issuer.example/userinfo",
		}).Error)
	var row rawCustomOAuthSecretRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Select("id, client_secret").Where("slug = ?", "cas-oauth").Take(&row).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Where("id = ?", row.ID).Update("client_secret", newer).Error)

	require.NoError(t, migrateCustomOAuthSecretRow(db, row.ID, stale))
	raw := rawCustomOAuthSecret(t, db, row.ID)
	assert.True(t, common.IsCredentialCiphertext(raw))
	assert.NotContains(t, raw, stale)
	assert.NotContains(t, raw, newer)
	loaded, err := GetCustomOAuthProviderById(row.ID)
	require.NoError(t, err)
	assert.Equal(t, newer, loaded.ClientSecret)
}

func TestCustomOAuthSecretWrongKeyAndCorruptEnvelopeFailClosed(t *testing.T) {
	db := setupCustomOAuthSecretStorageTest(t)
	provider := customOAuthProviderFixture("wrong-key", "correct-key-secret")
	require.NoError(t, CreateCustomOAuthProvider(&provider))

	common.CryptoSecret = "wrong-custom-oauth-storage-key"
	_, err := GetCustomOAuthProviderById(provider.Id)
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
	err = MigrateLegacyCustomOAuthSecrets(db)
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)

	common.CryptoSecret = "custom-oauth-secret-storage-test-key"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Where("id = ?", provider.Id).Update("client_secret", "enc:v1:not-valid-base64!").Error)
	_, err = GetCustomOAuthProviderById(provider.Id)
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
}

func TestCustomOAuthSecretRejectsUnstableKeyWithoutChangingLegacyRow(t *testing.T) {
	db := setupCustomOAuthSecretStorageTest(t)
	common.CredentialSecretConfigured = false
	provider := customOAuthProviderFixture("unstable-write", "must-not-persist")
	err := CreateCustomOAuthProvider(&provider)
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)
	var count int64
	require.NoError(t, db.Model(&CustomOAuthProvider{}).Count(&count).Error)
	assert.Zero(t, count)

	legacy := "legacy-secret-needs-stable-key"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Create(map[string]interface{}{
			"name": "Unstable Legacy", "slug": "unstable-legacy", "client_id": "legacy-client",
			"client_secret": legacy, "authorization_endpoint": "https://issuer.example/authorize",
			"token_endpoint": "https://issuer.example/token", "user_info_endpoint": "https://issuer.example/userinfo",
		}).Error)
	var row rawCustomOAuthSecretRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
		Select("id, client_secret").Where("slug = ?", "unstable-legacy").Take(&row).Error)
	err = MigrateLegacyCustomOAuthSecrets(db)
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)
	assert.Equal(t, legacy, rawCustomOAuthSecret(t, db, row.ID))
}
