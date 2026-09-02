package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupManageUserTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.UserSession{}, &model.Log{}, &model.CasbinRule{}, &model.AuthzRole{},
		&model.QuotaCacheRepair{},
	))

	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func setupManageUserRedisTestDB(t *testing.T) (*gorm.DB, *miniredis.Miniredis) {
	t.Helper()
	db := setupManageUserTestDB(t)
	server := miniredis.RunT(t)
	previousRDB := common.RDB
	previousRedisEnabled := common.RedisEnabled
	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	common.RedisEnabled = true
	common.BatchUpdateEnabled = false
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = previousRDB
		common.RedisEnabled = previousRedisEnabled
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
	})
	return db, server
}

func performManageUserRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 9999)
	c.Set("role", common.RoleRootUser)
	c.Set("username", "root-operator")
	ManageUser(c)
	return recorder
}

func performCreateUserRequest(t *testing.T, actorRole int, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 9999)
	c.Set("role", actorRole)
	c.Set("username", "admin-operator")
	CreateUser(c)
	return recorder
}

func TestCreateUserRejectsUnknownRoles(t *testing.T) {
	db := setupManageUserTestDB(t)

	for _, role := range []int{-1, 2, 9, 42} {
		t.Run(fmt.Sprintf("role_%d", role), func(t *testing.T) {
			username := fmt.Sprintf("invalid-role-%d", role)
			recorder := performCreateUserRequest(t, common.RoleRootUser, fmt.Sprintf(
				`{"username":%q,"password":"password123","role":%d}`,
				username,
				role,
			))
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)

			var count int64
			require.NoError(t, db.Model(&model.User{}).Where("username = ?", username).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestCreateUserStillAcceptsValidLowerRole(t *testing.T) {
	db := setupManageUserTestDB(t)
	recorder := performCreateUserRequest(
		t,
		common.RoleAdminUser,
		`{"username":"created-common-user","password":"password123","role":1}`,
	)

	assert.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	var created model.User
	require.NoError(t, db.Where("username = ?", "created-common-user").First(&created).Error)
	assert.Equal(t, common.RoleCommonUser, created.Role)
}

func TestDeleteSelfFailsCleanlyWhenAuthenticatedUserIsMissing(t *testing.T) {
	setupManageUserTestDB(t)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/user/self", nil)
	c.Set("id", 0)

	require.NotPanics(t, func() { DeleteSelf(c) })
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
}

func TestManageUserDisableAdvancesAuthVersionOnceAndRevokesSession(t *testing.T) {
	db := setupManageUserTestDB(t)
	now := time.Now().Unix()
	user := model.User{
		Username: "managed-disable-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "managed-disable-session", UserID: user.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "refresh-hash", LoginMethod: "password",
		LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"disable"}`, user.Id))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, updated.Status)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var session model.UserSession
	require.NoError(t, db.First(&session, "sid = ?", "managed-disable-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
}

func TestManageUserDemoteAdvancesAuthVersionAndRevokesSessionsOnce(t *testing.T) {
	db := setupManageUserTestDB(t)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, authz.Init(db))

	now := time.Now().Unix()
	user := model.User{
		Username: "managed-demote-user", Password: "password", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	for _, sid := range []string{"managed-demote-session-one", "managed-demote-session-two"} {
		require.NoError(t, db.Create(&model.UserSession{
			SID: sid, UserID: user.Id, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "refresh-" + sid, LoginMethod: "password",
			LastActiveAt: now, ExpiresAt: now + 3600,
		}).Error)
	}

	sessionUpdateCount := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:count_demote_session_updates", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "user_sessions" {
			sessionUpdateCount++
		}
	}))

	recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"demote"}`, user.Id))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.RoleCommonUser, updated.Role)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var sessions []model.UserSession
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("sid asc").Find(&sessions).Error)
	require.Len(t, sessions, 2)
	for _, session := range sessions {
		assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
		assert.Equal(t, "admin_demote", session.RevokedReason)
	}
	assert.Equal(t, 1, sessionUpdateCount)
}

func TestManageUserDeleteReturnsImmediatelyAndUnknownActionFails(t *testing.T) {
	db := setupManageUserTestDB(t)
	deleted := model.User{
		Username: "managed-delete-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "delete-aff",
	}
	require.NoError(t, db.Create(&deleted).Error)

	recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"delete"}`, deleted.Id))
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	var deletedCount int64
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("id = ? AND deleted_at IS NOT NULL", deleted.Id).Count(&deletedCount).Error)
	assert.EqualValues(t, 1, deletedCount)

	unchanged := model.User{
		Username: "managed-unknown-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "unknown-aff",
	}
	require.NoError(t, db.Create(&unchanged).Error)
	recorder = performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"unknown"}`, unchanged.Id))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	require.NoError(t, db.First(&unchanged, unchanged.Id).Error)
	assert.EqualValues(t, 1, unchanged.AuthVersion)
	assert.Equal(t, common.UserStatusEnabled, unchanged.Status)
}

func TestManageUserQuotaRespectsWalletCeiling(t *testing.T) {
	db := setupManageUserTestDB(t)
	user := model.User{
		Username: "managed-quota-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", Quota: common.MaxWalletQuota - 1,
	}
	require.NoError(t, db.Create(&user).Error)

	recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":2}`, user.Id))
	assert.Contains(t, recorder.Body.String(), `"success":false`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota-1, updated.Quota)

	recorder = performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"override","value":%d}`, user.Id, common.MaxWalletQuota+1))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.MaxWalletQuota-1, updated.Quota)
}

func TestManageUserQuotaOverrideInvalidatesRedisCache(t *testing.T) {
	db, server := setupManageUserRedisTestDB(t)
	user := model.User{
		Username: "managed-quota-cache-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", Quota: 100, AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)

	cached, err := model.GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 100, cached.Quota)
	cacheKey := fmt.Sprintf("user:%d", user.Id)
	assert.True(t, server.Exists(cacheKey))

	recorder := performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"override","value":1}`, user.Id))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	assert.False(t, server.Exists(cacheKey), "quota override must fence the stale Redis snapshot")

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, 1, updated.Quota)

	reserved, err := model.TryReserveUserQuota(user.Id, 2)
	require.NoError(t, err)
	assert.False(t, reserved, "a reservation must observe the overridden database balance")
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, 1, updated.Quota)
}
