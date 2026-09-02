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
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type rawCredentialRow struct {
	Key           sql.NullString `gorm:"column:key"`
	KeyCiphertext string         `gorm:"column:key_ciphertext"`
	KeyHash       sql.NullString `gorm:"column:key_hash"`
}

type credentialColumnUpdate struct {
	Key string `gorm:"column:key"`
}

func setupCredentialStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	previousRedis := common.RedisEnabled
	t.Cleanup(func() {
		DB = previousDB
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
		common.RedisEnabled = previousRedis
	})

	common.CryptoSecret = "credential-storage-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	common.RedisEnabled = false
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Token{}, &Channel{}))
	DB = db
	return db
}

func readRawCredential(t *testing.T, db *gorm.DB, table string, id int) rawCredentialRow {
	t.Helper()
	var row rawCredentialRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table(table).
		Select("key, key_ciphertext, key_hash").Where("id = ?", id).Scan(&row).Error)
	return row
}

func TestCredentialStorageNewWritesNeverPersistPlaintext(t *testing.T) {
	db := setupCredentialStorageTest(t)
	tokenSecret := "sk-user-super-secret"
	token := Token{UserId: 17, Key: tokenSecret, Name: "encrypted-token"}
	require.NoError(t, db.Create(&token).Error)

	rawToken := readRawCredential(t, db, "tokens", token.Id)
	require.True(t, rawToken.Key.Valid)
	assert.True(t, isCredentialFingerprintMarker(rawToken.Key.String))
	assert.NotContains(t, rawToken.Key.String, tokenSecret)
	assert.NotContains(t, rawToken.KeyCiphertext, tokenSecret)
	assert.Len(t, rawToken.KeyHash.String, 64)
	loadedToken, err := GetTokenByKey(tokenSecret, true)
	require.NoError(t, err)
	assert.Equal(t, tokenSecret, loadedToken.Key)

	channelSecret := `{"access_token":"upstream-secret","account_id":"acct-1"}`
	channel := Channel{Name: "encrypted-channel", Key: channelSecret}
	require.NoError(t, db.Create(&channel).Error)
	rawChannel := readRawCredential(t, db, "channels", channel.Id)
	assert.True(t, isCredentialFingerprintMarker(rawChannel.Key.String))
	assert.NotContains(t, rawChannel.Key.String, "upstream-secret")
	assert.NotContains(t, rawChannel.KeyCiphertext, "upstream-secret")
	loadedChannel, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, channelSecret, loadedChannel.Key)
}

func TestCredentialStorageMigratesLegacyPlaintextRows(t *testing.T) {
	db := setupCredentialStorageTest(t)
	legacyToken := "legacy-token-secret"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tokens").Create(map[string]interface{}{
		"user_id": 22,
		"name":    "legacy",
		"key":     legacyToken,
	}).Error)
	legacyChannel := "legacy-channel-secret"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("channels").Create(map[string]interface{}{
		"name": "legacy-channel",
		"key":  legacyChannel,
	}).Error)

	loaded, err := GetTokenByKey(legacyToken, true)
	require.NoError(t, err)
	assert.Equal(t, legacyToken, loaded.Key)
	require.NoError(t, MigrateLegacyCredentials(db))

	rawToken := readRawCredential(t, db, "tokens", loaded.Id)
	assert.True(t, isCredentialFingerprintMarker(rawToken.Key.String))
	assert.NotContains(t, rawToken.KeyCiphertext, legacyToken)
	var channelID int
	require.NoError(t, db.Table("channels").Where("name = ?", "legacy-channel").Pluck("id", &channelID).Error)
	rawChannel := readRawCredential(t, db, "channels", channelID)
	assert.True(t, isCredentialFingerprintMarker(rawChannel.Key.String))
	assert.NotContains(t, rawChannel.KeyCiphertext, legacyChannel)

	reloaded, err := GetTokenByKey(legacyToken, true)
	require.NoError(t, err)
	assert.Equal(t, legacyToken, reloaded.Key)
}

func TestCredentialStorageWrongSecretFailsClosed(t *testing.T) {
	db := setupCredentialStorageTest(t)
	token := Token{UserId: 31, Key: "restart-secret", Name: "restart"}
	require.NoError(t, db.Create(&token).Error)
	common.CryptoSecret = "different-deployment-secret"

	_, err := GetTokenById(token.Id)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialStorageCorrupt))
}

func TestCredentialStorageRejectsNewSecretsWithoutStableRuntimeKey(t *testing.T) {
	db := setupCredentialStorageTest(t)
	common.CredentialSecretConfigured = false
	common.CredentialSecretRuntimeReady = true
	token := Token{UserId: 41, Key: "must-not-persist", Name: "rejected"}
	err := db.Create(&token).Error
	assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)
	var count int64
	require.NoError(t, db.Model(&Token{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestCredentialStorageEncryptsDirectKeyUpdateMap(t *testing.T) {
	db := setupCredentialStorageTest(t)
	channel := Channel{Name: "rotating", Key: "old-secret"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).
		Update("key", "new-secret").Error)
	raw := readRawCredential(t, db, "channels", channel.Id)
	assert.True(t, isCredentialFingerprintMarker(raw.Key.String))
	assert.NotContains(t, raw.Key.String, "new-secret")
	assert.NotContains(t, raw.KeyCiphertext, "new-secret")
	loaded, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "new-secret", loaded.Key)
}

func TestCredentialStorageStructUpdatesNeverPersistPlaintext(t *testing.T) {
	db := setupCredentialStorageTest(t)
	token := Token{UserId: 51, Key: "old-token-secret", Name: "struct-token"}
	require.NoError(t, db.Create(&token).Error)
	rotatedToken := "struct-token-secret"
	tokenPatch := &Token{LegacyKey: &rotatedToken, RemainQuota: 123}
	require.NoError(t, db.Model(&Token{}).Where("id = ?", token.Id).Updates(tokenPatch).Error)
	assert.Equal(t, rotatedToken, *tokenPatch.LegacyKey, "the caller's runtime patch must remain plaintext")
	assert.Empty(t, tokenPatch.KeyCiphertext)
	assert.Nil(t, tokenPatch.KeyHash)
	tokenRaw := readRawCredential(t, db, "tokens", token.Id)
	assert.True(t, isCredentialFingerprintMarker(tokenRaw.Key.String))
	assert.NotContains(t, tokenRaw.KeyCiphertext, rotatedToken)
	loadedToken, err := GetTokenByKey(rotatedToken, true)
	require.NoError(t, err)
	assert.Equal(t, 123, loadedToken.RemainQuota)

	channel := Channel{Name: "struct-channel", Key: "old-channel-secret"}
	require.NoError(t, db.Create(&channel).Error)
	channelPatch := &Channel{LegacyKey: "struct-channel-secret", UsedQuota: 7}
	require.NoError(t, db.Model(&Channel{}).Where("id = ?", channel.Id).Updates(channelPatch).Error)
	assert.Equal(t, "struct-channel-secret", channelPatch.LegacyKey)
	assert.Empty(t, channelPatch.KeyCiphertext)
	assert.Empty(t, channelPatch.KeyHash)
	channelRaw := readRawCredential(t, db, "channels", channel.Id)
	assert.True(t, isCredentialFingerprintMarker(channelRaw.Key.String))
	assert.NotContains(t, channelRaw.KeyCiphertext, "struct-channel-secret")
	loadedChannel, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "struct-channel-secret", loadedChannel.Key)
	assert.EqualValues(t, 7, loadedChannel.UsedQuota)
}

func TestCredentialStorageRejectsDirectCompanionAndUnsupportedStructUpdates(t *testing.T) {
	db := setupCredentialStorageTest(t)
	token := Token{UserId: 52, Key: "valid-token-secret", Name: "guarded-token"}
	require.NoError(t, db.Create(&token).Error)
	original := readRawCredential(t, db, "tokens", token.Id)

	err := db.Model(&Token{}).Where("id = ?", token.Id).
		Update("key_ciphertext", "plaintext-in-ciphertext-column").Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
	assert.Equal(t, original, readRawCredential(t, db, "tokens", token.Id))

	err = db.Model(&Token{}).Where("id = ?", token.Id).Updates(Token{
		KeyCiphertext: "plaintext-in-struct-ciphertext-column",
	}).Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
	assert.Equal(t, original, readRawCredential(t, db, "tokens", token.Id))

	err = db.Model(&Token{}).Where("id = ?", token.Id).Updates(credentialColumnUpdate{
		Key: "custom-struct-plaintext",
	}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected column key")
	assert.Equal(t, original, readRawCredential(t, db, "tokens", token.Id))
}

func TestCredentialStorageRejectsAmbiguousAndSilentStructClear(t *testing.T) {
	db := setupCredentialStorageTest(t)
	token := Token{UserId: 53, Key: "valid-token-secret", Name: "ambiguous-token"}
	require.NoError(t, db.Create(&token).Error)
	original := readRawCredential(t, db, "tokens", token.Id)

	err := db.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]interface{}{
		"key": "first-secret",
		"Key": "second-secret",
	}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Equal(t, original, readRawCredential(t, db, "tokens", token.Id))

	empty := ""
	err = db.Model(&Token{}).Where("id = ?", token.Id).Updates(Token{
		LegacyKey:   &empty,
		RemainQuota: 999,
	}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update map")
	assert.Equal(t, original, readRawCredential(t, db, "tokens", token.Id))
	loaded, getErr := GetTokenByKey("valid-token-secret", true)
	require.NoError(t, getErr)
	assert.Zero(t, loaded.RemainQuota, "the rejected update must not partially apply")
}

func TestCredentialStorageChannelKeyRotationKeepsEnvelope(t *testing.T) {
	db := setupCredentialStorageTest(t)
	channel := Channel{Name: "rotation-primitive", Key: "old-secret"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, UpdateChannelKey(channel.Id, "rotated-secret"))
	raw := readRawCredential(t, db, "channels", channel.Id)
	assert.True(t, isCredentialFingerprintMarker(raw.Key.String))
	assert.NotContains(t, raw.Key.String, "rotated-secret")
	assert.NotContains(t, raw.KeyCiphertext, "rotated-secret")
	loaded, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "rotated-secret", loaded.Key)
}

func TestCredentialStorageRepairsPartialExpansion(t *testing.T) {
	db := setupCredentialStorageTest(t)
	plaintext := "partial-expansion-secret"
	ciphertext, err := common.EncryptCredential(plaintext)
	require.NoError(t, err)
	// Simulate a crash after writing ciphertext but before marker/hash.
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tokens").Create(map[string]interface{}{
		"user_id":        71,
		"name":           "partial",
		"key":            plaintext,
		"key_ciphertext": ciphertext,
		"key_hash":       nil,
	}).Error)
	require.NoError(t, MigrateLegacyCredentials(db))
	var row rawCredentialRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tokens").
		Select("key, key_ciphertext, key_hash").Where("name = ?", "partial").Scan(&row).Error)
	assert.True(t, isCredentialFingerprintMarker(row.Key.String))
	assert.Equal(t, ciphertext, row.KeyCiphertext)
	assert.Equal(t, common.CredentialFingerprint(plaintext), row.KeyHash.String)
	loaded, err := GetTokenByKey(plaintext, true)
	require.NoError(t, err)
	assert.Equal(t, plaintext, loaded.Key)
}

func TestCredentialStorageRejectsMarkerWithoutCiphertext(t *testing.T) {
	db := setupCredentialStorageTest(t)
	marker := credentialFingerprintMarker(common.CredentialFingerprint("lost-secret"))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tokens").Create(map[string]interface{}{
		"user_id": 72,
		"name":    "corrupt-marker",
		"key":     marker,
	}).Error)
	err := MigrateLegacyCredentials(db)
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
}

func TestCredentialStructUpdateIsSealedAcrossSupportedDialects(t *testing.T) {
	setupCredentialStorageTest(t)
	dialects := []struct {
		name      string
		dialector gorm.Dialector
	}{
		{name: "sqlite", dialector: sqlite.Open(":memory:")},
		{name: "mysql", dialector: mysql.New(mysql.Config{
			DSN:                       "newapi:newapi@tcp(127.0.0.1:3306)/newapi?parseTime=true",
			SkipInitializeWithVersion: true,
		})},
		{name: "postgres", dialector: postgres.New(postgres.Config{
			DSN:                  "host=127.0.0.1 user=newapi password=newapi dbname=newapi sslmode=disable",
			PreferSimpleProtocol: true,
		})},
	}

	for _, dialect := range dialects {
		t.Run(dialect.name, func(t *testing.T) {
			db, err := gorm.Open(dialect.dialector, &gorm.Config{
				DryRun:                 true,
				DisableAutomaticPing:   true,
				SkipDefaultTransaction: true,
			})
			require.NoError(t, err)
			plaintext := "dialect-struct-secret-" + dialect.name
			result := db.Model(&Token{}).Where("id = ?", 17).Updates(Token{
				LegacyKey: &plaintext,
			})
			require.NoError(t, result.Error)
			explained := result.Dialector.Explain(result.Statement.SQL.String(), result.Statement.Vars...)
			assert.NotContains(t, explained, plaintext)
			assert.Contains(t, explained, "key_ciphertext")
			assert.Contains(t, explained, "key_hash")
		})
	}
}
