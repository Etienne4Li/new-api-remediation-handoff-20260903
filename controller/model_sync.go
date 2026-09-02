package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 上游地址
const (
	upstreamModelsURL  = "https://basellm.github.io/llm-metadata/api/newapi/models.json"
	upstreamVendorsURL = "https://basellm.github.io/llm-metadata/api/newapi/vendors.json"

	// Sync metadata is small and should complete quickly. Keep environment
	// controls finite so a typo cannot create an effectively unbounded response
	// allocation, retry storm, or duration overflow.
	maxSyncHTTPTimeoutSeconds int64 = 24 * 60 * 60
	maxSyncHTTPRetries              = 10
	maxSyncHTTPResponseMB           = 64
)

func normalizeLocale(locale string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(locale))
	switch l {
	case "en":
		return "en", true
	case "zh-cn":
		return "zh-CN", true
	case "zh-tw":
		return "zh-TW", true
	case "ja":
		return "ja", true
	default:
		return "", false
	}
}

func getUpstreamBase() string {
	return common.GetEnvOrDefaultString("SYNC_UPSTREAM_BASE", "https://basellm.github.io/llm-metadata")
}

func getUpstreamURLs(locale string) (modelsURL, vendorsURL string) {
	base := strings.TrimRight(getUpstreamBase(), "/")
	if l, ok := normalizeLocale(locale); ok && l != "" {
		return fmt.Sprintf("%s/api/i18n/%s/newapi/models.json", base, l),
			fmt.Sprintf("%s/api/i18n/%s/newapi/vendors.json", base, l)
	}
	return fmt.Sprintf("%s/api/newapi/models.json", base), fmt.Sprintf("%s/api/newapi/vendors.json", base)
}

type upstreamEnvelope[T any] struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    []T    `json:"data"`
}

type upstreamModel struct {
	Description string          `json:"description"`
	Endpoints   json.RawMessage `json:"endpoints"`
	Icon        string          `json:"icon"`
	ModelName   string          `json:"model_name"`
	NameRule    int             `json:"name_rule"`
	Status      int             `json:"status"`
	Tags        string          `json:"tags"`
	VendorName  string          `json:"vendor_name"`
}

type upstreamVendor struct {
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Name        string `json:"name"`
	Status      int    `json:"status"`
}

var (
	etagCache  = make(map[string]string)
	bodyCache  = make(map[string][]byte)
	cacheMutex sync.RWMutex
)

type overwriteField struct {
	ModelName string   `json:"model_name"`
	Fields    []string `json:"fields"`
}

type syncRequest struct {
	Overwrite []overwriteField `json:"overwrite"`
	Locale    string           `json:"locale"`
}

func newHTTPClient() *http.Client {
	timeout := syncHTTPTimeout(10)
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	if common.TLSInsecureSkipVerify {
		transport.TLSClientConfig = common.InsecureTLSConfig
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		if strings.HasSuffix(host, "github.io") {
			if conn, err := dialer.DialContext(ctx, "tcp4", addr); err == nil {
				return conn, nil
			}
			return dialer.DialContext(ctx, "tcp6", addr)
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return &http.Client{
		Transport: transport,
		// Metadata synchronization is expected to use the configured URL
		// directly. Refusing redirects prevents a compromised endpoint or
		// proxy from silently changing the data source.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func syncHTTPTimeout(defaultSeconds int64) time.Duration {
	return common.GetEnvOrDefaultDurationSeconds(
		"SYNC_HTTP_TIMEOUT_SECONDS", defaultSeconds, 1, maxSyncHTTPTimeoutSeconds,
	)
}

func getSyncHTTPRetryCount() int {
	return common.GetEnvOrDefaultBounded("SYNC_HTTP_RETRY", 3, 1, maxSyncHTTPRetries)
}

func getSyncHTTPResponseLimitMB() int {
	return common.GetEnvOrDefaultBounded("SYNC_HTTP_MAX_MB", 10, 1, maxSyncHTTPResponseMB)
}

var (
	httpClientOnce sync.Once
	httpClient     *http.Client
)

func getHTTPClient() *http.Client {
	httpClientOnce.Do(func() {
		httpClient = newHTTPClient()
	})
	return httpClient
}

func fetchJSON[T any](ctx context.Context, url string, out *upstreamEnvelope[T]) error {
	var lastErr error
	attempts := getSyncHTTPRetryCount()
	baseDelay := 200 * time.Millisecond
	maxMB := getSyncHTTPResponseLimitMB()
	maxBytes := int64(maxMB) << 20
	for attempt := 0; attempt < attempts; attempt++ {
		// Clear data from a prior attempt before decoding a retry response.
		*out = upstreamEnvelope[T]{}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		// ETag conditional request
		cacheMutex.RLock()
		if et := etagCache[url]; et != "" {
			req.Header.Set("If-None-Match", et)
		}
		cacheMutex.RUnlock()

		resp, err := getHTTPClient().Do(req)
		if err != nil {
			lastErr = err
			if attempt+1 < attempts && !waitSyncRetry(ctx, baseDelay, attempt) {
				return ctx.Err()
			}
			continue
		}
		func() {
			defer resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				// Read one sentinel byte past the configured limit so chunked or
				// otherwise inaccurate Content-Length responses are rejected rather
				// than silently truncated and cached as valid metadata.
				buf, err := common.ReadBodyLimited(resp.Body, resp.ContentLength, maxBytes)
				if err != nil {
					lastErr = err
					return
				}
				// cache body and ETag
				cacheMutex.Lock()
				if et := resp.Header.Get("ETag"); et != "" {
					etagCache[url] = et
				}
				bodyCache[url] = buf
				cacheMutex.Unlock()

				// Try decode as envelope first
				if err := common.Unmarshal(buf, out); err != nil {
					// Try decode as pure array
					var arr []T
					if err2 := common.Unmarshal(buf, &arr); err2 != nil {
						lastErr = err
						return
					}
					out.Success = true
					out.Data = arr
					out.Message = ""
				} else {
					if !out.Success && len(out.Data) == 0 && out.Message == "" {
						out.Success = true
					}
				}
				lastErr = nil
			case http.StatusNotModified:
				// use cache
				cacheMutex.RLock()
				buf := bodyCache[url]
				cacheMutex.RUnlock()
				if len(buf) == 0 {
					lastErr = errors.New("cache miss for 304 response")
					return
				}
				if err := common.Unmarshal(buf, out); err != nil {
					var arr []T
					if err2 := common.Unmarshal(buf, &arr); err2 != nil {
						lastErr = err
						return
					}
					out.Success = true
					out.Data = arr
					out.Message = ""
				} else {
					if !out.Success && len(out.Data) == 0 && out.Message == "" {
						out.Success = true
					}
				}
				lastErr = nil
			default:
				lastErr = errors.New(resp.Status)
			}
		}()
		if lastErr == nil {
			return nil
		}
		if attempt+1 < attempts && !waitSyncRetry(ctx, baseDelay, attempt) {
			return ctx.Err()
		}
	}
	return lastErr
}

func waitSyncRetry(ctx context.Context, baseDelay time.Duration, attempt int) bool {
	// attempts is bounded above, so the shift and multiplication remain
	// representable. The timer/select makes cancellation prompt during backoff.
	sleep := baseDelay * time.Duration(1<<attempt)
	jitter := time.Duration(rand.Intn(150)) * time.Millisecond
	timer := time.NewTimer(sleep + jitter)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func ensureVendorID(vendorName string, vendorByName map[string]upstreamVendor, vendorIDCache map[string]int, createdVendors *int) (int, error) {
	if vendorName == "" {
		return 0, nil
	}
	if id, ok := vendorIDCache[vendorName]; ok {
		return id, nil
	}
	var existing model.Vendor
	lookupErr := model.DB.Where("name = ?", vendorName).First(&existing).Error
	if lookupErr == nil {
		vendorIDCache[vendorName] = existing.Id
		return existing.Id, nil
	}
	if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("lookup vendor %q: %w", vendorName, lookupErr)
	}
	uv := vendorByName[vendorName]
	v := &model.Vendor{
		Name:        vendorName,
		Description: uv.Description,
		Icon:        coalesce(uv.Icon, ""),
		Status:      chooseStatus(uv.Status, 1),
	}
	if err := v.Insert(); err == nil {
		*createdVendors++
		vendorIDCache[vendorName] = v.Id
		return v.Id, nil
	} else {
		return 0, fmt.Errorf("create vendor %q: %w", vendorName, err)
	}
}

// SyncUpstreamModels 同步上游模型与供应商：
// - 默认仅创建「未配置模型」
// - 可通过 overwrite 选择性覆盖更新本地已有模型的字段（前提：sync_official <> 0）
func SyncUpstreamModels(c *gin.Context) {
	var req syncRequest
	// An empty body is equivalent to the default request, but malformed JSON
	// must be rejected before any synchronization work starts.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		common.ApiError(c, err)
		return
	}
	// 1) 获取未配置模型列表
	missing, err := model.GetMissingModels()
	if err != nil {
		common.SysError("failed to get missing models: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "获取模型列表失败，请稍后重试"})
		return
	}

	// 若既无缺失模型需要创建，也未指定覆盖更新字段，则无需请求上游数据，直接返回
	if len(missing) == 0 && len(req.Overwrite) == 0 {
		modelsURL, vendorsURL := getUpstreamURLs(req.Locale)
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data": gin.H{
				"created_models":  0,
				"created_vendors": 0,
				"updated_models":  0,
				"skipped_models":  []string{},
				"created_list":    []string{},
				"updated_list":    []string{},
				"source": gin.H{
					"locale":      req.Locale,
					"models_url":  modelsURL,
					"vendors_url": vendorsURL,
				},
			},
		})
		return
	}

	// 2) 拉取上游 vendors 与 models
	ctx, cancel := context.WithTimeout(c.Request.Context(), syncHTTPTimeout(15))
	defer cancel()

	modelsURL, vendorsURL := getUpstreamURLs(req.Locale)
	var vendorsEnv upstreamEnvelope[upstreamVendor]
	var modelsEnv upstreamEnvelope[upstreamModel]
	var modelsErr error
	var vendorsErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		vendorsErr = fetchJSON(ctx, vendorsURL, &vendorsEnv)
	}()
	go func() {
		defer wg.Done()
		modelsErr = fetchJSON(ctx, modelsURL, &modelsEnv)
	}()
	wg.Wait()
	if modelsErr != nil || vendorsErr != nil {
		fetchErr := modelsErr
		if fetchErr == nil {
			fetchErr = vendorsErr
		}
		c.JSON(http.StatusOK, gin.H{"success": false, "message": common.MaskSensitiveInfo("获取上游元数据失败: " + fetchErr.Error()), "locale": req.Locale, "source_urls": gin.H{"models_url": common.MaskSensitiveInfo(modelsURL), "vendors_url": common.MaskSensitiveInfo(vendorsURL)}})
		return
	}

	// 建立映射
	vendorByName := make(map[string]upstreamVendor)
	for _, v := range vendorsEnv.Data {
		if v.Name != "" {
			vendorByName[v.Name] = v
		}
	}
	modelByName := make(map[string]upstreamModel)
	for _, m := range modelsEnv.Data {
		if m.ModelName != "" {
			modelByName[m.ModelName] = m
		}
	}

	// 3) 执行同步：仅创建缺失模型；若上游缺失该模型则跳过
	createdModels := 0
	createdVendors := 0
	updatedModels := 0
	skipped := make([]string, 0)
	createdList := make([]string, 0)
	updatedList := make([]string, 0)

	// 本地缓存：vendorName -> id
	vendorIDCache := make(map[string]int)

	for _, name := range missing {
		up, ok := modelByName[name]
		if !ok {
			skipped = append(skipped, name)
			continue
		}

		// 若本地已存在且设置为不同步，则跳过（极端情况：缺失列表与本地状态不同步时）
		var existing model.Model
		lookupErr := model.DB.Where("model_name = ?", name).First(&existing).Error
		if lookupErr == nil {
			if existing.SyncOfficial == 0 {
				skipped = append(skipped, name)
				continue
			}
		} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			common.ApiError(c, fmt.Errorf("lookup model %q: %w", name, lookupErr))
			return
		}

		// 确保 vendor 存在
		vendorID, vendorErr := ensureVendorID(up.VendorName, vendorByName, vendorIDCache, &createdVendors)
		if vendorErr != nil {
			common.ApiError(c, vendorErr)
			return
		}

		// 创建模型
		mi := &model.Model{
			ModelName:   name,
			Description: up.Description,
			Icon:        up.Icon,
			Tags:        up.Tags,
			VendorID:    vendorID,
			Status:      chooseStatus(up.Status, 1),
			NameRule:    up.NameRule,
		}
		if err := mi.Insert(); err == nil {
			createdModels++
			createdList = append(createdList, name)
		} else {
			common.ApiError(c, fmt.Errorf("create model %q: %w", name, err))
			return
		}
	}

	// 4) 处理可选覆盖（更新本地已有模型的差异字段）
	if len(req.Overwrite) > 0 {
		// vendorIDCache 已用于创建阶段，可复用
		for _, ow := range req.Overwrite {
			up, ok := modelByName[ow.ModelName]
			if !ok {
				continue
			}
			var local model.Model
			if err := model.DB.Where("model_name = ?", ow.ModelName).First(&local).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					common.ApiError(c, fmt.Errorf("lookup model %q: %w", ow.ModelName, err))
					return
				}
				continue
			}

			// 跳过被禁用官方同步的模型
			if local.SyncOfficial == 0 {
				continue
			}

			// 映射 vendor
			newVendorID, vendorErr := ensureVendorID(up.VendorName, vendorByName, vendorIDCache, &createdVendors)
			if vendorErr != nil {
				common.ApiError(c, vendorErr)
				return
			}

			// 应用字段覆盖（事务）
			updated := false
			if err := model.DB.Transaction(func(tx *gorm.DB) error {
				needUpdate := false
				if containsField(ow.Fields, "description") {
					local.Description = up.Description
					needUpdate = true
				}
				if containsField(ow.Fields, "icon") {
					local.Icon = up.Icon
					needUpdate = true
				}
				if containsField(ow.Fields, "tags") {
					local.Tags = up.Tags
					needUpdate = true
				}
				if containsField(ow.Fields, "vendor") {
					local.VendorID = newVendorID
					needUpdate = true
				}
				if containsField(ow.Fields, "name_rule") {
					local.NameRule = up.NameRule
					needUpdate = true
				}
				if containsField(ow.Fields, "status") {
					local.Status = chooseStatus(up.Status, local.Status)
					needUpdate = true
				}
				if !needUpdate {
					return nil
				}
				result := tx.Model(&model.Model{}).Where("id = ?", local.Id).
					Select("description", "icon", "tags", "vendor_id", "name_rule", "status", "updated_time").
					Updates(&local)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 0 {
					return gorm.ErrRecordNotFound
				}
				updated = true
				return nil
			}); err != nil {
				common.ApiError(c, fmt.Errorf("update model %q: %w", ow.ModelName, err))
				return
			}
			if updated {
				updatedModels++
				updatedList = append(updatedList, ow.ModelName)
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"created_models":  createdModels,
			"created_vendors": createdVendors,
			"updated_models":  updatedModels,
			"skipped_models":  skipped,
			"created_list":    createdList,
			"updated_list":    updatedList,
			"source": gin.H{
				"locale":      req.Locale,
				"models_url":  modelsURL,
				"vendors_url": vendorsURL,
			},
		},
	})
}

func containsField(fields []string, key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, f := range fields {
		if strings.ToLower(strings.TrimSpace(f)) == key {
			return true
		}
	}
	return false
}

func coalesce(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func chooseStatus(primary, fallback int) int {
	if primary == 0 && fallback != 0 {
		return fallback
	}
	if primary != 0 {
		return primary
	}
	return 1
}

// SyncUpstreamPreview 预览上游与本地的差异（仅用于弹窗选择）
func SyncUpstreamPreview(c *gin.Context) {
	// 1) 拉取上游数据
	ctx, cancel := context.WithTimeout(c.Request.Context(), syncHTTPTimeout(15))
	defer cancel()

	locale := c.Query("locale")
	modelsURL, vendorsURL := getUpstreamURLs(locale)

	var vendorsEnv upstreamEnvelope[upstreamVendor]
	var modelsEnv upstreamEnvelope[upstreamModel]
	var modelsErr error
	var vendorsErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		vendorsErr = fetchJSON(ctx, vendorsURL, &vendorsEnv)
	}()
	go func() {
		defer wg.Done()
		modelsErr = fetchJSON(ctx, modelsURL, &modelsEnv)
	}()
	wg.Wait()
	if modelsErr != nil || vendorsErr != nil {
		fetchErr := modelsErr
		if fetchErr == nil {
			fetchErr = vendorsErr
		}
		c.JSON(http.StatusOK, gin.H{"success": false, "message": common.MaskSensitiveInfo("获取上游元数据失败: " + fetchErr.Error()), "locale": locale, "source_urls": gin.H{"models_url": common.MaskSensitiveInfo(modelsURL), "vendors_url": common.MaskSensitiveInfo(vendorsURL)}})
		return
	}

	vendorByName := make(map[string]upstreamVendor)
	for _, v := range vendorsEnv.Data {
		if v.Name != "" {
			vendorByName[v.Name] = v
		}
	}
	modelByName := make(map[string]upstreamModel)
	upstreamNames := make([]string, 0, len(modelsEnv.Data))
	for _, m := range modelsEnv.Data {
		if m.ModelName != "" {
			modelByName[m.ModelName] = m
			upstreamNames = append(upstreamNames, m.ModelName)
		}
	}

	// 2) 本地已有模型
	var locals []model.Model
	if len(upstreamNames) > 0 {
		if err := model.DB.Where("model_name IN ? AND sync_official <> 0", upstreamNames).Find(&locals).Error; err != nil {
			common.ApiError(c, err)
			return
		}
	}

	// 本地 vendor 名称映射
	vendorIdSet := make(map[int]struct{})
	for _, m := range locals {
		if m.VendorID != 0 {
			vendorIdSet[m.VendorID] = struct{}{}
		}
	}
	vendorIDs := make([]int, 0, len(vendorIdSet))
	for id := range vendorIdSet {
		vendorIDs = append(vendorIDs, id)
	}
	idToVendorName := make(map[int]string)
	if len(vendorIDs) > 0 {
		var dbVendors []model.Vendor
		if err := model.DB.Where("id IN ?", vendorIDs).Find(&dbVendors).Error; err != nil {
			common.ApiError(c, err)
			return
		}
		for _, v := range dbVendors {
			idToVendorName[v.Id] = v.Name
		}
	}

	// 3) 缺失且上游存在的模型
	missingList, err := model.GetMissingModels()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var missing []string
	for _, name := range missingList {
		if _, ok := modelByName[name]; ok {
			missing = append(missing, name)
		}
	}

	// 4) 计算冲突字段
	type conflictField struct {
		Field    string      `json:"field"`
		Local    interface{} `json:"local"`
		Upstream interface{} `json:"upstream"`
	}
	type conflictItem struct {
		ModelName string          `json:"model_name"`
		Fields    []conflictField `json:"fields"`
	}

	var conflicts []conflictItem
	for _, local := range locals {
		up, ok := modelByName[local.ModelName]
		if !ok {
			continue
		}
		fields := make([]conflictField, 0, 6)
		if strings.TrimSpace(local.Description) != strings.TrimSpace(up.Description) {
			fields = append(fields, conflictField{Field: "description", Local: local.Description, Upstream: up.Description})
		}
		if strings.TrimSpace(local.Icon) != strings.TrimSpace(up.Icon) {
			fields = append(fields, conflictField{Field: "icon", Local: local.Icon, Upstream: up.Icon})
		}
		if strings.TrimSpace(local.Tags) != strings.TrimSpace(up.Tags) {
			fields = append(fields, conflictField{Field: "tags", Local: local.Tags, Upstream: up.Tags})
		}
		// vendor 对比使用名称
		localVendor := idToVendorName[local.VendorID]
		if strings.TrimSpace(localVendor) != strings.TrimSpace(up.VendorName) {
			fields = append(fields, conflictField{Field: "vendor", Local: localVendor, Upstream: up.VendorName})
		}
		if local.NameRule != up.NameRule {
			fields = append(fields, conflictField{Field: "name_rule", Local: local.NameRule, Upstream: up.NameRule})
		}
		if local.Status != chooseStatus(up.Status, local.Status) {
			fields = append(fields, conflictField{Field: "status", Local: local.Status, Upstream: up.Status})
		}
		if len(fields) > 0 {
			conflicts = append(conflicts, conflictItem{ModelName: local.ModelName, Fields: fields})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"missing":   missing,
			"conflicts": conflicts,
			"source": gin.H{
				"locale":      locale,
				"models_url":  modelsURL,
				"vendors_url": vendorsURL,
			},
		},
	})
}
