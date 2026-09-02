package model

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTaskProviderCredentialsAreEncryptedAtDatabaseBoundary(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	common.CryptoSecret = "task-provider-storage-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	truncateTables(t)

	task := &Task{
		TaskID:     "task-provider-storage",
		Platform:   constant.TaskPlatform("video"),
		Status:     TaskStatusInProgress,
		FailReason: "failed Authorization: Bearer create-secret",
		PrivateData: TaskPrivateData{
			Key:       "provider-api-key",
			ResultURL: "https://cdn.example/video.mp4?width=1280&X-Amz-Signature=create-signature",
		},
	}
	require.NoError(t, DB.Create(task).Error)

	var rawPrivateData, rawFailReason sql.NullString
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&rawPrivateData).Error)
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("fail_reason").Where("id = ?", task.ID).Scan(&rawFailReason).Error)
	assert.NotContains(t, rawPrivateData.String, "provider-api-key")
	assert.NotContains(t, rawPrivateData.String, "create-signature")
	assert.NotContains(t, rawPrivateData.String, "cdn.example")
	assert.Equal(t, 2, strings.Count(rawPrivateData.String, common.CredentialCiphertextPrefix))
	assert.NotContains(t, rawFailReason.String, "create-secret")

	var loaded Task
	require.NoError(t, DB.First(&loaded, task.ID).Error)
	assert.Equal(t, "provider-api-key", loaded.PrivateData.Key)
	assert.Equal(t, "https://cdn.example/video.mp4?width=1280&X-Amz-Signature=create-signature", loaded.PrivateData.ResultURL)

	loaded.FailReason = "poll failed token=poll-secret"
	loaded.PrivateData.ResultURL = "https://cdn.example/final.mp4?quality=high&token=poll-url-secret"
	won, err := loaded.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, won)

	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&rawPrivateData).Error)
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("fail_reason").Where("id = ?", task.ID).Scan(&rawFailReason).Error)
	assert.NotContains(t, rawPrivateData.String, "poll-url-secret")
	assert.NotContains(t, rawPrivateData.String, "quality=high")
	assert.NotContains(t, rawFailReason.String, "poll-secret")
	assert.Equal(t, 2, strings.Count(rawPrivateData.String, common.CredentialCiphertextPrefix))

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, "https://cdn.example/final.mp4?quality=high&token=poll-url-secret", reloaded.PrivateData.ResultURL)

	rawUpdate := `{"key":"replacement-provider-key","result_url":"https://cdn.example/map.mp4?format=mp4&sig=map-secret"}`
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("private_data", rawUpdate).Error)
	require.NoError(t, DB.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", task.ID).Scan(&rawPrivateData).Error)
	assert.NotContains(t, rawPrivateData.String, "replacement-provider-key")
	assert.NotContains(t, rawPrivateData.String, "map-secret")
	assert.NotContains(t, rawPrivateData.String, "format=mp4")
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, "replacement-provider-key", reloaded.PrivateData.Key)
	assert.Equal(t, "https://cdn.example/map.mp4?format=mp4&sig=map-secret", reloaded.PrivateData.ResultURL)

	reloaded.PrivateData.ResultURL = "/v1/videos/task-provider-storage/content"
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("private_data", reloaded.PrivateData).Error)
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, "/v1/videos/task-provider-storage/content", reloaded.PrivateData.ResultURL)
}

func TestProviderRuntimeEnvelopeUsesPlaintextStorageLimit(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	common.CryptoSecret = "provider-runtime-limit-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true

	const limit = 512
	prefix := "https://cdn.example/video.mp4?signature="
	plaintext := prefix + strings.Repeat("x", limit-len(prefix))
	sealed, err := sealProviderRuntimeValue("test URL", plaintext, limit)
	require.NoError(t, err)
	require.Greater(t, len(sealed), limit)

	resealed, err := sealProviderRuntimeValue("test URL", sealed, limit)
	require.NoError(t, err)
	assert.Equal(t, sealed, resealed)
	opened, err := openProviderRuntimeValue("test URL", sealed, limit)
	require.NoError(t, err)
	assert.Equal(t, plaintext, opened)
}

func TestMidjourneyProviderCredentialsAreEncryptedAtDatabaseBoundary(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.CryptoSecret = "midjourney-provider-storage-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	previousDB := DB
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Midjourney{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	task := &Midjourney{
		MjId:        "mj-provider-storage",
		Status:      string(TaskStatusInProgress),
		ImageUrl:    "https://cdn.example/image.png?width=1024&token=image-secret",
		VideoUrl:    "https://cdn.example/video.mp4?quality=high&X-Amz-Signature=video-secret",
		VideoUrls:   `[{"url":"https://cdn.example/clip.mp4?format=mp4&sig=list-secret"}]`,
		Description: "provider failed at https://provider.example/error?token=description-secret",
		FailReason:  "Authorization: Bearer failure-secret",
		Properties:  `{"finalPrompt":"city skyline","imageUrl":"https://cdn.example/property.png?size=large&key=property-secret","authorization":"Bearer property-secret"}`,
		Buttons:     `[{"customId":"MJ::JOB::upsample::1::task-id","label":"U1","token":"button-secret"}]`,
	}
	require.NoError(t, db.Create(task).Error)
	assert.Equal(t, "https://cdn.example/image.png?width=1024&token=image-secret", task.ImageUrl)
	assert.Equal(t, "https://cdn.example/video.mp4?quality=high&X-Amz-Signature=video-secret", task.VideoUrl)
	assert.Equal(t, `[{"url":"https://cdn.example/clip.mp4?format=mp4&sig=list-secret"}]`, task.VideoUrls)

	var stored Midjourney
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).First(&stored, task.Id).Error)
	encoded, err := common.Marshal(stored)
	require.NoError(t, err)
	raw := string(encoded)
	for _, secret := range []string{"image-secret", "video-secret", "list-secret", "description-secret", "failure-secret", "property-secret", "button-secret"} {
		assert.NotContains(t, raw, secret)
	}
	assert.True(t, common.IsCredentialCiphertext(stored.ImageUrl))
	assert.True(t, common.IsCredentialCiphertext(stored.VideoUrl))
	assert.True(t, common.IsCredentialCiphertext(stored.VideoUrls))
	assert.Contains(t, stored.Properties, "city skyline")
	assert.Contains(t, stored.Buttons, "MJ::JOB::upsample::1::task-id")
	assert.Contains(t, stored.Buttons, `"label":"U1"`)

	var loaded Midjourney
	require.NoError(t, db.First(&loaded, task.Id).Error)
	assert.Equal(t, task.ImageUrl, loaded.ImageUrl)
	assert.Equal(t, task.VideoUrl, loaded.VideoUrl)
	assert.Equal(t, task.VideoUrls, loaded.VideoUrls)

	loaded.FailReason = "token=cas-secret"
	loaded.ImageUrl = "https://cdn.example/final.png?download=1&signature=cas-url-secret"
	won, err := loaded.UpdateWithStatus(string(TaskStatusInProgress))
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).First(&stored, task.Id).Error)
	assert.True(t, common.IsCredentialCiphertext(stored.ImageUrl))
	assert.NotContains(t, stored.ImageUrl, "cas-url-secret")
	assert.NotContains(t, stored.FailReason, "cas-secret")
	require.NoError(t, db.First(&loaded, task.Id).Error)
	assert.Equal(t, "https://cdn.example/final.png?download=1&signature=cas-url-secret", loaded.ImageUrl)
}

func TestMidjourneyFailedStructWritesPreserveRuntimeValues(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.CryptoSecret = "midjourney-failed-write-storage-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Midjourney{}))
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	newTask := func(id string) *Midjourney {
		return &Midjourney{
			MjId:        id,
			ImageUrl:    "https://cdn.example/image.png?token=image-secret",
			VideoUrl:    "https://cdn.example/video.mp4?key=video-secret",
			VideoUrls:   `[{"url":"https://cdn.example/clip.mp4?sig=list-secret"}]`,
			Description: "failed Authorization: Bearer description-secret",
			FailReason:  "token=failure-secret",
			Properties:  `{"prompt":"keep","api_key":"property-secret"}`,
			Buttons:     `[{"label":"U1","token":"button-secret"}]`,
		}
	}

	require.NoError(t, db.Exec(`CREATE TRIGGER fail_midjourney_insert
		BEFORE INSERT ON midjourneys
		BEGIN
			SELECT RAISE(ABORT, 'forced midjourney insert failure');
		END`).Error)
	failedCreate := newTask("failed-create")
	createSnapshot := *failedCreate
	require.Error(t, db.Create(failedCreate).Error)
	assert.Equal(t, createSnapshot, *failedCreate)
	require.NoError(t, db.Exec("DROP TRIGGER fail_midjourney_insert").Error)

	failedSave := newTask("failed-save")
	require.NoError(t, db.Create(failedSave).Error)
	require.NotZero(t, failedSave.Id)
	failedSave.ImageUrl = "https://cdn.example/replacement.png?signature=replacement-secret"
	failedSave.FailReason = "Authorization: Bearer replacement-failure-secret"
	saveSnapshot := *failedSave
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_midjourney_update
		BEFORE UPDATE ON midjourneys
		BEGIN
			SELECT RAISE(ABORT, 'forced midjourney update failure');
		END`).Error)
	require.Error(t, db.Save(failedSave).Error)
	assert.Equal(t, saveSnapshot, *failedSave)
}

func TestMigrateLegacyProviderStorageEncryptsHistoricalMedia(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.CryptoSecret = "provider-storage-migration-test-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Task{}, &Midjourney{}))
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	legacyTask := &Task{
		TaskID:     "legacy-provider-storage",
		Status:     TaskStatusFailure,
		FailReason: "failed Authorization: Bearer legacy-task-secret",
		PrivateData: TaskPrivateData{
			ResultURL: "https://cdn.example/legacy.mp4?width=720&token=legacy-url-secret",
		},
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(legacyTask).Error)
	legacyMidjourney := map[string]interface{}{
		"mj_id":       "legacy-midjourney-storage",
		"image_url":   "https://cdn.example/legacy.png?size=large&sig=legacy-image-secret",
		"fail_reason": "token=legacy-failure-secret",
		"properties":  `{"prompt":"kept","api_key":"legacy-property-secret"}`,
		"buttons":     `[{"customId":"MJ::JOB::variation::1::legacy-id","authorization":"Bearer legacy-button-secret"}]`,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("midjourneys").Create(legacyMidjourney).Error)
	var legacyMidjourneyID int64
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("midjourneys").
		Select("id").Where("mj_id = ?", "legacy-midjourney-storage").Scan(&legacyMidjourneyID).Error)
	require.NotZero(t, legacyMidjourneyID)

	require.NoError(t, MigrateLegacyTaskCredentials(db))
	require.NoError(t, MigrateLegacyProviderStorage(db))

	var taskPrivateData, taskFailReason string
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", legacyTask.ID).Scan(&taskPrivateData).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("fail_reason").Where("id = ?", legacyTask.ID).Scan(&taskFailReason).Error)
	assert.NotContains(t, taskPrivateData, "legacy-url-secret")
	assert.NotContains(t, taskPrivateData, "width=720")
	assert.Contains(t, taskPrivateData, common.CredentialCiphertextPrefix)
	assert.NotContains(t, taskFailReason, "legacy-task-secret")
	var loadedTask Task
	require.NoError(t, db.First(&loadedTask, legacyTask.ID).Error)
	assert.Equal(t, "https://cdn.example/legacy.mp4?width=720&token=legacy-url-secret", loadedTask.PrivateData.ResultURL)

	var stored Midjourney
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).First(&stored, legacyMidjourneyID).Error)
	encoded, err := common.Marshal(stored)
	require.NoError(t, err)
	raw := string(encoded)
	for _, secret := range []string{"legacy-image-secret", "legacy-failure-secret", "legacy-property-secret", "legacy-button-secret"} {
		assert.NotContains(t, raw, secret)
	}
	assert.True(t, common.IsCredentialCiphertext(stored.ImageUrl))
	assert.Contains(t, stored.Properties, `"prompt":"kept"`)
	assert.Contains(t, stored.Buttons, "MJ::JOB::variation::1::legacy-id")
	var loadedMidjourney Midjourney
	require.NoError(t, db.First(&loadedMidjourney, legacyMidjourneyID).Error)
	assert.Equal(t, "https://cdn.example/legacy.png?size=large&sig=legacy-image-secret", loadedMidjourney.ImageUrl)

	firstTaskPrivateData := taskPrivateData
	firstMidjourneyImage := stored.ImageUrl
	require.NoError(t, MigrateLegacyTaskCredentials(db))
	require.NoError(t, MigrateLegacyProviderStorage(db))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", legacyTask.ID).Scan(&taskPrivateData).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).First(&stored, legacyMidjourneyID).Error)
	assert.Equal(t, firstTaskPrivateData, taskPrivateData)
	assert.Equal(t, firstMidjourneyImage, stored.ImageUrl)
}

func TestMigrateLegacyProviderStoragePreservesFailReasonSignedURL(t *testing.T) {
	previousSecret := common.CryptoSecret
	previousConfigured := common.CredentialSecretConfigured
	previousReady := common.CredentialSecretRuntimeReady
	common.CryptoSecret = "legacy-fail-reason-url-migration-secret"
	common.CredentialSecretConfigured = true
	common.CredentialSecretRuntimeReady = true
	t.Cleanup(func() {
		common.CryptoSecret = previousSecret
		common.CredentialSecretConfigured = previousConfigured
		common.CredentialSecretRuntimeReady = previousReady
	})

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Task{}))
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	const signedURL = "https://storage.example/video.mp4?key=storage-signature&X-Goog-Signature=private-signature"
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").Create(map[string]interface{}{
		"task_id":      "legacy-fail-reason-result-url",
		"platform":     constant.TaskPlatform("video"),
		"fail_reason":  signedURL,
		"private_data": `{"upstream_task_id":"provider-task","future":{"keep":true}}`,
	}).Error)

	var row struct {
		ID int64 `gorm:"column:id"`
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("id").Where("task_id = ?", "legacy-fail-reason-result-url").Scan(&row).Error)
	require.NotZero(t, row.ID)
	require.NoError(t, MigrateLegacyProviderStorage(db))

	var rawPrivateData, rawFailReason string
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", row.ID).Scan(&rawPrivateData).Error)
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("fail_reason").Where("id = ?", row.ID).Scan(&rawFailReason).Error)
	assert.NotContains(t, rawPrivateData, "private-signature")
	assert.Contains(t, rawPrivateData, common.CredentialCiphertextPrefix)
	assert.Contains(t, rawPrivateData, `"future"`)
	assert.NotContains(t, rawFailReason, "private-signature")

	var loaded Task
	require.NoError(t, db.First(&loaded, row.ID).Error)
	assert.Equal(t, signedURL, loaded.PrivateData.ResultURL)
	assert.Equal(t, signedURL, loaded.GetResultURL())
	assert.Equal(t, "provider-task", loaded.PrivateData.UpstreamTaskID)

	firstPrivateData := rawPrivateData
	require.NoError(t, MigrateLegacyProviderStorage(db))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("private_data").Where("id = ?", row.ID).Scan(&rawPrivateData).Error)
	assert.Equal(t, firstPrivateData, rawPrivateData)
}
