package middleware

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

func ConfigureTrustedProxies(engine *gin.Engine) error {
	rawTrustedProxies := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if rawTrustedProxies == "" {
		// Trusting an entire private network is not a safe default: any
		// container or host on that network can forge forwarding headers.  A
		// deployment must name its actual reverse-proxy addresses explicitly.
		// Keep the warning actionable while preserving a safe, direct-peer
		// fallback for local development and misconfigured production nodes.
		log.Print("WARNING: TRUSTED_PROXIES is unset or blank; no forwarded client-IP headers are trusted. Configure explicit proxy IPs/CIDRs, or set TRUSTED_PROXIES=none for the same strict direct-peer mode.")
		if err := engine.SetTrustedProxies(nil); err != nil {
			return fmt.Errorf("configure direct-peer mode: %w", err)
		}
		engine.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}
		return nil
	}
	if strings.EqualFold(rawTrustedProxies, "none") {
		if err := engine.SetTrustedProxies(nil); err != nil {
			return fmt.Errorf("configure direct-peer mode: %w", err)
		}
		engine.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}
		return nil
	}

	parts := strings.Split(rawTrustedProxies, ",")
	trustedProxies := make([]string, 0, len(parts))
	for _, part := range parts {
		trustedProxy := strings.TrimSpace(part)
		if trustedProxy == "" {
			return errors.New("TRUSTED_PROXIES contains an empty entry")
		}
		if strings.EqualFold(trustedProxy, "none") {
			return errors.New("TRUSTED_PROXIES=none must be used alone")
		}
		if net.ParseIP(trustedProxy) == nil {
			if _, _, err := net.ParseCIDR(trustedProxy); err != nil {
				return fmt.Errorf("invalid TRUSTED_PROXIES entry %q: expected an IP address or CIDR", trustedProxy)
			}
		}
		trustedProxies = append(trustedProxies, trustedProxy)
	}
	if len(trustedProxies) == 0 {
		return errors.New("TRUSTED_PROXIES does not contain an IP address or CIDR")
	}
	if err := engine.SetTrustedProxies(trustedProxies); err != nil {
		return fmt.Errorf("invalid TRUSTED_PROXIES: %w", err)
	}
	// CF-Connecting-IP is intentionally not a Gin platform header.  It is
	// accepted only after the sanitizer below validates it against the same
	// trusted-peer boundary and rewrites it into X-Forwarded-For.  Keeping Gin's
	// normal two-header list prevents an untrusted request from bypassing the
	// proxy check through a platform-specific header.
	engine.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}
	return nil
}

const maxTrustedForwardedHops = 32

var clientIPHeaderNames = []string{
	"X-Forwarded-For",
	"X-Real-IP",
	"CF-Connecting-IP",
	"Forwarded",
}

// SanitizeProxyHeaders removes client-controlled forwarding metadata before
// any route or middleware consumes Context.ClientIP().  Gin already performs
// a right-to-left X-Forwarded-For walk, but it intentionally accepts the first
// duplicate header value and still exposes X-Real-IP to downstream code.  A
// single request-level sanitizer gives all consumers the same fail-closed
// contract:
//   - an untrusted TCP peer can never influence the effective client IP;
//   - duplicate/invalid identity headers from a trusted peer are discarded;
//   - a valid chain is reduced to one canonical X-Forwarded-For value; and
//   - X-Real-IP/CF-Connecting-IP remain supported as a fallback only when the
//     immediate peer is explicitly trusted.
//
// TRUSTED_PROXIES is read at request time so tests and controlled in-process
// setup may install this middleware before configuring the environment.
// Production configuration is fixed before the server starts and is validated
// by ConfigureTrustedProxies during startup.
func SanitizeProxyHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		networks, err := configuredTrustedProxyNetworks()
		if err != nil {
			clearClientIPHeaders(c.Request)
			c.Next()
			return
		}

		remoteIP := remoteRequestIP(c.Request)
		if remoteIP == nil || !isTrustedProxy(remoteIP, networks) {
			clearClientIPHeaders(c.Request)
			c.Next()
			return
		}

		canonical, present, valid := canonicalForwardedClientIP(c.Request, networks)
		// Gin does not consume RFC 7239 Forwarded; remove it from the request
		// even when the proxy supplied a single well-formed value so downstream
		// handlers cannot accidentally treat it as an independent identity.
		c.Request.Header.Del("Forwarded")
		if !valid {
			clearClientIPHeaders(c.Request)
			c.Next()
			return
		}
		if !present {
			// No identity headers were supplied.  Keep the direct peer.
			c.Next()
			return
		}
		if canonical == "" {
			// A chain containing only trusted hops has no verifiable client.  Do
			// not let Gin's i==0 fallback turn a proxy address into an identity.
			clearClientIPHeaders(c.Request)
			c.Next()
			return
		}

		// Rewrite all accepted forms to one value.  This both prevents later
		// middleware from choosing a different header and removes a forged
		// left-hand XFF prefix from logs or application code that reads headers
		// directly.
		clearClientIPHeaders(c.Request)
		c.Request.Header.Set("X-Forwarded-For", canonical)
		c.Next()
	}
}

func configuredTrustedProxyNetworks() ([]*net.IPNet, error) {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if raw == "" || strings.EqualFold(raw, "none") {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" || strings.EqualFold(value, "none") {
			return nil, errors.New("TRUSTED_PROXIES contains an invalid empty/none entry")
		}
		values = append(values, value)
	}
	return trustedProxyNetworks(values)
}

func trustedProxyNetworks(values []string) ([]*net.IPNet, error) {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("trusted proxy list contains an empty entry")
		}
		if ip := net.ParseIP(value); ip != nil {
			if ipv4 := ip.To4(); ipv4 != nil {
				ip = ipv4
			}
			networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(len(ip)*8, len(ip)*8)})
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q", value)
		}
		networks = append(networks, network)
	}
	return networks, nil
}

func remoteRequestIP(request *http.Request) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

func isTrustedProxy(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func clearClientIPHeaders(request *http.Request) {
	for _, name := range clientIPHeaderNames {
		request.Header.Del(name)
	}
}

// canonicalForwardedClientIP validates all supported identity headers and
// returns (canonical value, any header present, valid).  X-Forwarded-For is
// authoritative when present; the single-value headers are compatibility
// fallbacks for explicitly trusted proxies and must agree with it when both
// forms are supplied.
func canonicalForwardedClientIP(request *http.Request, networks []*net.IPNet) (string, bool, bool) {
	xff, xffPresent, ok := oneHeader(request, "X-Forwarded-For")
	if !ok {
		return "", true, false
	}
	realIP, realPresent, ok := oneHeader(request, "X-Real-IP")
	if !ok {
		return "", true, false
	}
	cfIP, cfPresent, ok := oneHeader(request, "CF-Connecting-IP")
	if !ok {
		return "", true, false
	}
	// The standardized Forwarded header is not consumed by Gin.  A duplicate
	// value is ambiguous, but a single value is simply removed below so common
	// proxies that emit it do not lose a valid XFF identity.
	if values := request.Header.Values("Forwarded"); len(values) > 1 {
		return "", true, false
	}

	var canonical string
	if xffPresent {
		canonical, ok = parseForwardedFor(xff, networks)
		if !ok {
			return "", true, false
		}
	}

	var fallback string
	for _, value := range []struct {
		value   string
		present bool
	}{
		{realIP, realPresent},
		{cfIP, cfPresent},
	} {
		if !value.present {
			continue
		}
		parsed := strings.TrimSpace(value.value)
		if parsed == "" || strings.Contains(parsed, ",") || hasHeaderControl(parsed) || net.ParseIP(parsed) == nil {
			return "", true, false
		}
		parsed = net.ParseIP(parsed).String()
		if fallback != "" && fallback != parsed {
			return "", true, false
		}
		fallback = parsed
	}

	if xffPresent {
		if canonical != "" && fallback != "" && canonical != fallback {
			return "", true, false
		}
		if canonical == "" && fallback != "" {
			// An all-trusted XFF chain and a separate fallback disagree on
			// whether a client hop exists.  Reject rather than choose one.
			return "", true, false
		}
		return canonical, true, true
	}
	return fallback, fallback != "", true
}

func oneHeader(request *http.Request, name string) (string, bool, bool) {
	values := request.Header.Values(name)
	if len(values) > 1 {
		return "", true, false
	}
	if len(values) == 0 {
		return "", false, true
	}
	return values[0], true, true
}

func parseForwardedFor(value string, networks []*net.IPNet) (string, bool) {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > maxTrustedForwardedHops {
		return "", false
	}
	addresses := make([]net.IP, len(parts))
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || hasHeaderControl(part) {
			return "", false
		}
		addresses[index] = net.ParseIP(part)
		if addresses[index] == nil {
			return "", false
		}
	}
	for index := len(addresses) - 1; index >= 0; index-- {
		if !isTrustedProxy(addresses[index], networks) {
			return addresses[index].String(), true
		}
	}
	return "", true
}

func hasHeaderControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}
