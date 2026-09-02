package model

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type failAfterEvalHook struct {
	failed bool
}

func (h *failAfterEvalHook) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (h *failAfterEvalHook) AfterProcess(_ context.Context, cmd redis.Cmder) error {
	if cmd.Name() == "eval" && !h.failed {
		h.failed = true
		return errors.New("simulated lost eval response")
	}
	return nil
}

func (*failAfterEvalHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (*failAfterEvalHook) AfterProcessPipeline(context.Context, []redis.Cmder) error { return nil }

func createReserveTestUser(t *testing.T, quota int) User {
	t.Helper()
	user := User{
		Username:    "reserve-user-" + common.GetRandomString(6),
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		Quota:       quota,
		AffCode:     "reserve-aff-" + common.GetRandomString(8),
	}
	require.NoError(t, DB.Create(&user).Error)
	return user
}

func createReserveTestToken(t *testing.T, remainQuota int) Token {
	t.Helper()
	token := Token{
		UserId:      1,
		Key:         "reserve-token-" + common.GetRandomString(8),
		Name:        "reserve-test",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: remainQuota,
	}
	require.NoError(t, token.Insert())
	return token
}

func getUserQuotaFromDB(t *testing.T, id int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota").First(&user, id).Error)
	return user.Quota
}

func getTokenFromDB(t *testing.T, id int) Token {
	t.Helper()
	var token Token
	require.NoError(t, DB.First(&token, id).Error)
	return token
}

func resetBatchUpdateTestState(t *testing.T) {
	t.Helper()
	oldBatchEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	for i := 0; i < BatchUpdateTypeCount; i++ {
		batchUpdateLocks[i].Lock()
		batchUpdateStores[i] = make(map[int]int)
		batchUpdateLocks[i].Unlock()
	}
	t.Cleanup(func() {
		common.BatchUpdateEnabled = oldBatchEnabled
		for i := 0; i < BatchUpdateTypeCount; i++ {
			batchUpdateLocks[i].Lock()
			batchUpdateStores[i] = make(map[int]int)
			batchUpdateLocks[i].Unlock()
		}
	})
}

func TestTryReserveQuotaWithoutRedis(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)

	user := createReserveTestUser(t, 100)
	reserved, err := TryReserveUserQuota(user.Id, 60)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))

	reserved, err = TryReserveUserQuota(user.Id, 41)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 40, getUserQuotaFromDB(t, user.Id))

	token := createReserveTestToken(t, 80)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 25, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	reloaded := getTokenFromDB(t, token.Id)
	assert.Equal(t, 55, reloaded.RemainQuota)
	assert.Equal(t, 25, reloaded.UsedQuota)

	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 56, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 55, getTokenFromDB(t, token.Id).RemainQuota)
}

func TestRedisBatchReserveUsesDatabaseAsFinancialAuthority(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, 10)
	reserved, err := TryReserveUserQuota(user.Id, 8)
	require.NoError(t, err)
	assert.True(t, reserved)
	// Financial reservations are never held in the process-local batch map;
	// the DB conditional update is committed before the call returns.  This
	// closes the crash window where Redis was decremented but the batch queue
	// was lost on process restart.
	assert.Equal(t, 2, getUserQuotaFromDB(t, user.Id))
	_, queued := queuedBatchDelta(t, BatchUpdateTypeUserQuota, user.Id)
	assert.False(t, queued)

	reserved, err = TryReserveUserQuota(user.Id, 3)
	require.NoError(t, err)
	assert.False(t, reserved, "stale DB balance must not authorize a second spend")
	cachedUser, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 2, cachedUser.Quota)

	token := createReserveTestToken(t, 9)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 7, false)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 2, getTokenFromDB(t, token.Id).RemainQuota)
	_, queued = queuedBatchDelta(t, BatchUpdateTypeTokenQuota, token.Id)
	assert.False(t, queued)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 3, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 2, getTokenFromDB(t, token.Id).RemainQuota)

	batchUpdate()
	assert.Equal(t, 2, getUserQuotaFromDB(t, user.Id))
	reloadedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 2, reloadedToken.RemainQuota)
	assert.Equal(t, 7, reloadedToken.UsedQuota)
}

func TestBatchReserveCrashGapCannotAuthorizeFromStaleCache(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, 10)
	require.NoError(t, populateUserCache(user))
	// Simulate the process stopping immediately after the conditional DB
	// reservation and before its post-commit Redis invalidation.  The old cache
	// still advertises 10 while the durable ledger is 2.
	reserved, err := reserveUserQuotaDB(user.Id, 8)
	require.NoError(t, err)
	require.True(t, reserved)
	assert.Equal(t, 2, getUserQuotaFromDB(t, user.Id))
	quota, err := GetUserQuota(user.Id, false)
	require.NoError(t, err)
	assert.Equal(t, 2, quota, "batch mode must never trust a stale wallet cache")
	cachedUser, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 2, cachedUser.Quota, "cached auth context must refresh its financial field")

	token := createReserveTestToken(t, 9)
	_, err = GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	reserved, err = reserveTokenQuotaDB(token.Id, 7)
	require.NoError(t, err)
	require.True(t, reserved)
	reloaded, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 2, reloaded.RemainQuota, "batch mode token reads must use the DB after a crash gap")
}

func TestBatchUpdateAccumulatesTwoMaximumRequestCharges(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, common.MaxQuota*2+100)
	require.NoError(t, DecreaseUserQuota(user.Id, common.MaxQuota, false))
	require.NoError(t, DecreaseUserQuota(user.Id, common.MaxQuota, false))
	assert.Equal(t, 100, getUserQuotaFromDB(t, user.Id), "financial wallet mutations must commit immediately")
	_, queued := queuedBatchDelta(t, BatchUpdateTypeUserQuota, user.Id)
	assert.False(t, queued, "financial wallet mutations must not use the batch queue")

	batchUpdate()
	assert.Equal(t, 100, getUserQuotaFromDB(t, user.Id))
}

func TestBatchUpdateAccumulatorSaturatesOverflow(t *testing.T) {
	resetBatchUpdateTestState(t)

	addNewRecord(BatchUpdateTypeUserQuota, 1, math.MaxInt)
	addNewRecord(BatchUpdateTypeUserQuota, 1, 1)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	assert.Equal(t, math.MaxInt, batchUpdateStores[BatchUpdateTypeUserQuota][1])
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()

	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	batchUpdateStores[BatchUpdateTypeUserQuota] = make(map[int]int)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	addNewRecord(BatchUpdateTypeUserQuota, 1, math.MinInt)
	addNewRecord(BatchUpdateTypeUserQuota, 1, -1)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	assert.Equal(t, math.MinInt, batchUpdateStores[BatchUpdateTypeUserQuota][1])
	batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
}

func queuedBatchDelta(t *testing.T, updateType int, id int) (int, bool) {
	t.Helper()
	batchUpdateLocks[updateType].Lock()
	defer batchUpdateLocks[updateType].Unlock()
	value, ok := batchUpdateStores[updateType][id]
	return value, ok
}

func TestQuotaMutationMissingRowsAndZeroDeltas(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)

	const missingID = 987654
	assert.ErrorIs(t, IncreaseUserQuota(missingID, 1, true), gorm.ErrRecordNotFound)
	assert.ErrorIs(t, DecreaseUserQuota(missingID, 1, true), gorm.ErrRecordNotFound)
	assert.NoError(t, IncreaseUserQuota(missingID, 0, true))
	assert.NoError(t, DecreaseUserQuota(missingID, 0, true))

	assert.ErrorIs(t, IncreaseTokenQuota(missingID, "missing-token", 1), gorm.ErrRecordNotFound)
	assert.ErrorIs(t, DecreaseTokenQuota(missingID, "missing-token", 1), gorm.ErrRecordNotFound)
	assert.NoError(t, IncreaseTokenQuota(missingID, "missing-token", 0))
	assert.NoError(t, DecreaseTokenQuota(missingID, "missing-token", 0))
}

func TestTokenQuotaMutationRejectsOutOfRangeDeltas(t *testing.T) {
	resetBatchUpdateTestState(t)
	for _, quota := range []int{-1, common.MaxQuota + 1} {
		assert.Error(t, IncreaseTokenQuota(1, "unused", quota))
		assert.Error(t, DecreaseTokenQuota(1, "unused", quota))
	}
}

func TestBatchUpdateRetriesFailedRowsAndAppliesSuccessfulRows(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	common.BatchUpdateEnabled = true

	user := createReserveTestUser(t, 100)
	token := createReserveTestToken(t, 100)
	channel := Channel{
		Name:      "batch-update-channel",
		Key:       "batch-update-key",
		Status:    common.ChannelStatusEnabled,
		UsedQuota: 10,
	}
	require.NoError(t, DB.Create(&channel).Error)

	// These deltas are accepted asynchronously and must be applied by flush.
	require.NoError(t, DecreaseUserQuota(user.Id, 7, false))
	require.NoError(t, DecreaseTokenQuota(token.Id, token.Key, 5))
	UpdateUserUsedQuota(user.Id, 11)
	UpdateChannelUsedQuota(channel.Id, 13)

	batchUpdate()
	assert.Equal(t, 93, getUserQuotaFromDB(t, user.Id))
	reloadedToken := getTokenFromDB(t, token.Id)
	assert.Equal(t, 95, reloadedToken.RemainQuota)
	assert.Equal(t, 5, reloadedToken.UsedQuota)
	var reloadedUser User
	require.NoError(t, DB.First(&reloadedUser, user.Id).Error)
	assert.Equal(t, 11, reloadedUser.UsedQuota)
	assert.Equal(t, 0, reloadedUser.RequestCount)
	var reloadedChannel Channel
	require.NoError(t, DB.First(&reloadedChannel, channel.Id).Error)
	assert.Equal(t, int64(23), reloadedChannel.UsedQuota)

	// A missing financial target is rejected synchronously; there is no
	// process-local queue that could be lost or accidentally replayed later.
	const missingID = 876543
	assert.ErrorIs(t, DecreaseUserQuota(missingID, 3, false), gorm.ErrRecordNotFound)
	assert.ErrorIs(t, DecreaseTokenQuota(missingID, "missing-token", 4), gorm.ErrRecordNotFound)
	UpdateChannelUsedQuota(missingID, 6)
	UpdateUserUsedQuotaAndRequestCount(missingID, 8)
	batchUpdate()

	value, ok := queuedBatchDelta(t, BatchUpdateTypeUserQuota, missingID)
	assert.False(t, ok)
	assert.Zero(t, value)
	value, ok = queuedBatchDelta(t, BatchUpdateTypeUsedQuota, missingID)
	assert.True(t, ok)
	assert.Equal(t, 8, value)
	value, ok = queuedBatchDelta(t, BatchUpdateTypeRequestCount, missingID)
	assert.True(t, ok)
	assert.Equal(t, 1, value)
	value, ok = queuedBatchDelta(t, BatchUpdateTypeTokenQuota, missingID)
	assert.False(t, ok)
	assert.Zero(t, value)
	value, ok = queuedBatchDelta(t, BatchUpdateTypeChannelUsedQuota, missingID)
	assert.True(t, ok)
	assert.Equal(t, 6, value)
}

func TestReserveFailsClosedWhenRedisStateCannotBeInvalidated(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 20)
	require.NoError(t, populateUserCache(user))
	server.Close()

	// An EVAL response can be lost after Redis committed the reservation. If the
	// cache key cannot be invalidated, falling back to the database could charge
	// twice, so the reserve must fail closed and leave the DB untouched.
	reserved, err := TryReserveUserQuota(user.Id, 5)
	assert.False(t, reserved)
	assert.Error(t, err)
	assert.Equal(t, 20, getUserQuotaFromDB(t, user.Id))

	reserved, err = TryReserveUserQuota(user.Id, 16)
	assert.Error(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 20, getUserQuotaFromDB(t, user.Id))
}

func TestReserveInvalidatesCacheBeforeFallbackOnAmbiguousRedisError(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 20)
	require.NoError(t, populateUserCache(user))
	hook := &failAfterEvalHook{}
	common.RDB.AddHook(hook)

	reserved, err := TryReserveUserQuota(user.Id, 5)
	require.NoError(t, err)
	assert.True(t, reserved)
	assert.Equal(t, 15, getUserQuotaFromDB(t, user.Id))
	exists, existsErr := common.RDB.Exists(t.Context(), getUserCacheKey(user.Id)).Result()
	require.NoError(t, existsErr)
	assert.EqualValues(t, 1, exists, "the fallback may republish only the committed database snapshot")
	cached, cacheErr := cacheGetUserBase(user.Id)
	require.NoError(t, cacheErr)
	assert.Equal(t, 15, cached.Quota, "an ambiguous EVAL result must converge to the committed database balance")
}

func TestSynchronousReserveCompensatesCacheWhenPersistenceFails(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 10)
	require.NoError(t, populateUserCache(user))
	require.NoError(t, DB.Delete(&user).Error)

	reserved, err := TryReserveUserQuota(user.Id, 6)
	assert.False(t, reserved)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	cached, cacheErr := cacheGetUserBase(user.Id)
	require.NoError(t, cacheErr)
	assert.Equal(t, 10, cached.Quota)

	token := createReserveTestToken(t, 12)
	_, err = GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	require.NoError(t, DB.Delete(&token).Error)
	reserved, err = TryReserveTokenQuota(token.Id, token.Key, 7, false)
	assert.False(t, reserved)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	cachedToken, cacheErr := cacheGetTokenByKey(token.Key)
	require.NoError(t, cacheErr)
	assert.Equal(t, 12, cachedToken.RemainQuota)
	assert.Zero(t, cachedToken.UsedQuota)
}

func TestRedisReserveRejectsStaleUserCacheWithoutOverdrawingDatabase(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)

	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))
	// Simulate an out-of-band durable update that leaves Redis advertising an
	// older, larger balance. The DB predicate must win after the cache fast
	// path has tentatively reserved the amount.
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("quota", 3).Error)
	reserved, err := TryReserveUserQuota(user.Id, 5)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 3, getUserQuotaFromDB(t, user.Id))
	refreshed, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 3, refreshed.Quota, "a stale cache must be fenced after the durable predicate rejects the reservation")
}

func TestRedisReserveRejectsStaleTokenCacheWithoutOverdrawingDatabase(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	useUserCacheMiniRedis(t)

	token := createReserveTestToken(t, 100)
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", token.Id).Update("remain_quota", 3).Error)
	reserved, err := TryReserveTokenQuota(token.Id, token.Key, 5, false)
	require.NoError(t, err)
	assert.False(t, reserved)
	assert.Equal(t, 3, getTokenFromDB(t, token.Id).RemainQuota)
	refreshed, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 3, refreshed.RemainQuota, "a stale token cache must be fenced after the durable predicate rejects the reservation")
}

func TestTokenCacheInitPreservesLiveQuotaAndFenceBlocksStaleSnapshot(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)

	token := createReserveTestToken(t, 100)
	loaded, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	stale := *loaded

	result, err := cacheApplyTokenQuotaDelta(token.Id, token.Key, -70)
	require.NoError(t, err)
	require.Equal(t, cacheQuotaOK, result)

	// 已存在的哈希只刷新 TTL：数据库快照不得覆盖已被原子预扣的余额。
	code, err := cacheInitToken(stale)
	require.NoError(t, err)
	assert.Equal(t, 2, code)
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 30, cached.RemainQuota)

	// 变更期间：fence 删除缓存并拦截并发读者手中的过期快照。
	require.NoError(t, invalidateTokenCacheForMutation(token.Key))
	code, err = cacheInitToken(stale)
	require.NoError(t, err)
	assert.Zero(t, code, "the pre-mutation snapshot must not be published while fenced")
	_, err = cacheGetTokenByKey(token.Key)
	assert.Error(t, err)

	// fence 过期后可重新从数据库水合。
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	fresh, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 100, fresh.RemainQuota)
	cached, err = cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, 100, cached.RemainQuota)
}
