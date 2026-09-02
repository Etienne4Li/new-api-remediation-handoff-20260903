package taskcommon

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeLocalTaskIDCanonicalizesWhitespace(t *testing.T) {
	operationName := "projects/test/locations/us-central1/models/veo-3.0-generate-001/operations/op-1"

	encoded := EncodeLocalTaskID(" \n\t" + operationName + " \r\n")
	assert.Equal(t, EncodeLocalTaskID(operationName), encoded)

	decoded, err := DecodeLocalTaskID(encoded)
	require.NoError(t, err)
	assert.Equal(t, operationName, decoded)
	assert.Empty(t, EncodeLocalTaskID(" \n\t\r "))
}

func TestBuildProxyURLNormalizesBaseAndEscapesTaskID(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = " https://api.example.test/// "
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	assert.Equal(t,
		"https://api.example.test/v1/videos/task%2Fwith%2Fslash/content",
		BuildProxyURL(" task/with/slash "),
	)
}

func TestBuildProxyURLUsesRelativePathWithoutServerAddress(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = ""
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	assert.Equal(t, "/v1/videos/task_123/content", BuildProxyURL("task_123"))
}

func TestNormalizeTaskResultURLValidatesProviderMedia(t *testing.T) {
	got, err := NormalizeTaskResultURL(" https://cdn.example.test/video.mp4?signature=private ")
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.test/video.mp4?signature=private", got)

	for _, value := range []string{
		"data:video/mp4;base64,AAAA",
		"file:///tmp/video.mp4",
		"https://user:secret@cdn.example.test/video.mp4",
		"https://cdn.example.test/video.mp4#fragment",
		strings.Repeat("x", 16<<10+1),
	} {
		t.Run(value[:minInt(len(value), 24)], func(t *testing.T) {
			_, err := NormalizeTaskResultURL(value)
			assert.Error(t, err)
		})
	}

	got, err = NormalizeTaskResultURL("")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestEscapeTaskIDPathSegmentPreventsPathTargetInjection(t *testing.T) {
	got, err := EscapeTaskIDPathSegment(" task/with?query#fragment ")
	require.NoError(t, err)
	assert.Equal(t, "task%2Fwith%3Fquery%23fragment", got)

	for _, value := range []string{"", ".", "..", "task\n42", strings.Repeat("x", 16<<10+1)} {
		_, err := EscapeTaskIDPathSegment(value)
		assert.Error(t, err, "value=%q", value)
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func TestUnmarshalMetadataDoesNotAllowProviderCallbackOverride(t *testing.T) {
	type payload struct {
		CallbackURL string `json:"callback_url,omitempty"`
		Model       string `json:"model,omitempty"`
	}

	for _, key := range []string{"callback_url", "callbackUrl", "CALLBACK_URL"} {
		t.Run(key, func(t *testing.T) {
			var target payload
			err := UnmarshalMetadata(map[string]any{
				key:     "https://attacker.example/callback",
				"model": "attacker-model",
			}, &target)
			require.NoError(t, err)
			assert.Empty(t, target.CallbackURL)
			assert.Empty(t, target.Model)
		})
	}
}
