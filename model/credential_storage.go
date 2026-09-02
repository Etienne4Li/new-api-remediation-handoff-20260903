package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	credentialFingerprintPrefix  = "fp:v1:"
	credentialMigrationBatchSize = 100
)

var (
	ErrCredentialStorageCorrupt     = errors.New("credential storage is corrupt")
	ErrCredentialStorageUnavailable = errors.New("credential storage encryption is unavailable")
)

func credentialFingerprintMarker(hash string) string {
	if strings.TrimSpace(hash) == "" {
		return ""
	}
	return credentialFingerprintPrefix + strings.TrimSpace(hash)
}

func isCredentialFingerprintMarker(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), credentialFingerprintPrefix)
}

// sealCredential prepares the durable fields for a runtime plaintext value.
// The plaintext is deliberately never copied into a persisted field.  Empty
// values clear all three durable representations so an explicit credential
// removal cannot leave an old secret decryptable.
func sealCredential(plaintext string) (marker, ciphertext, hash string, err error) {
	if plaintext == "" {
		return "", "", "", nil
	}
	if !common.CredentialEncryptionReady() {
		return "", "", "", ErrCredentialStorageUnavailable
	}
	ciphertext, err = common.EncryptCredential(plaintext)
	if err != nil {
		return "", "", "", fmt.Errorf("encrypt credential: %w", err)
	}
	hash = common.CredentialFingerprint(plaintext)
	marker = credentialFingerprintMarker(hash)
	return marker, ciphertext, hash, nil
}

// openCredential resolves a durable row.  A ciphertext is authoritative.  A
// non-marker value in the historical key column is treated as legacy plaintext
// during the one-way startup migration; callers can then normalize it with
// migrateCredentialRow.
// A marker without ciphertext is corruption and fails closed rather than
// accidentally sending the marker upstream.
func openCredential(legacy, ciphertext string) (string, error) {
	legacy = strings.TrimSpace(legacy)
	if strings.TrimSpace(ciphertext) != "" {
		plaintext, err := common.DecryptCredential(strings.TrimSpace(ciphertext))
		if err != nil {
			return "", fmt.Errorf("%w: decrypt credential: %v", ErrCredentialStorageCorrupt, err)
		}
		return plaintext, nil
	}
	if isCredentialFingerprintMarker(legacy) {
		return "", fmt.Errorf("%w: fingerprint marker has no ciphertext", ErrCredentialStorageCorrupt)
	}
	return legacy, nil
}

func canonicalCredentialTuple(runtime, legacy, ciphertext, storedHash string) (string, string, string, error) {
	ciphertext = strings.TrimSpace(ciphertext)
	storedHash = strings.TrimSpace(storedHash)
	if runtime != "" {
		if ciphertext != "" {
			stored, err := openCredential(legacy, ciphertext)
			if err != nil {
				return "", "", "", err
			}
			if stored == runtime {
				hash := common.CredentialFingerprint(runtime)
				return credentialFingerprintMarker(hash), ciphertext, hash, nil
			}
		}
		return sealCredential(runtime)
	}

	legacyValue := strings.TrimSpace(legacy)
	if legacyValue == "" && ciphertext == "" && storedHash == "" {
		return "", "", "", nil
	}
	plaintext, err := openCredential(legacyValue, ciphertext)
	if err != nil {
		return "", "", "", err
	}
	if plaintext == "" {
		if storedHash != "" {
			return "", "", "", fmt.Errorf("%w: credential hash has no credential", ErrCredentialStorageCorrupt)
		}
		return "", "", "", nil
	}
	hash := common.CredentialFingerprint(plaintext)
	marker := credentialFingerprintMarker(hash)
	if ciphertext == "" {
		return sealCredential(plaintext)
	}
	return marker, ciphertext, hash, nil
}

func tokenRuntimeKey(token *Token) (string, error) {
	if token == nil {
		return "", nil
	}
	legacy := ""
	if token.LegacyKey != nil {
		legacy = *token.LegacyKey
	}
	return openCredential(legacy, token.KeyCiphertext)
}

func channelRuntimeKey(channel *Channel) (string, error) {
	if channel == nil {
		return "", nil
	}
	return openCredential(channel.LegacyKey, channel.KeyCiphertext)
}

func (token *Token) prepareCredentialFields() error {
	if token == nil {
		return nil
	}
	legacy := ""
	if token.LegacyKey != nil {
		legacy = *token.LegacyKey
	}
	storedHash := ""
	if token.KeyHash != nil {
		storedHash = *token.KeyHash
	}
	marker, ciphertext, hash, err := canonicalCredentialTuple(token.Key, legacy, token.KeyCiphertext, storedHash)
	if err != nil {
		return err
	}
	if marker == "" {
		token.LegacyKey = nil
		token.KeyCiphertext = ""
		token.KeyHash = nil
		return nil
	}
	token.KeyCiphertext = ciphertext
	token.KeyHash = &hash
	token.LegacyKey = &marker
	return nil
}

func (channel *Channel) prepareCredentialFields() error {
	if channel == nil {
		return nil
	}
	marker, ciphertext, hash, err := canonicalCredentialTuple(
		channel.Key,
		channel.LegacyKey,
		channel.KeyCiphertext,
		channel.KeyHash,
	)
	if err != nil {
		return err
	}
	channel.KeyCiphertext = ciphertext
	channel.KeyHash = hash
	channel.LegacyKey = marker
	return nil
}

func (channel *Channel) prepareCredentialAndConfigFields() error {
	if err := ValidateChannelBaseURL(channel.BaseURL); err != nil {
		return err
	}
	if err := channel.prepareCredentialFields(); err != nil {
		return err
	}
	return channel.prepareConfigSecretFields()
}

// BeforeSave protects direct GORM Create/Save calls in addition to model-level
// Insert/Update wrappers.  Map-based key updates are normalized as well, so a
// legacy controller/plugin cannot accidentally reintroduce plaintext storage.
func (token *Token) BeforeSave(tx *gorm.DB) error {
	if token == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizeCredentialUpdateMap(values, true)
		}
		switch destination := tx.Statement.Dest.(type) {
		case Token:
			if destination.LegacyKey != nil && *destination.LegacyKey == "" && gormStatementUsesSeparateDestination(tx.Statement) {
				return errors.New("clearing a token key with Updates(struct) is unsupported; use an update map")
			}
		case *Token:
			if destination != nil && destination.LegacyKey != nil && *destination.LegacyKey == "" && gormStatementUsesSeparateDestination(tx.Statement) {
				return errors.New("clearing a token key with Updates(struct) is unsupported; use an update map")
			}
		}
		handled, err := prepareProtectedStructDestination(
			tx,
			(*Token).prepareCredentialFields,
			"key", "key_ciphertext", "key_hash",
		)
		if handled || err != nil {
			return err
		}
	}
	return token.prepareCredentialFields()
}

func (channel *Channel) BeforeSave(tx *gorm.DB) error {
	if channel == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if err := normalizeChannelBaseURLUpdateMap(values); err != nil {
				return err
			}
			if err := normalizeChannelConfigSecretUpdateMap(values); err != nil {
				return err
			}
			if err := normalizeCredentialUpdateMap(values, false); err != nil {
				return err
			}
			return nil
		}
		handled, err := prepareProtectedStructDestination(
			tx,
			(*Channel).prepareCredentialAndConfigFields,
			"key", "key_ciphertext", "key_hash", "base_url", "setting", "param_override", "header_override", "settings",
		)
		if handled || err != nil {
			return err
		}
	}
	return channel.prepareCredentialAndConfigFields()
}

func normalizeChannelBaseURLUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		present  bool
	)
	for _, name := range []string{"base_url", "BaseURL"} {
		if value, ok := values[name]; ok {
			if present {
				return errors.New("channel base URL update is ambiguous")
			}
			rawValue = value
			present = true
		}
		delete(values, name)
	}
	if !present {
		return nil
	}
	if rawValue == nil {
		values["base_url"] = nil
		return nil
	}
	var baseURL *string
	switch value := rawValue.(type) {
	case string:
		baseURL = &value
	case *string:
		baseURL = value
	default:
		return errors.New("channel base URL must be a string")
	}
	if err := ValidateChannelBaseURL(baseURL); err != nil {
		return err
	}
	values["base_url"] = rawValue
	return nil
}

func (token *Token) AfterFind(_ *gorm.DB) error {
	key, err := tokenRuntimeKey(token)
	if err != nil {
		return err
	}
	token.Key = key
	return nil
}

func (channel *Channel) AfterFind(_ *gorm.DB) error {
	key, err := channelRuntimeKey(channel)
	if err != nil {
		return err
	}
	channel.Key = key
	return channel.openConfigSecretFields()
}

// normalizeCredentialUpdateMap catches direct Updates(map[string]any) calls.
// GORM invokes model hooks for Model(&Token/Channel{}).Updates maps; preserving
// the original map shape lets existing callers continue to work while moving
// the value into the encrypted/hash columns atomically.
func normalizeCredentialUpdateMap(values map[string]interface{}, token bool) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		present  bool
	)
	for _, name := range []string{"key", "Key", "LegacyKey"} {
		if value, ok := values[name]; ok {
			if present {
				return errors.New("credential key update is ambiguous")
			}
			rawValue = value
			present = true
		}
		delete(values, name)
	}
	companionPresent := false
	for _, name := range []string{"key_ciphertext", "KeyCiphertext", "key_hash", "KeyHash"} {
		if _, ok := values[name]; ok {
			companionPresent = true
		}
		delete(values, name)
	}
	if !present {
		if companionPresent {
			return fmt.Errorf("%w: direct credential companion update", ErrCredentialStorageCorrupt)
		}
		return nil
	}
	raw := ""
	switch value := rawValue.(type) {
	case nil:
	case string:
		raw = value
	case *string:
		if value != nil {
			raw = *value
		}
	default:
		return fmt.Errorf("credential key must be a string")
	}
	marker, ciphertext, hash, err := sealCredential(raw)
	if err != nil {
		return err
	}
	if token {
		if marker == "" {
			values["key"] = nil
			values["key_hash"] = nil
		} else {
			values["key"] = marker
			hashCopy := hash
			values["key_hash"] = &hashCopy
		}
		values["key_ciphertext"] = ciphertext
		return nil
	}
	values["key"] = marker
	values["key_hash"] = hash
	values["key_ciphertext"] = ciphertext
	return nil
}

// migrateCredentialRow atomically normalises one credential row.  Besides
// historical plaintext rows, it repairs partially-expanded rows (for example
// a process that wrote ciphertext but crashed before writing the marker/hash).
// It uses SkipHooks because the durable values are already prepared and must
// not be interpreted as runtime plaintext by BeforeSave.
func migrateCredentialRow(db *gorm.DB, table string, id int, legacy, ciphertext, storedHash string) error {
	if db == nil || id <= 0 {
		return nil
	}
	legacyValue := legacy
	ciphertextValue := strings.TrimSpace(ciphertext)
	legacyTrimmed := strings.TrimSpace(legacy)
	if legacyTrimmed == "" && ciphertextValue == "" {
		return nil
	}

	var (
		plaintext string
		sealed    string
	)
	switch {
	case ciphertextValue != "":
		// A ciphertext is authoritative.  Decrypt it before changing any
		// companion fields; never treat a marker as the upstream credential.
		var err error
		plaintext, err = common.DecryptCredential(ciphertextValue)
		if err != nil {
			return fmt.Errorf("%w: decrypt %s id=%d: %v", ErrCredentialStorageCorrupt, table, id, err)
		}
		sealed = ciphertextValue
	case isCredentialFingerprintMarker(legacyTrimmed):
		// A marker without an authenticated envelope cannot be recovered.  Fail
		// closed so startup does not silently route a fingerprint upstream.
		return fmt.Errorf("%w: %s id=%d has fingerprint marker without ciphertext", ErrCredentialStorageCorrupt, table, id)
	default:
		plaintext = legacyValue
	}

	if plaintext == "" {
		// Empty credentials are represented by empty durable fields.  A valid
		// encrypted envelope should never decrypt to an empty value because
		// EncryptCredential short-circuits empty input; clear a malformed
		// partially-expanded row rather than leaving an unusable marker.
		if ciphertextValue != "" {
			updates := map[string]interface{}{"key": nil, "key_ciphertext": "", "key_hash": nil}
			result := db.Session(&gorm.Session{SkipHooks: true}).Table(table).
				Where("id = ? AND key = ? AND key_ciphertext = ?", id, legacyValue, ciphertextValue).
				Updates(updates)
			return result.Error
		}
		return nil
	}

	marker := credentialFingerprintMarker(common.CredentialFingerprint(plaintext))
	hash := common.CredentialFingerprint(plaintext)
	if sealed == "" {
		var err error
		marker, sealed, hash, err = sealCredential(plaintext)
		if err != nil {
			return err
		}
	}
	// If the row already has the canonical marker, ciphertext, and hash, avoid
	// issuing a write (and avoid needlessly rotating the nonce on every boot).
	if strings.TrimSpace(legacy) == marker && strings.TrimSpace(storedHash) == hash && ciphertextValue == sealed {
		return nil
	}

	updates := map[string]interface{}{
		"key":            marker,
		"key_ciphertext": sealed,
		"key_hash":       hash,
	}
	// The optimistic predicate prevents a stale reader from overwriting a
	// concurrently rotated credential.  Quoted table names are unnecessary
	// here: table is a package constant selected by the caller.  Include the
	// observed ciphertext in the predicate when present so a partial update is
	// never clobbered by a later migration pass.
	where := "id = ? AND key = ?"
	args := []interface{}{id, legacyValue}
	if ciphertextValue == "" {
		where += " AND (key_ciphertext IS NULL OR key_ciphertext = '')"
	} else {
		where += " AND key_ciphertext = ?"
		args = append(args, ciphertextValue)
	}
	result := db.Session(&gorm.Session{SkipHooks: true}).Table(table).
		Where(where, args...).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	return nil
}

// MigrateLegacyCredentials performs an idempotent, keyset-paginated expand
// migration. Existing plaintext rows remain readable until the migration
// reaches them; once this returns, each row seen in the database stores a
// fingerprint marker plus ciphertext.  It is exported for startup tooling and
// deterministic migration tests.
func MigrateLegacyCredentials(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if err := migrateLegacyCredentialTable(db, "tokens", true); err != nil {
		return err
	}
	return migrateLegacyCredentialTable(db, "channels", false)
}

func migrateLegacyCredentialTable(db *gorm.DB, table string, token bool) error {
	if !db.Migrator().HasTable(table) || !db.Migrator().HasColumn(table, "key") || !db.Migrator().HasColumn(table, "key_ciphertext") {
		return nil
	}
	lastID := 0
	for {
		query := db.Session(&gorm.Session{SkipHooks: true}).Table(table).
			Select("id, key, key_ciphertext, key_hash")
		var rows []credentialMigrationRow
		if err := query.Where("id > ?", lastID).Order("id").Limit(credentialMigrationBatchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy %s credentials: %w", table, err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if err := migrateCredentialRow(db, table, row.ID, row.LegacyKey, row.Ciphertext, row.Hash); err != nil {
				return fmt.Errorf("migrate %s credential id=%d: %w", table, row.ID, err)
			}
		}
		if len(rows) < credentialMigrationBatchSize {
			return nil
		}
		_ = token // retained for call-site clarity and future table-specific policy
	}
}

type credentialMigrationRow struct {
	ID         int    `gorm:"column:id"`
	LegacyKey  string `gorm:"column:key"`
	Ciphertext string `gorm:"column:key_ciphertext"`
	Hash       string `gorm:"column:key_hash"`
}

// UpdateChannelKey is the single write primitive for credential rotation paths
// that historically issued a raw Update("key", ...).  It keeps the key marker,
// encrypted envelope, and fingerprint in one DB statement and is safe for all
// supported GORM dialects.
func UpdateChannelKey(channelID int, plaintext string) error {
	if channelID <= 0 {
		return errors.New("channel id is invalid")
	}
	marker, ciphertext, hash, err := sealCredential(plaintext)
	if err != nil {
		return err
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	// The values below are already sealed.  Skip model hooks so BeforeSave does
	// not mistake the `fp:v1:` marker for a new plaintext key and encrypt the
	// marker a second time.
	result := DB.Session(&gorm.Session{SkipHooks: true}).Model(&Channel{}).Where("id = ?", channelID).Updates(map[string]interface{}{
		"key":            marker,
		"key_ciphertext": ciphertext,
		"key_hash":       hash,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
