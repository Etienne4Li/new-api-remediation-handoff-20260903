package geminichat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiGenerateContentRequestToOpenAIChatMapsImageGenerationConfig(t *testing.T) {
	var request dto.GeminiChatRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"contents":[{"role":"user","parts":[{"text":"draw a blue square"}]}],
		"generationConfig":{
			"responseModalities":["TEXT","IMAGE"],
			"imageConfig":{"aspectRatio":"16:9","imageSize":"2K"}
		}
	}`), &request))

	converted, err := GeminiGenerateContentRequestToOpenAIChat(&request, &convmeta.Values{
		UpstreamModelName: "gemini-3-pro-image-preview",
	})
	require.NoError(t, err)

	var modalities []string
	require.NoError(t, json.Unmarshal(converted.Modalities, &modalities))
	assert.Equal(t, []string{"text", "image"}, modalities)

	var extraBody map[string]any
	require.NoError(t, json.Unmarshal(converted.ExtraBody, &extraBody))
	assert.Equal(t, map[string]any{
		"google": map[string]any{
			"image_config": map[string]any{
				"aspect_ratio": "16:9",
				"image_size":   "2K",
			},
		},
	}, extraBody)
}
