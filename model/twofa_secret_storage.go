package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const twoFASecretMigrationBatchSize = 100

func openTwoFASecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if !common.IsCredentialCiphertext(trimmed) {
		if strings.HasPrefix(trimmed, "enc:") {
			return "", fmt.Errorf("%w: unsupported 2FA secret envelope", ErrCredentialStorageCorrupt)
		}
		return value, nil
	}
	plaintext, err := common.DecryptCredential(trimmed)
	if err == nil {
		return plaintext, nil
	}
	if errors.Is(err, common.ErrCredentialSecretUnavailable) {
		return "", fmt.Errorf("%w: decrypt 2FA secret: %v", ErrCredentialStorageUnavailable, err)
	}
	return "", fmt.Errorf("%w: decrypt 2FA secret: %v", ErrCredentialStorageCorrupt, err)
}

func sealTwoFASecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if common.IsCredentialCiphertext(trimmed) {
		if _, err := openTwoFASecret(trimmed); err != nil {
			return "", err
		}
		return trimmed, nil
	}
	if strings.HasPrefix(trimmed, "enc:") {
		return "", fmt.Errorf("%w: unsupported 2FA secret envelope", ErrCredentialStorageCorrupt)
	}
	if !common.CredentialEncryptionReady() {
		return "", ErrCredentialStorageUnavailable
	}
	sealed, err := common.EncryptCredential(value)
	if err != nil {
		return "", fmt.Errorf("%w: encrypt 2FA secret: %v", ErrCredentialStorageUnavailable, err)
	}
	return sealed, nil
}

func (twoFA *TwoFA) prepareSecretField() error {
	if twoFA == nil {
		return nil
	}
	sealed, err := sealTwoFASecret(twoFA.Secret)
	if err != nil {
		return err
	}
	twoFA.Secret = sealed
	return nil
}

func normalizeTwoFASecretUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		present  bool
	)
	for _, alias := range []string{"secret", "Secret"} {
		if value, ok := values[alias]; ok {
			if present {
				return errors.New("2FA secret update is ambiguous")
			}
			rawValue = value
			present = true
		}
		delete(values, alias)
	}
	if !present {
		return nil
	}
	plaintext := ""
	switch value := rawValue.(type) {
	case nil:
	case string:
		plaintext = value
	case *string:
		if value != nil {
			plaintext = *value
		}
	default:
		return errors.New("2FA secret must be a string")
	}
	sealed, err := sealTwoFASecret(plaintext)
	if err != nil {
		return err
	}
	values["secret"] = sealed
	return nil
}

func (twoFA *TwoFA) BeforeSave(tx *gorm.DB) error {
	if twoFA == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizeTwoFASecretUpdateMap(values)
		}
		handled, err := prepareProtectedStructDestination(
			tx,
			(*TwoFA).prepareSecretField,
			"secret",
		)
		if handled || err != nil {
			return err
		}
	}
	return twoFA.prepareSecretField()
}

func (twoFA *TwoFA) AfterSave(_ *gorm.DB) error {
	if twoFA == nil {
		return nil
	}
	plaintext, err := openTwoFASecret(twoFA.Secret)
	if err != nil {
		return err
	}
	twoFA.Secret = plaintext
	return nil
}

func (twoFA *TwoFA) AfterFind(_ *gorm.DB) error {
	if twoFA == nil {
		return nil
	}
	plaintext, err := openTwoFASecret(twoFA.Secret)
	if err != nil {
		return err
	}
	twoFA.Secret = plaintext
	return nil
}

type twoFASecretMigrationRow struct {
	ID     int    `gorm:"column:id"`
	Secret string `gorm:"column:secret"`
}

// MigrateLegacyTwoFASecrets rewrites legacy plaintext TOTP seeds in bounded
// primary-key order. The observed secret participates in each UPDATE so a
// concurrent factor replacement is re-read and never overwritten.
func MigrateLegacyTwoFASecrets(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable(&TwoFA{}) || !db.Migrator().HasColumn(&TwoFA{}, "secret") {
		return nil
	}

	lastID := 0
	for {
		var rows []twoFASecretMigrationRow
		if err := db.Session(&gorm.Session{SkipHooks: true}).Table("two_fas").
			Select("id, secret").Where("id > ?", lastID).Order("id").
			Limit(twoFASecretMigrationBatchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy 2FA secrets: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if err := migrateTwoFASecretRow(db, row.ID, row.Secret); err != nil {
				return fmt.Errorf("migrate 2FA secret id=%d: %w", row.ID, err)
			}
		}
		if len(rows) < twoFASecretMigrationBatchSize {
			return nil
		}
	}
}

func migrateTwoFASecretRow(db *gorm.DB, id int, observed string) error {
	for attempt := 0; attempt < 5; attempt++ {
		if observed == "" || common.IsCredentialCiphertext(strings.TrimSpace(observed)) {
			_, err := openTwoFASecret(observed)
			return err
		}
		if strings.HasPrefix(strings.TrimSpace(observed), "enc:") {
			return fmt.Errorf("%w: unsupported 2FA secret envelope", ErrCredentialStorageCorrupt)
		}
		sealed, err := sealTwoFASecret(observed)
		if err != nil {
			return err
		}
		result := db.Session(&gorm.Session{SkipHooks: true}).Table("two_fas").
			Where("id = ? AND secret = ?", id, observed).Update("secret", sealed)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}

		var current twoFASecretMigrationRow
		err = db.Session(&gorm.Session{SkipHooks: true}).Table("two_fas").
			Select("id, secret").Where("id = ?", id).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		observed = current.Secret
	}
	return errors.New("2FA secret changed repeatedly during migration")
}
