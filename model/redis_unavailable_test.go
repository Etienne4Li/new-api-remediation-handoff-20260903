package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useUnavailableRedisClient(t *testing.T) {
	t.Helper()
	previousEnabled, previousClient := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousEnabled
		common.RDB = previousClient
	})
}

func TestDatabaseFirstQuotaMutationsCommitWhenRedisClientIsUnavailable(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUnavailableRedisClient(t)

	user := createReserveTestUser(t, 100)
	token := createReserveTestToken(t, 100)

	require.NoError(t, IncreaseUserQuota(user.Id, 10, false))
	require.NoError(t, DecreaseUserQuota(user.Id, 5, false))
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 10))
	require.NoError(t, IncreaseTokenQuota(token.Id, token.Key, 3))

	assert.Equal(t, 105, getUserQuotaFromDB(t, user.Id))
	reloadedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 93, reloadedToken.RemainQuota)
	assert.Equal(t, 7, reloadedToken.UsedQuota)

	for _, entity := range []struct {
		entityType string
		entityID   int
	}{
		{QuotaCacheRepairEntityUser, user.Id},
		{QuotaCacheRepairEntityToken, token.Id},
	} {
		var repair QuotaCacheRepair
		require.NoError(t, DB.Where("entity_type = ? AND entity_id = ?", entity.entityType, entity.entityID).First(&repair).Error)
		assert.Equal(t, quotaCacheRepairPending, repair.Status)
		assert.Zero(t, repair.Attempts)
	}
}

func TestQuotaReserveUsesDatabaseWhenRedisClientIsUnavailable(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUnavailableRedisClient(t)

	user := createReserveTestUser(t, 100)
	reserved, err := TryReserveUserQuota(user.Id, 30)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 70, getUserQuotaFromDB(t, user.Id))

	token := createReserveTestToken(t, 80)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 25, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	reloadedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 55, reloadedToken.RemainQuota)
	assert.Equal(t, 25, reloadedToken.UsedQuota)

	var repairCount int64
	require.NoError(t, DB.Model(&QuotaCacheRepair{}).
		Where("(entity_type = ? AND entity_id = ?) OR (entity_type = ? AND entity_id = ?)",
			QuotaCacheRepairEntityUser, user.Id, QuotaCacheRepairEntityToken, token.Id).
		Count(&repairCount).Error)
	assert.EqualValues(t, 2, repairCount, "database fallback must retain both durable cache repairs")
}

func TestQuotaCacheRepairWorkerDoesNotClaimWithoutRedisClient(t *testing.T) {
	truncateTables(t)
	useUnavailableRedisClient(t)

	user := createReserveTestUser(t, 100)
	require.NoError(t, enqueueQuotaCacheRepair(
		QuotaCacheRepairEntityUser,
		user.Id,
		getUserCacheKey(user.Id),
		ErrQuotaCacheMiss,
	))

	var before QuotaCacheRepair
	require.NoError(t, DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&before).Error)
	processed, pending := ProcessQuotaCacheRepairs(t.Context(), 10)
	assert.Zero(t, processed)
	assert.Zero(t, pending)

	var after QuotaCacheRepair
	require.NoError(t, DB.First(&after, before.ID).Error)
	assert.Equal(t, before.Status, after.Status)
	assert.Equal(t, before.Attempts, after.Attempts)
	assert.Equal(t, before.NextAttemptAt, after.NextAttemptAt)
	assert.Equal(t, before.LockedBy, after.LockedBy)
	assert.Equal(t, before.LockedUntil, after.LockedUntil)
}

func TestColdCachesFallBackToDatabaseWhenRedisClientIsUnavailable(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUnavailableRedisClient(t)

	user := createReserveTestUser(t, 100)
	token := createReserveTestToken(t, 80)

	cachedUser, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, user.Id, cachedUser.Id)
	assert.Equal(t, user.Quota, cachedUser.Quota)

	cachedToken, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, token.Id, cachedToken.Id)
	assert.Equal(t, token.RemainQuota, cachedToken.RemainQuota)

	now := time.Now().Unix()
	session := newTestUserSession("redis-unavailable-session", user.Id, now)
	require.NoError(t, CreateUserSession(session))
	cachedSession, err := GetUserSessionCached(session.SID)
	require.NoError(t, err)
	assert.Equal(t, session.SID, cachedSession.SID)
	assert.Equal(t, session.UserID, cachedSession.UserID)
}

func TestDirectRedisCacheOperationsReportUnavailableClient(t *testing.T) {
	useUnavailableRedisClient(t)

	quotaOperations := []struct {
		name string
		run  func() (cacheQuotaResult, error)
	}{
		{"reserve user quota", func() (cacheQuotaResult, error) { return cacheTryReserveUserQuota(1, 1) }},
		{"apply user quota", func() (cacheQuotaResult, error) { return cacheApplyUserQuotaDelta(1, 1) }},
		{"reserve token quota", func() (cacheQuotaResult, error) { return cacheTryReserveTokenQuota(1, "token-key", 1) }},
		{"apply token quota", func() (cacheQuotaResult, error) { return cacheApplyTokenQuotaDelta(1, "token-key", 1) }},
	}
	for _, operation := range quotaOperations {
		t.Run(operation.name, func(t *testing.T) {
			result, err := operation.run()
			assert.Equal(t, cacheQuotaMiss, result)
			assert.ErrorIs(t, err, errRedisClientUnavailable)
		})
	}

	assert.ErrorIs(t, invalidateTokenCacheForMutation("token-key"), errRedisClientUnavailable)
	_, err := cacheInitToken(Token{Id: 1, Key: "token-key"})
	assert.ErrorIs(t, err, errRedisClientUnavailable)

	user := &UserBase{Id: 1, AuthVersion: 1}
	assert.ErrorIs(t, writeUserCache(user, true), errRedisClientUnavailable)
	_, err = getUserAuthVersionFloor(user.Id)
	assert.ErrorIs(t, err, errRedisClientUnavailable)
	assert.ErrorIs(t, SetUserAuthVersionFence(user.Id, user.AuthVersion), errRedisClientUnavailable)
	assert.ErrorIs(t, publishCommittedUserAuthVersion(user.Id, user.AuthVersion), errRedisClientUnavailable)
	assert.ErrorIs(t, updateUserCacheFieldAtVersion(user.Id, "Status", common.UserStatusEnabled, user.AuthVersion), errRedisClientUnavailable)

	now := time.Now().Unix()
	session := newTestUserSession("direct-redis-unavailable-session", user.Id, now)
	assert.ErrorIs(t, writeUserSessionCache(session.cacheEntry(), userSessionCacheDeadline()), errRedisClientUnavailable)
}
