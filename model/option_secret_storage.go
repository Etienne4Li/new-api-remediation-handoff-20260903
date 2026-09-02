package model

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const optionSecretMigrationBatchSize = 100

var (
	ErrOptionSecretStorageCorrupt     = errors.New("option secret storage is corrupt")
	ErrOptionSecretStorageUnavailable = errors.New("option secret storage encryption is unavailable")
)

var sensitiveLegacyOptionKeys = map[string]struct{}{
	"CreemApiKey":            {},
	"CreemWebhookSecret":     {},
	"DiscordClientSecret":    {},
	"EpayKey":                {},
	"GitHubClientSecret":     {},
	"LinuxDOClientSecret":    {},
	"OIDCClientSecret":       {},
	"SMTPToken":              {},
	"StripeApiSecret":        {},
	"StripeWebhookSecret":    {},
	"TelegramBotToken":       {},
	"TurnstileSecretKey":     {},
	"WaffoApiKey":            {},
	"WaffoPancakePrivateKey": {},
	"WaffoPrivateKey":        {},
	"WaffoSandboxApiKey":     {},
	"WaffoSandboxPrivateKey": {},
	"WeChatServerToken":      {},
	"WorkerValidKey":         {},
}

var sensitiveNamespacedOptionFields = map[string]struct{}{
	"accesskey":     {},
	"accesstoken":   {},
	"apikey":        {},
	"apisecret":     {},
	"botsecret":     {},
	"bottoken":      {},
	"clientsecret":  {},
	"credential":    {},
	"password":      {},
	"privatekey":    {},
	"refreshtoken":  {},
	"secret":        {},
	"secretkey":     {},
	"signingkey":    {},
	"signingsecret": {},
	"token":         {},
	"validkey":      {},
	"webhooksecret": {},
}

// IsSensitiveOptionKey is the persistence and response-filtering policy for
// secrets stored in options. Generic suffix checks are intentionally avoided:
// public values such as TurnstileSiteKey and WaffoPublicCert must not be
// confused with credentials, while snake_case private_key fields must be.
func IsSensitiveOptionKey(key string) bool {
	key = strings.TrimSpace(key)
	if _, ok := sensitiveLegacyOptionKeys[key]; ok {
		return true
	}
	separator := strings.LastIndexByte(key, '.')
	if separator < 0 || separator == len(key)-1 {
		return false
	}
	field := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key[separator+1:])
	_, ok := sensitiveNamespacedOptionFields[field]
	return ok
}

func optionValueLooksEncrypted(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "enc:")
}

func openOptionSecretValue(key, value string) (string, error) {
	if !IsSensitiveOptionKey(key) || value == "" {
		return value, nil
	}
	trimmed := strings.TrimSpace(value)
	if !common.IsCredentialCiphertext(trimmed) {
		if optionValueLooksEncrypted(trimmed) {
			return "", fmt.Errorf("%w: unsupported envelope for %s", ErrOptionSecretStorageCorrupt, key)
		}
		// Rolling upgrades must continue to read legacy plaintext until the
		// startup migration has converted the row.
		return value, nil
	}
	plaintext, err := common.DecryptCredential(trimmed)
	if err == nil {
		return plaintext, nil
	}
	if errors.Is(err, common.ErrCredentialSecretUnavailable) {
		return "", fmt.Errorf("%w: decrypt %s: %v", ErrOptionSecretStorageUnavailable, key, err)
	}
	return "", fmt.Errorf("%w: decrypt %s: %v", ErrOptionSecretStorageCorrupt, key, err)
}

func sealOptionSecretValue(key, value string) (string, error) {
	if !IsSensitiveOptionKey(key) || value == "" {
		return value, nil
	}
	trimmed := strings.TrimSpace(value)
	if common.IsCredentialCiphertext(trimmed) {
		if _, err := openOptionSecretValue(key, trimmed); err != nil {
			return "", err
		}
		return trimmed, nil
	}
	if optionValueLooksEncrypted(trimmed) {
		return "", fmt.Errorf("%w: unsupported envelope for %s", ErrOptionSecretStorageCorrupt, key)
	}
	if !common.CredentialEncryptionReady() {
		return "", fmt.Errorf("%w: %s", ErrOptionSecretStorageUnavailable, key)
	}
	sealed, err := common.EncryptCredential(value)
	if err != nil {
		return "", fmt.Errorf("%w: encrypt %s: %v", ErrOptionSecretStorageUnavailable, key, err)
	}
	return sealed, nil
}

func (option *Option) prepareSecretValue() error {
	if option == nil {
		return nil
	}
	value, err := sealOptionSecretValue(option.Key, option.Value)
	if err != nil {
		return err
	}
	option.Value = value
	return nil
}

func optionModelKey(statement *gorm.Statement) string {
	if statement == nil {
		return ""
	}
	switch model := statement.Model.(type) {
	case Option:
		return model.Key
	case *Option:
		if model != nil {
			return model.Key
		}
	}
	return ""
}

func normalizeOptionSecretUpdateMap(option *Option, values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	key := ""
	keyPresent := false
	for _, alias := range []string{"key", "Key"} {
		if value, ok := values[alias]; ok {
			if keyPresent {
				return errors.New("option key update is ambiguous")
			}
			parsed, ok := value.(string)
			if !ok {
				return errors.New("option key must be a string")
			}
			key = parsed
			keyPresent = true
		}
		delete(values, alias)
	}
	if !keyPresent && option != nil {
		key = option.Key
	}

	var (
		rawValue interface{}
		valueSet bool
	)
	for _, alias := range []string{"value", "Value"} {
		if value, ok := values[alias]; ok {
			if valueSet {
				return errors.New("option value update is ambiguous")
			}
			rawValue = value
			valueSet = true
		}
		delete(values, alias)
	}
	if keyPresent {
		values["key"] = key
	}
	if !valueSet {
		return nil
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("option value update requires a concrete option key")
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
		return errors.New("option value must be a string")
	}
	sealed, err := sealOptionSecretValue(key, plaintext)
	if err != nil {
		return err
	}
	values["value"] = sealed
	return nil
}

// BeforeSave protects both struct and map writes. A map value update must use
// Model(&Option{Key: ...}) (or include the key in the map) so the hook can
// decide whether the target value is a credential without parsing SQL WHERE.
func (option *Option) BeforeSave(tx *gorm.DB) error {
	if option == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizeOptionSecretUpdateMap(option, values)
		}
		prepare := func(destination *Option) error {
			if destination == nil {
				return nil
			}
			if destination.Key == "" && gormStatementUsesSeparateDestination(tx.Statement) {
				key := optionModelKey(tx.Statement)
				if key == "" && destination.Value != "" {
					return errors.New("option value update requires a concrete option key")
				}
				value, err := sealOptionSecretValue(key, destination.Value)
				if err != nil {
					return err
				}
				destination.Value = value
				return nil
			}
			return destination.prepareSecretValue()
		}
		handled, err := prepareProtectedStructDestination(tx, prepare, "value")
		if handled || err != nil {
			return err
		}
	}
	return option.prepareSecretValue()
}

func (option *Option) AfterSave(_ *gorm.DB) error {
	if option == nil {
		return nil
	}
	value, err := openOptionSecretValue(option.Key, option.Value)
	if err != nil {
		return err
	}
	option.Value = value
	return nil
}

// AfterFind exposes plaintext only to the runtime model. Corrupt or
// authenticated-with-another-key envelopes fail closed before publication.
func (option *Option) AfterFind(_ *gorm.DB) error {
	if option == nil {
		return nil
	}
	value, err := openOptionSecretValue(option.Key, option.Value)
	if err != nil {
		return err
	}
	option.Value = value
	return nil
}

type optionSecretMigrationRow struct {
	Key   string `gorm:"column:key"`
	Value string `gorm:"column:value"`
}

// MigrateLegacyOptionSecrets converts sensitive options in key order. Each
// write compares the exact observed value so concurrent admin rotation wins;
// a changed row is re-read and normalised rather than overwritten.
func MigrateLegacyOptionSecrets(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable(&Option{}) || !db.Migrator().HasColumn(&Option{}, "value") {
		return nil
	}

	lastKey := ""
	for {
		query := db.Session(&gorm.Session{SkipHooks: true}).Model(&Option{}).
			Select([]string{"key", "value"}).
			Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).
			Limit(optionSecretMigrationBatchSize)
		if lastKey != "" {
			query = query.Where(clause.Gt{Column: clause.Column{Name: "key"}, Value: lastKey})
		}
		var rows []optionSecretMigrationRow
		if err := query.Find(&rows).Error; err != nil {
			return fmt.Errorf("read option secrets: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := migrateOptionSecretRow(db, row.Key, row.Value); err != nil {
				return fmt.Errorf("migrate option secret %s: %w", row.Key, err)
			}
		}
		lastKey = rows[len(rows)-1].Key
		if len(rows) < optionSecretMigrationBatchSize {
			return nil
		}
	}
}

func migrateOptionSecretRow(db *gorm.DB, key, observed string) error {
	if !IsSensitiveOptionKey(key) {
		return nil
	}
	for attempt := 0; attempt < 5; attempt++ {
		if observed == "" || common.IsCredentialCiphertext(strings.TrimSpace(observed)) {
			_, err := openOptionSecretValue(key, observed)
			return err
		}
		if optionValueLooksEncrypted(observed) {
			return fmt.Errorf("%w: unsupported envelope", ErrOptionSecretStorageCorrupt)
		}
		sealed, err := sealOptionSecretValue(key, observed)
		if err != nil {
			return err
		}
		result := db.Session(&gorm.Session{SkipHooks: true}).Model(&Option{}).
			Where(&Option{Key: key, Value: observed}).
			UpdateColumn("value", sealed)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}

		var current optionSecretMigrationRow
		err = db.Session(&gorm.Session{SkipHooks: true}).Model(&Option{}).
			Select([]string{"key", "value"}).
			Where(&Option{Key: key}).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		observed = current.Value
	}
	return errors.New("option changed repeatedly during secret migration")
}
