package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockQuotaCacheEvalHook struct {
	command      string
	entered      chan struct{}
	release      chan struct{}
	completed    chan struct{}
	once         sync.Once
	completeOnce sync.Once
}

func (h *blockQuotaCacheEvalHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	command := h.command
	if command == "" {
		command = "eval"
	}
	if cmd.Name() != command {
		return ctx, nil
	}
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return ctx, nil
}

func (h *blockQuotaCacheEvalHook) AfterProcess(_ context.Context, cmd redis.Cmder) error {
	command := h.command
	if command == "" {
		command = "eval"
	}
	if h.completed != nil && cmd.Name() == command {
		h.completeOnce.Do(func() { close(h.completed) })
	}
	return nil
}

func (*blockQuotaCacheEvalHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (*blockQuotaCacheEvalHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func TestQuotaCacheMutationMissIsNotReportedAsSuccess(t *testing.T) {
	useUserCacheMiniRedis(t)
	const userID = 7301
	// No hash has been hydrated yet. A guarded increment must surface the miss
	// so the caller can fence and enqueue a repair instead of silently losing a
	// credit in Redis.
	err := cacheIncrUserQuota(userID, 10)
	assert.ErrorIs(t, err, ErrQuotaCacheMiss)
}

func TestUserQuotaMutationStagesDurableRepairBeforeRedisSync(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))

	hook := &blockQuotaCacheEvalHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		done <- IncreaseUserQuota(user.Id, 10, false)
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err := DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error
	close(hook.release)
	mutationErr := <-done

	require.NoError(t, err, "the committed wallet mutation must already have a durable cache-repair intent")
	assert.Equal(t, 110, getUserQuotaFromDB(t, user.Id))
	require.NoError(t, mutationErr)
}

func TestTokenQuotaMutationStagesDurableRepairBeforeRedisSync(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	token := createReserveTestToken(t, 100)
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)

	hook := &blockQuotaCacheEvalHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		done <- DecreaseTokenQuota(token.Id, token.Key, 10)
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err = DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityToken, token.Id).First(&repair).Error
	close(hook.release)
	mutationErr := <-done

	require.NoError(t, err, "the committed token mutation must already have a durable cache-repair intent")
	reloaded := getTokenFromDB(t, token.Id)
	assert.Equal(t, 90, reloaded.RemainQuota)
	assert.Equal(t, 10, reloaded.UsedQuota)
	require.NoError(t, mutationErr)
}

func TestUserQuotaDebitStagesDurableRepairBeforeRedisSync(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))

	hook := &blockQuotaCacheEvalHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		done <- DecreaseUserQuota(user.Id, 10, false)
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err := DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error
	close(hook.release)
	mutationErr := <-done

	require.NoError(t, err, "the committed wallet debit must already have a durable cache-repair intent")
	assert.Equal(t, 90, getUserQuotaFromDB(t, user.Id))
	require.NoError(t, mutationErr)
}

func TestTokenQuotaCreditStagesDurableRepairBeforeRedisSync(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	token := createReserveTestToken(t, 100)
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", token.Id).Update("used_quota", 20).Error)
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)

	hook := &blockQuotaCacheEvalHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		done <- IncreaseTokenQuota(token.Id, token.Key, 10)
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err = DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityToken, token.Id).First(&repair).Error
	close(hook.release)
	mutationErr := <-done

	require.NoError(t, err, "the committed token credit must already have a durable cache-repair intent")
	reloaded := getTokenFromDB(t, token.Id)
	assert.Equal(t, 110, reloaded.RemainQuota)
	assert.Equal(t, 10, reloaded.UsedQuota)
	require.NoError(t, mutationErr)
}

func TestSetUserQuotaStagesDurableRepairBeforeCacheInvalidation(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))

	hook := &blockQuotaCacheEvalHook{
		command: "del",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		done <- SetUserQuota(user.Id, 75)
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err := DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error
	close(hook.release)
	mutationErr := <-done

	require.NoError(t, err, "the absolute wallet write must stage repair before invalidating Redis")
	assert.Equal(t, 75, getUserQuotaFromDB(t, user.Id))
	require.NoError(t, mutationErr)
}

func TestBillingOperationStagesDurableRepairWithLedgerCommit(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))

	hook := &blockQuotaCacheEvalHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		done <- ApplyBillingOperation(BillingOperationSpec{
			RequestID:   "quota-cache-repair-billing-operation",
			Component:   "settle",
			UserID:      user.Id,
			WalletDelta: -10,
		})
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err := DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error
	close(hook.release)
	mutationErr := <-done

	require.NoError(t, err, "billing must commit its cache-repair intent with the financial ledger")
	assert.Equal(t, 90, getUserQuotaFromDB(t, user.Id))
	require.NoError(t, mutationErr)
}

func TestRedemptionStagesDurableRepairWithCreditTransaction(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Redemption{}))
	useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))
	redemption := Redemption{
		Key:         common.GetRandomString(32),
		Name:        "quota-cache-repair-redemption",
		Status:      common.RedemptionCodeStatusEnabled,
		Quota:       10,
		CreatedTime: common.GetTimestamp(),
	}
	require.NoError(t, DB.Create(&redemption).Error)

	hook := &blockQuotaCacheEvalHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	type redeemResult struct {
		quota int
		err   error
	}
	done := make(chan redeemResult, 1)
	go func() {
		quota, err := Redeem(redemption.Key, user.Id)
		done <- redeemResult{quota: quota, err: err}
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err := DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error
	close(hook.release)
	result := <-done

	require.NoError(t, err, "redemption credit and its cache-repair intent must commit atomically")
	assert.Equal(t, 110, getUserQuotaFromDB(t, user.Id))
	assert.Equal(t, 10, result.quota)
	require.NoError(t, result.err)
}

func TestCheckinStagesDurableRepairWithCreditTransaction(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&Checkin{}))
	useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))
	checkin := &Checkin{
		UserId:       user.Id,
		CheckinDate:  "2099-01-01",
		QuotaAwarded: 10,
		CreatedAt:    common.GetTimestamp(),
	}

	hook := &blockQuotaCacheEvalHook{
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
		completed: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	type checkinResult struct {
		checkin *Checkin
		err     error
	}
	done := make(chan checkinResult, 1)
	go func() {
		result, err := userCheckinWithTransaction(checkin, user.Id, 10)
		done <- checkinResult{checkin: result, err: err}
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err := DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error
	close(hook.release)
	result := <-done
	<-hook.completed

	require.NoError(t, err, "check-in credit and its cache-repair intent must commit atomically")
	assert.Equal(t, 110, getUserQuotaFromDB(t, user.Id))
	require.NotNil(t, result.checkin)
	require.NoError(t, result.err)
}

func TestDatabaseFallbackReserveStagesRepairAgainstConcurrentRehydrate(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))

	// Reproduce the fallback race: Redis was fenced first, then another reader
	// republished the still-unmodified database value before the conditional DB
	// reservation committed.
	require.NoError(t, invalidateUserQuotaCacheForReserveFallback(user.Id))
	stale, err := GetUserCache(user.Id)
	require.NoError(t, err)
	require.Equal(t, 100, stale.Quota)

	reserved, err := reserveUserQuotaDB(user.Id, 20)
	require.NoError(t, err)
	require.True(t, reserved)
	var repair QuotaCacheRepair
	err = DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error
	require.NoError(t, err, "the fallback DB reservation must retain a durable repair for the republished stale snapshot")

	processed, pending := ProcessQuotaCacheRepairs(t.Context(), 10)
	assert.Equal(t, 1, processed)
	assert.Zero(t, pending)
	fresh, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 80, fresh.Quota)
}

func TestTokenDatabaseFallbackStagesRepairAfterFenceExpires(t *testing.T) {
	truncateTables(t)
	server := useUserCacheMiniRedis(t)
	token := createReserveTestToken(t, 100)
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)

	require.NoError(t, invalidateTokenCacheForReserveFallback(token.Id, token.Key))
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	stale, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	require.Equal(t, 100, stale.RemainQuota)

	reserved, err := reserveTokenQuotaDB(token.Id, 20)
	require.NoError(t, err)
	require.True(t, reserved)
	var repair QuotaCacheRepair
	err = DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityToken, token.Id).First(&repair).Error
	require.NoError(t, err, "the token DB reservation must survive a process exit after its short fence expires")

	processed, pending := ProcessQuotaCacheRepairs(t.Context(), 10)
	assert.Equal(t, 1, processed)
	assert.Zero(t, pending)
	fresh, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, 80, fresh.RemainQuota)
}

func TestAffiliateTransferStagesAndRepairsWalletCache(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 1
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	user := createReserveTestUser(t, 100)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("aff_quota", 20).Error)
	require.NoError(t, populateUserCache(user))
	user.AffQuota = 20

	require.NoError(t, user.TransferAffQuotaToQuota(10))
	var repair QuotaCacheRepair
	require.NoError(t, DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error,
		"affiliate transfer must commit a durable wallet-cache repair intent")

	processed, pending := ProcessQuotaCacheRepairs(t.Context(), 10)
	assert.Equal(t, 1, processed)
	assert.Zero(t, pending)
	cached, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 110, cached.Quota)
}

func TestTokenAbsoluteQuotaUpdateStagesRepairWhenRedisIsDown(t *testing.T) {
	truncateTables(t)
	server := useUserCacheMiniRedis(t)
	token := createReserveTestToken(t, 100)
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	server.Close()

	token.RemainQuota = 55
	require.NoError(t, token.Update())
	var repair QuotaCacheRepair
	require.NoError(t, DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityToken, token.Id).First(&repair).Error,
		"an admin quota update must retain repair intent when its pre-write Redis fence fails")
	assert.Equal(t, 55, getTokenFromDB(t, token.Id).RemainQuota)
}

func TestTokenQuotaMutationUsesDatabaseKeyForCacheRepair(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(tokenID int, staleKey string) error
		wantQuota int
	}{
		{
			name: "decrease",
			mutate: func(tokenID int, staleKey string) error {
				return DecreaseTokenQuota(tokenID, staleKey, 10)
			},
			wantQuota: 90,
		},
		{
			name: "increase",
			mutate: func(tokenID int, staleKey string) error {
				return IncreaseTokenQuota(tokenID, staleKey, 10)
			},
			wantQuota: 110,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			truncateTables(t)
			useUserCacheMiniRedis(t)
			token := createReserveTestToken(t, 100)
			_, err := GetTokenByKey(token.Key, true)
			require.NoError(t, err)

			require.NoError(t, tt.mutate(token.Id, token.Key+"-stale"))
			cached, err := cacheGetTokenByKey(token.Key)
			require.NoError(t, err)
			assert.Equal(t, tt.wantQuota, cached.RemainQuota)
			assert.Equal(t, tt.wantQuota, getTokenFromDB(t, token.Id).RemainQuota)

			var repairCount int64
			require.NoError(t, DB.Model(&QuotaCacheRepair{}).
				Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityToken, token.Id).
				Count(&repairCount).Error)
			assert.Zero(t, repairCount, "successful synchronization of the authoritative key must complete the repair")
		})
	}
}

func TestQuotaCacheRepairConvergesUserSnapshot(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	user := User{
		Username:    "quota-cache-repair-user",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		Quota:       100,
	}
	require.NoError(t, DB.Create(&user).Error)
	require.NoError(t, populateUserCache(user))
	// Simulate a committed DB mutation whose Redis update was lost.
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("quota", 70).Error)
	require.NoError(t, enqueueQuotaCacheRepair(QuotaCacheRepairEntityUser, user.Id, getUserCacheKey(user.Id), ErrQuotaCacheMiss))
	require.NoError(t, enqueueQuotaCacheRepair(QuotaCacheRepairEntityUser, user.Id, getUserCacheKey(user.Id), errors.New("second failure")))

	processed, pending := ProcessQuotaCacheRepairs(t.Context(), 10)
	assert.Equal(t, 1, processed, "coalesced repairs should be processed once")
	assert.Zero(t, pending)
	var count int64
	require.NoError(t, DB.Model(&QuotaCacheRepair{}).Where("entity_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count)
	// The worker deliberately invalidates rather than publishing a stale
	// snapshot; the next read hydrates the committed value.
	cached, err := GetUserCache(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 70, cached.Quota)
}

func TestQuotaCacheRepairConvergesTokenAfterKeyChange(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	token := createReserveTestToken(t, 80)
	_, err := GetTokenByKey(token.Key, true)
	require.NoError(t, err)
	oldCacheKey := getTokenCacheKey(token.Key)
	// Rotate the token key after the failed cache mutation. The durable marker
	// stores the old key, while the worker resolves the current DB key and fences
	// both entries.
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", token.Id).Update("key", "rotated-"+token.Key).Error)
	require.NoError(t, enqueueQuotaCacheRepair(QuotaCacheRepairEntityToken, token.Id, oldCacheKey, ErrQuotaCacheMiss))
	processed, pending := ProcessQuotaCacheRepairs(t.Context(), 10)
	assert.Equal(t, 1, processed)
	assert.Zero(t, pending)
	assert.Zero(t, common.RDB.Exists(t.Context(), oldCacheKey).Val())
	var count int64
	require.NoError(t, DB.Model(&QuotaCacheRepair{}).Where("entity_id = ?", token.Id).Count(&count).Error)
	assert.Zero(t, count)
}
