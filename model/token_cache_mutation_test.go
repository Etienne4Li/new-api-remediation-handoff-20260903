package model

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failDeleteCacheHook struct{}

func (*failDeleteCacheHook) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (*failDeleteCacheHook) AfterProcess(_ context.Context, cmd redis.Cmder) error {
	if cmd.Name() == "del" {
		return errors.New("simulated lost cache delete response")
	}
	return nil
}

func (*failDeleteCacheHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (*failDeleteCacheHook) AfterProcessPipeline(context.Context, []redis.Cmder) error { return nil }

// These tests model the failure window where the authoritative database write
// succeeds but Redis is unavailable.  The mutation must still leave a durable
// repair hint; a one-shot best-effort DEL is not enough because an old token
// hash can otherwise remain usable until its TTL expires.
func TestTokenSelectUpdateStagesRepairWhenRedisIsDown(t *testing.T) {
	truncateTables(t)
	server := useUserCacheMiniRedis(t)
	token := createReserveTestToken(t, 100)
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)

	server.Close()
	token.Status = 0
	token.AccessedTime++
	require.NoError(t, token.SelectUpdate())

	stored := getTokenFromDB(t, token.Id)
	assert.Equal(t, 0, stored.Status)
	var repair QuotaCacheRepair
	require.NoError(t, DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityToken, token.Id).First(&repair).Error)
	assert.Equal(t, getTokenCacheKey(token.Key), repair.CacheKey)
}

func TestTokenDeleteStagesRepairWhenRedisIsDown(t *testing.T) {
	truncateTables(t)
	server := useUserCacheMiniRedis(t)
	token := createReserveTestToken(t, 100)
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	cacheKey := getTokenCacheKey(token.Key)

	server.Close()
	require.NoError(t, token.Delete())

	var stored Token
	require.NoError(t, DB.Unscoped().First(&stored, token.Id).Error)
	assert.True(t, stored.DeletedAt.Valid)
	var repair QuotaCacheRepair
	require.NoError(t, DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityToken, token.Id).First(&repair).Error)
	assert.Equal(t, cacheKey, repair.CacheKey)
}

func TestBatchDeleteTokensStagesRepairForEveryDeletedTokenWhenRedisIsDown(t *testing.T) {
	truncateTables(t)
	server := useUserCacheMiniRedis(t)
	first := createReserveTestToken(t, 100)
	second := createReserveTestToken(t, 100)
	for _, token := range []Token{first, second} {
		_, err := GetTokenByKey(token.Key, true)
		require.NoError(t, err)
	}

	server.Close()
	count, err := BatchDeleteTokens([]int{first.Id, second.Id}, first.UserId)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	var repairs []QuotaCacheRepair
	require.NoError(t, DB.Where("entity_type = ? AND entity_id IN ?", QuotaCacheRepairEntityToken, []int{first.Id, second.Id}).Find(&repairs).Error)
	assert.Len(t, repairs, 2)
}

func TestQuotaCacheRepairDeletesUserCacheAfterHardDelete(t *testing.T) {
	truncateTables(t)
	server := useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))
	cacheKey := getUserCacheKey(user.Id)

	// Simulate a process that committed the hard delete but crashed before its
	// post-commit cache invalidation.
	require.NoError(t, DB.Unscoped().Delete(&user).Error)
	require.NoError(t, enqueueQuotaCacheRepair(QuotaCacheRepairEntityUser, user.Id, cacheKey, ErrQuotaCacheMiss))

	processed, pending := ProcessQuotaCacheRepairs(context.Background(), 10)
	assert.Equal(t, 1, processed)
	assert.Zero(t, pending)
	assert.False(t, server.Exists(cacheKey))
}

func TestHardDeleteStagesTokenAndUserRepairsWhenPostCommitInvalidationFails(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	token := createReserveTestToken(t, 100)
	require.NoError(t, populateUserCache(user))
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	common.RDB.AddHook(&failDeleteCacheHook{})

	require.NoError(t, HardDeleteUserById(user.Id))

	var repairs []QuotaCacheRepair
	require.NoError(t, DB.Where("(entity_type = ? AND entity_id = ?) OR (entity_type = ? AND entity_id = ?)",
		QuotaCacheRepairEntityUser, user.Id, QuotaCacheRepairEntityToken, token.Id).Find(&repairs).Error)
	assert.Len(t, repairs, 2, "post-commit cache failures must leave both tombstone repairs durable")
}
