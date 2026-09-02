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

const providerStorageMigrationBatchSize = 200

// MigrateLegacyProviderStorage encrypts replayable media and minimizes
// historical diagnostics. Per-row optimistic predicates prevent a startup
// migration from overwriting a concurrent poll result.
func MigrateLegacyProviderStorage(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if err := migrateLegacyTaskDiagnostics(db); err != nil {
		return err
	}
	return migrateLegacyMidjourneyStorage(db)
}

type taskProviderStorageMigrationRow struct {
	ID          int64          `gorm:"column:id"`
	FailReason  sql.NullString `gorm:"column:fail_reason"`
	PrivateData sql.NullString `gorm:"column:private_data"`
}

func migrateLegacyTaskDiagnostics(db *gorm.DB) error {
	if !db.Migrator().HasTable("tasks") || !db.Migrator().HasColumn(&Task{}, "fail_reason") {
		return nil
	}
	lastID := int64(0)
	for {
		var rows []taskProviderStorageMigrationRow
		if err := db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
			Select("id, fail_reason, private_data").Where("id > ?", lastID).
			Order("id").Limit(providerStorageMigrationBatchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy task diagnostics: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			lastID = row.ID
			if !row.FailReason.Valid {
				continue
			}
			updates := map[string]interface{}{}
			candidateURL := strings.TrimSpace(row.FailReason.String)
			legacyResultURL := common.ValidateHTTPURL(candidateURL) == nil
			if legacyResultURL {
				encodedPrivateData, changed, err := addLegacyTaskResultURL(row.PrivateData, candidateURL)
				if err != nil {
					return fmt.Errorf("preserve legacy task result URL id=%d: %w", row.ID, err)
				}
				if changed {
					updates["private_data"] = encodedPrivateData
				}
			}

			sanitized := common.SanitizeProviderDiagnosticForStorage(row.FailReason.String, common.MaxHTTPURLLength)
			if sanitized != row.FailReason.String {
				updates["fail_reason"] = sanitized
			}
			if len(updates) == 0 {
				continue
			}
			query := db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
				Where("id = ? AND fail_reason = ?", row.ID, row.FailReason.String)
			if legacyResultURL {
				if row.PrivateData.Valid {
					query = query.Where("private_data = ?", row.PrivateData.String)
				} else {
					query = query.Where("private_data IS NULL")
				}
			}
			result := query.Updates(updates)
			if result.Error != nil {
				return fmt.Errorf("sanitize legacy task diagnostics id=%d: %w", row.ID, result.Error)
			}
		}
		if len(rows) < providerStorageMigrationBatchSize {
			return nil
		}
	}
}

// addLegacyTaskResultURL moves the historical fail_reason URL into the private
// replayable storage field without discarding unrecognized task metadata.
func addLegacyTaskResultURL(raw sql.NullString, resultURL string) (string, bool, error) {
	object := make(map[string]json.RawMessage)
	if raw.Valid && strings.TrimSpace(raw.String) != "" && strings.TrimSpace(raw.String) != "null" {
		if err := common.Unmarshal([]byte(raw.String), &object); err != nil {
			return "", false, fmt.Errorf("invalid private_data JSON: %w", err)
		}
		if object == nil {
			object = make(map[string]json.RawMessage)
		}
	}

	resultURLField := ""
	for _, candidate := range []string{"result_url", "ResultURL"} {
		if _, ok := object[candidate]; ok {
			if resultURLField != "" {
				return "", false, errors.New("task private result URL is ambiguous")
			}
			resultURLField = candidate
		}
	}
	if resultURLField == "" {
		resultURLField = "result_url"
	} else if rawURL := object[resultURLField]; len(rawURL) != 0 && string(rawURL) != "null" {
		var existing string
		if err := common.Unmarshal(rawURL, &existing); err != nil {
			return "", false, fmt.Errorf("task private result URL is not a string: %w", err)
		}
		if strings.TrimSpace(existing) != "" {
			return raw.String, false, nil
		}
	}

	sealed, err := sealProviderRuntimeValue("task private result URL", resultURL, common.MaxHTTPURLLength)
	if err != nil {
		return "", false, err
	}
	encodedURL, err := common.Marshal(sealed)
	if err != nil {
		return "", false, err
	}
	object[resultURLField] = encodedURL
	encoded, err := common.Marshal(object)
	if err != nil {
		return "", false, fmt.Errorf("encode private_data: %w", err)
	}
	return string(encoded), true, nil
}

type midjourneyProviderStorageMigrationRow struct {
	ID          int64          `gorm:"column:id"`
	ImageURL    sql.NullString `gorm:"column:image_url"`
	VideoURL    sql.NullString `gorm:"column:video_url"`
	VideoURLs   sql.NullString `gorm:"column:video_urls"`
	Description sql.NullString `gorm:"column:description"`
	FailReason  sql.NullString `gorm:"column:fail_reason"`
	Properties  sql.NullString `gorm:"column:properties"`
	Buttons     sql.NullString `gorm:"column:buttons"`
}

func migrateLegacyMidjourneyStorage(db *gorm.DB) error {
	if !db.Migrator().HasTable("midjourneys") {
		return nil
	}
	lastID := int64(0)
	for {
		var rows []midjourneyProviderStorageMigrationRow
		if err := db.Session(&gorm.Session{SkipHooks: true}).Table("midjourneys").
			Select("id, image_url, video_url, video_urls, description, fail_reason, properties, buttons").
			Where("id > ?", lastID).Order("id").Limit(providerStorageMigrationBatchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy midjourney provider data: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			lastID = row.ID
			updates := map[string]interface{}{}
			query := db.Session(&gorm.Session{SkipHooks: true}).Table("midjourneys").Where("id = ?", row.ID)
			var err error
			query, err = addProviderStorageMigrationUpdate(query, updates, "image_url", row.ImageURL, func(value string) (string, error) {
				return sealProviderRuntimeValue("midjourney image URL", value, maxMidjourneyStorageURLBytes)
			})
			if err != nil {
				return fmt.Errorf("encrypt legacy midjourney image URL id=%d: %w", row.ID, err)
			}
			query, err = addProviderStorageMigrationUpdate(query, updates, "video_url", row.VideoURL, func(value string) (string, error) {
				return sealProviderRuntimeValue("midjourney video URL", value, maxMidjourneyStorageURLBytes)
			})
			if err != nil {
				return fmt.Errorf("encrypt legacy midjourney video URL id=%d: %w", row.ID, err)
			}
			query, err = addProviderStorageMigrationUpdate(query, updates, "video_urls", row.VideoURLs, func(value string) (string, error) {
				return sealProviderRuntimeValue("midjourney video URLs", value, maxMidjourneyStorageJSONBytes)
			})
			if err != nil {
				return fmt.Errorf("encrypt legacy midjourney video URLs id=%d: %w", row.ID, err)
			}
			query, err = addProviderStorageMigrationUpdate(query, updates, "description", row.Description, func(value string) (string, error) {
				return common.SanitizeProviderDiagnosticForStorage(value, maxMidjourneyStorageTextBytes), nil
			})
			if err != nil {
				return err
			}
			query, err = addProviderStorageMigrationUpdate(query, updates, "fail_reason", row.FailReason, func(value string) (string, error) {
				return common.SanitizeProviderDiagnosticForStorage(value, maxMidjourneyStorageTextBytes), nil
			})
			if err != nil {
				return err
			}
			query, err = addProviderStorageMigrationUpdate(query, updates, "properties", row.Properties, func(value string) (string, error) {
				return common.SanitizeProviderJSONForStorage(value, maxMidjourneyStorageJSONBytes), nil
			})
			if err != nil {
				return err
			}
			query, err = addProviderStorageMigrationUpdate(query, updates, "buttons", row.Buttons, func(value string) (string, error) {
				return common.SanitizeProviderJSONForStorage(value, maxMidjourneyStorageJSONBytes), nil
			})
			if err != nil {
				return err
			}
			if len(updates) == 0 {
				continue
			}
			if result := query.Updates(updates); result.Error != nil {
				return fmt.Errorf("sanitize legacy midjourney provider data id=%d: %w", row.ID, result.Error)
			}
		}
		if len(rows) < providerStorageMigrationBatchSize {
			return nil
		}
	}
}

func addProviderStorageMigrationUpdate(query *gorm.DB, updates map[string]interface{}, column string, raw sql.NullString, prepare func(string) (string, error)) (*gorm.DB, error) {
	if !raw.Valid {
		return query, nil
	}
	prepared, err := prepare(raw.String)
	if err != nil {
		return query, err
	}
	if prepared == raw.String {
		return query, nil
	}
	updates[column] = prepared
	return query.Where(column+" = ?", raw.String), nil
}
