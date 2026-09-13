package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	affRebateInviterId = 9001
	affRebateInviteeId = 9002
)

func setupAffRebateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := DB
	originalLogDB := LOG_DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &TopUp{}, &Log{}, &AffRebate{}))
	DB = db
	LOG_DB = db
	t.Cleanup(func() {
		DB = originalDB
		LOG_DB = originalLogDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})
	return db
}

// setAffRebateConfig installs the rebate configuration for one test and restores
// the process-wide defaults afterwards.
func setAffRebateConfig(t *testing.T, enabled bool, percent int, maxTimes int) {
	t.Helper()
	oldEnabled := common.AffRebateEnabled
	oldPercent := common.AffRebatePercent
	oldMaxTimes := common.AffRebateMaxTimes
	oldRedis := common.RedisEnabled
	common.AffRebateEnabled = enabled
	common.AffRebatePercent = percent
	common.AffRebateMaxTimes = maxTimes
	common.RedisEnabled = false
	t.Cleanup(func() {
		common.AffRebateEnabled = oldEnabled
		common.AffRebatePercent = oldPercent
		common.AffRebateMaxTimes = oldMaxTimes
		common.RedisEnabled = oldRedis
	})
}

// seedAffRebateUsers creates an inviter and an invitee linked by inviter_id.
func seedAffRebateUsers(t *testing.T, db *gorm.DB, inviterId int) (*User, *User) {
	t.Helper()
	// aff_code carries a UNIQUE index, so every seeded user needs its own.
	inviter := &User{
		Id:       affRebateInviterId,
		Username: "rebate-inviter",
		Email:    "inviter@example.com",
		Status:   common.UserStatusEnabled,
		AffCode:  "rebate-inviter-code",
	}
	require.NoError(t, db.Create(inviter).Error)
	invitee := &User{
		Id:        affRebateInviteeId,
		Username:  "rebate-invitee",
		Email:     "invitee@example.com",
		Status:    common.UserStatusEnabled,
		AffCode:   "rebate-invitee-code",
		InviterId: inviterId,
	}
	require.NoError(t, db.Create(invitee).Error)
	return inviter, invitee
}

func seedPendingTopUpFor(t *testing.T, db *gorm.DB, userId int, tradeNo string, amount int64, money float64) *TopUp {
	t.Helper()
	topUp := &TopUp{
		UserId:          userId,
		Amount:          amount,
		Money:           money,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      common.GetTimestamp(),
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, db.Create(topUp).Error)
	return topUp
}

// seedSettledTopUp inserts an already-successful order, for tests that drive
// GrantAffRebate directly instead of through a payment callback.
func seedSettledTopUp(t *testing.T, db *gorm.DB, userId int, tradeNo string, amount int64, money float64) *TopUp {
	t.Helper()
	topUp := seedPendingTopUpFor(t, db, userId, tradeNo, amount, money)
	topUp.Status = common.TopUpStatusSuccess
	topUp.CompleteTime = common.GetTimestamp()
	require.NoError(t, db.Save(topUp).Error)
	return topUp
}

func affQuotaOf(t *testing.T, db *gorm.DB, userId int) (affQuota int, affHistory int) {
	t.Helper()
	var user User
	require.NoError(t, db.First(&user, userId).Error)
	return user.AffQuota, user.AffHistoryQuota
}

func countAffRebates(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&AffRebate{}).Count(&count).Error)
	return count
}

// SPEC 7.1 — a replayed payment callback must not pay the rebate twice. The
// UNIQUE index on top_up_id, not a read-then-write check, is what guarantees it.
func TestGrantAffRebateIsIdempotentPerTopUp(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, true, 5, 3)
	seedAffRebateUsers(t, db, affRebateInviterId)
	topUp := seedSettledTopUp(t, db, affRebateInviteeId, "REBATE-idem", 10, 10)

	GrantAffRebate(topUp)
	affQuota, affHistory := affQuotaOf(t, db, affRebateInviterId)
	expected := int(10 * common.QuotaPerUnit * 5 / 100)
	require.Equal(t, expected, affQuota)
	require.Equal(t, expected, affHistory)
	require.Equal(t, int64(1), countAffRebates(t, db))

	GrantAffRebate(topUp)
	affQuota, affHistory = affQuotaOf(t, db, affRebateInviterId)
	require.Equal(t, expected, affQuota, "a replayed callback must not credit the inviter twice")
	require.Equal(t, expected, affHistory)
	require.Equal(t, int64(1), countAffRebates(t, db))
}

// SPEC 7.2 — the first three top-ups earn a rebate, the fourth does not. Driven
// through the real epay callback so the production call order is covered.
func TestGrantAffRebateStopsAfterMaxTimes(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, true, 5, 3)
	seedAffRebateUsers(t, db, affRebateInviterId)

	perTopUp := int(10 * common.QuotaPerUnit * 5 / 100)
	for i := 1; i <= 4; i++ {
		tradeNo := fmt.Sprintf("REBATE-seq-%d", i)
		seedPendingTopUpFor(t, db, affRebateInviteeId, tradeNo, 10, 10)

		alreadyDone, err := RechargeEpay(tradeNo, "alipay", "10.00", "127.0.0.1")
		require.NoError(t, err)
		require.False(t, alreadyDone)

		affQuota, affHistory := affQuotaOf(t, db, affRebateInviterId)
		expectedRebates := i
		if i > 3 {
			expectedRebates = 3
		}
		require.Equal(t, perTopUp*expectedRebates, affQuota, "top-up #%d", i)
		require.Equal(t, perTopUp*expectedRebates, affHistory, "top-up #%d", i)
		require.Equal(t, int64(expectedRebates), countAffRebates(t, db), "top-up #%d", i)
	}

	var rebates []AffRebate
	require.NoError(t, db.Order("id asc").Find(&rebates).Error)
	require.Len(t, rebates, 3)
	for i, rebate := range rebates {
		require.Equal(t, i+1, rebate.Sequence)
		require.Equal(t, affRebateInviterId, rebate.InviterId)
		require.Equal(t, affRebateInviteeId, rebate.InviteeId)
	}
}

// SPEC 7.3 — a user with no inviter produces no rebate and no failure.
func TestGrantAffRebateSkipsUserWithoutInviter(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, true, 5, 3)
	seedAffRebateUsers(t, db, 0)
	tradeNo := "REBATE-noinviter"
	seedPendingTopUpFor(t, db, affRebateInviteeId, tradeNo, 10, 10)

	alreadyDone, err := RechargeEpay(tradeNo, "alipay", "10.00", "127.0.0.1")
	require.NoError(t, err, "a missing inviter must not break the top-up")
	require.False(t, alreadyDone)

	require.Equal(t, int64(0), countAffRebates(t, db))
	affQuota, affHistory := affQuotaOf(t, db, affRebateInviterId)
	require.Equal(t, 0, affQuota)
	require.Equal(t, 0, affHistory)

	var invitee User
	require.NoError(t, db.First(&invitee, affRebateInviteeId).Error)
	require.Equal(t, int(10*common.QuotaPerUnit), invitee.Quota, "the top-up itself must still be credited")
}

// SPEC 7.4 — a user recorded as its own inviter earns nothing.
func TestGrantAffRebateSkipsSelfInvite(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, true, 5, 3)
	seedAffRebateUsers(t, db, affRebateInviteeId)
	topUp := seedSettledTopUp(t, db, affRebateInviteeId, "REBATE-self", 10, 10)

	GrantAffRebate(topUp)

	require.Equal(t, int64(0), countAffRebates(t, db))
	var invitee User
	require.NoError(t, db.First(&invitee, affRebateInviteeId).Error)
	require.Equal(t, 0, invitee.AffQuota)
	require.Equal(t, 0, invitee.AffHistoryQuota)
}

// SPEC 7.5 — the rebate is exactly 5% of the quota the same payment credits, so
// the percentage is applied on the money -> quota path and not re-derived.
func TestGrantAffRebateRatioMatchesTopUpQuota(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, true, 5, 3)
	seedAffRebateUsers(t, db, affRebateInviterId)
	tradeNo := "REBATE-ratio"
	seedPendingTopUpFor(t, db, affRebateInviteeId, tradeNo, 10, 10)

	alreadyDone, err := RechargeEpay(tradeNo, "alipay", "10.00", "127.0.0.1")
	require.NoError(t, err)
	require.False(t, alreadyDone)

	var invitee User
	require.NoError(t, db.First(&invitee, affRebateInviteeId).Error)
	topUpQuota := invitee.Quota
	require.Equal(t, int(10*common.QuotaPerUnit), topUpQuota)

	var rebate AffRebate
	require.NoError(t, db.First(&rebate).Error)
	require.Equal(t, 1, rebate.Sequence)
	require.Equal(t, 10.0, rebate.TopupMoney)
	// Exact integer relation: 5% means rebate * 20 == credited quota.
	require.Equal(t, topUpQuota, rebate.RebateQuota*20)
	require.InDelta(t, 0.05, float64(rebate.RebateQuota)/float64(topUpQuota), 0)

	affQuota, affHistory := affQuotaOf(t, db, affRebateInviterId)
	require.Equal(t, rebate.RebateQuota, affQuota)
	require.Equal(t, rebate.RebateQuota, affHistory)
}

// SPEC 7.6 — with the master switch off nothing is written at all.
func TestGrantAffRebateDisabled(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, false, 5, 3)
	seedAffRebateUsers(t, db, affRebateInviterId)
	tradeNo := "REBATE-disabled"
	seedPendingTopUpFor(t, db, affRebateInviteeId, tradeNo, 10, 10)

	alreadyDone, err := RechargeEpay(tradeNo, "alipay", "10.00", "127.0.0.1")
	require.NoError(t, err)
	require.False(t, alreadyDone)

	require.Equal(t, int64(0), countAffRebates(t, db))
	affQuota, affHistory := affQuotaOf(t, db, affRebateInviterId)
	require.Equal(t, 0, affQuota)
	require.Equal(t, 0, affHistory)

	var invitee User
	require.NoError(t, db.First(&invitee, affRebateInviteeId).Error)
	require.Equal(t, int(10*common.QuotaPerUnit), invitee.Quota)
}

// SPEC 7.7 — a failing rebate write must leave the top-up successful. The table
// is dropped to stand in for any write failure (missing migration, dead table).
func TestRechargeSucceedsWhenRebateWriteFails(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, true, 5, 3)
	seedAffRebateUsers(t, db, affRebateInviterId)
	tradeNo := "REBATE-writefail"
	seedPendingTopUpFor(t, db, affRebateInviteeId, tradeNo, 10, 10)
	require.NoError(t, db.Migrator().DropTable(&AffRebate{}))

	alreadyDone, err := RechargeEpay(tradeNo, "alipay", "10.00", "127.0.0.1")
	require.NoError(t, err, "a broken rebate write must not fail the top-up")
	require.False(t, alreadyDone)

	var settled TopUp
	require.NoError(t, db.Where("trade_no = ?", tradeNo).First(&settled).Error)
	require.Equal(t, common.TopUpStatusSuccess, settled.Status)

	var invitee User
	require.NoError(t, db.First(&invitee, affRebateInviteeId).Error)
	require.Equal(t, int(10*common.QuotaPerUnit), invitee.Quota)

	// The inviter simply gets nothing; no partial credit is left behind.
	affQuota, affHistory := affQuotaOf(t, db, affRebateInviterId)
	require.Equal(t, 0, affQuota)
	require.Equal(t, 0, affHistory)
}

func TestAffRebateQuotaConversion(t *testing.T) {
	unit := int(common.QuotaPerUnit)

	// 10 at 5% is half a unit.
	quota, err := affRebateQuota(10, 5)
	require.NoError(t, err)
	require.Equal(t, unit/2, quota)

	// 2 at 5% is a tenth of a unit.
	quota, err = affRebateQuota(2, 5)
	require.NoError(t, err)
	require.Equal(t, unit/10, quota)

	// 0.01 at 5% is 1/2000 of a unit: still a whole quota amount rather than
	// something float arithmetic truncates to zero.
	quota, err = affRebateQuota(0.01, 5)
	require.NoError(t, err)
	require.Equal(t, unit/2000, quota)

	quota, err = affRebateQuota(0, 5)
	require.NoError(t, err)
	require.Equal(t, 0, quota)

	quota, err = affRebateQuota(10, 100)
	require.NoError(t, err)
	require.Equal(t, unit*10, quota, "100% must equal the top-up itself, never more")
}

// The three options are only useful if the admin settings API actually reaches
// the globals GrantAffRebate reads, so exercise the real option update path.
func TestAffRebateOptionsReachRuntimeConfig(t *testing.T) {
	setAffRebateConfig(t, false, 0, 0)

	// OptionMap is normally built by InitOptionMap() at boot; stand one up so
	// updateOptionMap can write to it.
	common.OptionMapRWMutex.Lock()
	originalOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	require.NoError(t, updateOptionMap("AffRebateEnabled", "true"))
	require.NoError(t, updateOptionMap("AffRebatePercent", "7"))
	require.NoError(t, updateOptionMap("AffRebateMaxTimes", "2"))
	require.True(t, common.AffRebateEnabled)
	require.Equal(t, 7, common.AffRebatePercent)
	require.Equal(t, 2, common.AffRebateMaxTimes)

	require.NoError(t, updateOptionMap("AffRebateEnabled", "false"))
	require.False(t, common.AffRebateEnabled)

	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	require.Equal(t, "false", common.OptionMap["AffRebateEnabled"])
	require.Equal(t, "7", common.OptionMap["AffRebatePercent"])
	require.Equal(t, "2", common.OptionMap["AffRebateMaxTimes"])
}

func TestIsDuplicateKeyError(t *testing.T) {
	db := setupAffRebateTestDB(t)
	rebate := &AffRebate{InviterId: 1, InviteeId: 2, TopUpId: 77, RebateQuota: 1, Sequence: 1}
	require.NoError(t, db.Create(rebate).Error)

	duplicate := &AffRebate{InviterId: 1, InviteeId: 2, TopUpId: 77, RebateQuota: 1, Sequence: 1}
	err := db.Create(duplicate).Error
	require.Error(t, err, "top_up_id must be unique at the database level")
	require.True(t, isDuplicateKeyError(err), "unexpected error text: %v", err)

	require.False(t, isDuplicateKeyError(nil))
	require.False(t, isDuplicateKeyError(gorm.ErrRecordNotFound))
}

// GrantAffRebate must tolerate the nil / unsettled inputs a future caller could
// hand it without panicking or writing anything.
func TestGrantAffRebateIgnoresUnsettledInput(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, true, 5, 3)
	seedAffRebateUsers(t, db, affRebateInviterId)

	GrantAffRebate(nil)
	pending := seedPendingTopUpFor(t, db, affRebateInviteeId, "REBATE-pending", 10, 10)
	GrantAffRebate(pending)

	zeroMoney := seedSettledTopUp(t, db, affRebateInviteeId, "REBATE-zero", 0, 0)
	GrantAffRebate(zeroMoney)

	require.Equal(t, int64(0), countAffRebates(t, db))
	affQuota, _ := affQuotaOf(t, db, affRebateInviterId)
	require.Equal(t, 0, affQuota)
}

func TestGetUserAffRebatesPaginatesNewestFirst(t *testing.T) {
	db := setupAffRebateTestDB(t)
	setAffRebateConfig(t, true, 5, 3)
	seedAffRebateUsers(t, db, affRebateInviterId)
	for i := 1; i <= 3; i++ {
		require.NoError(t, db.Create(&AffRebate{
			InviterId:   affRebateInviterId,
			InviteeId:   affRebateInviteeId,
			TopUpId:     100 + i,
			TopupMoney:  float64(i),
			RebateQuota: i * 10,
			Sequence:    i,
			CreatedTime: common.GetTimestamp(),
		}).Error)
	}
	// A rebate belonging to somebody else must never leak into the page.
	require.NoError(t, db.Create(&AffRebate{
		InviterId: 7777, InviteeId: 8888, TopUpId: 999, RebateQuota: 1, Sequence: 1,
	}).Error)

	rebates, total, err := GetUserAffRebates(affRebateInviterId, &common.PageInfo{Page: 1, PageSize: 2})
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, rebates, 2)
	require.Equal(t, 103, rebates[0].TopUpId)
	require.Equal(t, 102, rebates[1].TopUpId)

	rebates, total, err = GetUserAffRebates(affRebateInviterId, &common.PageInfo{Page: 2, PageSize: 2})
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, rebates, 1)
	require.Equal(t, 101, rebates[0].TopUpId)

	_, _, err = GetUserAffRebates(0, &common.PageInfo{Page: 1, PageSize: 2})
	require.Error(t, err)
}

func TestGetAffRebateInviteeIdentities(t *testing.T) {
	db := setupAffRebateTestDB(t)
	seedAffRebateUsers(t, db, affRebateInviterId)

	identities, err := GetAffRebateInviteeIdentities(nil)
	require.NoError(t, err)
	require.Empty(t, identities)

	rebates := []*AffRebate{
		{InviterId: affRebateInviterId, InviteeId: affRebateInviteeId, TopUpId: 1},
		{InviterId: affRebateInviterId, InviteeId: affRebateInviteeId, TopUpId: 2},
		{InviterId: affRebateInviterId, InviteeId: 123456, TopUpId: 3},
	}
	identities, err = GetAffRebateInviteeIdentities(rebates)
	require.NoError(t, err)
	require.Len(t, identities, 1, "deleted invitees are simply absent")
	require.Equal(t, "invitee@example.com", identities[affRebateInviteeId].Email)
	require.Equal(t, "rebate-invitee", identities[affRebateInviteeId].Username)
}
