package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const customOAuthSecretMigrationBatchSize = 100

func openCustomOAuthSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if !common.IsCredentialCiphertext(trimmed) {
		if strings.HasPrefix(trimmed, "enc:") {
			return "", fmt.Errorf("%w: unsupported custom OAuth client secret envelope", ErrCredentialStorageCorrupt)
		}
		// Rolling upgrades continue to read legacy plaintext until the startup
		// migration has normalised the row.
		return value, nil
	}
	plaintext, err := common.DecryptCredential(trimmed)
	if err == nil {
		return plaintext, nil
	}
	if errors.Is(err, common.ErrCredentialSecretUnavailable) {
		return "", fmt.Errorf("%w: decrypt custom OAuth client secret: %v", ErrCredentialStorageUnavailable, err)
	}
	return "", fmt.Errorf("%w: decrypt custom OAuth client secret: %v", ErrCredentialStorageCorrupt, err)
}

func sealCustomOAuthSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if common.IsCredentialCiphertext(trimmed) {
		if _, err := openCustomOAuthSecret(trimmed); err != nil {
			return "", err
		}
		return trimmed, nil
	}
	if strings.HasPrefix(trimmed, "enc:") {
		return "", fmt.Errorf("%w: unsupported custom OAuth client secret envelope", ErrCredentialStorageCorrupt)
	}
	if !common.CredentialEncryptionReady() {
		return "", ErrCredentialStorageUnavailable
	}
	sealed, err := common.EncryptCredential(value)
	if err != nil {
		if errors.Is(err, common.ErrCredentialSecretUnavailable) {
			return "", fmt.Errorf("%w: encrypt custom OAuth client secret: %v", ErrCredentialStorageUnavailable, err)
		}
		return "", fmt.Errorf("encrypt custom OAuth client secret: %w", err)
	}
	return sealed, nil
}

func normalizeCustomOAuthSecretUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		present  bool
	)
	for _, name := range []string{"client_secret", "ClientSecret"} {
		if value, ok := values[name]; ok {
			if present {
				return errors.New("custom OAuth client secret update is ambiguous")
			}
			rawValue = value
			present = true
		}
		delete(values, name)
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
		return errors.New("custom OAuth client secret must be a string")
	}
	sealed, err := sealCustomOAuthSecret(plaintext)
	if err != nil {
		return err
	}
	values["client_secret"] = sealed
	return nil
}

func (provider *CustomOAuthProvider) prepareClientSecretField() error {
	if provider == nil {
		return nil
	}
	sealed, err := sealCustomOAuthSecret(provider.ClientSecret)
	if err != nil {
		return err
	}
	provider.ClientSecret = sealed
	return nil
}

// BeforeSave protects both struct and map based GORM writes. Map updates are
// important because they otherwise bypass field mutation on the model value.
func (provider *CustomOAuthProvider) BeforeSave(tx *gorm.DB) error {
	if provider == nil {
		return nil
	}
	if tx != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizeCustomOAuthSecretUpdateMap(values)
		}
		handled, err := prepareProtectedStructDestination(
			tx,
			(*CustomOAuthProvider).prepareClientSecretField,
			"client_secret",
		)
		if handled || err != nil {
			return err
		}
	}
	return provider.prepareClientSecretField()
}

// AfterSave restores the runtime object after BeforeSave sealed it. The
// controller registers this same object in the OAuth registry immediately
// after Create/Save, so leaving the envelope here would break token exchange.
func (provider *CustomOAuthProvider) AfterSave(_ *gorm.DB) error {
	if provider == nil {
		return nil
	}
	plaintext, err := openCustomOAuthSecret(provider.ClientSecret)
	if err != nil {
		return err
	}
	provider.ClientSecret = plaintext
	return nil
}

func (provider *CustomOAuthProvider) AfterFind(_ *gorm.DB) error {
	if provider == nil {
		return nil
	}
	plaintext, err := openCustomOAuthSecret(provider.ClientSecret)
	if err != nil {
		return err
	}
	provider.ClientSecret = plaintext
	return nil
}

type customOAuthSecretMigrationRow struct {
	ID           int    `gorm:"column:id"`
	ClientSecret string `gorm:"column:client_secret"`
}

// MigrateLegacyCustomOAuthSecrets seals legacy plaintext client secrets in
// bounded primary-key order. Each update compares the exact value observed so
// a concurrent administrator rotation wins instead of being overwritten.
func MigrateLegacyCustomOAuthSecrets(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable(&CustomOAuthProvider{}) ||
		!db.Migrator().HasColumn(&CustomOAuthProvider{}, "client_secret") {
		return nil
	}

	lastID := 0
	for {
		var rows []customOAuthSecretMigrationRow
		if err := db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
			Select("id, client_secret").Where("id > ?", lastID).Order("id").
			Limit(customOAuthSecretMigrationBatchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy custom OAuth client secrets: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if err := migrateCustomOAuthSecretRow(db, row.ID, row.ClientSecret); err != nil {
				return fmt.Errorf("migrate custom OAuth client secret id=%d: %w", row.ID, err)
			}
		}
		if len(rows) < customOAuthSecretMigrationBatchSize {
			return nil
		}
	}
}

func migrateCustomOAuthSecretRow(db *gorm.DB, id int, observed string) error {
	for attempt := 0; attempt < 5; attempt++ {
		if observed == "" || common.IsCredentialCiphertext(strings.TrimSpace(observed)) {
			_, err := openCustomOAuthSecret(observed)
			return err
		}
		if strings.HasPrefix(strings.TrimSpace(observed), "enc:") {
			return fmt.Errorf("%w: unsupported custom OAuth client secret envelope", ErrCredentialStorageCorrupt)
		}
		sealed, err := sealCustomOAuthSecret(observed)
		if err != nil {
			return err
		}
		result := db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
			Where("id = ? AND client_secret = ?", id, observed).Update("client_secret", sealed)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}

		var current customOAuthSecretMigrationRow
		err = db.Session(&gorm.Session{SkipHooks: true}).Table("custom_oauth_providers").
			Select("id, client_secret").Where("id = ?", id).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		observed = current.ClientSecret
	}
	return errors.New("custom OAuth client secret changed repeatedly during migration")
}
