package common

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustJSON(t *testing.T, value any) string {
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func TestRelayInfoConvOptionsUsesNormalizedGeminiSafetySettings(t *testing.T) {
	original := model_setting.GetGeminiSettings().SafetySettings
	t.Cleanup(func() {
		_ = config.GlobalConfig.LoadFromDB(map[string]string{"gemini.safety_settings": mustJSON(t, original)})
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"gemini.safety_settings": `{"HARM_CATEGORY_HATE_SPEECH":"","HARM_CATEGORY_DANGEROUS_CONTENT":"BLOCK_ONLY_HIGH"}`}))

	options := (&RelayInfo{}).ConvOptions()

	assert.Equal(t, "OFF", options.Gemini.SafetySetting("HARM_CATEGORY_HATE_SPEECH"))
	assert.Equal(t, "BLOCK_ONLY_HIGH", options.Gemini.SafetySetting("HARM_CATEGORY_DANGEROUS_CONTENT"))
}
