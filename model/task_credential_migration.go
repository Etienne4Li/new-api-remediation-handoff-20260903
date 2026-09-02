package model

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const taskCredentialMigrationBatchSize = 200

// MigrateLegacyTaskCredentials expands the task private_data JSON in place.
// Older rows may contain a provider API key as plaintext under `key` or a
// replayable signed media URL under `result_url`. The migration seals those
// credentials while preserving every other (including future) JSON
// property. An optimistic raw-JSON predicate makes concurrent pollers safe:
// if a task changed after it was read, the writer simply leaves the newer row
// for the next pass.
func MigrateLegacyTaskCredentials(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable("tasks") || !db.Migrator().HasColumn(&Task{}, "private_data") {
		return nil
	}
	lastID := int64(0)
	for {
		var rows []taskCredentialMigrationRow
		if err := db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
			Select("id, private_data").
			Where("id > ?", lastID).
			Order("id").
			Limit(taskCredentialMigrationBatchSize).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy task credentials: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if !row.PrivateData.Valid || strings.TrimSpace(row.PrivateData.String) == "" {
				continue
			}
			if err := migrateTaskPrivateDataRow(db, row.ID, row.PrivateData.String); err != nil {
				return fmt.Errorf("migrate task private key id=%d: %w", row.ID, err)
			}
		}
		if len(rows) < taskCredentialMigrationBatchSize {
			return nil
		}
	}
}

type taskCredentialMigrationRow struct {
	ID          int64          `gorm:"column:id"`
	PrivateData sql.NullString `gorm:"column:private_data"`
}

func migrateTaskPrivateDataRow(db *gorm.DB, id int64, raw string) error {
	encoded, changed, err := sealTaskPrivateDataJSON(raw)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	result := db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Where("id = ? AND private_data = ?", id, raw).
		Update("private_data", encoded)
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func sealTaskPrivateDataJSON(raw string) (string, bool, error) {
	var object map[string]json.RawMessage
	if err := common.Unmarshal([]byte(raw), &object); err != nil {
		return "", false, fmt.Errorf("invalid private_data JSON: %w", err)
	}
	if object == nil {
		return raw, false, nil
	}
	changed := false
	resultURLField := ""
	for field := range object {
		if !strings.EqualFold(field, "result_url") && !strings.EqualFold(field, "ResultURL") {
			continue
		}
		if resultURLField != "" {
			return "", false, errors.New("task private result URL is ambiguous")
		}
		resultURLField = field
	}
	if resultURLField != "" && len(object[resultURLField]) != 0 && string(object[resultURLField]) != "null" {
		var resultURL string
		if err := common.Unmarshal(object[resultURLField], &resultURL); err != nil {
			return "", false, fmt.Errorf("task private result URL is not a string: %w", err)
		}
		sealed, err := sealProviderRuntimeValue("task private result URL", resultURL, common.MaxHTTPURLLength)
		if err != nil {
			return "", false, err
		}
		if sealed != resultURL {
			encodedURL, err := common.Marshal(sealed)
			if err != nil {
				return "", false, err
			}
			object[resultURLField] = encodedURL
			changed = true
		}
	}
	keyField := ""
	var rawKey json.RawMessage
	for field, value := range object {
		if !strings.EqualFold(field, "key") {
			continue
		}
		if keyField != "" {
			return "", false, errors.New("task private key is ambiguous")
		}
		keyField = field
		rawKey = value
	}
	if keyField == "" || len(rawKey) == 0 || string(rawKey) == "null" {
		return encodeTaskPrivateDataObject(raw, object, changed)
	}
	var key string
	if err := common.Unmarshal(rawKey, &key); err != nil {
		return "", false, fmt.Errorf("task private key is not a string: %w", err)
	}
	if strings.TrimSpace(key) == "" {
		return encodeTaskPrivateDataObject(raw, object, changed)
	}
	if common.IsCredentialCiphertext(key) {
		// Validate existing envelopes so a corrupt row is never silently marked
		// migrated.  TaskPrivateData.Scan will fail closed on the same row later.
		if _, err := common.DecryptCredential(strings.TrimSpace(key)); err != nil {
			return "", false, fmt.Errorf("%w: %v", ErrCredentialStorageCorrupt, err)
		}
		return encodeTaskPrivateDataObject(raw, object, changed)
	}

	sealed, err := common.EncryptCredential(key)
	if err != nil {
		return "", false, err
	}
	encodedKey, err := common.Marshal(sealed)
	if err != nil {
		return "", false, err
	}
	object[keyField] = encodedKey
	changed = true
	return encodeTaskPrivateDataObject(raw, object, changed)
}

func encodeTaskPrivateDataObject(raw string, object map[string]json.RawMessage, changed bool) (string, bool, error) {
	if !changed {
		return raw, false, nil
	}
	encoded, err := common.Marshal(object)
	if err != nil {
		return "", false, fmt.Errorf("encode private_data: %w", err)
	}
	return string(encoded), true, nil
}
