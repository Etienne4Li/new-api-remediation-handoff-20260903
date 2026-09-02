package model

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type taskPrivateDataColumnUpdate struct {
	PrivateData string `gorm:"column:private_data"`
}

func TestTaskPrivateDataKeyIsEncryptedAtDatabaseBoundary(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	common.CryptoSecret = "task-credential-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	DB.Exec("DELETE FROM tasks")

	secret := "vertex-service-account-secret"
	task := &Task{
		TaskID:   "task-private-key-encrypted",
		Platform: constant.TaskPlatform("video"),
		PrivateData: TaskPrivateData{
			Key: secret,
		},
	}
	require.NoError(t, DB.Create(task).Error)
	var raw sql.NullString
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&raw).Error)
	assert.True(t, raw.Valid)
	assert.NotContains(t, raw.String, secret)
	assert.Contains(t, raw.String, common.CredentialCiphertextPrefix)

	var loaded Task
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	assert.Equal(t, secret, loaded.PrivateData.Key)
}

func TestTaskPrivateDataKeyRemainsEncryptedAfterMapUpdate(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	common.CryptoSecret = "task-credential-map-update-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	DB.Exec("DELETE FROM tasks")

	secret := "provider-key-preserved-across-poll"
	task := &Task{
		TaskID:   "task-private-key-map-update",
		Platform: constant.TaskPlatform("video"),
		PrivateData: TaskPrivateData{
			Key:            secret,
			UpstreamTaskID: "provider-task-id",
			ResultURL:      "https://example.invalid/old",
		},
	}
	require.NoError(t, DB.Create(task).Error)

	var loaded Task
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	loaded.PrivateData.ResultURL = "https://example.invalid/new"
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("private_data", loaded.PrivateData).Error)

	var raw sql.NullString
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&raw).Error)
	assert.True(t, raw.Valid)
	assert.NotContains(t, raw.String, secret)
	assert.Equal(t, 2, strings.Count(raw.String, common.CredentialCiphertextPrefix))

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, secret, reloaded.PrivateData.Key)
	assert.Equal(t, "provider-task-id", reloaded.PrivateData.UpstreamTaskID)
	assert.Equal(t, "https://example.invalid/new", reloaded.PrivateData.ResultURL)
}

func TestTaskPrivateDataRawJSONMapUpdateIsEncrypted(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	common.CryptoSecret = "task-credential-raw-map-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	DB.Exec("DELETE FROM tasks")

	task := &Task{TaskID: "task-private-raw-map", Platform: constant.TaskPlatform("video")}
	require.NoError(t, DB.Create(task).Error)
	const secret = "raw-json-provider-secret"
	rawJSON := `{"key":"` + secret + `","future":{"keep":true}}`
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("private_data", rawJSON).Error)

	var raw sql.NullString
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&raw).Error)
	assert.NotContains(t, raw.String, secret)
	assert.Contains(t, raw.String, common.CredentialCiphertextPrefix)
	assert.Contains(t, raw.String, `"future"`)

	var loaded Task
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	assert.Equal(t, secret, loaded.PrivateData.Key)

	const upperSecret = "uppercase-raw-json-provider-secret"
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("private_data", `{"KEY":"`+upperSecret+`","future":{"keep":true}}`).Error)
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&raw).Error)
	assert.NotContains(t, raw.String, upperSecret)
	assert.Contains(t, raw.String, common.CredentialCiphertextPrefix)
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	assert.Equal(t, upperSecret, loaded.PrivateData.Key)

	const upperResultURL = "https://cdn.example/uppercase.mp4?signature=uppercase-result-secret"
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("private_data", `{"RESULT_URL":"`+upperResultURL+`"}`).Error)
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&raw).Error)
	assert.NotContains(t, raw.String, "uppercase-result-secret")
	assert.Contains(t, raw.String, common.CredentialCiphertextPrefix)
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	assert.Equal(t, upperResultURL, loaded.PrivateData.ResultURL)

	beforeAmbiguousUpdate := raw.String
	err := DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("private_data", `{"key":"","Key":"ambiguous-provider-secret"}`).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&raw).Error)
	assert.Equal(t, beforeAmbiguousUpdate, raw.String)

	err = DB.Model(&Task{}).Where("id = ?", task.ID).Updates(taskPrivateDataColumnUpdate{
		PrivateData: `{"key":"custom-struct-plaintext"}`,
	}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected column private_data")
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	assert.Empty(t, loaded.PrivateData.Key)
	assert.Equal(t, upperResultURL, loaded.PrivateData.ResultURL)
}

func TestMigrateLegacyTaskPrivateDataKeyPreservesOtherFields(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	common.CryptoSecret = "task-credential-migration-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	DB.Exec("DELETE FROM tasks")

	legacyKey := "legacy-task-provider-key"
	legacyJSON := `{"key":"` + legacyKey + `","upstream_task_id":"provider-id","future_field":{"keep":true}}`
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").Create(map[string]interface{}{
		"task_id":      "legacy-task-private-key",
		"platform":     constant.TaskPlatform("video"),
		"channel_id":   901,
		"private_data": legacyJSON,
	}).Error)
	// Read the generated id by task id.
	var row struct {
		ID int64 `gorm:"column:id"`
	}
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("id").Where("task_id = ?", "legacy-task-private-key").Scan(&row).Error)
	require.NotZero(t, row.ID)

	require.NoError(t, MigrateLegacyTaskCredentials(DB))
	var raw sql.NullString
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", row.ID).Scan(&raw).Error)
	assert.NotContains(t, raw.String, legacyKey)
	assert.Contains(t, raw.String, common.CredentialCiphertextPrefix)
	assert.Contains(t, raw.String, "future_field")
	var loaded Task
	require.NoError(t, DB.First(&loaded, row.ID).Error)
	assert.Equal(t, legacyKey, loaded.PrivateData.Key)
	assert.Equal(t, "provider-id", loaded.PrivateData.UpstreamTaskID)

	// The migration is idempotent and must not rotate the ciphertext nonce on a
	// second startup pass.
	first := raw.String
	require.NoError(t, MigrateLegacyTaskCredentials(DB))
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", row.ID).Scan(&raw).Error)
	assert.Equal(t, first, raw.String)
	assert.False(t, strings.Contains(raw.String, legacyKey))
}

func TestMigrateLegacyTaskPrivateDataEncryptsCaseInsensitiveKey(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	common.CryptoSecret = "task-uppercase-credential-migration-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	DB.Exec("DELETE FROM tasks")

	const legacyKey = "uppercase-legacy-task-provider-key"
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").Create(map[string]interface{}{
		"task_id":      "legacy-uppercase-task-private-key",
		"platform":     constant.TaskPlatform("video"),
		"channel_id":   902,
		"private_data": `{"KEY":"` + legacyKey + `","future_field":{"keep":true}}`,
	}).Error)

	require.NoError(t, MigrateLegacyTaskCredentials(DB))
	var raw sql.NullString
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("task_id = ?", "legacy-uppercase-task-private-key").Scan(&raw).Error)
	assert.NotContains(t, raw.String, legacyKey)
	assert.Contains(t, raw.String, common.CredentialCiphertextPrefix)
	assert.Contains(t, raw.String, "future_field")

	var loaded Task
	require.NoError(t, DB.Where("task_id = ?", "legacy-uppercase-task-private-key").First(&loaded).Error)
	assert.Equal(t, legacyKey, loaded.PrivateData.Key)
}
