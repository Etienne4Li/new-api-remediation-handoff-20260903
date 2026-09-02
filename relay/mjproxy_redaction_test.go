package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactMidjourneyResponseBodyProtectsProviderMedia(t *testing.T) {
	previousConfig := setting.GetMidjourneyConfig()
	previousAddress := system_setting.ServerAddress
	setting.UpdateMidjourneyConfig(func(config *setting.MidjourneyConfig) {
		config.ForwardURLEnabled = true
	})
	system_setting.ServerAddress = "https://api.example.test///"
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		setting.UpdateMidjourneyConfig(func(config *setting.MidjourneyConfig) {
			*config = previousConfig
		})
	})

	body := []byte(`{"code":21,"description":"failed at https://provider.example/error?token=private","result":"mj-result","properties":{"imageUrl":"https://cdn.example/image.png?X-Amz-Signature=private","videoUrl":"https://cdn.example/video.mp4?sig=private","nested":{"url":"https://cdn.example/clip.mp4?token=private"},"authorization":"Bearer secret-token"}}`)
	redacted := redactMidjourneyResponseBody(body, "mj-result")
	encoded := string(redacted)
	assert.NotContains(t, encoded, "X-Amz-Signature")
	assert.NotContains(t, encoded, "sig=private")
	assert.NotContains(t, encoded, "token=private")
	assert.NotContains(t, encoded, "secret-token")
	assert.Contains(t, encoded, "https://api.example.test/mj/image/mj-result")

	var payload map[string]any
	require.NoError(t, common.Unmarshal(redacted, &payload))
	properties, ok := payload["properties"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://api.example.test/mj/image/mj-result", properties["imageUrl"])
	assert.Equal(t, "https://cdn.example/video.mp4", properties["videoUrl"])
}

func TestRedactMidjourneyResponseBodyHandlesMalformedInput(t *testing.T) {
	assert.JSONEq(t, `{}`, string(redactMidjourneyResponseBody([]byte("<html>token=secret"), "mj-result")))
	assert.Nil(t, redactMidjourneyResponseBody(nil, "mj-result"))
}

func TestRedactMidjourneyResponseBodySanitizesGenericURLFields(t *testing.T) {
	body := []byte(`{"result":"https://cdn.example/video.mp4?signature=private","value":"data:video/mp4;base64,AAAA"}`)
	redacted := redactMidjourneyResponseBody(body, "")
	assert.JSONEq(t, `{"result":"https://cdn.example/video.mp4","value":"[redacted-data-url]"}`, string(redacted))
}

func TestRedactMidjourneyResponseBodyDropsUnsafeURLValuesAndMasksDiagnostics(t *testing.T) {
	body := []byte(`{"imageUrl":"javascript:alert(1)","videoUrl":"file:///etc/passwd","error_message":"failed https://cdn.example/error?token=secret Authorization: Bearer private"}`)
	redacted := redactMidjourneyResponseBody(body, "")
	assert.JSONEq(t, `{"imageUrl":"","videoUrl":"","error_message":"failed https://***.example/***?token=*** Authorization: Bearer ***"}`, string(redacted))
}

func TestRedactMidjourneyResponseBodyUsesRouteSafeImageTaskID(t *testing.T) {
	previousConfig := setting.GetMidjourneyConfig()
	previousAddress := system_setting.ServerAddress
	setting.UpdateMidjourneyConfig(func(config *setting.MidjourneyConfig) {
		config.ForwardURLEnabled = true
	})
	system_setting.ServerAddress = "https://api.example.test/"
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		setting.UpdateMidjourneyConfig(func(config *setting.MidjourneyConfig) {
			*config = previousConfig
		})
	})

	redacted := redactMidjourneyResponseBody(
		[]byte(`{"imageUrl":"https://cdn.example/image.png?signature=private"}`),
		"task/with/slash",
	)
	assert.JSONEq(t, `{"imageUrl":"https://api.example.test/mj/image/~mj1~dGFzay93aXRoL3NsYXNo"}`, string(redacted))
}
