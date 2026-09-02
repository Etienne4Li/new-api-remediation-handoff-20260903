package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/console_setting"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

const (
	uptimeRequestTimeout            = 30 * time.Second
	uptimeHTTPTimeout               = 10 * time.Second
	uptimeStatusCacheTTL            = 15 * time.Second
	uptimeResponseBodyLimitBytes    = 1 << 20
	maxUptimeMonitorsPerGroup       = 1000
	maxUptimeProviderNameCharacters = 200
	uptimeKeySuffix                 = "_24"
	apiStatusPath                   = "/api/status-page/"
	apiHeartbeatPath                = "/api/status-page/heartbeat/"
)

type uptimeResultCache struct {
	sync.Mutex
	key       string
	results   []UptimeGroupResult
	expiresAt time.Time
}

var cachedUptimeResults uptimeResultCache

type Monitor struct {
	Name   string  `json:"name"`
	Uptime float64 `json:"uptime"`
	Status int     `json:"status"`
	Group  string  `json:"group,omitempty"`
}

type UptimeGroupResult struct {
	CategoryName string    `json:"categoryName"`
	Monitors     []Monitor `json:"monitors"`
}

func getAndDecode(ctx context.Context, client *http.Client, url string, dest interface{}) error {
	if client == nil {
		return errors.New("uptime HTTP client is unavailable")
	}
	if err := common.ValidateHTTPURL(url); err != nil {
		return fmt.Errorf("invalid uptime URL: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, uptimeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("non-200 status")
	}

	body, err := service.ReadProviderResponseBody(resp, uptimeResponseBodyLimitBytes)
	if err != nil {
		return err
	}
	return common.Unmarshal(body, dest)
}

func truncateUptimeProviderName(value string) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if len(characters) > maxUptimeProviderNameCharacters {
		characters = characters[:maxUptimeProviderNameCharacters]
	}
	return string(characters)
}

func normalizeUptime(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func fetchGroupData(ctx context.Context, client *http.Client, groupConfig map[string]interface{}) UptimeGroupResult {
	url, _ := groupConfig["url"].(string)
	slug, _ := groupConfig["slug"].(string)
	categoryName, _ := groupConfig["categoryName"].(string)

	result := UptimeGroupResult{
		CategoryName: truncateUptimeProviderName(categoryName),
		Monitors:     []Monitor{},
	}

	if url == "" || slug == "" {
		return result
	}

	baseURL := strings.TrimSuffix(url, "/")

	var statusData struct {
		PublicGroupList []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			MonitorList []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"monitorList"`
		} `json:"publicGroupList"`
	}

	var heartbeatData struct {
		HeartbeatList map[string][]struct {
			Status int `json:"status"`
		} `json:"heartbeatList"`
		UptimeList map[string]float64 `json:"uptimeList"`
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return getAndDecode(gCtx, client, baseURL+apiStatusPath+slug, &statusData)
	})
	g.Go(func() error {
		return getAndDecode(gCtx, client, baseURL+apiHeartbeatPath+slug, &heartbeatData)
	})

	if g.Wait() != nil {
		return result
	}

	for _, pg := range statusData.PublicGroupList {
		if len(pg.MonitorList) == 0 {
			continue
		}

		for _, m := range pg.MonitorList {
			if len(result.Monitors) >= maxUptimeMonitorsPerGroup {
				return result
			}
			monitor := Monitor{
				Name:  truncateUptimeProviderName(m.Name),
				Group: truncateUptimeProviderName(pg.Name),
			}

			monitorID := strconv.Itoa(m.ID)

			if uptime, exists := heartbeatData.UptimeList[monitorID+uptimeKeySuffix]; exists {
				monitor.Uptime = normalizeUptime(uptime)
			}

			if heartbeats, exists := heartbeatData.HeartbeatList[monitorID]; exists && len(heartbeats) > 0 {
				monitor.Status = heartbeats[0].Status
			}

			result.Monitors = append(result.Monitors, monitor)
		}
	}

	return result
}

func fetchUptimeGroupResults(ctx context.Context, client *http.Client, groups []map[string]interface{}) []UptimeGroupResult {
	results := make([]UptimeGroupResult, len(groups))
	g, gCtx := errgroup.WithContext(ctx)
	for i, group := range groups {
		i, group := i, group
		g.Go(func() error {
			results[i] = fetchGroupData(gCtx, client, group)
			return nil
		})
	}
	_ = g.Wait()
	return results
}

func uptimeGroupsCacheKey(groups []map[string]interface{}) string {
	encoded, err := common.Marshal(groups)
	if err != nil {
		return ""
	}
	return common.GenerateHMAC(string(encoded))
}

func (cache *uptimeResultCache) get(ctx context.Context, client *http.Client, groups []map[string]interface{}) []UptimeGroupResult {
	key := uptimeGroupsCacheKey(groups)
	cache.Lock()
	defer cache.Unlock()

	now := time.Now()
	if key != "" && key == cache.key && now.Before(cache.expiresAt) {
		return cache.results
	}
	if ctx.Err() != nil {
		return []UptimeGroupResult{}
	}

	results := fetchUptimeGroupResults(ctx, client, groups)
	if key != "" && ctx.Err() == nil {
		cache.key = key
		cache.results = results
		cache.expiresAt = time.Now().Add(uptimeStatusCacheTTL)
	}
	return results
}

func GetUptimeKumaStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	if !console_setting.GetConsoleSetting().UptimeKumaEnabled {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": []UptimeGroupResult{}})
		return
	}

	groups := console_setting.GetUptimeKumaGroups()
	if len(groups) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": []UptimeGroupResult{}})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), uptimeRequestTimeout)
	defer cancel()

	client := service.GetSSRFProtectedHTTPClient()
	if client == nil {
		common.ApiErrorMsg(c, "uptime HTTP client is unavailable")
		return
	}
	results := cachedUptimeResults.get(ctx, client, groups)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": results})
}
