package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newManagementTestContext(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx, recorder
}

func decodeManagementResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload
}

func TestCreateLogCleanupSystemTaskRejectsMalformedTimestamp(t *testing.T) {
	for _, raw := range []string{"not-a-timestamp", "0", "-1"} {
		t.Run(raw, func(t *testing.T) {
			ctx, recorder := newManagementTestContext(http.MethodPost, "/api/system-task/log-cleanup?target_timestamp="+raw, "")
			CreateLogCleanupSystemTask(ctx)

			assert.Equal(t, http.StatusOK, recorder.Code)
			payload := decodeManagementResponse(t, recorder)
			assert.Equal(t, false, payload["success"])
			assert.Equal(t, "target timestamp must be a positive integer", payload["message"])
		})
	}
}

func TestListSystemTasksRejectsMalformedLimit(t *testing.T) {
	for _, raw := range []string{"not-a-limit", "0", "-5"} {
		t.Run(raw, func(t *testing.T) {
			ctx, recorder := newManagementTestContext(http.MethodGet, "/api/system-task/list?limit="+raw, "")
			ListSystemTasks(ctx)

			assert.Equal(t, http.StatusOK, recorder.Code)
			payload := decodeManagementResponse(t, recorder)
			assert.Equal(t, false, payload["success"])
			assert.Equal(t, "limit must be a positive integer", payload["message"])
		})
	}
}

func TestPerfMetricsRejectsMalformedHours(t *testing.T) {
	for _, raw := range []string{"not-an-hours-value", "0", "-2"} {
		t.Run(raw, func(t *testing.T) {
			for _, test := range []struct {
				path    string
				handler gin.HandlerFunc
			}{
				{path: "/api/perf-metrics/summary?hours=" + raw, handler: GetPerfMetricsSummary},
				{path: "/api/perf-metrics?model=test&hours=" + raw, handler: GetPerfMetrics},
			} {
				ctx, recorder := newManagementTestContext(http.MethodGet, test.path, "")
				test.handler(ctx)

				assert.Equal(t, http.StatusOK, recorder.Code)
				payload := decodeManagementResponse(t, recorder)
				assert.Equal(t, false, payload["success"])
				assert.Equal(t, "hours must be a positive integer", payload["message"])
			}
		})
	}
}

func TestSyncUpstreamModelsRejectsMalformedJSON(t *testing.T) {
	ctx, recorder := newManagementTestContext(http.MethodPost, "/api/models/sync_upstream", "{")
	SyncUpstreamModels(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeManagementResponse(t, recorder)
	assert.Equal(t, false, payload["success"])
	assert.NotEmpty(t, payload["message"])
}

func TestSyncUpstreamModelsRejectsVendorFetchFailure(t *testing.T) {
	originalDB := model.DB
	originalMainDBType := common.MainDatabaseType()
	originalLogDBType := common.LogDatabaseType()
	t.Cleanup(func() { common.SetDatabaseTypes(originalMainDBType, originalLogDBType) })
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Ability{}, &model.Model{}, &model.Vendor{}))
	require.NoError(t, db.Create(&model.Ability{
		Group:     "default",
		Model:     "sync-test-model",
		ChannelId: 1,
		Enabled:   true,
	}).Error)
	model.DB = db
	t.Cleanup(func() {
		model.DB = originalDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/vendors.json") {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"model_name":"sync-test-model","vendor_name":"Acme","status":1,"name_rule":0}]}`))
	}))
	defer server.Close()
	t.Setenv("SYNC_UPSTREAM_BASE", server.URL)
	t.Setenv("SYNC_HTTP_RETRY", "1")
	t.Setenv("SYNC_HTTP_TIMEOUT_SECONDS", "1")

	ctx, recorder := newManagementTestContext(http.MethodPost, "/api/models/sync_upstream", "{}")
	SyncUpstreamModels(ctx)

	assert.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeManagementResponse(t, recorder)
	assert.Equal(t, false, payload["success"])
	assert.Contains(t, payload["message"], "上游元数据")

	var count int64
	require.NoError(t, db.Model(&model.Model{}).Where("model_name = ?", "sync-test-model").Count(&count).Error)
	assert.Zero(t, count)
}

func setupMetadataDeleteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := model.DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Model{}, &model.Vendor{}, &model.PrefillGroup{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = originalDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestMetadataDeletesRejectMissingRecords(t *testing.T) {
	setupMetadataDeleteTestDB(t)
	tests := []struct {
		name    string
		handler gin.HandlerFunc
		message string
	}{
		{name: "model", handler: DeleteModelMeta, message: "模型不存在"},
		{name: "vendor", handler: DeleteVendorMeta, message: "供应商不存在"},
		{name: "prefill", handler: DeletePrefillGroup, message: "预填组不存在"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, recorder := newManagementTestContext(http.MethodDelete, "/metadata/999", "")
			ctx.Params = gin.Params{{Key: "id", Value: "999"}}
			test.handler(ctx)

			assert.Equal(t, http.StatusOK, recorder.Code)
			payload := decodeManagementResponse(t, recorder)
			assert.Equal(t, false, payload["success"])
			assert.Equal(t, test.message, payload["message"])
		})
	}
}

func TestEnrichModelsPreservesDatabaseFailure(t *testing.T) {
	originalDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:enrich-models-closed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.DB = db
	t.Cleanup(func() { model.DB = originalDB })

	err = enrichModels([]*model.Model{{ModelName: "model-a", NameRule: model.NameRuleExact}})
	require.Error(t, err)
}
