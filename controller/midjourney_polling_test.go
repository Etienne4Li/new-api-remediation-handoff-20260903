package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A provider may legitimately issue the same task id on two independent
// channels.  Polling must use the channel namespace when correlating the
// response; otherwise one map entry overwrites the other and one user's task
// can receive the other channel's terminal status (and refund decision).
func TestMidjourneyPollingScopesSharedProviderIDByChannel(t *testing.T) {
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Midjourney{}))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	sharedID := "provider-id-shared-between-channels"
	newProvider := func(status, progress, reason string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"id":%q,"status":%q,"progress":%q,"failReason":%q}]`, sharedID, status, progress, reason)
		}))
	}
	firstProvider := newProvider("FAILURE", "100%", "first channel failed")
	secondProvider := newProvider("SUCCESS", "100%", "")
	defer firstProvider.Close()
	defer secondProvider.Close()

	firstURL, secondURL := firstProvider.URL, secondProvider.URL
	const firstChannelID, secondChannelID = 9301, 9302
	require.NoError(t, db.Create(&model.Channel{
		Id: firstChannelID, Type: constant.ChannelTypeMidjourney, Name: "mj-first",
		Key: "first-key", Status: common.ChannelStatusEnabled, BaseURL: &firstURL,
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id: secondChannelID, Type: constant.ChannelTypeMidjourney, Name: "mj-second",
		Key: "second-key", Status: common.ChannelStatusEnabled, BaseURL: &secondURL,
	}).Error)

	nowMillis := time.Now().UnixNano() / int64(time.Millisecond)
	firstTask := &model.Midjourney{
		Id: firstChannelID, UserId: 1, Code: 1, MjId: sharedID,
		ChannelId: firstChannelID, Status: "IN_PROGRESS", Progress: "20%", SubmitTime: nowMillis,
	}
	secondTask := &model.Midjourney{
		Id: secondChannelID, UserId: 2, Code: 1, MjId: sharedID,
		ChannelId: secondChannelID, Status: "IN_PROGRESS", Progress: "20%", SubmitTime: nowMillis,
	}
	require.NoError(t, db.Create(firstTask).Error)
	require.NoError(t, db.Create(secondTask).Error)

	service.InitHttpClient()
	summary := runMidjourneyTaskUpdateOnce(nil, nil)
	assert.Equal(t, 2, summary.ChannelsScanned)

	var gotFirst, gotSecond model.Midjourney
	require.NoError(t, db.First(&gotFirst, firstTask.Id).Error)
	require.NoError(t, db.First(&gotSecond, secondTask.Id).Error)
	assert.Equal(t, "FAILURE", gotFirst.Status)
	assert.Equal(t, "first channel failed", gotFirst.FailReason)
	assert.Equal(t, "SUCCESS", gotSecond.Status)
	assert.Empty(t, gotSecond.FailReason)
}

func TestLookupMidjourneyPollingTaskRequiresChannelScopedKey(t *testing.T) {
	first := &model.Midjourney{ChannelId: 9401, MjId: "same-id"}
	second := &model.Midjourney{ChannelId: 9402, MjId: "same-id"}
	tasks := map[string]*model.Midjourney{
		midjourneyPollingTaskKey(first.ChannelId, first.MjId):   first,
		midjourneyPollingTaskKey(second.ChannelId, second.MjId): second,
	}
	assert.Same(t, first, lookupMidjourneyPollingTask(tasks, first.ChannelId, first.MjId))
	assert.Same(t, second, lookupMidjourneyPollingTask(tasks, second.ChannelId, second.MjId))
	assert.Nil(t, lookupMidjourneyPollingTask(tasks, 9403, "same-id"))
}

func TestMidjourneyPollingRejectsRegressiveAndOppositeTerminalStates(t *testing.T) {
	tests := []struct {
		name     string
		current  string
		incoming string
		want     bool
	}{
		{name: "success cannot become failure", current: "SUCCESS", incoming: "FAILURE", want: false},
		{name: "failure cannot become success", current: "FAILURE", incoming: "SUCCESS", want: false},
		{name: "in progress cannot become queued", current: "IN_PROGRESS", incoming: "QUEUED", want: false},
		{name: "queued can become success", current: "QUEUED", incoming: "SUCCESS", want: true},
		{name: "provider completed alias is success", current: "IN_PROGRESS", incoming: "COMPLETED", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldTask := &model.Midjourney{Code: 1, Status: tt.current, Progress: "20%"}
			newTask := dto.MidjourneyDto{Status: tt.incoming, Progress: oldTask.Progress}
			assert.Equal(t, tt.want, checkMjTaskNeedUpdate(oldTask, newTask))
		})
	}
}

func TestMidjourneyPollingFailureReasonWithoutStatusIsAuthoritative(t *testing.T) {
	oldTask := &model.Midjourney{Code: 1, Status: "SUCCESS", Progress: "100%"}
	newTask := dto.MidjourneyDto{Progress: "100%", FailReason: "late provider failure"}
	assert.False(t, checkMjTaskNeedUpdate(oldTask, newTask), "a delayed failure reason must not overwrite SUCCESS")

	oldTask.Status = "IN_PROGRESS"
	oldTask.Progress = "20%"
	assert.True(t, checkMjTaskNeedUpdate(oldTask, newTask), "failure reason should advance an in-flight task")
}

func TestMidjourneyPollingRejectsInvalidMediaAtomically(t *testing.T) {
	validVideoURLs := []dto.ImgUrls{{Url: "https://new-cdn.example/video-list.mp4?sig=new"}}
	tooManyVideoURLs := make([]dto.ImgUrls, service.MaxMidjourneyVideoURLCount+1)
	for index := range tooManyVideoURLs {
		tooManyVideoURLs[index].Url = fmt.Sprintf("https://new-cdn.example/video-%d.mp4", index)
	}
	maxLengthURL := "https://new-cdn.example/" + strings.Repeat("a", service.MaxMidjourneyMediaURLLength-len("https://new-cdn.example/"))
	oversizedVideoURLsJSON := make([]dto.ImgUrls, service.MaxMidjourneyVideoURLCount)
	for index := range oversizedVideoURLsJSON {
		oversizedVideoURLsJSON[index].Url = maxLengthURL
	}

	tests := []struct {
		name      string
		imageURL  string
		videoURL  string
		videoURLs []dto.ImgUrls
		status    string
		progress  string
	}{
		{
			name:      "non-terminal response with unsafe image scheme",
			imageURL:  "file:///etc/passwd",
			videoURL:  "https://new-cdn.example/video.mp4?sig=new",
			videoURLs: validVideoURLs,
			status:    "IN_PROGRESS",
			progress:  "30%",
		},
		{
			name:      "oversized single video URL",
			imageURL:  "https://new-cdn.example/image.png?sig=new",
			videoURL:  maxLengthURL + "x",
			videoURLs: validVideoURLs,
		},
		{
			name:      "too many video URLs",
			imageURL:  "https://new-cdn.example/image.png?sig=new",
			videoURL:  "https://new-cdn.example/video.mp4?sig=new",
			videoURLs: tooManyVideoURLs,
		},
		{
			name:     "unsafe video list element",
			imageURL: "https://new-cdn.example/image.png?sig=new",
			videoURL: "https://new-cdn.example/video.mp4?sig=new",
			videoURLs: []dto.ImgUrls{
				{Url: "https://new-cdn.example/valid.mp4"},
				{Url: "javascript:alert(1)"},
			},
		},
		{
			name:      "oversized encoded video URL list",
			imageURL:  "https://new-cdn.example/image.png?sig=new",
			videoURL:  "https://new-cdn.example/video.mp4?sig=new",
			videoURLs: oversizedVideoURLsJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, progress := tt.status, tt.progress
			if status == "" {
				status, progress = "SUCCESS", "100%"
			}
			response := dto.MidjourneyDto{
				MjId:       "provider-invalid-media",
				PromptEn:   "provider replacement prompt",
				State:      "provider replacement state",
				SubmitTime: 101,
				StartTime:  102,
				FinishTime: 103,
				ImageUrl:   tt.imageURL,
				VideoUrl:   tt.videoURL,
				VideoUrls:  tt.videoURLs,
				Status:     status,
				Progress:   progress,
				Buttons:    []map[string]any{{"label": "replacement"}},
				Properties: &dto.Properties{FinalPrompt: "replacement"},
			}

			summary, before, after := pollMidjourneyResponsesForTest(t, response.MjId, []dto.MidjourneyDto{response}, 0, 0)
			assert.Equal(t, 1, summary.ChannelsScanned)
			assert.Equal(t, 1, summary.PollErrors, "the rejected provider item must surface as a polling error")
			assert.Equal(t, before.Status, after.Status)
			assert.Equal(t, before.Progress, after.Progress)
			assert.Equal(t, before.PromptEn, after.PromptEn)
			assert.Equal(t, before.State, after.State)
			assert.Equal(t, before.SubmitTime, after.SubmitTime)
			assert.Equal(t, before.StartTime, after.StartTime)
			assert.Equal(t, before.FinishTime, after.FinishTime)
			assert.Equal(t, before.ImageUrl, after.ImageUrl)
			assert.Equal(t, before.VideoUrl, after.VideoUrl)
			assert.Equal(t, before.VideoUrls, after.VideoUrls)
			assert.Equal(t, before.Buttons, after.Buttons)
			assert.Equal(t, before.Properties, after.Properties)
		})
	}
}

func TestMidjourneyPollingAppliesTerminalFailureWithInvalidMediaAndRefunds(t *testing.T) {
	const refundQuota = 10
	response := dto.MidjourneyDto{
		MjId:       "provider-invalid-media",
		PromptEn:   "provider failure prompt",
		State:      "provider failure state",
		SubmitTime: 101,
		StartTime:  102,
		FinishTime: 103,
		ImageUrl:   "file:///etc/passwd",
		VideoUrl:   "javascript:alert(1)",
		VideoUrls:  []dto.ImgUrls{{Url: "https://new-cdn.example/should-not-persist.mp4?sig=unsafe"}},
		Status:     "FAILED",
		Progress:   "42%",
		FailReason: "provider rendering failed",
		Buttons:    []map[string]any{{"label": "failure diagnostic"}},
		Properties: &dto.Properties{FinalPrompt: "failure diagnostic"},
	}

	summary, before, after := pollMidjourneyResponsesForTest(t, response.MjId, []dto.MidjourneyDto{response}, refundQuota, 2*time.Hour)
	assert.Equal(t, 1, summary.ChannelsScanned)
	assert.Equal(t, 1, summary.PollErrors, "invalid media remains an observable provider error")
	assert.Equal(t, string(model.TaskStatusFailure), after.Status)
	assert.Equal(t, "100%", after.Progress, "a terminal failure must leave the polling queue")
	assert.Equal(t, response.FailReason, after.FailReason)
	assert.Equal(t, response.FinishTime, after.FinishTime)
	assert.Equal(t, response.PromptEn, after.PromptEn)
	assert.Equal(t, response.State, after.State)
	assert.Equal(t, before.ImageUrl, after.ImageUrl)
	assert.Equal(t, before.VideoUrl, after.VideoUrl)
	assert.Equal(t, before.VideoUrls, after.VideoUrls)
	assert.Zero(t, after.Quota, "the durable refund must clear the task marker")

	var userLedger struct {
		Quota     int
		UsedQuota int
	}
	require.NoError(t, model.DB.Model(&model.User{}).
		Select("quota", "used_quota").Where("id = ?", before.UserId).Scan(&userLedger).Error)
	assert.Equal(t, 100, userLedger.Quota)
	assert.Zero(t, userLedger.UsedQuota)

	var channelUsedQuota int64
	require.NoError(t, model.DB.Model(&model.Channel{}).
		Select("used_quota").Where("id = ?", before.ChannelId).Scan(&channelUsedQuota).Error)
	assert.Zero(t, channelUsedQuota)

	var operation model.BillingOperation
	require.NoError(t, model.DB.First(&operation).Error)
	assert.Equal(t, model.BillingOperationLegacyTaskRefundComponent, operation.Component)
	assert.Equal(t, model.BillingOperationApplied, operation.Status)

	var refundLog model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", model.LogTypeRefund).First(&refundLog).Error)
	assert.Equal(t, refundQuota, refundLog.Quota)
	assert.Equal(t, before.ChannelId, refundLog.ChannelId)
}

func TestMidjourneyPollingTimesOutInvalidNonTerminalMediaAndRefunds(t *testing.T) {
	const refundQuota = 10
	response := dto.MidjourneyDto{
		MjId:      "provider-invalid-media",
		ImageUrl:  "file:///etc/passwd",
		VideoUrl:  "javascript:alert(1)",
		Status:    "IN_PROGRESS",
		Progress:  "42%",
		VideoUrls: []dto.ImgUrls{{Url: "https://new-cdn.example/should-not-persist.mp4?sig=unsafe"}},
	}

	summary, before, after := pollMidjourneyResponsesForTest(t, response.MjId, []dto.MidjourneyDto{response}, refundQuota, 2*time.Hour)
	assert.Equal(t, 1, summary.PollErrors, "invalid media remains observable when the local timeout closes the task")
	assert.Equal(t, string(model.TaskStatusFailure), after.Status)
	assert.Equal(t, "100%", after.Progress)
	assert.Equal(t, "上游任务超时（超过1小时）", after.FailReason)
	assert.Equal(t, before.ImageUrl, after.ImageUrl)
	assert.Equal(t, before.VideoUrl, after.VideoUrl)
	assert.Equal(t, before.VideoUrls, after.VideoUrls)
	assert.Zero(t, after.Quota)

	var userLedger struct {
		Quota     int
		UsedQuota int
	}
	require.NoError(t, model.DB.Model(&model.User{}).
		Select("quota", "used_quota").Where("id = ?", before.UserId).Scan(&userLedger).Error)
	assert.Equal(t, 100, userLedger.Quota)
	assert.Zero(t, userLedger.UsedQuota)
}

func TestMidjourneyPollingTimesOutTaskOmittedFromProviderResponse(t *testing.T) {
	const refundQuota = 10
	summary, before, after := pollMidjourneyResponsesForTest(
		t,
		"provider-omitted-task",
		[]dto.MidjourneyDto{},
		refundQuota,
		2*time.Hour,
	)

	assert.Equal(t, 1, summary.ChannelsScanned)
	assert.Equal(t, 1, summary.TimedOutTasksFailed)
	assert.Equal(t, string(model.TaskStatusFailure), after.Status)
	assert.Equal(t, "100%", after.Progress)
	assert.Equal(t, "上游任务超时（超过1小时）", after.FailReason)
	assert.Zero(t, after.Quota)

	var userLedger struct {
		Quota     int
		UsedQuota int
	}
	require.NoError(t, model.DB.Model(&model.User{}).
		Select("quota", "used_quota").Where("id = ?", before.UserId).Scan(&userLedger).Error)
	assert.Equal(t, 100, userLedger.Quota)
	assert.Zero(t, userLedger.UsedQuota)

	var channelUsedQuota int64
	require.NoError(t, model.DB.Model(&model.Channel{}).
		Select("used_quota").Where("id = ?", before.ChannelId).Scan(&channelUsedQuota).Error)
	assert.Zero(t, channelUsedQuota)
}

func pollMidjourneyResponsesForTest(t *testing.T, taskMJID string, responses []dto.MidjourneyDto, taskQuota int, taskAge time.Duration) (midjourneyPollSummary, model.Midjourney, model.Midjourney) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMemoryCache := common.MemoryCacheEnabled
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Channel{},
		&model.Midjourney{},
		&model.MidjourneySubmitIntent{},
		&model.BillingOperation{},
		&model.Log{},
	))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.MemoryCacheEnabled = previousMemoryCache
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})

	responseBody, err := common.Marshal(responses)
	require.NoError(t, err)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
	}))
	t.Cleanup(provider.Close)

	providerURL := provider.URL
	const channelID = 9501
	require.NoError(t, db.Create(&model.User{
		Id:        1,
		Username:  "mj-poll-user",
		Password:  "unused-test-password",
		Role:      common.RoleCommonUser,
		Status:    common.UserStatusEnabled,
		Quota:     100 - taskQuota,
		UsedQuota: taskQuota,
		Group:     "default",
		AffCode:   "mj-poll-user",
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id: channelID, Type: constant.ChannelTypeMidjourney, Name: "mj-invalid-media",
		Key: "provider-key", Status: common.ChannelStatusEnabled, BaseURL: &providerURL, UsedQuota: int64(taskQuota),
	}).Error)

	task := &model.Midjourney{
		Id:         channelID,
		UserId:     1,
		Code:       1,
		MjId:       taskMJID,
		ChannelId:  channelID,
		PromptEn:   "existing prompt",
		State:      "existing state",
		SubmitTime: time.Now().Add(-taskAge).UnixNano() / int64(time.Millisecond),
		StartTime:  11,
		ImageUrl:   "https://existing-cdn.example/image.png?sig=existing",
		VideoUrl:   "https://existing-cdn.example/video.mp4?sig=existing",
		VideoUrls:  `[{"url":"https://existing-cdn.example/list.mp4?sig=existing"}]`,
		Status:     "IN_PROGRESS",
		Progress:   "20%",
		Quota:      taskQuota,
		Buttons:    `[{"label":"existing"}]`,
		Properties: `{"finalPrompt":"existing"}`,
	}
	require.NoError(t, db.Create(task).Error)
	before := *task

	service.InitHttpClient()
	summary := runMidjourneyTaskUpdateOnce(t.Context(), nil)
	var after model.Midjourney
	require.NoError(t, db.First(&after, task.Id).Error)
	return summary, before, after
}
