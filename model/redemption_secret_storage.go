package model

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	redemptionSecretMigrationBatchSize = 100
	redemptionSecretCandidateLimit     = 8
)

type redemptionSecretMigrationRow struct {
	ID         int            `gorm:"column:id"`
	Legacy     sql.NullString `gorm:"column:key"`
	Ciphertext sql.NullString `gorm:"column:key_ciphertext"`
	Hash       sql.NullString `gorm:"column:key_hash"`
}

func redemptionKeyHashValue(redemption *Redemption) string {
	if redemption == nil || redemption.KeyHash == nil {
		return ""
	}
	return *redemption.KeyHash
}

func openRedemptionKey(legacy, ciphertext string) (string, error) {
	legacy = strings.TrimSpace(legacy)
	ciphertext = strings.TrimSpace(ciphertext)
	if legacy == "" && ciphertext == "" {
		return "", nil
	}
	if !common.CredentialEncryptionReady() {
		return "", ErrCredentialStorageUnavailable
	}
	if ciphertext != "" {
		plaintext, err := common.DecryptCredential(ciphertext)
		if err != nil {
			if errors.Is(err, common.ErrCredentialSecretUnavailable) {
				return "", fmt.Errorf("%w: decrypt redemption key: %v", ErrCredentialStorageUnavailable, err)
			}
			return "", fmt.Errorf("%w: decrypt redemption key: %v", ErrCredentialStorageCorrupt, err)
		}
		if plaintext == "" {
			return "", fmt.Errorf("%w: encrypted redemption key is empty", ErrCredentialStorageCorrupt)
		}
		return plaintext, nil
	}
	if isCredentialFingerprintMarker(legacy) {
		return "", fmt.Errorf("%w: redemption key marker has no ciphertext", ErrCredentialStorageCorrupt)
	}
	return legacy, nil
}

func redemptionKeyEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (redemption *Redemption) prepareKeyFields() error {
	if redemption == nil {
		return nil
	}
	if redemption.Key != "" {
		if strings.TrimSpace(redemption.KeyCiphertext) != "" {
			stored, err := openRedemptionKey(redemption.LegacyKey, redemption.KeyCiphertext)
			if err != nil {
				return err
			}
			if redemptionKeyEqual(stored, redemption.Key) {
				hash := common.CredentialFingerprint(redemption.Key)
				redemption.LegacyKey = credentialFingerprintMarker(hash)
				redemption.KeyHash = &hash
				return nil
			}
		}

		marker, ciphertext, hash, err := sealCredential(redemption.Key)
		if err != nil {
			return err
		}
		redemption.LegacyKey = marker
		redemption.KeyCiphertext = ciphertext
		redemption.KeyHash = &hash
		return nil
	}

	legacy := strings.TrimSpace(redemption.LegacyKey)
	ciphertext := strings.TrimSpace(redemption.KeyCiphertext)
	storedHash := strings.TrimSpace(redemptionKeyHashValue(redemption))
	if legacy == "" && ciphertext == "" && storedHash == "" {
		return nil
	}
	plaintext, err := openRedemptionKey(legacy, ciphertext)
	if err != nil {
		return err
	}
	if plaintext == "" {
		return fmt.Errorf("%w: redemption key fields are incomplete", ErrCredentialStorageCorrupt)
	}
	hash := common.CredentialFingerprint(plaintext)
	marker := credentialFingerprintMarker(hash)
	if ciphertext == "" {
		marker, ciphertext, hash, err = sealCredential(plaintext)
		if err != nil {
			return err
		}
		redemption.KeyCiphertext = ciphertext
	}
	redemption.LegacyKey = marker
	redemption.KeyHash = &hash
	return nil
}

// BeforeSave is the persistence boundary for redemption codes. It covers
// direct Create/Save calls and map updates so future administrative paths
// cannot accidentally put a redeemable code back into the historical column.
func (redemption *Redemption) BeforeSave(tx *gorm.DB) error {
	if redemption == nil {
		return nil
	}
	if tx != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizeRedemptionKeyUpdateMap(values)
		}
		handled, err := prepareProtectedStructDestination(
			tx,
			(*Redemption).prepareKeyFields,
			"key", "key_ciphertext", "key_hash",
		)
		if handled || err != nil {
			return err
		}
	}
	return redemption.prepareKeyFields()
}

func (redemption *Redemption) AfterFind(_ *gorm.DB) error {
	if redemption == nil {
		return nil
	}
	legacy := strings.TrimSpace(redemption.LegacyKey)
	ciphertext := strings.TrimSpace(redemption.KeyCiphertext)
	storedHash := strings.TrimSpace(redemptionKeyHashValue(redemption))
	if legacy == "" && ciphertext == "" && storedHash == "" {
		redemption.Key = ""
		return nil
	}
	plaintext, err := openRedemptionKey(legacy, ciphertext)
	if err != nil {
		return err
	}
	if plaintext == "" {
		return fmt.Errorf("%w: redemption key fields are incomplete", ErrCredentialStorageCorrupt)
	}
	redemption.Key = plaintext
	return nil
}

func normalizeRedemptionKeyUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		present  bool
	)
	for _, candidate := range []string{"key", "Key", "LegacyKey"} {
		if value, ok := values[candidate]; ok {
			if present {
				return errors.New("redemption key update is ambiguous")
			}
			rawValue = value
			present = true
		}
		delete(values, candidate)
	}
	companionKeys := []string{
		"key_ciphertext", "KeyCiphertext",
		"key_hash", "KeyHash",
	}
	if !present {
		for _, key := range companionKeys {
			if _, ok := values[key]; ok {
				return fmt.Errorf("%w: direct redemption key companion update", ErrCredentialStorageCorrupt)
			}
		}
		return nil
	}
	for _, key := range companionKeys {
		delete(values, key)
	}

	plaintext := ""
	switch value := rawValue.(type) {
	case string:
		plaintext = value
	case *string:
		if value != nil {
			plaintext = *value
		}
	case nil:
	default:
		return errors.New("redemption key must be a string")
	}
	if plaintext == "" {
		return errors.New("redemption key must not be empty")
	}
	marker, ciphertext, hash, err := sealCredential(plaintext)
	if err != nil {
		return err
	}
	values["key"] = marker
	values["key_ciphertext"] = ciphertext
	values["key_hash"] = hash
	return nil
}

func findRedemptionByKeyForUpdate(tx *gorm.DB, key string) (*Redemption, error) {
	if tx == nil {
		return nil, errors.New("database is not initialized")
	}
	if key == "" {
		return nil, gorm.ErrRecordNotFound
	}
	if !common.CredentialEncryptionReady() {
		return nil, ErrCredentialStorageUnavailable
	}

	hash := common.CredentialFingerprint(key)
	var candidates []Redemption
	if err := lockForUpdate(tx).Where("key_hash = ?", hash).
		Limit(redemptionSecretCandidateLimit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	for i := range candidates {
		if redemptionKeyEqual(candidates[i].Key, key) {
			return &candidates[i], nil
		}
	}

	// A new-version process can still see legacy plaintext rows until the
	// startup migration reaches them. Verify the hydrated value even after an
	// exact legacy-column hit so a stored fingerprint marker is never accepted
	// as though it were the redeemable code.
	var legacy Redemption
	err := lockForUpdate(tx).Where(mainKeyColumn(tx)+" = ?", key).First(&legacy).Error
	if err != nil {
		return nil, err
	}
	if !redemptionKeyEqual(legacy.Key, key) {
		return nil, gorm.ErrRecordNotFound
	}
	return &legacy, nil
}

// MigrateLegacyRedemptionKeys replaces plaintext redemption codes with a
// fingerprint marker and authenticated ciphertext. Primary-key pagination and
// exact-value CAS updates make the pass idempotent and safe across concurrent
// new-version instances or administrative changes.
func MigrateLegacyRedemptionKeys(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable(&Redemption{}) {
		return nil
	}
	for _, column := range []string{"key", "key_ciphertext", "key_hash"} {
		if !db.Migrator().HasColumn(&Redemption{}, column) {
			return fmt.Errorf("redemptions.%s is required for encrypted key storage", column)
		}
	}

	lastID := 0
	for {
		var rows []redemptionSecretMigrationRow
		if err := db.Session(&gorm.Session{SkipHooks: true}).Table("redemptions").
			Select("id, key, key_ciphertext, key_hash").
			Where("id > ?", lastID).Order("id").Limit(redemptionSecretMigrationBatchSize).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy redemption keys: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if err := migrateRedemptionKeyRow(db, row); err != nil {
				return fmt.Errorf("migrate redemption key id=%d: %w", row.ID, err)
			}
		}
		if len(rows) < redemptionSecretMigrationBatchSize {
			return nil
		}
	}
}

func migrateRedemptionKeyRow(db *gorm.DB, observed redemptionSecretMigrationRow) error {
	if db == nil || observed.ID <= 0 {
		return nil
	}
	for attempt := 0; attempt < 5; attempt++ {
		legacy := ""
		if observed.Legacy.Valid {
			legacy = observed.Legacy.String
		}
		ciphertext := ""
		if observed.Ciphertext.Valid {
			ciphertext = strings.TrimSpace(observed.Ciphertext.String)
		}
		storedHash := ""
		if observed.Hash.Valid {
			storedHash = strings.TrimSpace(observed.Hash.String)
		}
		if strings.TrimSpace(legacy) == "" && ciphertext == "" {
			if storedHash != "" {
				return fmt.Errorf("%w: redemption key hash has no credential", ErrCredentialStorageCorrupt)
			}
			return nil
		}

		plaintext, err := openRedemptionKey(legacy, ciphertext)
		if err != nil {
			return err
		}
		if plaintext == "" {
			return fmt.Errorf("%w: redemption key is empty", ErrCredentialStorageCorrupt)
		}
		hash := common.CredentialFingerprint(plaintext)
		marker := credentialFingerprintMarker(hash)
		sealed := ciphertext
		if sealed == "" {
			marker, sealed, hash, err = sealCredential(plaintext)
			if err != nil {
				return err
			}
		}
		if strings.TrimSpace(legacy) == marker && ciphertext == sealed && storedHash == hash {
			return nil
		}

		query := db.Session(&gorm.Session{SkipHooks: true}).Table("redemptions").Where("id = ?", observed.ID)
		query = whereObservedNullableString(query, "key", observed.Legacy)
		query = whereObservedNullableString(query, "key_ciphertext", observed.Ciphertext)
		query = whereObservedNullableString(query, "key_hash", observed.Hash)
		result := query.Updates(map[string]interface{}{
			"key":            marker,
			"key_ciphertext": sealed,
			"key_hash":       hash,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}

		current, err := readRedemptionSecretMigrationRow(db, observed.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		observed = current
	}
	return errors.New("redemption key changed repeatedly during migration")
}

func readRedemptionSecretMigrationRow(db *gorm.DB, id int) (redemptionSecretMigrationRow, error) {
	var row redemptionSecretMigrationRow
	err := db.Session(&gorm.Session{SkipHooks: true}).Table("redemptions").
		Select("id, key, key_ciphertext, key_hash").Where("id = ?", id).Take(&row).Error
	return row, err
}
