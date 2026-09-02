package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactTaskResponseBodyRemovesCredentialsAndSignedURLs(t *testing.T) {
	body := []byte(`{"clips":[{"audio_url":"https://cdn.example/audio.mp3?token=secret&X-Amz-Signature=abc","video_url":"https://cdn.example/video.mp4?sig=private","image_url":"https://cdn.example/image.png?key=private","api_key":"sk-live-secret","access_token":"access-secret","nested":{"authorization":"Bearer private"},"bytesBase64Encoded":"` + strings.Repeat("A", 512) + `"}]}`)

	redacted := RedactTaskResponseBody(body)
	var got map[string]any
	require.NoError(t, common.Unmarshal(redacted, &got))
	encoded := string(redacted)
	assert.NotContains(t, encoded, "sk-live-secret")
	assert.NotContains(t, encoded, "access-secret")
	assert.NotContains(t, encoded, "Bearer private")
	assert.NotContains(t, encoded, "bytesBase64Encoded")
	assert.NotContains(t, encoded, "X-Amz-Signature")
	assert.NotContains(t, encoded, "token=secret")
	assert.NotContains(t, encoded, "?sig=private")
	assert.NotContains(t, encoded, "?key=private")

	clips, ok := got["clips"].([]any)
	require.True(t, ok)
	require.Len(t, clips, 1)
	clip, ok := clips[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://cdn.example/audio.mp3", clip["audio_url"])
	assert.Equal(t, "https://cdn.example/video.mp4", clip["video_url"])
	assert.Equal(t, "https://cdn.example/image.png", clip["image_url"])
}

func TestRedactTaskResponseBodyHandlesMalformedAndNilInput(t *testing.T) {
	assert.Nil(t, RedactTaskResponseBody(nil))
	assert.JSONEq(t, `{}`, string(RedactTaskResponseBody([]byte("<html>token=secret"))))
}

func TestRedactTaskResponseBodyBoundsOutput(t *testing.T) {
	largeString := strings.Repeat("x", maxRedactedTaskDataString+1024)
	items := make([]string, maxRedactedTaskDataItems+64)
	for i := range items {
		items[i] = largeString
	}
	body, err := common.Marshal(map[string]any{
		"items": items,
		"large": largeString,
	})
	require.NoError(t, err)

	redacted := RedactTaskResponseBody(body)
	assert.LessOrEqual(t, len(redacted), maxRedactedTaskDataBytes)
	var decoded any
	require.NoError(t, common.Unmarshal(redacted, &decoded))

	tooLarge := []byte(strings.Repeat("x", maxRedactedTaskInputBytes+1))
	assert.JSONEq(t, `{"_redacted":true}`, string(RedactTaskResponseBody(tooLarge)))
}

func TestRedactTaskResponseBodyRemovesURLUserinfo(t *testing.T) {
	body := []byte(`{"location":"https://user:password@cdn.example/file.mp4?token=secret#fragment"}`)
	redacted := RedactTaskResponseBody(body)
	assert.JSONEq(t, `{"location":"https://cdn.example/file.mp4"}`, string(redacted))
}

func TestRedactTaskResultURLAllowsOnlyHTTPOrLocalVideoProxy(t *testing.T) {
	assert.Equal(t, "https://cdn.example/video.mp4", RedactTaskResultURL("https://cdn.example/video.mp4?signature=private#fragment"))
	assert.Equal(t, "/v1/videos/task_123/content", RedactTaskResultURL("/v1/videos/task_123/content"))
	assert.Equal(t, "[redacted-data-url]", RedactTaskResultURL("data:video/mp4;base64,AAAA"))

	for _, value := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"ftp://cdn.example/video.mp4",
		"//cdn.example/video.mp4",
		"relative/video.mp4",
		"/v1/videos/task_123/content?token=private",
		"/v1/videos/content",
		"/v1/videos//content",
		"/other/local/path",
	} {
		assert.Empty(t, RedactTaskResultURL(value), "value=%q", value)
	}
}

func TestNormalizeTaskResultURLBoundsAndValidatesProviderValues(t *testing.T) {
	valid := " https://cdn.example/video.mp4?width=1280&X-Amz-Signature=private#fragment "
	assert.Equal(t, "https://cdn.example/video.mp4?width=1280&X-Amz-Signature=private", NormalizeTaskResultURL(valid))
	for _, value := range []string{
		"",
		"data:video/mp4;base64,AAAA",
		"file:///tmp/video.mp4",
		"https://cdn.example:99999/video.mp4",
		strings.Repeat("x", maxTaskResultURLBytes+1),
	} {
		assert.Empty(t, NormalizeTaskResultURL(value), "value=%q", value)
	}
	assert.Equal(t, "https://cdn.example/video.mp4", NormalizeTaskResultURL("https://user:password@cdn.example/video.mp4"))
}
