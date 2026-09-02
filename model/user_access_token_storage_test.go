package model

import (
	"database/sql"
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

type rawUserAccessTokenRow struct {
	ID         int            `gorm:"column:id"`
	Legacy     sql.NullString `gorm:"column:access_token"`
	Ciphertext sql.NullString `gorm:"column:access_token_ciphertext"`
	Hash       sql.NullString `gorm:"column:access_token_hash"`
}

type userAccessTokenColumnUpdate struct {
	LegacyAccessToken *string `gorm:"column:access_token"`
}

func setupUserAccessTokenStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	previousRedis := common.RedisEnabled
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
		common.RedisEnabled = previousRedis
	})

	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.CryptoSecret = "user-access-token-storage-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	common.RedisEnabled = false
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	DB = db
	return db
}

func createAccessTokenStorageUser(t *testing.T, db *gorm.DB, suffix, token string) User {
	t.Helper()
	user := User{
		Username:    "pat-storage-" + suffix,
		Password:    "password-placeholder",
		Status:      common.UserStatusEnabled,
		Role:        common.RoleCommonUser,
		Group:       "default",
		AffCode:     "pat-aff-" + suffix,
		AuthVersion: 1,
	}
	if token != "" {
		user.SetAccessToken(token)
	}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func readRawUserAccessToken(t *testing.T, db *gorm.DB, id int) rawUserAccessTokenRow {
	t.Helper()
	var row rawUserAccessTokenRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").
		Select("id, access_token, access_token_ciphertext, access_token_hash").
		Where("id = ?", id).Take(&row).Error)
	return row
}

func assertSealedUserAccessToken(t *testing.T, row rawUserAccessTokenRow, plaintext string) {
	t.Helper()
	require.True(t, row.Legacy.Valid)
	assert.Equal(t, credentialFingerprintMarker(common.CredentialFingerprint(plaintext)), row.Legacy.String)
	require.True(t, row.Ciphertext.Valid)
	assert.True(t, common.IsCredentialCiphertext(row.Ciphertext.String))
	assert.NotContains(t, row.Ciphertext.String, plaintext)
	require.True(t, row.Hash.Valid)
	assert.Equal(t, common.CredentialFingerprint(plaintext), row.Hash.String)
}

func TestUserAccessTokenNewWritesNeverPersistPlaintext(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	const token = "dashboard-personal-access-token"
	user := createAccessTokenStorageUser(t, db, "new-write", token)

	assert.Equal(t, token, user.GetAccessToken())
	assertSealedUserAccessToken(t, readRawUserAccessToken(t, db, user.Id), token)

	var loaded User
	require.NoError(t, db.First(&loaded, user.Id).Error)
	assert.Equal(t, token, loaded.GetAccessToken())
	exists, err := AccessTokenExists(token)
	require.NoError(t, err)
	assert.True(t, exists)
	exists, err = AccessTokenExists("not-the-token")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestUserAccessTokenMapRotationIsAtomicAndPreservesAccounting(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	user := createAccessTokenStorageUser(t, db, "map-rotation", "old-token")
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]interface{}{
		"quota":         gorm.Expr("quota + ?", 300),
		"request_count": gorm.Expr("request_count + ?", 1),
		"access_token":  "rotated-token",
	}).Error)

	assertSealedUserAccessToken(t, readRawUserAccessToken(t, db, user.Id), "rotated-token")
	var loaded User
	require.NoError(t, db.First(&loaded, user.Id).Error)
	assert.Equal(t, "rotated-token", loaded.GetAccessToken())
	assert.Equal(t, 300, loaded.Quota)
	assert.Equal(t, 1, loaded.RequestCount)
}

func TestUserAccessTokenStructUpdateNeverPersistsPlaintext(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	user := createAccessTokenStorageUser(t, db, "struct-update", "old-token")
	plaintext := "struct-rotated-token"
	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Updates(User{
		LegacyAccessToken: &plaintext,
		Quota:             200,
	}).Error)

	assertSealedUserAccessToken(t, readRawUserAccessToken(t, db, user.Id), plaintext)
	var loaded User
	require.NoError(t, db.First(&loaded, user.Id).Error)
	assert.Equal(t, plaintext, loaded.GetAccessToken())
	assert.Equal(t, 200, loaded.Quota)
}

func TestUserAccessTokenStructClearFailsInsteadOfSilentlySkipping(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	user := createAccessTokenStorageUser(t, db, "struct-clear", "valid-token")
	empty := ""
	err := db.Model(&User{}).Where("id = ?", user.Id).Updates(User{
		LegacyAccessToken: &empty,
		Quota:             200,
	}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "use UpdateUserAccessToken")
	assertSealedUserAccessToken(t, readRawUserAccessToken(t, db, user.Id), "valid-token")

	var loaded User
	require.NoError(t, db.First(&loaded, user.Id).Error)
	assert.Zero(t, loaded.Quota, "the rejected struct update must not partially apply")
	require.NoError(t, UpdateUserAccessToken(user.Id, ""))
	assert.False(t, readRawUserAccessToken(t, db, user.Id).Legacy.Valid)
}

func TestUserAccessTokenClearUsesNullAndPartialProjectionDoesNotFail(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	first := createAccessTokenStorageUser(t, db, "clear-first", "first-token")
	second := createAccessTokenStorageUser(t, db, "clear-second", "second-token")
	require.NoError(t, UpdateUserAccessToken(first.Id, ""))
	require.NoError(t, UpdateUserAccessToken(second.Id, ""))
	for _, id := range []int{first.Id, second.Id} {
		raw := readRawUserAccessToken(t, db, id)
		assert.False(t, raw.Legacy.Valid, "empty credentials must use NULL under the unique index")
		assert.False(t, raw.Hash.Valid)
		assert.Empty(t, raw.Ciphertext.String)
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": "pat-storage-legacy-empty", "password": "password-placeholder",
		"status": common.UserStatusEnabled, "role": common.RoleCommonUser,
		"group": "default", "aff_code": "pat-aff-legacy-empty", "auth_version": 1,
		"access_token": "",
	}).Error)
	var legacyEmptyID int
	require.NoError(t, db.Table("users").Where("username = ?", "pat-storage-legacy-empty").Pluck("id", &legacyEmptyID).Error)
	require.NoError(t, MigrateLegacyUserAccessTokens(db))
	assert.False(t, readRawUserAccessToken(t, db, legacyEmptyID).Legacy.Valid)

	withToken := createAccessTokenStorageUser(t, db, "projection", "projection-token")
	var projected User
	require.NoError(t, db.Select("id", "role").First(&projected, withToken.Id).Error)
	assert.Equal(t, withToken.Id, projected.Id)
	assert.Empty(t, projected.GetAccessToken())
	assert.Empty(t, projected.AccessTokenCiphertext)
	assert.Nil(t, projected.AccessTokenHash)
}

func TestUserAccessTokenLookupVerifiesDecryptedCandidateAndFallsBackToLegacy(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	const requested = "requested-legacy-token"
	collision := createAccessTokenStorageUser(t, db, "collision", "different-token")
	targetHash := common.CredentialFingerprint(requested)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Where("id = ?", collision.Id).
		Updates(map[string]interface{}{
			"access_token":      credentialFingerprintMarker(targetHash),
			"access_token_hash": targetHash,
		}).Error)

	legacy := User{
		Username: "pat-storage-legacy", Password: "password-placeholder",
		Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
		Group: "default", AffCode: "pat-aff-legacy", AuthVersion: 1,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": legacy.Username, "password": legacy.Password, "status": legacy.Status,
		"role": legacy.Role, "group": legacy.Group, "aff_code": legacy.AffCode,
		"auth_version": legacy.AuthVersion, "access_token": requested,
	}).Error)

	matched, err := ValidateAccessToken(requested)
	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, legacy.Username, matched.Username)
	assert.NotEqual(t, collision.Id, matched.Id)
}

func TestUserAccessTokenMigrationIsIdempotentAndRepairsPartialRows(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	const legacyToken = "legacy-user-pat"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": "pat-storage-migrate", "password": "password-placeholder",
		"status": common.UserStatusEnabled, "role": common.RoleCommonUser,
		"group": "default", "aff_code": "pat-aff-migrate", "auth_version": 1,
		"access_token": legacyToken,
	}).Error)
	const partialToken = "partially-expanded-user-pat"
	partialCiphertext, err := common.EncryptCredential(partialToken)
	require.NoError(t, err)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": "pat-storage-partial", "password": "password-placeholder",
		"status": common.UserStatusEnabled, "role": common.RoleCommonUser,
		"group": "default", "aff_code": "pat-aff-partial", "auth_version": 1,
		"access_token": partialToken, "access_token_ciphertext": partialCiphertext,
	}).Error)

	require.NoError(t, MigrateLegacyUserAccessTokens(db))
	require.NoError(t, MigrateLegacyUserAccessTokens(db))

	var rows []rawUserAccessTokenRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").
		Select("id, access_token, access_token_ciphertext, access_token_hash").Order("id").Find(&rows).Error)
	require.Len(t, rows, 2)
	assertSealedUserAccessToken(t, rows[0], legacyToken)
	assertSealedUserAccessToken(t, rows[1], partialToken)
	assert.Equal(t, partialCiphertext, rows[1].Ciphertext.String, "repair must preserve an authenticated envelope")

	matched, err := ValidateAccessToken(legacyToken)
	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "pat-storage-migrate", matched.Username)
}

func TestUserAccessTokenMigrationCASDoesNotOverwriteConcurrentRotation(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	const oldToken = "observed-before-rotation"
	const rotatedToken = "concurrently-rotated-token"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": "pat-storage-cas", "password": "password-placeholder",
		"status": common.UserStatusEnabled, "role": common.RoleCommonUser,
		"group": "default", "aff_code": "pat-aff-cas", "auth_version": 1,
		"access_token": oldToken,
	}).Error)
	var observed userAccessTokenMigrationRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").
		Select("id, access_token, access_token_ciphertext, access_token_hash").Take(&observed).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Where("id = ?", observed.ID).
		Update("access_token", rotatedToken).Error)

	require.NoError(t, migrateUserAccessTokenRow(db, observed))
	assertSealedUserAccessToken(t, readRawUserAccessToken(t, db, observed.ID), rotatedToken)
}

func TestUserAccessTokenWrongOrCorruptKeyFailsClosed(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	user := createAccessTokenStorageUser(t, db, "wrong-key", "wrong-key-token")
	common.CryptoSecret = "different-deployment-key"

	var loaded User
	err := db.First(&loaded, user.Id).Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
	matched, err := ValidateAccessToken("wrong-key-token")
	require.NoError(t, err)
	assert.Nil(t, matched, "a fingerprint derived with the wrong key must never authenticate")

	common.CryptoSecret = "user-access-token-storage-test-secret"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Where("id = ?", user.Id).
		Update("access_token_ciphertext", "enc:v1:not-valid").Error)
	matched, err = ValidateAccessToken("wrong-key-token")
	assert.Nil(t, matched)
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
}

func TestUserAccessTokenMissingStableKeyRejectsWritesAndMigration(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	common.CredentialSecretConfigured = false
	common.CredentialSecretRuntimeReady = true

	withoutToken := createAccessTokenStorageUser(t, db, "no-token", "")
	assert.Greater(t, withoutToken.Id, 0)
	withToken := User{
		Username: "pat-storage-rejected", Password: "password-placeholder",
		Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
		Group: "default", AffCode: "pat-aff-rejected", AuthVersion: 1,
	}
	withToken.SetAccessToken("must-not-persist")
	err := db.Create(&withToken).Error
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)

	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": "pat-storage-legacy-no-key", "password": "password-placeholder",
		"status": common.UserStatusEnabled, "role": common.RoleCommonUser,
		"group": "default", "aff_code": "pat-aff-legacy-no-key", "auth_version": 1,
		"access_token": "legacy-needs-stable-key",
	}).Error)
	matched, validateErr := ValidateAccessToken("legacy-needs-stable-key")
	assert.Nil(t, matched)
	assert.ErrorIs(t, validateErr, ErrCredentialStorageUnavailable)
	err = MigrateLegacyUserAccessTokens(db)
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)

	var count int64
	require.NoError(t, db.Table("users").Where("access_token = ?", "must-not-persist").Count(&count).Error)
	assert.Zero(t, count)
}

func TestUserAccessTokenRejectsDirectCompanionColumnInjection(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	user := createAccessTokenStorageUser(t, db, "companion-injection", "valid-token")
	err := db.Model(&User{}).Where("id = ?", user.Id).
		Update("access_token_ciphertext", "plaintext-in-ciphertext-column").Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
	assertSealedUserAccessToken(t, readRawUserAccessToken(t, db, user.Id), "valid-token")
}

func TestUserAccessTokenRejectsStructCompanionColumnInjection(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	user := createAccessTokenStorageUser(t, db, "struct-companion-injection", "valid-token")
	err := db.Model(&User{}).Where("id = ?", user.Id).Updates(&User{
		AccessTokenCiphertext: "plaintext-in-ciphertext-column",
	}).Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
	assertSealedUserAccessToken(t, readRawUserAccessToken(t, db, user.Id), "valid-token")
}

func TestUserAccessTokenRejectsUnsupportedStructDestination(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	user := createAccessTokenStorageUser(t, db, "custom-struct-injection", "valid-token")
	plaintext := "custom-struct-plaintext"
	err := db.Model(&User{}).Where("id = ?", user.Id).Updates(userAccessTokenColumnUpdate{
		LegacyAccessToken: &plaintext,
	}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected column access_token")
	assertSealedUserAccessToken(t, readRawUserAccessToken(t, db, user.Id), "valid-token")
}

func TestUserAccessTokenMigrationRejectsUnrecoverableRows(t *testing.T) {
	db := setupUserAccessTokenStorageTest(t)
	marker := credentialFingerprintMarker(common.CredentialFingerprint("lost-token"))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("users").Create(map[string]interface{}{
		"username": "pat-storage-corrupt", "password": "password-placeholder",
		"status": common.UserStatusEnabled, "role": common.RoleCommonUser,
		"group": "default", "aff_code": "pat-aff-corrupt", "auth_version": 1,
		"access_token": marker,
	}).Error)
	err := MigrateLegacyUserAccessTokens(db)
	assert.True(t, errors.Is(err, ErrCredentialStorageCorrupt))
}
