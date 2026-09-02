package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const channelConfigSecretMigrationBatchSize = 100

type channelConfigSecretField struct {
	column  string
	aliases []string
}

var channelConfigSecretFields = []channelConfigSecretField{
	{column: "setting", aliases: []string{"setting", "Setting"}},
	{column: "param_override", aliases: []string{"param_override", "ParamOverride"}},
	{column: "header_override", aliases: []string{"header_override", "HeaderOverride"}},
	{column: "settings", aliases: []string{"settings", "OtherSettings"}},
}

func openChannelConfigSecret(field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if !common.IsCredentialCiphertext(trimmed) {
		if strings.HasPrefix(trimmed, "enc:") {
			return "", fmt.Errorf("%w: unsupported channel %s envelope", ErrCredentialStorageCorrupt, field)
		}
		// A new-version process can consume a legacy row before the startup
		// migration reaches it.
		return value, nil
	}
	plaintext, err := common.DecryptCredential(trimmed)
	if err == nil {
		return plaintext, nil
	}
	if errors.Is(err, common.ErrCredentialSecretUnavailable) {
		return "", fmt.Errorf("%w: decrypt channel %s: %v", ErrCredentialStorageUnavailable, field, err)
	}
	return "", fmt.Errorf("%w: decrypt channel %s: %v", ErrCredentialStorageCorrupt, field, err)
}

func sealChannelConfigSecret(field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if common.IsCredentialCiphertext(trimmed) {
		if _, err := openChannelConfigSecret(field, trimmed); err != nil {
			return "", err
		}
		return trimmed, nil
	}
	if strings.HasPrefix(trimmed, "enc:") {
		return "", fmt.Errorf("%w: unsupported channel %s envelope", ErrCredentialStorageCorrupt, field)
	}
	if !common.CredentialEncryptionReady() {
		return "", fmt.Errorf("%w: channel %s", ErrCredentialStorageUnavailable, field)
	}
	sealed, err := common.EncryptCredential(value)
	if err != nil {
		if errors.Is(err, common.ErrCredentialSecretUnavailable) {
			return "", fmt.Errorf("%w: encrypt channel %s: %v", ErrCredentialStorageUnavailable, field, err)
		}
		return "", fmt.Errorf("encrypt channel %s: %w", field, err)
	}
	return sealed, nil
}

func sealChannelConfigSecretPointer(field string, value **string) error {
	if value == nil || *value == nil {
		return nil
	}
	sealed, err := sealChannelConfigSecret(field, **value)
	if err != nil {
		return err
	}
	**value = sealed
	return nil
}

func openChannelConfigSecretPointer(field string, value **string) error {
	if value == nil || *value == nil {
		return nil
	}
	plaintext, err := openChannelConfigSecret(field, **value)
	if err != nil {
		return err
	}
	**value = plaintext
	return nil
}

func (channel *Channel) prepareConfigSecretFields() error {
	if channel == nil {
		return nil
	}
	if err := sealChannelConfigSecretPointer("setting", &channel.Setting); err != nil {
		return err
	}
	if err := sealChannelConfigSecretPointer("param_override", &channel.ParamOverride); err != nil {
		return err
	}
	if err := sealChannelConfigSecretPointer("header_override", &channel.HeaderOverride); err != nil {
		return err
	}
	sealed, err := sealChannelConfigSecret("settings", channel.OtherSettings)
	if err != nil {
		return err
	}
	channel.OtherSettings = sealed
	return nil
}

func (channel *Channel) openConfigSecretFields() error {
	if channel == nil {
		return nil
	}
	if err := openChannelConfigSecretPointer("setting", &channel.Setting); err != nil {
		return err
	}
	if err := openChannelConfigSecretPointer("param_override", &channel.ParamOverride); err != nil {
		return err
	}
	if err := openChannelConfigSecretPointer("header_override", &channel.HeaderOverride); err != nil {
		return err
	}
	plaintext, err := openChannelConfigSecret("settings", channel.OtherSettings)
	if err != nil {
		return err
	}
	channel.OtherSettings = plaintext
	return nil
}

// AfterSave restores the caller's runtime object after BeforeSave sealed the
// in-place JSON columns. GORM runs this callback before committing, so a
// decryption failure still fails the write rather than returning an envelope
// to provider code.
func (channel *Channel) AfterSave(_ *gorm.DB) error {
	return channel.openConfigSecretFields()
}

func normalizeChannelConfigSecretUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	for _, field := range channelConfigSecretFields {
		var (
			rawValue interface{}
			present  bool
		)
		for _, alias := range field.aliases {
			if value, ok := values[alias]; ok {
				if present {
					return fmt.Errorf("channel %s update is ambiguous", field.column)
				}
				rawValue = value
				present = true
			}
			delete(values, alias)
		}
		if !present {
			continue
		}
		if rawValue == nil {
			values[field.column] = nil
			continue
		}
		plaintext := ""
		switch value := rawValue.(type) {
		case string:
			plaintext = value
		case *string:
			if value != nil {
				plaintext = *value
			}
		default:
			return fmt.Errorf("channel %s must be a string", field.column)
		}
		sealed, err := sealChannelConfigSecret(field.column, plaintext)
		if err != nil {
			return err
		}
		values[field.column] = sealed
	}
	return nil
}

type channelConfigSecretMigrationRow struct {
	ID             int            `gorm:"column:id"`
	Setting        sql.NullString `gorm:"column:setting"`
	ParamOverride  sql.NullString `gorm:"column:param_override"`
	HeaderOverride sql.NullString `gorm:"column:header_override"`
	OtherSettings  sql.NullString `gorm:"column:settings"`
}

// MigrateLegacyChannelConfigSecrets encrypts the channel JSON columns that
// can carry proxy credentials, arbitrary headers/body keys, and Advanced
// Custom route authentication. Rows are paged by primary key and updated with
// an exact observed-value CAS so a concurrent administrator edit wins.
func MigrateLegacyChannelConfigSecrets(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable(&Channel{}) {
		return nil
	}
	for _, field := range channelConfigSecretFields {
		if !db.Migrator().HasColumn(&Channel{}, field.column) {
			return fmt.Errorf("channels.%s is required for encrypted channel configuration", field.column)
		}
	}

	lastID := 0
	for {
		var rows []channelConfigSecretMigrationRow
		if err := readChannelConfigSecretRows(db, lastID, channelConfigSecretMigrationBatchSize, &rows); err != nil {
			return fmt.Errorf("read legacy channel configuration: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if err := migrateChannelConfigSecretRow(db, row); err != nil {
				return fmt.Errorf("migrate channel configuration id=%d: %w", row.ID, err)
			}
		}
		if len(rows) < channelConfigSecretMigrationBatchSize {
			return nil
		}
	}
}

func readChannelConfigSecretRows(db *gorm.DB, lastID, limit int, rows *[]channelConfigSecretMigrationRow) error {
	return db.Session(&gorm.Session{SkipHooks: true}).Table("channels").
		Select("id, setting, param_override, header_override, settings").
		Where("id > ?", lastID).Order("id").Limit(limit).Find(rows).Error
}

func migrateChannelConfigSecretRow(db *gorm.DB, observed channelConfigSecretMigrationRow) error {
	for attempt := 0; attempt < 5; attempt++ {
		updates := make(map[string]interface{})
		values := []struct {
			field    string
			observed sql.NullString
		}{
			{field: "setting", observed: observed.Setting},
			{field: "param_override", observed: observed.ParamOverride},
			{field: "header_override", observed: observed.HeaderOverride},
			{field: "settings", observed: observed.OtherSettings},
		}
		for _, value := range values {
			if !value.observed.Valid || value.observed.String == "" {
				continue
			}
			sealed, err := sealChannelConfigSecret(value.field, value.observed.String)
			if err != nil {
				return err
			}
			if sealed != value.observed.String {
				updates[value.field] = sealed
			}
		}
		if len(updates) == 0 {
			return nil
		}

		query := db.Session(&gorm.Session{SkipHooks: true}).Table("channels").Where("id = ?", observed.ID)
		query = whereObservedChannelConfigString(query, "setting", observed.Setting)
		query = whereObservedChannelConfigString(query, "param_override", observed.ParamOverride)
		query = whereObservedChannelConfigString(query, "header_override", observed.HeaderOverride)
		query = whereObservedChannelConfigString(query, "settings", observed.OtherSettings)
		result := query.Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}

		var current channelConfigSecretMigrationRow
		err := db.Session(&gorm.Session{SkipHooks: true}).Table("channels").
			Select("id, setting, param_override, header_override, settings").
			Where("id = ?", observed.ID).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		observed = current
	}
	return errors.New("channel configuration changed repeatedly during migration")
}

func whereObservedChannelConfigString(query *gorm.DB, column string, observed sql.NullString) *gorm.DB {
	if observed.Valid {
		return query.Where(column+" = ?", observed.String)
	}
	return query.Where(column + " IS NULL")
}
