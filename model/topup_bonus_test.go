package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	bonusUserId    = 7101
	bonusInviterId = 7102
)

// productionBonusTiers is the ladder SPEC section 1 signs off on: 1% from 100,
// 1.5% from 200, 2% from 500 and above.
var productionBonusTiers = map[int]float64{100: 0.01, 200: 0.015, 500: 0.02}

func setupTopUpBonusTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := DB
	originalLogDB := LOG_DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &TopUp{}, &Log{}, &AffRebate{}, &Redemption{}))
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

// setTopUpBonusConfig installs a bonus ladder for one test and restores the
// process-wide payment settings afterwards.
func setTopUpBonusConfig(t *testing.T, enabled bool, tiers map[int]float64) {
	t.Helper()
	setting := operation_setting.GetPaymentSetting()
	oldEnabled := setting.TopupBonusEnabled
	oldTiers := setting.TopupBonus
	oldRedis := common.RedisEnabled
	setting.TopupBonusEnabled = enabled
	setting.TopupBonus = tiers
	// The cache sync is a no-op without Redis; keep it off so the tests read the
	// committed database balance rather than a cached one.
	common.RedisEnabled = false
	t.Cleanup(func() {
		setting.TopupBonusEnabled = oldEnabled
		setting.TopupBonus = oldTiers
		common.RedisEnabled = oldRedis
	})
}

// unitsToQuota converts a display-unit amount (what the recharge form asks for)
// into wallet quota, the way every non-Creem settlement path does.
func unitsToQuota(units float64) int {
	return int(units * common.QuotaPerUnit)
}

func seedBonusUser(t *testing.T, db *gorm.DB, quota int) *User {
	t.Helper()
	user := &User{
		Id:       bonusUserId,
		Username: "bonus-user",
		Email:    "bonus@example.com",
		Status:   common.UserStatusEnabled,
		AffCode:  "bonus-user-code",
		Quota:    quota,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func seedBonusOrder(t *testing.T, db *gorm.DB, tradeNo string, provider string, amount int64, money float64) *TopUp {
	t.Helper()
	method := provider
	if provider == PaymentProviderEpay {
		method = "alipay"
	}
	topUp := &TopUp{
		UserId:          bonusUserId,
		Amount:          amount,
		Money:           money,
		TradeNo:         tradeNo,
		PaymentMethod:   method,
		PaymentProvider: provider,
		CreateTime:      common.GetTimestamp(),
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, db.Create(topUp).Error)
	return topUp
}

func quotaOf(t *testing.T, db *gorm.DB, userId int) int {
	t.Helper()
	var user User
	require.NoError(t, db.First(&user, userId).Error)
	return user.Quota
}

// ---------------------------------------------------------------------------
// SPEC 7.5 — tier selection
// ---------------------------------------------------------------------------

func TestTopUpBonusTierSelection(t *testing.T) {
	setTopUpBonusConfig(t, true, productionBonusTiers)

	// Amount in display units -> bonus in display units, straight from SPEC 7.5.
	cases := []struct {
		amount float64
		bonus  float64
	}{
		{99, 0},
		{100, 1},
		{199, 1.99},
		{200, 3},
		{300, 4.5},
		{500, 10},
		{1000, 20},
		{2000, 40},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("amount_%g", tc.amount), func(t *testing.T) {
			require.Equal(t, unitsToQuota(tc.bonus), topUpBonusQuota(unitsToQuota(tc.amount)))
		})
	}
}

// The ladder is a percentage of the amount, not a flat per-tier payout: 300 has
// to earn more than 200 even though both sit in the same tier.
func TestTopUpBonusScalesWithinATier(t *testing.T) {
	setTopUpBonusConfig(t, true, productionBonusTiers)

	at200 := topUpBonusQuota(unitsToQuota(200))
	at300 := topUpBonusQuota(unitsToQuota(300))
	require.Greater(t, at300, at200)
	require.Equal(t, unitsToQuota(3), at200)
	require.Equal(t, unitsToQuota(4.5), at300)
}

func TestTopUpBonusRejectsUnusableTiers(t *testing.T) {
	// Non-positive thresholds and ratios are ignored, and a ratio above 100% is
	// read as a misconfiguration rather than clamped, so nothing is granted.
	setTopUpBonusConfig(t, true, map[int]float64{0: 0.5, -100: 0.5, 100: 0, 200: -0.1, 500: 2})
	for _, amount := range []float64{50, 100, 200, 500, 1000} {
		require.Zero(t, topUpBonusQuota(unitsToQuota(amount)), "amount %g must earn nothing", amount)
	}

	// A single usable tier still works alongside the rejected ones.
	setTopUpBonusConfig(t, true, map[int]float64{100: 0.01, 500: 2})
	require.Equal(t, unitsToQuota(1), topUpBonusQuota(unitsToQuota(100)))
	require.Equal(t, unitsToQuota(10), topUpBonusQuota(unitsToQuota(1000)),
		"the unusable 500 tier must not shadow the 100 tier")
}

// ---------------------------------------------------------------------------
// SPEC 7.6 — switched off or unconfigured
// ---------------------------------------------------------------------------

func TestTopUpBonusIsZeroWhenDisabled(t *testing.T) {
	setTopUpBonusConfig(t, false, productionBonusTiers)
	require.Zero(t, topUpBonusQuota(unitsToQuota(1000)))
	require.Nil(t, operation_setting.TopupBonusTiers())
}

func TestTopUpBonusIsZeroWhenTiersAreEmpty(t *testing.T) {
	setTopUpBonusConfig(t, true, map[int]float64{})
	require.Zero(t, topUpBonusQuota(unitsToQuota(1000)))
	require.Nil(t, operation_setting.TopupBonusTiers())
}

// With the promotion off a settled top-up credits exactly what it always did.
func TestSettlementCreditsNothingExtraWhenDisabled(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, false, productionBonusTiers)
	seedBonusUser(t, db, 0)
	seedBonusOrder(t, db, "off-500", PaymentProviderEpay, 500, 500)

	_, err := RechargeEpay("off-500", "alipay", "500.00", "127.0.0.1")
	require.NoError(t, err)
	require.Equal(t, unitsToQuota(500), quotaOf(t, db, bonusUserId))

	total, err := SumTopUpBonusQuota(0, 0)
	require.NoError(t, err)
	require.Zero(t, total, "a disabled promotion writes no bonus audit rows")
}

// ---------------------------------------------------------------------------
// SPEC 7.1 — every settlement path grants the bonus, on the same scale
// ---------------------------------------------------------------------------

// Each provider reaches creditTopUpQuota with its own conversion already
// applied, so the only thing that makes the six agree is deriving the tier from
// the credited quota. Creem is the one that proves it: it stores the quota
// straight in TopUp.Amount, so an implementation keyed on TopUp.Amount * the
// per-unit rate would over-grant it by five orders of magnitude.
func TestEverySettlementPathGrantsTheSameBonus(t *testing.T) {
	want := unitsToQuota(10) // 500 display units at the 2% tier

	t.Run("epay", func(t *testing.T) {
		db := setupTopUpBonusTestDB(t)
		setTopUpBonusConfig(t, true, productionBonusTiers)
		seedBonusUser(t, db, 0)
		seedBonusOrder(t, db, "epay-500", PaymentProviderEpay, 500, 500)

		_, err := RechargeEpay("epay-500", "alipay", "500.00", "127.0.0.1")
		require.NoError(t, err)
		require.Equal(t, unitsToQuota(500)+want, quotaOf(t, db, bonusUserId))
	})

	t.Run("stripe", func(t *testing.T) {
		db := setupTopUpBonusTestDB(t)
		setTopUpBonusConfig(t, true, productionBonusTiers)
		seedBonusUser(t, db, 0)
		seedBonusOrder(t, db, "stripe-500", PaymentProviderStripe, 500, 500)

		require.NoError(t, Recharge("stripe-500", "cus_1", "127.0.0.1"))
		require.Equal(t, unitsToQuota(500)+want, quotaOf(t, db, bonusUserId))
	})

	t.Run("creem", func(t *testing.T) {
		db := setupTopUpBonusTestDB(t)
		setTopUpBonusConfig(t, true, productionBonusTiers)
		seedBonusUser(t, db, 0)
		// Creem prices the product in quota, not currency.
		seedBonusOrder(t, db, "creem-500", PaymentProviderCreem, int64(unitsToQuota(500)), 500)

		require.NoError(t, RechargeCreem("creem-500", "", "", "127.0.0.1"))
		require.Equal(t, unitsToQuota(500)+want, quotaOf(t, db, bonusUserId))
	})

	t.Run("waffo", func(t *testing.T) {
		db := setupTopUpBonusTestDB(t)
		setTopUpBonusConfig(t, true, productionBonusTiers)
		seedBonusUser(t, db, 0)
		seedBonusOrder(t, db, "waffo-500", PaymentProviderWaffo, 500, 500)

		require.NoError(t, RechargeWaffo("waffo-500", "127.0.0.1"))
		require.Equal(t, unitsToQuota(500)+want, quotaOf(t, db, bonusUserId))
	})

	t.Run("waffo_pancake", func(t *testing.T) {
		db := setupTopUpBonusTestDB(t)
		setTopUpBonusConfig(t, true, productionBonusTiers)
		seedBonusUser(t, db, 0)
		seedBonusOrder(t, db, "pancake-500", PaymentProviderWaffoPancake, 500, 500)

		require.NoError(t, RechargeWaffoPancake("pancake-500"))
		require.Equal(t, unitsToQuota(500)+want, quotaOf(t, db, bonusUserId))
	})

	t.Run("manual_back_fill", func(t *testing.T) {
		db := setupTopUpBonusTestDB(t)
		setTopUpBonusConfig(t, true, productionBonusTiers)
		seedBonusUser(t, db, 0)
		seedBonusOrder(t, db, "manual-500", PaymentProviderEpay, 500, 500)

		require.NoError(t, ManualCompleteTopUp("manual-500", "127.0.0.1"))
		require.Equal(t, unitsToQuota(500)+want, quotaOf(t, db, bonusUserId))
	})
}

// The bonus shares the main credit's UPDATE, so a credit that the wallet
// ceiling rejects leaves the balance completely untouched — there is no window
// where the bonus landed and the principal did not, or the reverse.
func TestBonusAndPrincipalAreCreditedAtomically(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, true, productionBonusTiers)

	// Room for the 500 principal but not for the 10 bonus on top.
	credited := unitsToQuota(500)
	bonus := unitsToQuota(10)
	startingQuota := common.MaxWalletQuota - credited - bonus + 1
	seedBonusUser(t, db, startingQuota)
	seedBonusOrder(t, db, "atomic-500", PaymentProviderEpay, 500, 500)

	_, err := RechargeEpay("atomic-500", "alipay", "500.00", "127.0.0.1")
	require.ErrorIs(t, err, ErrTopUpQuotaLimitExceeded)
	require.Equal(t, startingQuota, quotaOf(t, db, bonusUserId),
		"a rejected credit must not move the balance at all")

	var reloaded TopUp
	require.NoError(t, db.Where("trade_no = ?", "atomic-500").First(&reloaded).Error)
	require.Equal(t, common.TopUpStatusPending, reloaded.Status,
		"the whole transaction rolls back, leaving the order settleable")
}

// ---------------------------------------------------------------------------
// SPEC 7.2 — idempotency
// ---------------------------------------------------------------------------

func TestReplayedCallbackGrantsTheBonusOnce(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, true, productionBonusTiers)
	seedBonusUser(t, db, 0)
	seedBonusOrder(t, db, "replay-500", PaymentProviderEpay, 500, 500)

	expected := unitsToQuota(500) + unitsToQuota(10)

	alreadyDone, err := RechargeEpay("replay-500", "alipay", "500.00", "127.0.0.1")
	require.NoError(t, err)
	require.False(t, alreadyDone)
	require.Equal(t, expected, quotaOf(t, db, bonusUserId))

	alreadyDone, err = RechargeEpay("replay-500", "alipay", "500.00", "127.0.0.1")
	require.NoError(t, err)
	require.True(t, alreadyDone)
	require.Equal(t, expected, quotaOf(t, db, bonusUserId),
		"the pending -> success transition is what makes the bonus single-shot")

	total, err := SumTopUpBonusQuota(0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(unitsToQuota(10)), total, "the audit trail must not double-count either")
}

// A back-filled order that was already settled must not top the bonus up again.
func TestManualCompleteDoesNotRegrantTheBonus(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, true, productionBonusTiers)
	seedBonusUser(t, db, 0)
	seedBonusOrder(t, db, "manual-replay", PaymentProviderEpay, 500, 500)

	expected := unitsToQuota(500) + unitsToQuota(10)
	require.NoError(t, ManualCompleteTopUp("manual-replay", "127.0.0.1"))
	require.Equal(t, expected, quotaOf(t, db, bonusUserId))

	require.NoError(t, ManualCompleteTopUp("manual-replay", "127.0.0.1"))
	require.Equal(t, expected, quotaOf(t, db, bonusUserId))
}

// ---------------------------------------------------------------------------
// SPEC 7.3 — the ceiling counts the bonus, and it does so before payment
// ---------------------------------------------------------------------------

// The money path this guards: without the bonus in the pre-payment check the
// order passes checkout, the user pays, and settlement then refuses to credit.
func TestCheckoutRejectsATopUpThatOnlyTheBonusPushesOverTheCeiling(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, true, productionBonusTiers)

	credited := unitsToQuota(500)
	bonus := unitsToQuota(10)
	// One quota short of fitting the principal plus its bonus, and one quota
	// clear of fitting the principal on its own.
	seedBonusUser(t, db, common.MaxWalletQuota-credited-bonus+1)

	// Without the bonus this same balance passes, which is exactly the gap.
	setTopUpBonusConfig(t, false, productionBonusTiers)
	require.NoError(t, ValidateTopUpQuotaCapacity(bonusUserId, credited),
		"the principal alone still fits, so only the bonus can make this fail")

	setTopUpBonusConfig(t, true, productionBonusTiers)
	require.ErrorIs(t, ValidateTopUpQuotaCapacity(bonusUserId, credited), ErrTopUpQuotaLimitExceeded)

	// And the user must be turned away before paying rather than after: no
	// order was created, so nothing is left half-settled.
	var orders int64
	require.NoError(t, db.Model(&TopUp{}).Count(&orders).Error)
	require.Zero(t, orders)
}

// The settlement-side predicate agrees with the pre-payment check: a balance
// that grew after checkout is still stopped, and stopped as a whole.
func TestSettlementCeilingCountsTheBonus(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, true, productionBonusTiers)

	credited := unitsToQuota(500)
	bonus := unitsToQuota(10)
	headroom := common.MaxWalletQuota - credited - bonus

	// Exactly enough room for principal and bonus together: this one settles.
	seedBonusUser(t, db, headroom)
	seedBonusOrder(t, db, "ceiling-fits", PaymentProviderEpay, 500, 500)
	_, err := RechargeEpay("ceiling-fits", "alipay", "500.00", "127.0.0.1")
	require.NoError(t, err)
	require.Equal(t, headroom+credited+bonus, quotaOf(t, db, bonusUserId))
	require.Equal(t, common.MaxWalletQuota, quotaOf(t, db, bonusUserId))
}

// ---------------------------------------------------------------------------
// SPEC 7.4 — the referral rebate is unaffected
// ---------------------------------------------------------------------------

// The rebate is deliberately computed from what the invitee paid, never from
// what landed in their wallet, so that top-up promotions cannot inflate it.
// The bonus changes neither TopUp.Money nor TopUp.Amount, so the same order has
// to produce a byte-identical rebate with the promotion on and off.
func TestBonusDoesNotChangeTheReferralRebate(t *testing.T) {
	runOnce := func(t *testing.T, bonusEnabled bool) (rebateQuota int, walletQuota int) {
		t.Helper()
		db := setupTopUpBonusTestDB(t)
		setAffRebateConfig(t, true, 5, 3)
		setTopUpBonusConfig(t, bonusEnabled, productionBonusTiers)

		inviter := &User{
			Id: bonusInviterId, Username: "bonus-inviter", Email: "bonus-inviter@example.com",
			Status: common.UserStatusEnabled, AffCode: "bonus-inviter-code",
		}
		require.NoError(t, db.Create(inviter).Error)
		invitee := &User{
			Id: bonusUserId, Username: "bonus-user", Email: "bonus@example.com",
			Status: common.UserStatusEnabled, AffCode: "bonus-user-code", InviterId: bonusInviterId,
		}
		require.NoError(t, db.Create(invitee).Error)
		seedBonusOrder(t, db, "rebate-500", PaymentProviderEpay, 500, 500)

		_, err := RechargeEpay("rebate-500", "alipay", "500.00", "127.0.0.1")
		require.NoError(t, err)

		var rebate AffRebate
		require.NoError(t, db.Where("top_up_id > 0").First(&rebate).Error)
		return rebate.RebateQuota, quotaOf(t, db, bonusUserId)
	}

	var withoutBonus, withBonus int
	var walletWithout, walletWith int
	t.Run("promotion_off", func(t *testing.T) { withoutBonus, walletWithout = runOnce(t, false) })
	t.Run("promotion_on", func(t *testing.T) { withBonus, walletWith = runOnce(t, true) })

	require.Equal(t, unitsToQuota(25), withoutBonus, "5% of the 500 actually paid")
	require.Equal(t, withoutBonus, withBonus, "the bonus must not move the rebate by a single quota")
	// Sanity: the promotion really was on for the second run.
	require.Equal(t, walletWithout+unitsToQuota(10), walletWith)
}

// ---------------------------------------------------------------------------
// SPEC 7.7 — redemption codes are excluded
// ---------------------------------------------------------------------------

// Redemption reaches the same atomic credit primitive as a top-up, so the
// exclusion has to be explicit; nothing about the code path makes it automatic.
func TestRedemptionCodeEarnsNoBonus(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, true, productionBonusTiers)
	seedBonusUser(t, db, 0)

	redemption := &Redemption{
		UserId: bonusUserId,
		Key:    "REDEEM-BONUS-TEST",
		Status: common.RedemptionCodeStatusEnabled,
		Name:   "bonus-test",
		Quota:  unitsToQuota(500),
	}
	require.NoError(t, db.Create(redemption).Error)

	credited, err := Redeem("REDEEM-BONUS-TEST", bonusUserId)
	require.NoError(t, err)
	require.Equal(t, unitsToQuota(500), credited)
	require.Equal(t, unitsToQuota(500), quotaOf(t, db, bonusUserId),
		"a redemption code credits its face value and nothing more")

	total, err := SumTopUpBonusQuota(0, 0)
	require.NoError(t, err)
	require.Zero(t, total)
}

// ---------------------------------------------------------------------------
// SPEC 7.8 — auditability
// ---------------------------------------------------------------------------

func TestGrantedBonusIsAuditable(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, true, productionBonusTiers)
	seedBonusUser(t, db, 0)
	seedBonusOrder(t, db, "audit-500", PaymentProviderEpay, 500, 500)

	_, err := RechargeEpay("audit-500", "alipay", "500.00", "127.0.0.1")
	require.NoError(t, err)

	var bonusLog Log
	require.NoError(t, db.Where("type = ? AND quota > 0", LogTypeTopup).First(&bonusLog).Error)
	require.Equal(t, unitsToQuota(10), bonusLog.Quota)
	require.True(t, strings.HasPrefix(bonusLog.Content, topUpBonusLogPrefix), "content was %q", bonusLog.Content)
	// SPEC 3.6 wants both sides of the grant readable in the log list.
	require.Contains(t, bonusLog.Content, "充值")
	require.Contains(t, bonusLog.Content, "赠送")
	require.Contains(t, bonusLog.Other, "bonus_quota")
	require.Contains(t, bonusLog.Other, "credited_quota")

	total, err := SumTopUpBonusQuota(0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(unitsToQuota(10)), total)
}

func TestSumTopUpBonusQuotaWindowsByTime(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	setTopUpBonusConfig(t, true, productionBonusTiers)
	seedBonusUser(t, db, 0)

	RecordTopUpBonusLog(bonusUserId, unitsToQuota(500), unitsToQuota(10), "127.0.0.1", "alipay", PaymentProviderEpay)
	RecordTopUpBonusLog(bonusUserId, unitsToQuota(200), unitsToQuota(3), "127.0.0.1", "alipay", PaymentProviderEpay)

	// Age the first row so the two fall on either side of a cutoff.
	var rows []Log
	require.NoError(t, db.Where("type = ? AND quota > 0", LogTypeTopup).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.NoError(t, db.Model(&Log{}).Where("id = ?", rows[0].Id).Update("created_at", 1000).Error)
	require.NoError(t, db.Model(&Log{}).Where("id = ?", rows[1].Id).Update("created_at", 2000).Error)

	all, err := SumTopUpBonusQuota(0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(unitsToQuota(13)), all)

	early, err := SumTopUpBonusQuota(0, 1500)
	require.NoError(t, err)
	require.Equal(t, int64(unitsToQuota(10)), early)

	late, err := SumTopUpBonusQuota(1500, 0)
	require.NoError(t, err)
	require.Equal(t, int64(unitsToQuota(3)), late)

	none, err := SumTopUpBonusQuota(3000, 4000)
	require.NoError(t, err)
	require.Zero(t, none, "an empty window sums to zero rather than failing on a NULL")
}

// The aggregate keys on "LogTypeTopup with a non-zero quota and the bonus
// marker". Pin that the other writers of LogTypeTopup do not satisfy it, so a
// plain top-up or a redemption can never be counted as money given away.
func TestSumTopUpBonusQuotaIgnoresOtherTopUpLogs(t *testing.T) {
	db := setupTopUpBonusTestDB(t)
	seedBonusUser(t, db, 0)

	// Every other writer of a LogTypeTopup row, exercised for real.
	RecordTopupLog(bonusUserId, "使用在线充值成功，充值金额: 500，支付金额：500", "127.0.0.1", "alipay", PaymentProviderEpay)
	RecordLog(bonusUserId, LogTypeTopup, "通过兑换码充值 500，兑换码ID 1")
	RecordLogWithAdminInfo(bonusUserId, LogTypeTopup, "管理员调整额度 500", nil, nil)
	RecordLog(bonusUserId, LogTypeSystem, "邀请充值返利 25")

	var topUpRows []Log
	require.NoError(t, db.Where("type = ?", LogTypeTopup).Find(&topUpRows).Error)
	require.Len(t, topUpRows, 3)
	for _, row := range topUpRows {
		require.Zero(t, row.Quota, "no other LogTypeTopup writer may fill Quota: %q", row.Content)
		require.False(t, strings.HasPrefix(row.Content, topUpBonusLogPrefix))
	}

	total, err := SumTopUpBonusQuota(0, 0)
	require.NoError(t, err)
	require.Zero(t, total)
}

// ---------------------------------------------------------------------------
// Configuration plumbing
// ---------------------------------------------------------------------------

// The tiers the wallet advertises have to be the tiers the settlement path
// would actually grant, or the card promises a bonus that never arrives.
func TestAdvertisedTiersMatchWhatIsGranted(t *testing.T) {
	setTopUpBonusConfig(t, true, map[int]float64{100: 0.01, 200: 0.015, 500: 0.02, 0: 0.5, 1000: 3})

	tiers := operation_setting.TopupBonusTiers()
	require.Equal(t, map[int]float64{100: 0.01, 200: 0.015, 500: 0.02}, tiers,
		"unusable tiers are filtered before they reach the client")

	for threshold, ratio := range tiers {
		want := int(float64(unitsToQuota(float64(threshold))) * ratio)
		require.Equal(t, want, topUpBonusQuota(unitsToQuota(float64(threshold))),
			"threshold %d advertises %v but grants something else", threshold, ratio)
	}
}

// Tokens mode denominates the recharge form in quota rather than currency, so
// the thresholds are read on that scale too and the two ends stay in step.
func TestTopUpBonusFollowsTheQuotaDisplayUnit(t *testing.T) {
	setTopUpBonusConfig(t, true, map[int]float64{100: 0.01})

	general := operation_setting.GetGeneralSetting()
	original := general.QuotaDisplayType
	t.Cleanup(func() { general.QuotaDisplayType = original })

	general.QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	require.Equal(t, unitsToQuota(1), topUpBonusQuota(unitsToQuota(100)))
	require.Zero(t, topUpBonusQuota(unitsToQuota(99)))

	general.QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens
	require.Equal(t, 1, topUpBonusQuota(100))
	require.Zero(t, topUpBonusQuota(99))
}

func TestTopUpBonusRejectsNonPositiveCredits(t *testing.T) {
	setTopUpBonusConfig(t, true, productionBonusTiers)
	require.Zero(t, topUpBonusQuota(0))
	require.Zero(t, topUpBonusQuota(-1))
}
