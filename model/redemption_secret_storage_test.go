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

type rawRedemptionSecretRow struct {
	ID         int            `gorm:"column:id"`
	Legacy     sql.NullString `gorm:"column:key"`
	Ciphertext sql.NullString `gorm:"column:key_ciphertext"`
	Hash       sql.NullString `gorm:"column:key_hash"`
}

type redemptionKeyColumnUpdate struct {
	Key string `gorm:"column:key"`
}

type legacyRedemptionSecretSchema struct {
	ID          int    `gorm:"column:id;primaryKey"`
	Key         string `gorm:"column:key;type:char(32);uniqueIndex"`
	Name        string `gorm:"column:name"`
	Status      int    `gorm:"column:status"`
	Quota       int    `gorm:"column:quota"`
	CreatedTime int64  `gorm:"column:created_time"`
}

func (legacyRedemptionSecretSchema) TableName() string {
	return "redemptions"
}

func setupRedemptionSecretStorageTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousType := common.MainDatabaseType()
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.CryptoSecret = "redemption-secret-storage-test-key"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Redemption{}))
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

func redemptionSecretFixture(name, key string) Redemption {
	return Redemption{
		Name: name, Key: key, Status: common.RedemptionCodeStatusEnabled,
		Quota: 100, CreatedTime: common.GetTimestamp(),
	}
}

func readRawRedemptionSecret(t *testing.T, db *gorm.DB, id int) rawRedemptionSecretRow {
	t.Helper()
	var row rawRedemptionSecretRow
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("redemptions").
		Select("id, key, key_ciphertext, key_hash").Where("id = ?", id).Take(&row).Error)
	return row
}

func assertSealedRedemptionKey(t *testing.T, row rawRedemptionSecretRow, plaintext string) {
	t.Helper()
	hash := common.CredentialFingerprint(plaintext)
	require.True(t, row.Legacy.Valid)
	assert.Equal(t, credentialFingerprintMarker(hash), row.Legacy.String)
	require.True(t, row.Ciphertext.Valid)
	assert.True(t, common.IsCredentialCiphertext(row.Ciphertext.String))
	assert.NotContains(t, row.Ciphertext.String, plaintext)
	require.True(t, row.Hash.Valid)
	assert.Equal(t, hash, row.Hash.String)
}

func insertLegacyRedemptionSecret(t *testing.T, db *gorm.DB, name, key string) int {
	t.Helper()
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("redemptions").Create(map[string]interface{}{
		"name": name, "key": key, "status": common.RedemptionCodeStatusEnabled,
		"quota": 100, "created_time": common.GetTimestamp(),
	}).Error)
	var id int
	require.NoError(t, db.Table("redemptions").Where("name = ?", name).Pluck("id", &id).Error)
	return id
}

func TestRedemptionKeyStructAndMapWritesAreEncrypted(t *testing.T) {
	db := setupRedemptionSecretStorageTest(t)
	const original = "redemption-code-original-000001"
	redemption := redemptionSecretFixture("new-write", original)
	require.NoError(t, db.Create(&redemption).Error)
	assert.Equal(t, original, redemption.Key)
	assertSealedRedemptionKey(t, readRawRedemptionSecret(t, db, redemption.Id), original)

	var loaded Redemption
	require.NoError(t, db.First(&loaded, redemption.Id).Error)
	assert.Equal(t, original, loaded.Key)

	const rotated = "redemption-code-rotated-000002"
	require.NoError(t, db.Model(&Redemption{}).Where("id = ?", redemption.Id).Updates(map[string]interface{}{
		"key": rotated, "quota": 250,
	}).Error)
	assertSealedRedemptionKey(t, readRawRedemptionSecret(t, db, redemption.Id), rotated)
	require.NoError(t, db.First(&loaded, redemption.Id).Error)
	assert.Equal(t, rotated, loaded.Key)
	assert.Equal(t, 250, loaded.Quota)

	err := db.Model(&Redemption{}).Where("id = ?", redemption.Id).
		Update("key_ciphertext", "attacker-controlled").Error
	assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)

	const structRotated = "redemption-code-struct-000003"
	patch := &Redemption{LegacyKey: structRotated, Quota: 300}
	require.NoError(t, db.Model(&Redemption{}).Where("id = ?", redemption.Id).Updates(patch).Error)
	assert.Equal(t, structRotated, patch.LegacyKey, "the caller's runtime patch must remain plaintext")
	assert.Empty(t, patch.KeyCiphertext)
	assert.Nil(t, patch.KeyHash)
	assertSealedRedemptionKey(t, readRawRedemptionSecret(t, db, redemption.Id), structRotated)
	require.NoError(t, db.First(&loaded, redemption.Id).Error)
	assert.Equal(t, structRotated, loaded.Key)
	assert.Equal(t, 300, loaded.Quota)

	err = db.Model(&Redemption{}).Where("id = ?", redemption.Id).Updates(redemptionKeyColumnUpdate{
		Key: "custom-struct-redemption-plaintext",
	}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected column key")
	assertSealedRedemptionKey(t, readRawRedemptionSecret(t, db, redemption.Id), structRotated)
}

func TestRedemptionKeyLookupVerifiesCiphertextAndSupportsLegacyRows(t *testing.T) {
	db := setupRedemptionSecretStorageTest(t)
	const encryptedKey = "encrypted-redemption-code-00001"
	encrypted := redemptionSecretFixture("encrypted", encryptedKey)
	require.NoError(t, db.Create(&encrypted).Error)

	err := db.Transaction(func(tx *gorm.DB) error {
		matched, err := findRedemptionByKeyForUpdate(tx, encryptedKey)
		require.NoError(t, err)
		assert.Equal(t, encrypted.Id, matched.Id)
		_, err = findRedemptionByKeyForUpdate(tx, encrypted.LegacyKey)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
		return nil
	})
	require.NoError(t, err)

	const legacyKey = "legacy-redemption-code-00000002"
	legacyID := insertLegacyRedemptionSecret(t, db, "legacy-lookup", legacyKey)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		matched, err := findRedemptionByKeyForUpdate(tx, legacyKey)
		require.NoError(t, err)
		assert.Equal(t, legacyID, matched.Id)
		assert.Equal(t, legacyKey, matched.Key)
		return nil
	}))
}

func TestMigrateLegacyRedemptionKeysIsIdempotentAndRepairsPartialRows(t *testing.T) {
	db := setupRedemptionSecretStorageTest(t)
	const legacyKey = "legacy-redemption-code-00000003"
	legacyID := insertLegacyRedemptionSecret(t, db, "legacy-migrate", legacyKey)

	const partialKey = "partial-redemption-code-0000004"
	partialCiphertext, err := common.EncryptCredential(partialKey)
	require.NoError(t, err)
	partialID := insertLegacyRedemptionSecret(t, db, "partial-migrate", "stale-marker")
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("redemptions").Where("id = ?", partialID).
		Updates(map[string]interface{}{"key_ciphertext": partialCiphertext, "key_hash": "stale-hash"}).Error)

	require.NoError(t, MigrateLegacyRedemptionKeys(db))
	legacyFirst := readRawRedemptionSecret(t, db, legacyID)
	partialFirst := readRawRedemptionSecret(t, db, partialID)
	assertSealedRedemptionKey(t, legacyFirst, legacyKey)
	assertSealedRedemptionKey(t, partialFirst, partialKey)
	require.NoError(t, MigrateLegacyRedemptionKeys(db))
	assert.Equal(t, legacyFirst, readRawRedemptionSecret(t, db, legacyID))
	assert.Equal(t, partialFirst, readRawRedemptionSecret(t, db, partialID))
}

func TestMigrateLegacyRedemptionKeysExpandsHistoricalSchema(t *testing.T) {
	db := setupRedemptionSecretStorageTest(t)
	require.NoError(t, db.Migrator().DropTable(&Redemption{}))
	require.NoError(t, db.AutoMigrate(&legacyRedemptionSecretSchema{}))
	const legacyKey = "historical-schema-code-00000001"
	require.NoError(t, db.Create(&legacyRedemptionSecretSchema{
		Key: legacyKey, Name: "historical-schema", Status: common.RedemptionCodeStatusEnabled,
		Quota: 100, CreatedTime: common.GetTimestamp(),
	}).Error)

	require.NoError(t, db.AutoMigrate(&Redemption{}))
	for _, column := range []string{"key", "key_ciphertext", "key_hash"} {
		assert.True(t, db.Migrator().HasColumn(&Redemption{}, column))
	}
	require.NoError(t, MigrateLegacyRedemptionKeys(db))
	var loaded Redemption
	require.NoError(t, db.Where("name = ?", "historical-schema").First(&loaded).Error)
	assert.Equal(t, legacyKey, loaded.Key)
	assertSealedRedemptionKey(t, readRawRedemptionSecret(t, db, loaded.Id), legacyKey)
}

func TestMigrateLegacyRedemptionKeyCASPreservesConcurrentEdit(t *testing.T) {
	db := setupRedemptionSecretStorageTest(t)
	id := insertLegacyRedemptionSecret(t, db, "legacy-cas", "observed-redemption-code-00001")
	observed, err := readRedemptionSecretMigrationRow(db, id)
	require.NoError(t, err)

	const newerKey = "newer-redemption-code-00000002"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("redemptions").Where("id = ?", id).
		Update("key", newerKey).Error)
	require.NoError(t, migrateRedemptionKeyRow(db, observed))
	assertSealedRedemptionKey(t, readRawRedemptionSecret(t, db, id), newerKey)

	var loaded Redemption
	require.NoError(t, db.First(&loaded, id).Error)
	assert.Equal(t, newerKey, loaded.Key)
}

func TestRedemptionKeyStorageFailsClosedForWrongCorruptOrMissingKey(t *testing.T) {
	t.Run("wrong deployment key", func(t *testing.T) {
		db := setupRedemptionSecretStorageTest(t)
		redemption := redemptionSecretFixture("wrong-key", "wrong-key-redemption-code-0001")
		require.NoError(t, db.Create(&redemption).Error)
		common.CryptoSecret = "different-redemption-storage-key"
		var loaded Redemption
		err := db.First(&loaded, redemption.Id).Error
		assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
	})

	t.Run("corrupt ciphertext", func(t *testing.T) {
		db := setupRedemptionSecretStorageTest(t)
		redemption := redemptionSecretFixture("corrupt", "corrupt-redemption-code-0000001")
		require.NoError(t, db.Create(&redemption).Error)
		require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("redemptions").Where("id = ?", redemption.Id).
			Update("key_ciphertext", "enc:v1:not-valid").Error)
		var loaded Redemption
		err := db.First(&loaded, redemption.Id).Error
		assert.ErrorIs(t, err, ErrCredentialStorageCorrupt)
	})

	t.Run("missing stable key", func(t *testing.T) {
		db := setupRedemptionSecretStorageTest(t)
		insertLegacyRedemptionSecret(t, db, "missing-key", "missing-key-redemption-code-0001")
		common.CredentialSecretConfigured = false
		err := MigrateLegacyRedemptionKeys(db)
		assert.ErrorIs(t, err, ErrCredentialStorageUnavailable)
	})
}

func TestRedemptionKeyPartialProjectionDoesNotRequireCredentialColumns(t *testing.T) {
	db := setupRedemptionSecretStorageTest(t)
	redemption := redemptionSecretFixture("projection", "projection-redemption-code-00001")
	require.NoError(t, db.Create(&redemption).Error)
	var projected Redemption
	require.NoError(t, db.Select("id", "name").First(&projected, redemption.Id).Error)
	assert.Equal(t, redemption.Id, projected.Id)
	assert.Empty(t, projected.Key)
	assert.Empty(t, projected.LegacyKey)
	assert.Nil(t, projected.KeyHash)

	_, err := findRedemptionByKeyForUpdate(db, "not-a-redemption-code")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}
