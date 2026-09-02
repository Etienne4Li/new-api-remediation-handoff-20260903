package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PurchaseSubscriptionWithBalance commits the wallet deduction in the same
// transaction that creates the subscription.  If the user cache is malformed
// at that point, the post-commit cache delta cannot be applied; the mutation
// must therefore trigger the durable repair path instead of leaving a hash
// without Quota (which GetUserCache would otherwise interpret as zero).
func TestPurchaseSubscriptionWithBalanceRepairsMalformedUserQuotaCache(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)

	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 10
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	user := &User{
		Username:    "subscription-balance-cache-user",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		Quota:       100,
	}
	require.NoError(t, DB.Create(user).Error)
	plan := &SubscriptionPlan{
		Title:           "Cache repair plan",
		PriceAmount:     2,
		Currency:        "USD",
		DurationUnit:    SubscriptionDurationMonth,
		DurationValue:   1,
		TotalAmount:     100,
		Enabled:         true,
		AllowBalancePay: common.GetPointer(true),
	}
	require.NoError(t, DB.Create(plan).Error)

	// Deliberately create a schema-valid but incomplete hash.  The normal cache
	// reader accepts this shape for backwards compatibility, while the guarded
	// quota Lua script rejects it and returns ErrQuotaCacheMiss.
	cacheKey := getUserCacheKey(user.Id)
	require.NoError(t, common.RDB.HSet(context.Background(), cacheKey, map[string]interface{}{
		"Id":          user.Id,
		"AuthVersion": user.AuthVersion,
		"CacheSchema": userCacheSchemaVersion,
		"Status":      common.UserStatusEnabled,
		"Username":    user.Username,
	}).Err())

	require.NoError(t, PurchaseSubscriptionWithBalance(user.Id, plan.Id))

	var persisted User
	require.NoError(t, DB.Select("quota").First(&persisted, user.Id).Error)
	assert.Equal(t, 80, persisted.Quota)

	// The repaired hash must immediately reflect the committed wallet value,
	// rather than remaining incomplete/zero until its TTL expires.
	cached, err := cacheGetUserBase(user.Id)
	require.NoError(t, err)
	assert.Equal(t, 80, cached.Quota)
	assert.Equal(t, user.Username, cached.Username)
}

func TestPurchaseSubscriptionWithBalanceStagesDurableCacheRepair(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)

	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 10
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })

	user := createReserveTestUser(t, 100)
	require.NoError(t, populateUserCache(user))
	plan := &SubscriptionPlan{
		Title:           "Durable cache repair plan",
		PriceAmount:     2,
		Currency:        "USD",
		DurationUnit:    SubscriptionDurationMonth,
		DurationValue:   1,
		TotalAmount:     100,
		Enabled:         true,
		AllowBalancePay: common.GetPointer(true),
	}
	require.NoError(t, DB.Create(plan).Error)

	hook := &blockQuotaCacheEvalHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	common.RDB.AddHook(hook)
	done := make(chan error, 1)
	go func() {
		done <- PurchaseSubscriptionWithBalance(user.Id, plan.Id)
	}()

	<-hook.entered
	var repair QuotaCacheRepair
	err := DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error
	close(hook.release)
	mutationErr := <-done

	require.NoError(t, err, "subscription purchase must commit repair intent with the wallet debit")
	assert.Equal(t, 80, getUserQuotaFromDB(t, user.Id))
	require.NoError(t, mutationErr)
}
