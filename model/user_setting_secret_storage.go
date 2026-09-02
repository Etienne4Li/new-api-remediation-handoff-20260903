package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

const userSettingSecretMigrationBatchSize = 100

var userSettingSecretFields = []string{
	"webhook_url",
	"webhook_secret",
	"bark_url",
	"gotify_url",
	"gotify_token",
}

func openUserSettingCredential(field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if !common.IsCredentialCiphertext(trimmed) {
		if strings.HasPrefix(trimmed, "enc:") {
			return "", fmt.Errorf("%w: unsupported user setting envelope in %s", ErrCredentialStorageCorrupt, field)
		}
		return value, nil
	}
	plaintext, err := common.DecryptCredential(trimmed)
	if err == nil {
		return plaintext, nil
	}
	if errors.Is(err, common.ErrCredentialSecretUnavailable) {
		return "", fmt.Errorf("%w: decrypt user setting %s: %v", ErrCredentialStorageUnavailable, field, err)
	}
	return "", fmt.Errorf("%w: decrypt user setting %s: %v", ErrCredentialStorageCorrupt, field, err)
}

func sealUserSettingCredential(field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !common.CredentialEncryptionReady() {
		return "", fmt.Errorf("%w: user setting %s", ErrCredentialStorageUnavailable, field)
	}
	sealed, err := common.EncryptCredential(value)
	if err != nil {
		return "", fmt.Errorf("%w: encrypt user setting %s: %v", ErrCredentialStorageUnavailable, field, err)
	}
	return sealed, nil
}

func marshalUserSettingForStorage(setting dto.UserSetting) (string, error) {
	var err error
	if setting.WebhookUrl, err = sealUserSettingCredential("webhook_url", setting.WebhookUrl); err != nil {
		return "", err
	}
	if setting.WebhookSecret, err = sealUserSettingCredential("webhook_secret", setting.WebhookSecret); err != nil {
		return "", err
	}
	if setting.BarkUrl, err = sealUserSettingCredential("bark_url", setting.BarkUrl); err != nil {
		return "", err
	}
	if setting.GotifyUrl, err = sealUserSettingCredential("gotify_url", setting.GotifyUrl); err != nil {
		return "", err
	}
	if setting.GotifyToken, err = sealUserSettingCredential("gotify_token", setting.GotifyToken); err != nil {
		return "", err
	}
	encoded, err := common.Marshal(setting)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func unmarshalUserSettingFromStorage(raw string) (dto.UserSetting, error) {
	setting := dto.UserSetting{}
	if strings.TrimSpace(raw) == "" {
		return setting, nil
	}
	if err := common.Unmarshal([]byte(raw), &setting); err != nil {
		return dto.UserSetting{}, fmt.Errorf("decode user setting: %w", err)
	}
	var err error
	if setting.WebhookUrl, err = openUserSettingCredential("webhook_url", setting.WebhookUrl); err != nil {
		return dto.UserSetting{}, err
	}
	if setting.WebhookSecret, err = openUserSettingCredential("webhook_secret", setting.WebhookSecret); err != nil {
		return dto.UserSetting{}, err
	}
	if setting.BarkUrl, err = openUserSettingCredential("bark_url", setting.BarkUrl); err != nil {
		return dto.UserSetting{}, err
	}
	if setting.GotifyUrl, err = openUserSettingCredential("gotify_url", setting.GotifyUrl); err != nil {
		return dto.UserSetting{}, err
	}
	if setting.GotifyToken, err = openUserSettingCredential("gotify_token", setting.GotifyToken); err != nil {
		return dto.UserSetting{}, err
	}
	return setting, nil
}

type userSettingSecretMigrationRow struct {
	ID      int    `gorm:"column:id"`
	Setting string `gorm:"column:setting"`
}

// MigrateLegacyUserSettingSecrets encrypts credential-bearing JSON fields
// while preserving every unknown setting field as raw JSON. Rows are updated
// with an exact original-value predicate so concurrent preference changes win.
func MigrateLegacyUserSettingSecrets(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable(&User{}) || !db.Migrator().HasColumn(&User{}, "setting") {
		return nil
	}

	lastID := 0
	for {
		var rows []userSettingSecretMigrationRow
		if err := db.Session(&gorm.Session{SkipHooks: true}).Table("users").
			Select("id, setting").Where("id > ?", lastID).Order("id").
			Limit(userSettingSecretMigrationBatchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy user setting secrets: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if err := migrateUserSettingSecretRow(db, row.ID, row.Setting); err != nil {
				return fmt.Errorf("migrate user setting secrets id=%d: %w", row.ID, err)
			}
		}
		if len(rows) < userSettingSecretMigrationBatchSize {
			return nil
		}
	}
}

func migrateUserSettingSecretRow(db *gorm.DB, id int, observed string) error {
	for attempt := 0; attempt < 5; attempt++ {
		encoded, changed, err := migrateUserSettingSecretJSON(observed)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		result := db.Session(&gorm.Session{SkipHooks: true}).Table("users").
			Where("id = ? AND setting = ?", id, observed).Update("setting", encoded)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}

		var current userSettingSecretMigrationRow
		err = db.Session(&gorm.Session{SkipHooks: true}).Table("users").
			Select("id, setting").Where("id = ?", id).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		observed = current.Setting
	}
	return errors.New("user setting changed repeatedly during secret migration")
}

func migrateUserSettingSecretJSON(raw string) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, false, nil
	}
	var object map[string]json.RawMessage
	if err := common.Unmarshal([]byte(raw), &object); err != nil {
		return "", false, fmt.Errorf("invalid user setting JSON: %w", err)
	}
	if object == nil {
		return raw, false, nil
	}
	changed := false
	for _, field := range userSettingSecretFields {
		rawValue, ok := object[field]
		if !ok || len(rawValue) == 0 || string(rawValue) == "null" {
			continue
		}
		var value string
		if err := common.Unmarshal(rawValue, &value); err != nil {
			return "", false, fmt.Errorf("user setting %s must be a string: %w", field, err)
		}
		if value == "" {
			continue
		}
		if common.IsCredentialCiphertext(strings.TrimSpace(value)) {
			if _, err := openUserSettingCredential(field, value); err != nil {
				return "", false, err
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(value), "enc:") {
			return "", false, fmt.Errorf("%w: unsupported user setting envelope in %s", ErrCredentialStorageCorrupt, field)
		}
		sealed, err := sealUserSettingCredential(field, value)
		if err != nil {
			return "", false, err
		}
		encodedValue, err := common.Marshal(sealed)
		if err != nil {
			return "", false, err
		}
		object[field] = encodedValue
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := common.Marshal(object)
	if err != nil {
		return "", false, fmt.Errorf("encode user setting: %w", err)
	}
	return string(encoded), true, nil
}
