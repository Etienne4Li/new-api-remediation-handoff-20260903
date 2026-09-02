package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func uptimeTestServer(t *testing.T, requestCount *atomic.Int32, monitorCount int) *httptest.Server {
	t.Helper()
	type monitor struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	monitors := make([]monitor, monitorCount)
	for i := range monitors {
		monitors[i] = monitor{ID: i + 1, Name: strings.Repeat("监", maxUptimeProviderNameCharacters+10)}
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var payload interface{}
		switch {
		case strings.Contains(r.URL.Path, "/heartbeat/"):
			payload = map[string]interface{}{
				"heartbeatList": map[string]interface{}{"1": []map[string]int{{"status": 1}}},
				"uptimeList":    map[string]float64{"1_24": 2, "2_24": -1},
			}
		default:
			payload = map[string]interface{}{
				"publicGroupList": []interface{}{map[string]interface{}{
					"id":          1,
					"name":        strings.Repeat("group", maxUptimeProviderNameCharacters),
					"monitorList": monitors,
				}},
			}
		}
		encoded, err := common.Marshal(payload)
		if err != nil {
			t.Errorf("marshal uptime response: %v", err)
			return
		}
		if _, err = w.Write(encoded); err != nil {
			t.Errorf("write uptime response: %v", err)
		}
	}))
}

func TestFetchGroupDataBoundsProviderOutput(t *testing.T) {
	var requests atomic.Int32
	server := uptimeTestServer(t, &requests, maxUptimeMonitorsPerGroup+1)
	defer server.Close()

	result := fetchGroupData(context.Background(), server.Client(), map[string]interface{}{
		"url":          server.URL,
		"slug":         "public",
		"categoryName": " Primary ",
	})

	require.Len(t, result.Monitors, maxUptimeMonitorsPerGroup)
	assert.Equal(t, int32(2), requests.Load())
	assert.Equal(t, "Primary", result.CategoryName)
	assert.Len(t, []rune(result.Monitors[0].Name), maxUptimeProviderNameCharacters)
	assert.Len(t, []rune(result.Monitors[0].Group), maxUptimeProviderNameCharacters)
	assert.Equal(t, 1.0, result.Monitors[0].Uptime)
	assert.Equal(t, 0.0, result.Monitors[1].Uptime)
}

func TestUptimeResultCacheCoalescesRepeatedReads(t *testing.T) {
	var requests atomic.Int32
	server := uptimeTestServer(t, &requests, 1)
	defer server.Close()
	groups := []map[string]interface{}{{
		"url":          server.URL,
		"slug":         "public",
		"categoryName": "Primary",
	}}
	cache := &uptimeResultCache{}

	first := cache.get(context.Background(), server.Client(), groups)
	second := cache.get(context.Background(), server.Client(), groups)

	require.Len(t, first, 1)
	require.Len(t, second, 1)
	assert.Equal(t, int32(2), requests.Load())
}

func TestGetAndDecodeRejectsOversizedUptimeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(uptimeResponseBodyLimitBytes+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := getAndDecode(context.Background(), server.Client(), server.URL, &map[string]interface{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "too large")
}

func TestGetUptimeKumaStatusDisablesClientCaching(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/uptime/status", nil)

	// Empty groups return before an outbound client is needed, while still
	// exercising the public response's cache boundary.
	GetUptimeKumaStatus(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "no-store, no-cache, must-revalidate, private, max-age=0", recorder.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", recorder.Header().Get("Pragma"))
	assert.Equal(t, "0", recorder.Header().Get("Expires"))
}
