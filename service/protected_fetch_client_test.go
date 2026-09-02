package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

type staticSSRFResolver map[string][]net.IPAddr

func (r staticSSRFResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if ips, ok := r[host]; ok {
		return ips, nil
	}
	return nil, fmt.Errorf("unexpected lookup for %s", host)
}

func staticProtection(protection *common.SSRFProtection) func() (*common.SSRFProtection, bool, error) {
	return func() (*common.SSRFProtection, bool, error) {
		return protection, true, nil
	}
}

func testConn(t *testing.T) net.Conn {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		clientConn.Close()
		serverConn.Close()
	})
	return clientConn
}

func configureSSRFTestFetchSetting(t *testing.T) {
	t.Helper()
	original := system_setting.GetFetchSetting()
	t.Cleanup(func() {
		_ = config.GlobalConfig.LoadFromDB(map[string]string{"fetch_setting.enable_ssrf_protection": fmt.Sprintf("%t", original.EnableSSRFProtection), "fetch_setting.allow_private_ip": fmt.Sprintf("%t", original.AllowPrivateIp), "fetch_setting.domain_filter_mode": fmt.Sprintf("%t", original.DomainFilterMode), "fetch_setting.ip_filter_mode": fmt.Sprintf("%t", original.IpFilterMode), "fetch_setting.domain_list": "[]", "fetch_setting.ip_list": "[]", "fetch_setting.allowed_ports": `["80","443"]`, "fetch_setting.apply_ip_filter_for_domain": fmt.Sprintf("%t", original.ApplyIPFilterForDomain)})
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"fetch_setting.enable_ssrf_protection": "true", "fetch_setting.allow_private_ip": "false", "fetch_setting.domain_filter_mode": "false", "fetch_setting.ip_filter_mode": "false", "fetch_setting.domain_list": "[]", "fetch_setting.ip_list": "[]", "fetch_setting.allowed_ports": `["80","443"]`, "fetch_setting.apply_ip_filter_for_domain": "true"}))
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsedURL, err := url.Parse(rawURL)
	require.NoError(t, err)
	return parsedURL
}

func TestProtectedFetchDialerRejectsPrivateReboundAddress(t *testing.T) {
	dialer := &protectedFetchDialer{
		resolver: staticSSRFResolver{
			"safe.example": {{IP: net.ParseIP("127.0.0.1")}},
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			t.Fatalf("dialContext should not be called for blocked address %s", address)
			return nil, nil
		},
		getProtection: staticProtection(&common.SSRFProtection{
			AllowPrivateIp:         false,
			DomainFilterMode:       false,
			IpFilterMode:           false,
			ApplyIPFilterForDomain: true,
		}),
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", "safe.example:80")

	require.Error(t, err)
	require.Nil(t, conn)
	require.Contains(t, err.Error(), "private IP address not allowed")
}

func TestProtectedFetchDialerRejectsMixedResolvedIPs(t *testing.T) {
	var dialed []string
	dialer := &protectedFetchDialer{
		resolver: staticSSRFResolver{
			"safe.example": {
				{IP: net.ParseIP("10.0.0.1")},
				{IP: net.ParseIP("8.8.8.8")},
			},
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return testConn(t), nil
		},
		getProtection: staticProtection(&common.SSRFProtection{
			AllowPrivateIp:         false,
			DomainFilterMode:       false,
			IpFilterMode:           false,
			ApplyIPFilterForDomain: true,
		}),
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", "safe.example:443")
	require.Error(t, err)
	require.Nil(t, conn)

	require.Empty(t, dialed)
	require.Contains(t, err.Error(), "private IP address not allowed")
}

func TestProtectedFetchDialerDialsWhenAllResolvedIPsAllowed(t *testing.T) {
	var dialed []string
	dialer := &protectedFetchDialer{
		resolver: staticSSRFResolver{
			"safe.example": {
				{IP: net.ParseIP("8.8.8.8")},
				{IP: net.ParseIP("1.1.1.1")},
			},
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return testConn(t), nil
		},
		getProtection: staticProtection(&common.SSRFProtection{
			AllowPrivateIp:         false,
			DomainFilterMode:       false,
			IpFilterMode:           false,
			ApplyIPFilterForDomain: true,
		}),
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", "safe.example:443")
	require.NoError(t, err)
	require.NotNil(t, conn)

	require.Equal(t, []string{"8.8.8.8:443"}, dialed)
}

func TestProtectedFetchDialerAllowsPrivateIPWhenWhitelisted(t *testing.T) {
	var dialed []string
	dialer := &protectedFetchDialer{
		resolver: staticSSRFResolver{
			"internal.example": {{IP: net.ParseIP("10.1.2.3")}},
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return testConn(t), nil
		},
		getProtection: staticProtection(&common.SSRFProtection{
			AllowPrivateIp:         true,
			DomainFilterMode:       false,
			IpFilterMode:           true,
			IpList:                 []string{"10.0.0.0/8"},
			ApplyIPFilterForDomain: true,
		}),
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", "internal.example:80")
	require.NoError(t, err)
	require.NotNil(t, conn)

	require.Equal(t, []string{"10.1.2.3:80"}, dialed)
}

func TestProtectedFetchDialerSkipsResolvedIPCheckWhenDisabled(t *testing.T) {
	var dialed []string
	dialer := &protectedFetchDialer{
		resolver: staticSSRFResolver{},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return testConn(t), nil
		},
		getProtection: staticProtection(&common.SSRFProtection{
			AllowPrivateIp:         false,
			DomainFilterMode:       false,
			IpFilterMode:           false,
			ApplyIPFilterForDomain: false,
		}),
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", "safe.example:80")
	require.NoError(t, err)
	require.NotNil(t, conn)

	require.Equal(t, []string{"safe.example:80"}, dialed)
}

func TestGetSSRFProtectedHTTPClientFallsBackToDefaultClientWhenProtectionDisabled(t *testing.T) {
	originalFetchSetting := *system_setting.GetFetchSetting()
	originalHTTPClient := httpClient
	originalProtectedClient := ssrfProtectedHTTPClient
	t.Cleanup(func() {
		_ = config.GlobalConfig.LoadFromDB(map[string]string{"fetch_setting.enable_ssrf_protection": fmt.Sprintf("%t", originalFetchSetting.EnableSSRFProtection)})
		httpClient = originalHTTPClient
		ssrfProtectedHTTPClient = originalProtectedClient
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"fetch_setting.enable_ssrf_protection": "false"}))
	expected := &http.Client{}
	httpClient = expected
	ssrfProtectedHTTPClient = &http.Client{}

	require.Same(t, expected, GetSSRFProtectedHTTPClient())
}

func TestProtectedFetchRoundTripperUsesConfiguredProxy(t *testing.T) {
	configureSSRFTestFetchSetting(t)
	proxyURL := mustParseURL(t, "http://127.0.0.1:3128")
	var dialed []string
	client := newProtectedFetchHTTPClientWithProxy(
		staticSSRFResolver{},
		func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return nil, errors.New("stop after proxy dial")
		},
		staticProtection(&common.SSRFProtection{
			AllowPrivateIp:         false,
			DomainFilterMode:       false,
			IpFilterMode:           false,
			ApplyIPFilterForDomain: true,
		}),
		func(req *http.Request) (*url.URL, error) {
			return proxyURL, nil
		},
	)
	req, err := http.NewRequest(http.MethodGet, "http://93.184.216.34/resource", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Equal(t, []string{"127.0.0.1:3128"}, dialed)
}

func TestProtectedFetchRoundTripperRejectsPrivateTargetBeforeProxy(t *testing.T) {
	configureSSRFTestFetchSetting(t)
	proxyURL := mustParseURL(t, "http://127.0.0.1:3128")
	var dialed []string
	client := newProtectedFetchHTTPClientWithProxy(
		staticSSRFResolver{},
		func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return nil, errors.New("proxy should not be dialed")
		},
		staticProtection(&common.SSRFProtection{
			AllowPrivateIp:         false,
			DomainFilterMode:       false,
			IpFilterMode:           false,
			ApplyIPFilterForDomain: true,
		}),
		func(req *http.Request) (*url.URL, error) {
			return proxyURL, nil
		},
	)
	req, err := http.NewRequest(http.MethodGet, "http://localhost/resource", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "private IP address not allowed")
	require.Empty(t, dialed)
}

func TestProtectedFetchRoundTripperNoProxyUsesProtectedDialer(t *testing.T) {
	configureSSRFTestFetchSetting(t)
	var dialed []string
	client := newProtectedFetchHTTPClientWithProxy(
		staticSSRFResolver{},
		func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return nil, errors.New("unexpected direct dial")
		},
		staticProtection(&common.SSRFProtection{
			AllowPrivateIp:         false,
			DomainFilterMode:       false,
			IpFilterMode:           false,
			ApplyIPFilterForDomain: true,
		}),
		func(req *http.Request) (*url.URL, error) {
			return nil, nil
		},
	)
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/resource", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Contains(t, err.Error(), "private IP address not allowed")
	require.Empty(t, dialed)
}

func TestProtectedFetchRoundTripperReusesTransportPerProxy(t *testing.T) {
	client := newProtectedFetchHTTPClientWithDialer(nil, nil, nil)
	roundTripper, ok := client.Transport.(*ssrfProtectedRoundTripper)
	require.True(t, ok)

	direct := roundTripper.transportFor(nil)
	directAgain := roundTripper.transportFor(nil)
	proxied := roundTripper.transportFor(mustParseURL(t, "http://127.0.0.1:3128"))

	require.Same(t, direct, directAgain)
	require.NotSame(t, direct, proxied)
	require.True(t, direct.ForceAttemptHTTP2)
	require.False(t, direct.DisableKeepAlives)
}
