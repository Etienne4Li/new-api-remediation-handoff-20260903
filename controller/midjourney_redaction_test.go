package controller

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRedactMidjourneyModelForResponse(t *testing.T) {
	task := &model.Midjourney{
		ImageUrl:    "https://cdn.example/image.png?X-Amz-Signature=private",
		VideoUrl:    "https://cdn.example/video.mp4?token=private",
		VideoUrls:   `[{"url":"https://cdn.example/clip.mp4?sig=private"}]`,
		FailReason:  "provider failed at https://cdn.example/video.mp4?token=private Authorization: Bearer secret-token",
		Description: "upstream failed at https://cdn.example/error.json?token=private",
		Properties:  `{"imageUrl":"https://cdn.example/image.png?sig=private","authorization":"Bearer secret"}`,
		Buttons:     `[{"customId":"https://cdn.example/action?token=private"}]`,
	}

	got := redactMidjourneyModelForResponse(task)

	require.NotNil(t, got)
	assert.Equal(t, "https://cdn.example/image.png", got.ImageUrl)
	assert.Equal(t, "https://cdn.example/video.mp4", got.VideoUrl)
	assert.NotContains(t, got.FailReason, "token=private")
	assert.NotContains(t, got.FailReason, "secret-token")
	assert.NotContains(t, got.Description, "token=private")
	assert.NotContains(t, got.Properties, "token=private")
	assert.NotContains(t, got.Properties, "Bearer secret")
	assert.NotContains(t, got.Buttons, "token=private")
	var videoURLs []struct {
		URL string `json:"url"`
	}
	require.NoError(t, common.Unmarshal([]byte(got.VideoUrls), &videoURLs))
	require.Len(t, videoURLs, 1)
	assert.Equal(t, "https://cdn.example/clip.mp4", videoURLs[0].URL)

	// Projection must not alter the source row: the authenticated image proxy
	// still needs its original signed URL to fetch the provider asset.
	assert.Equal(t, "https://cdn.example/image.png?X-Amz-Signature=private", task.ImageUrl)
	assert.Equal(t, "https://cdn.example/video.mp4?token=private", task.VideoUrl)
	assert.Contains(t, task.FailReason, "secret-token")
	assert.Contains(t, task.Properties, "Bearer secret")
}

func TestRedactMidjourneyModelForResponseDropsMalformedOrOversizedVideoURLs(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed": "not-json",
		"oversized": "[\"" + string(make([]byte, (1<<20)+1)) + "\"]",
	} {
		t.Run(name, func(t *testing.T) {
			task := &model.Midjourney{VideoUrls: raw}
			got := redactMidjourneyModelForResponse(task)
			require.NotNil(t, got)
			assert.Empty(t, got.VideoUrls)
			assert.Equal(t, raw, task.VideoUrls)
		})
	}
}

func TestRedactMidjourneyModelForResponseHandlesNil(t *testing.T) {
	assert.Nil(t, redactMidjourneyModelForResponse(nil))
}

func TestGetUserMidjourneyEmitsRedactedCopy(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousConfig := setting.GetMidjourneyConfig()
	previousAddress := system_setting.ServerAddress
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}))
	setting.UpdateMidjourneyConfig(func(config *setting.MidjourneyConfig) {
		config.ForwardURLEnabled = false
	})
	system_setting.ServerAddress = "https://api.example.test/"
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		setting.UpdateMidjourneyConfig(func(config *setting.MidjourneyConfig) {
			*config = previousConfig
		})
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	const userID = 7101
	original := &model.Midjourney{
		UserId:      userID,
		MjId:        "mj-controller-redaction",
		ImageUrl:    "https://cdn.example/image.png?X-Amz-Signature=private",
		VideoUrl:    "https://cdn.example/video.mp4?token=private",
		VideoUrls:   `[{"url":"https://cdn.example/clip.mp4?sig=private"}]`,
		Description: "upstream error https://cdn.example/error?token=private",
		FailReason:  "failed Authorization: Bearer private-token",
		Properties:  `{"imageUrl":"https://cdn.example/image.png?sig=private","authorization":"Bearer private"}`,
		Buttons:     `[{"customId":"https://cdn.example/action?token=private"}]`,
	}
	require.NoError(t, db.Create(original).Error)
	var persistedBefore model.Midjourney
	require.NoError(t, db.First(&persistedBefore, original.Id).Error)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/mj/self?p=1&page_size=10", nil)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = request
	c.Set("id", userID)
	GetUserMidjourney(c)

	require.Equal(t, 200, recorder.Code)
	var envelope struct {
		Data struct {
			Items []model.Midjourney `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Items, 1)
	public := envelope.Data.Items[0]
	assert.Equal(t, "https://cdn.example/image.png", public.ImageUrl)
	assert.Equal(t, "https://api.example.test/mj/video/mj-controller-redaction", public.VideoUrl)
	assert.NotContains(t, public.Description, "token=private")
	assert.NotContains(t, public.FailReason, "private-token")
	assert.NotContains(t, public.Properties, "private")
	assert.NotContains(t, public.Buttons, "token=private")
	var publicVideoURLs []struct {
		URL string `json:"url"`
	}
	require.NoError(t, common.Unmarshal([]byte(public.VideoUrls), &publicVideoURLs))
	require.Len(t, publicVideoURLs, 1)
	assert.Equal(t, "https://api.example.test/mj/video/mj-controller-redaction/0", publicVideoURLs[0].URL)

	var persisted model.Midjourney
	require.NoError(t, db.First(&persisted, original.Id).Error)
	assert.Equal(t, persistedBefore.ImageUrl, persisted.ImageUrl)
	assert.Equal(t, persistedBefore.VideoUrl, persisted.VideoUrl)
	assert.Equal(t, persistedBefore.VideoUrls, persisted.VideoUrls)
	assert.Equal(t, persistedBefore.Description, persisted.Description)
	assert.Equal(t, persistedBefore.FailReason, persisted.FailReason)
	assert.Equal(t, persistedBefore.Properties, persisted.Properties)
	assert.Equal(t, persistedBefore.Buttons, persisted.Buttons)
}
