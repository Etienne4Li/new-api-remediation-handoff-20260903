package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAIVideoResponseUsesAuthenticatedProxy(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://api.example.test/"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	task := &model.Task{
		TaskID: "task_video_123",
		Status: model.TaskStatusSuccess,
	}
	response := []byte(`{"id":"task_video_123","status":"completed","metadata":{"url":"https://cdn.example/video.mp4?X-Amz-Signature=secret"}}`)

	got, err := normalizeOpenAIVideoResponse(task, response)
	require.NoError(t, err)
	text := string(got)
	assert.Contains(t, text, `"url":"https://api.example.test/v1/videos/task_video_123/content"`)
	assert.NotContains(t, text, "X-Amz-Signature")
	assert.NotContains(t, text, "cdn.example")
}

func TestNormalizeOpenAIVideoResponseDoesNotInventURLBeforeCompletion(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://api.example.test"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	task := &model.Task{TaskID: "task_video_queued", Status: model.TaskStatusInProgress}
	got, err := normalizeOpenAIVideoResponse(task, []byte(`{"id":"task_video_queued","status":"in_progress"}`))
	require.NoError(t, err)
	assert.NotContains(t, string(got), `"metadata"`)
}

func TestNormalizeOpenAIVideoResponseRejectsNonObject(t *testing.T) {
	task := &model.Task{TaskID: "task_video_bad", Status: model.TaskStatusSuccess}
	_, err := normalizeOpenAIVideoResponse(task, []byte(`[]`))
	assert.Error(t, err)
}

func TestPublicTaskVideoURLUsesProxyForCompletedTask(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://api.example.test/"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	task := &model.Task{
		TaskID: "task_video_completed",
		Status: model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://cdn.example/video.mp4?signature=secret",
		},
	}
	assert.Equal(t, "https://api.example.test/v1/videos/task_video_completed/content", publicTaskVideoURL(task))
}

func TestPublicTaskVideoURLRedactsNonTerminalLegacyURL(t *testing.T) {
	task := &model.Task{
		TaskID: "task_video_pending",
		Status: model.TaskStatusInProgress,
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://cdn.example/video.mp4?signature=secret",
		},
	}
	assert.Equal(t, "https://cdn.example/video.mp4", publicTaskVideoURL(task))
}
