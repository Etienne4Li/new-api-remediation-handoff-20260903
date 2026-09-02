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

func setupEpayTopUpTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB := DB
	originalLogDB := LOG_DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &TopUp{}, &Log{}))
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

func seedPendingEpayTopUp(t *testing.T, db *gorm.DB, tradeNo string, amount int64, money float64) *User {
	t.Helper()
	user := &User{Id: 4201, Username: "epay-user", Quota: 1000, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(user).Error)
	topUp := &TopUp{
		UserId:          user.Id,
		Amount:          amount,
		Money:           money,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		CreateTime:      common.GetTimestamp(),
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, db.Create(topUp).Error)
	return user
}

func TestEpayPaidMoneyMatches(t *testing.T) {
	require.True(t, epayPaidMoneyMatches("10.00", 10))
	require.True(t, epayPaidMoneyMatches("10", 10.0))
	require.True(t, epayPaidMoneyMatches(" 0.30 ", 0.1+0.2))
	require.False(t, epayPaidMoneyMatches("9.99", 10))
	require.False(t, epayPaidMoneyMatches("10.001", 10))
	require.False(t, epayPaidMoneyMatches("", 10))
	require.False(t, epayPaidMoneyMatches("abc", 10))
	require.False(t, epayPaidMoneyMatches("0", 0))
}

func TestRechargeEpayRejectsUnderpaidCallback(t *testing.T) {
	db := setupEpayTopUpTestDB(t)
	user := seedPendingEpayTopUp(t, db, "TUC-underpaid", 100, 100)

	alreadyDone, err := RechargeEpay("TUC-underpaid", "alipay", "1.00", "127.0.0.1")
	require.ErrorIs(t, err, ErrEpayAmountMismatch)
	require.False(t, alreadyDone)

	var topUp TopUp
	require.NoError(t, db.Where("trade_no = ?", "TUC-underpaid").First(&topUp).Error)
	require.Equal(t, common.TopUpStatusPending, topUp.Status, "order must stay pending so a genuine callback can still settle it")

	var reloaded User
	require.NoError(t, db.First(&reloaded, user.Id).Error)
	require.Equal(t, 1000, reloaded.Quota, "no quota may be credited on an amount mismatch")
}

func TestRechargeEpayRejectsMissingMoney(t *testing.T) {
	db := setupEpayTopUpTestDB(t)
	seedPendingEpayTopUp(t, db, "TUC-nomoney", 100, 100)

	_, err := RechargeEpay("TUC-nomoney", "alipay", "", "127.0.0.1")
	require.ErrorIs(t, err, ErrEpayAmountMismatch)
}

func TestRechargeEpaySettlesMatchingCallbackOnce(t *testing.T) {
	db := setupEpayTopUpTestDB(t)
	user := seedPendingEpayTopUp(t, db, "TUC-ok", 10, 10)

	alreadyDone, err := RechargeEpay("TUC-ok", "alipay", "10.00", "127.0.0.1")
	require.NoError(t, err)
	require.False(t, alreadyDone)

	var topUp TopUp
	require.NoError(t, db.Where("trade_no = ?", "TUC-ok").First(&topUp).Error)
	require.Equal(t, common.TopUpStatusSuccess, topUp.Status)

	var reloaded User
	require.NoError(t, db.First(&reloaded, user.Id).Error)
	expected := 1000 + int(float64(10)*common.QuotaPerUnit)
	require.Equal(t, expected, reloaded.Quota)

	// A replayed callback, even with a different amount, is idempotent and
	// must not credit again.
	alreadyDone, err = RechargeEpay("TUC-ok", "alipay", "999.00", "127.0.0.1")
	require.NoError(t, err)
	require.True(t, alreadyDone)
	require.NoError(t, db.First(&reloaded, user.Id).Error)
	require.Equal(t, expected, reloaded.Quota)
}
