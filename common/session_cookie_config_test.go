package common

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionCookieConfigPublishesDetachedSnapshots(t *testing.T) {
	previous := GetSessionCookieConfig()
	t.Cleanup(func() {
		SetSessionCookieConfig(previous.Secure, previous.TrustedURLs)
	})

	trusted := []string{"https://panel.example.com", "https://admin.example.com"}
	SetSessionCookieConfig(true, trusted)
	trusted[0] = "https://attacker.example.com"

	snapshot := GetSessionCookieConfig()
	require.True(t, snapshot.Secure)
	require.Equal(t, []string{"https://panel.example.com", "https://admin.example.com"}, snapshot.TrustedURLs)

	// A caller mutating its detached snapshot must not alter the published
	// policy used by subsequent requests.
	snapshot.TrustedURLs[0] = "https://mutated.example.com"
	require.Equal(t, "https://panel.example.com", GetSessionCookieConfig().TrustedURLs[0])
}

func TestSessionCookieConfigConcurrentPublishAndRead(t *testing.T) {
	previous := GetSessionCookieConfig()
	t.Cleanup(func() {
		SetSessionCookieConfig(previous.Secure, previous.TrustedURLs)
	})

	first := SessionCookieConfig{Secure: true, TrustedURLs: []string{"https://first.example.com"}}
	second := SessionCookieConfig{Secure: false, TrustedURLs: []string{"https://second.example.com"}}
	SetSessionCookieConfig(first.Secure, first.TrustedURLs)

	start := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		<-start
		for i := 0; i < 512; i++ {
			value := first
			if i%2 == 1 {
				value = second
			}
			UpdateSessionCookieConfig(func(cfg *SessionCookieConfig) {
				cfg.Secure = value.Secure
				cfg.TrustedURLs = value.TrustedURLs
			})
		}
	}()

	seen := make(chan SessionCookieConfig, 512)
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		defer close(seen)
		<-start
		for i := 0; i < 512; i++ {
			seen <- GetSessionCookieConfig()
		}
	}()

	close(start)
	writers.Wait()
	readers.Wait()
	for got := range seen {
		if !sessionCookieConfigEqual(got, first) && !sessionCookieConfigEqual(got, second) {
			t.Fatalf("mixed session-cookie snapshot: %+v", got)
		}
	}
}

func sessionCookieConfigEqual(a, b SessionCookieConfig) bool {
	if a.Secure != b.Secure || len(a.TrustedURLs) != len(b.TrustedURLs) {
		return false
	}
	for i := range a.TrustedURLs {
		if a.TrustedURLs[i] != b.TrustedURLs[i] {
			return false
		}
	}
	return true
}
