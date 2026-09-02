package console_setting

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func uptimeGroupsJSON(t *testing.T, rawURL string) string {
	t.Helper()
	encoded, err := common.Marshal([]map[string]interface{}{{
		"categoryName": "Primary",
		"url":          rawURL,
		"slug":         "public-status",
	}})
	require.NoError(t, err)
	return string(encoded)
}

func TestValidateUptimeKumaGroupsRejectsUnsafeBaseURLs(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		message string
	}{
		{name: "userinfo", rawURL: "https://user:secret@status.example.test", message: "userinfo"},
		{name: "fragment", rawURL: "https://status.example.test/#private", message: "fragment"},
		{name: "query", rawURL: "https://status.example.test/?token=private", message: "查询参数"},
		{name: "empty port", rawURL: "https://status.example.test:", message: "port"},
		{name: "invalid port", rawURL: "https://status.example.test:65536", message: "port"},
		{name: "non HTTP scheme", rawURL: "file:///etc/passwd", message: "scheme"},
		{name: "leading whitespace", rawURL: " https://status.example.test", message: "空白"},
		{name: "control character", rawURL: "https://status.example.test/\x7f", message: "control"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateConsoleSettings(uptimeGroupsJSON(t, test.rawURL), "uptime_kuma_groups")
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), strings.ToLower(test.message))
		})
	}
}

func TestValidateUptimeKumaGroupsAcceptsStrictHTTPBaseURL(t *testing.T) {
	require.NoError(t, ValidateConsoleSettings(
		uptimeGroupsJSON(t, "https://[2001:4860:4860::8888]:8443/status"),
		"uptime_kuma_groups",
	))
}

func TestApplyConsoleConfigUsesPersistedLowercaseKeys(t *testing.T) {
	candidate := defaultConsoleSetting
	groups := uptimeGroupsJSON(t, "https://status.example.test")

	require.NoError(t, applyConsoleConfig(&candidate, map[string]string{
		"uptime_kuma_groups":  groups,
		"uptime_kuma_enabled": "false",
	}))
	require.Equal(t, groups, candidate.UptimeKumaGroups)
	require.False(t, candidate.UptimeKumaEnabled)
}
