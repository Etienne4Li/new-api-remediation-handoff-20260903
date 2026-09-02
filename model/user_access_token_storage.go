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
	userAccessTokenMigrationBatchSize = 100
	userAccessTokenCandidateLimit     = 8
)

type userAccessTokenMigrationRow struct {
	ID         int            `gorm:"column:id"`
	Legacy     sql.NullString `gorm:"column:access_token"`
	Ciphertext sql.NullString `gorm:"column:access_token_ciphertext"`
	Hash       sql.NullString `gorm:"column:access_token_hash"`
}

func userAccessTokenLegacyValue(user *User) string {
	if user == nil || user.LegacyAccessToken == nil {
		return ""
	}
	return *user.LegacyAccessToken
}

func userAccessTokenHashValue(user *User) string {
	if user == nil || user.AccessTokenHash == nil {
		return ""
	}
	return *user.AccessTokenHash
}

func openUserAccessToken(legacy, ciphertext string) (string, error) {
	if legacy == "" && strings.TrimSpace(ciphertext) == "" {
		return "", nil
	}
	if !common.CredentialEncryptionReady() {
		return "", ErrCredentialStorageUnavailable
	}
	if strings.TrimSpace(ciphertext) != "" {
		plaintext, err := common.DecryptCredential(strings.TrimSpace(ciphertext))
		if err != nil {
			if errors.Is(err, common.ErrCredentialSecretUnavailable) {
				return "", fmt.Errorf("%w: decrypt user access token: %v", ErrCredentialStorageUnavailable, err)
			}
			return "", fmt.Errorf("%w: decrypt user access token: %v", ErrCredentialStorageCorrupt, err)
		}
		if plaintext == "" {
			return "", fmt.Errorf("%w: encrypted user access token is empty", ErrCredentialStorageCorrupt)
		}
		return plaintext, nil
	}
	if isCredentialFingerprintMarker(legacy) {
		return "", fmt.Errorf("%w: user access token marker has no ciphertext", ErrCredentialStorageCorrupt)
	}
	return legacy, nil
}

func (user *User) prepareAccessTokenFields() error {
	if user == nil {
		return nil
	}
	if user.AccessToken != nil {
		plaintext := *user.AccessToken
		if plaintext == "" {
			user.LegacyAccessToken = nil
			user.AccessTokenCiphertext = ""
			user.AccessTokenHash = nil
			return nil
		}

		// Preserve the authenticated envelope for an unchanged loaded user. This
		// keeps unrelated Save operations from rotating the GCM nonce on every write.
		if strings.TrimSpace(user.AccessTokenCiphertext) != "" {
			stored, err := openUserAccessToken(userAccessTokenLegacyValue(user), user.AccessTokenCiphertext)
			if err != nil {
				return err
			}
			if accessTokenEqual(stored, plaintext) {
				hash := common.CredentialFingerprint(plaintext)
				marker := credentialFingerprintMarker(hash)
				user.LegacyAccessToken = &marker
				user.AccessTokenHash = &hash
				return nil
			}
		}

		marker, ciphertext, hash, err := sealCredential(plaintext)
		if err != nil {
			return err
		}
		user.LegacyAccessToken = &marker
		user.AccessTokenCiphertext = ciphertext
		user.AccessTokenHash = &hash
		return nil
	}

	legacy := userAccessTokenLegacyValue(user)
	ciphertext := strings.TrimSpace(user.AccessTokenCiphertext)
	hash := userAccessTokenHashValue(user)
	if legacy == "" && ciphertext == "" && hash == "" {
		return nil
	}
	plaintext, err := openUserAccessToken(legacy, ciphertext)
	if err != nil {
		return err
	}
	if plaintext == "" {
		if hash != "" {
			return fmt.Errorf("%w: user access token hash has no credential", ErrCredentialStorageCorrupt)
		}
		return nil
	}
	canonicalHash := common.CredentialFingerprint(plaintext)
	canonicalMarker := credentialFingerprintMarker(canonicalHash)
	if ciphertext == "" {
		canonicalMarker, ciphertext, canonicalHash, err = sealCredential(plaintext)
		if err != nil {
			return err
		}
		user.AccessTokenCiphertext = ciphertext
	}
	user.LegacyAccessToken = &canonicalMarker
	user.AccessTokenHash = &canonicalHash
	return nil
}

func prepareUserCredentialFields(user *User) error {
	if err := user.prepareAccessTokenFields(); err != nil {
		return err
	}
	encoded, changed, err := migrateUserSettingSecretJSON(user.Setting)
	if err != nil {
		return err
	}
	if changed {
		user.Setting = encoded
	}
	return nil
}

func userAccessTokenStructClearIsUnsupported(statement *gorm.Statement, user *User) bool {
	if statement == nil || user == nil || statement.Model == nil || statement.Dest == nil {
		return false
	}
	requested := (user.AccessToken != nil && *user.AccessToken == "") ||
		(user.LegacyAccessToken != nil && *user.LegacyAccessToken == "")
	if !requested {
		return false
	}

	return gormStatementUsesSeparateDestination(statement)
}

// BeforeSave is the shared persistence boundary for User credentials. GORM
// invokes it for Create, Save, Update and Updates. Map and struct Updates use
// Statement.Dest rather than populating the hook receiver, so normalize the
// actual destination before GORM builds the SQL assignments.
func (user *User) BeforeSave(tx *gorm.DB) error {
	if user == nil {
		return nil
	}
	if tx != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if err := normalizeUserSettingUpdateMap(values); err != nil {
				return err
			}
			return normalizeUserAccessTokenUpdateMap(values)
		}
		switch destination := tx.Statement.Dest.(type) {
		case User:
			if userAccessTokenStructClearIsUnsupported(tx.Statement, &destination) {
				return errors.New("clearing a user access token with Updates(struct) is unsupported; use UpdateUserAccessToken")
			}
		case *User:
			if userAccessTokenStructClearIsUnsupported(tx.Statement, destination) {
				return errors.New("clearing a user access token with Updates(struct) is unsupported; use UpdateUserAccessToken")
			}
		}
		handled, err := prepareProtectedStructDestination(
			tx,
			prepareUserCredentialFields,
			"access_token", "access_token_ciphertext", "access_token_hash", "setting",
		)
		if handled || err != nil {
			return err
		}
	}
	return prepareUserCredentialFields(user)
}

func (user *User) AfterFind(_ *gorm.DB) error {
	if user == nil {
		return nil
	}
	legacy := userAccessTokenLegacyValue(user)
	ciphertext := strings.TrimSpace(user.AccessTokenCiphertext)
	hash := userAccessTokenHashValue(user)
	if legacy == "" && ciphertext == "" && hash == "" {
		user.AccessToken = nil
		return nil
	}
	plaintext, err := openUserAccessToken(legacy, ciphertext)
	if err != nil {
		return err
	}
	if plaintext == "" {
		if hash != "" {
			return fmt.Errorf("%w: user access token hash has no credential", ErrCredentialStorageCorrupt)
		}
		user.AccessToken = nil
		return nil
	}
	user.AccessToken = &plaintext
	return nil
}

func normalizeUserSettingUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		key      string
		present  bool
	)
	for _, candidate := range []string{"setting", "Setting"} {
		if value, ok := values[candidate]; ok {
			if present {
				return errors.New("user setting update is ambiguous")
			}
			rawValue = value
			key = candidate
			present = true
		}
	}
	if !present || rawValue == nil {
		return nil
	}
	raw, ok := rawValue.(string)
	if !ok {
		return errors.New("user setting must be a JSON string")
	}
	encoded, changed, err := migrateUserSettingSecretJSON(raw)
	if err != nil {
		return err
	}
	if key != "setting" {
		delete(values, key)
	}
	if changed {
		values["setting"] = encoded
	} else {
		values["setting"] = raw
	}
	return nil
}

func normalizeUserAccessTokenUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		present  bool
	)
	for _, candidate := range []string{"access_token", "AccessToken", "LegacyAccessToken"} {
		if value, ok := values[candidate]; ok {
			if present {
				return errors.New("user access token update is ambiguous")
			}
			rawValue = value
			present = true
		}
		delete(values, candidate)
	}
	companionKeys := []string{
		"access_token_ciphertext", "AccessTokenCiphertext",
		"access_token_hash", "AccessTokenHash",
	}
	if !present {
		for _, key := range companionKeys {
			if _, ok := values[key]; ok {
				return fmt.Errorf("%w: direct user access token companion update", ErrCredentialStorageCorrupt)
			}
		}
		return nil
	}
	for _, key := range companionKeys {
		delete(values, key)
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
		return errors.New("user access token must be a string")
	}
	marker, ciphertext, hash, err := sealCredential(plaintext)
	if err != nil {
		return err
	}
	if marker == "" {
		values["access_token"] = nil
		values["access_token_hash"] = nil
	} else {
		values["access_token"] = marker
		values["access_token_hash"] = hash
	}
	values["access_token_ciphertext"] = ciphertext
	return nil
}

func accessTokenEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func queryUserAccessTokenCandidates(query *gorm.DB, token string) (*User, error) {
	var candidates []User
	if err := query.Limit(userAccessTokenCandidateLimit).Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDatabase, err)
	}
	for i := range candidates {
		if accessTokenEqual(candidates[i].GetAccessToken(), token) {
			return &candidates[i], nil
		}
	}
	return nil, nil
}

func lookupUserByAccessToken(token string) (*User, error) {
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	if !common.CredentialEncryptionReady() {
		return nil, ErrCredentialStorageUnavailable
	}
	hash := common.CredentialFingerprint(token)
	user, err := queryUserAccessTokenCandidates(DB.Where("access_token_hash = ?", hash), token)
	if err != nil || user != nil {
		return user, err
	}
	// Rolling deployments may still have old plaintext rows until the startup
	// expand pass reaches them. The indexed historical column remains unique, so
	// this compatibility query is bounded and disappears after migration.
	return queryUserAccessTokenCandidates(DB.Where("access_token = ?", token), token)
}

// AccessTokenExists is the hash-aware collision check used by PAT generation.
// Authentication still relies on the database uniqueness fence because a
// preflight check alone cannot prevent a concurrent duplicate update.
func AccessTokenExists(token string) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, nil
	}
	user, err := lookupUserByAccessToken(token)
	return user != nil, err
}

// MigrateLegacyUserAccessTokens converts users.access_token plaintext into a
// fingerprint marker plus authenticated ciphertext. It is safe to run on every
// startup and uses primary-key pagination with an exact-value CAS per row.
func MigrateLegacyUserAccessTokens(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable(&User{}) {
		return nil
	}
	for _, column := range []string{"access_token", "access_token_ciphertext", "access_token_hash"} {
		if !db.Migrator().HasColumn(&User{}, column) {
			return fmt.Errorf("users.%s is required for encrypted access-token storage", column)
		}
	}

	lastID := 0
	for {
		var rows []userAccessTokenMigrationRow
		if err := db.Session(&gorm.Session{SkipHooks: true}).Table("users").
			Select("id, access_token, access_token_ciphertext, access_token_hash").
			Where("id > ?", lastID).Order("id").Limit(userAccessTokenMigrationBatchSize).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy user access tokens: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if err := migrateUserAccessTokenRow(db, row); err != nil {
				return fmt.Errorf("migrate user access token id=%d: %w", row.ID, err)
			}
		}
		if len(rows) < userAccessTokenMigrationBatchSize {
			return nil
		}
	}
}

func migrateUserAccessTokenRow(db *gorm.DB, observed userAccessTokenMigrationRow) error {
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
			storedHash = observed.Hash.String
		}
		if legacy == "" && ciphertext == "" {
			if storedHash != "" {
				return fmt.Errorf("%w: access token hash has no credential", ErrCredentialStorageCorrupt)
			}
			if !observed.Legacy.Valid && !observed.Hash.Valid &&
				(!observed.Ciphertext.Valid || observed.Ciphertext.String == "") {
				return nil
			}
			query := db.Session(&gorm.Session{SkipHooks: true}).Table("users").Where("id = ?", observed.ID)
			query = whereObservedNullableString(query, "access_token", observed.Legacy)
			query = whereObservedNullableString(query, "access_token_ciphertext", observed.Ciphertext)
			query = whereObservedNullableString(query, "access_token_hash", observed.Hash)
			result := query.Updates(map[string]interface{}{
				"access_token":            nil,
				"access_token_ciphertext": "",
				"access_token_hash":       nil,
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				return nil
			}
			current, err := readUserAccessTokenMigrationRow(db, observed.ID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			observed = current
			continue
		}

		plaintext, err := openUserAccessToken(legacy, ciphertext)
		if err != nil {
			return err
		}
		if plaintext == "" {
			return fmt.Errorf("%w: empty user access token representation", ErrCredentialStorageCorrupt)
		}
		marker := credentialFingerprintMarker(common.CredentialFingerprint(plaintext))
		hash := common.CredentialFingerprint(plaintext)
		sealed := ciphertext
		if sealed == "" {
			marker, sealed, hash, err = sealCredential(plaintext)
			if err != nil {
				return err
			}
		}
		if observed.Legacy.Valid && observed.Legacy.String == marker &&
			observed.Ciphertext.Valid && observed.Ciphertext.String == sealed &&
			observed.Hash.Valid && observed.Hash.String == hash {
			return nil
		}

		query := db.Session(&gorm.Session{SkipHooks: true}).Table("users").Where("id = ?", observed.ID)
		query = whereObservedNullableString(query, "access_token", observed.Legacy)
		query = whereObservedNullableString(query, "access_token_ciphertext", observed.Ciphertext)
		query = whereObservedNullableString(query, "access_token_hash", observed.Hash)
		result := query.Updates(map[string]interface{}{
			"access_token":            marker,
			"access_token_ciphertext": sealed,
			"access_token_hash":       hash,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}

		current, err := readUserAccessTokenMigrationRow(db, observed.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		observed = current
	}
	return errors.New("user access token changed repeatedly during migration")
}

func readUserAccessTokenMigrationRow(db *gorm.DB, id int) (userAccessTokenMigrationRow, error) {
	var row userAccessTokenMigrationRow
	err := db.Session(&gorm.Session{SkipHooks: true}).Table("users").
		Select("id, access_token, access_token_ciphertext, access_token_hash").
		Where("id = ?", id).Take(&row).Error
	return row, err
}

func whereObservedNullableString(query *gorm.DB, column string, observed sql.NullString) *gorm.DB {
	if observed.Valid {
		return query.Where(column+" = ?", observed.String)
	}
	return query.Where(column + " IS NULL")
}
