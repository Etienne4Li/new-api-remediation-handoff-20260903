package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMidjourneyVideoURLsValidatesAndNormalizesEntries(t *testing.T) {
	raw, err := common.Marshal([]dto.ImgUrls{{Url: " https://cdn.example/one.mp4?sig=one "}, {Url: "https://cdn.example/two.mp4?sig=two"}})
	require.NoError(t, err)

	got, err := ParseMidjourneyVideoURLs(string(raw))
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "https://cdn.example/one.mp4?sig=one", got[0].Url)
	assert.Equal(t, "https://cdn.example/two.mp4?sig=two", got[1].Url)
}

func TestParseMidjourneyVideoURLsRejectsInvalidBounds(t *testing.T) {
	tooMany := make([]dto.ImgUrls, MaxMidjourneyVideoURLCount+1)
	for index := range tooMany {
		tooMany[index].Url = "https://cdn.example/video.mp4"
	}
	encoded, err := common.Marshal(tooMany)
	require.NoError(t, err)

	for name, raw := range map[string]string{
		"malformed":   "not-json",
		"empty entry": `[{"url":""}]`,
		"invalid URL": `[{"url":"file:///etc/passwd"}]`,
		"too many":    string(encoded),
		"too large":   strings.Repeat("x", MaxMidjourneyVideoURLsJSONBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseMidjourneyVideoURLs(raw)
			require.Error(t, err)
		})
	}
}

func TestBuildMidjourneyMediaProxyURLsUseRouteSafeTaskID(t *testing.T) {
	const encodedID = "~mj1~dGFzay93aXRoL3NsYXNo"
	assert.Equal(t, "/mj/image/"+encodedID, BuildMidjourneyImageProxyURL("", "task/with/slash"))
	assert.Equal(t, "/mj/video/"+encodedID, BuildMidjourneyVideoProxyURL("", "task/with/slash"))
	assert.Equal(t, "https://api.example.test/mj/video/"+encodedID+"/3", BuildMidjourneyIndexedVideoProxyURL("https://api.example.test///", "task/with/slash", 3))
	assert.Equal(t, "/mj/video/plain-task-id", BuildMidjourneyVideoProxyURL("", "plain-task-id"))

	decoded, err := DecodeMidjourneyProxyTaskID(encodedID)
	require.NoError(t, err)
	assert.Equal(t, "task/with/slash", decoded)
	decoded, err = DecodeMidjourneyProxyTaskID("plain-task-id")
	require.NoError(t, err)
	assert.Equal(t, "plain-task-id", decoded)
	_, err = DecodeMidjourneyProxyTaskID("~mj1~not*base64")
	require.Error(t, err)
}
