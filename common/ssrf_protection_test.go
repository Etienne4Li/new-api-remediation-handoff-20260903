package common

import (
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSSRFProtectionRejectsLiteralPrivateAndReservedIPs(t *testing.T) {
	protection := &SSRFProtection{
		AllowPrivateIp:   false,
		DomainFilterMode: false,
		IpFilterMode:     false,
	}

	tests := []string{
		"127.0.0.1",
		"10.0.0.1",
		"169.254.169.254",
		"fc00::1",
		"::ffff:127.0.0.1",
	}
	for _, host := range tests {
		t.Run(host, func(t *testing.T) {
			require.Error(t, protection.ValidateNetworkTarget(host, 80))
		})
	}
}

func TestSSRFProtectionAllowsPrivateIPWhenExplicitlyEnabled(t *testing.T) {
	protection := &SSRFProtection{
		AllowPrivateIp:   true,
		DomainFilterMode: false,
		IpFilterMode:     false,
	}

	require.NoError(t, protection.ValidateNetworkTarget("10.0.0.1", 80))
}

func TestSSRFProtectionRejectsResolvedPrivateIP(t *testing.T) {
	protection := &SSRFProtection{
		AllowPrivateIp:         false,
		DomainFilterMode:       false,
		IpFilterMode:           false,
		ApplyIPFilterForDomain: true,
	}

	require.NoError(t, protection.ValidateNetworkTarget("example.com", 80))
	require.Error(t, protection.ValidateResolvedIP("example.com", net.ParseIP("169.254.169.254")))
}

func TestNewSSRFProtectionFromFetchSettingParsesPortRanges(t *testing.T) {
	protection, err := NewSSRFProtectionFromFetchSetting(false, false, false, nil, nil, []string{"80", "8000-8001"}, true)
	require.NoError(t, err)

	require.NoError(t, protection.ValidateNetworkTarget("example.com", 8001))
	require.Error(t, protection.ValidateNetworkTarget("example.com", 9000))
}

func TestValidateURLWithFetchSettingKeepsSyntaxBoundaryWhenDisabled(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		message string
	}{
		{name: "unsupported scheme", rawURL: "file:///etc/passwd", message: "scheme"},
		{name: "userinfo", rawURL: "https://user:secret@example.test/file", message: "userinfo"},
		{name: "fragment", rawURL: "https://example.test/file#private", message: "fragment"},
		{name: "invalid port", rawURL: "https://example.test:65536/file", message: "port"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateURLWithFetchSetting(test.rawURL, false, true, false, false, nil, nil, nil, false)
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), test.message)
		})
	}

	require.NoError(t, ValidateURLWithFetchSetting(
		"https://example.test/file",
		false,
		true,
		false,
		false,
		nil,
		nil,
		nil,
		false,
	))
}
