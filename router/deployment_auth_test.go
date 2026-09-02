package router

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

func setupDeploymentAuthTest(t *testing.T) (*gin.Engine, string, string) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()

	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	model.LOG_DB = db

	createUser := func(username, accessToken string, role int) {
		user := model.User{
			Username:    username,
			Password:    "unused-password-hash",
			Role:        role,
			Status:      common.UserStatusEnabled,
			Group:       "default",
			AuthVersion: 1,
			AffCode:     "deployment-auth-" + username,
		}
		user.SetAccessToken(accessToken)
		require.NoError(t, db.Create(&user).Error)
	}

	adminToken := "deployment-admin-pat"
	rootToken := "deployment-root-pat"
	createUser("deployment-admin", adminToken, common.RoleAdminUser)
	createUser("deployment-root", rootToken, common.RoleRootUser)

	engine := gin.New()
	SetApiRouter(engine)

	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	return engine, adminToken, rootToken
}

func requestDeploymentSettings(router http.Handler, accessToken string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/deployments/settings", nil)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestDeploymentRoutesRequireRootRole(t *testing.T) {
	router, adminToken, rootToken := setupDeploymentAuthTest(t)

	adminResponse := requestDeploymentSettings(router, adminToken)
	assert.Equal(t, http.StatusForbidden, adminResponse.Code)
	assert.Contains(t, adminResponse.Body.String(), "AUTH_INSUFFICIENT_PRIVILEGE")

	rootResponse := requestDeploymentSettings(router, rootToken)
	require.Equal(t, http.StatusOK, rootResponse.Code)
	assert.Contains(t, rootResponse.Body.String(), `"success":true`)
}
