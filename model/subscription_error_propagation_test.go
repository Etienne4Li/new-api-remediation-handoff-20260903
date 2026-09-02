package model

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useIsolatedSubscriptionDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousLogDB := LOG_DB
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB, LOG_DB = db, db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		_ = sqlDB.Close()
	})
	return db
}

func TestSubscriptionDatabaseEntryPointsFailClosedWithoutDatabase(t *testing.T) {
	previousDB := DB
	DB = nil
	t.Cleanup(func() { DB = previousDB })

	_, err := ExpireDueSubscriptions(1)
	require.ErrorIs(t, err, ErrDatabase)
	_, err = ResetDueSubscriptions(1)
	require.ErrorIs(t, err, ErrDatabase)
	_, err = CleanupSubscriptionPreConsumeRecords(1)
	require.ErrorIs(t, err, ErrDatabase)
	_, err = PreConsumeUserSubscription("nil-db-preconsume", 1, "test", 0, 1)
	require.ErrorIs(t, err, ErrDatabase)
	err = RefundSubscriptionPreConsume("nil-db-refund")
	require.ErrorIs(t, err, ErrDatabase)
	_, err = GetSubscriptionPlanInfoByUserSubscriptionId(991001)
	require.ErrorIs(t, err, ErrDatabase)
	err = PostConsumeUserSubscriptionDelta(991001, 1)
	require.ErrorIs(t, err, ErrDatabase)
}

func TestPreConsumeUserSubscriptionPreservesLookupError(t *testing.T) {
	db := useIsolatedSubscriptionDatabase(t)
	require.NoError(t, db.AutoMigrate(&User{}, &SubscriptionPreConsumeRecord{}))
	require.NoError(t, db.Exec(`CREATE TABLE user_subscriptions (
		id INTEGER PRIMARY KEY,
		user_id INTEGER,
		status TEXT,
		amount_total BIGINT,
		amount_used BIGINT
	)`).Error)
	require.NoError(t, db.Create(&User{Id: 991010, Username: "subscription-query-error"}).Error)

	_, err := PreConsumeUserSubscription("lookup-error", 991010, "test", 0, 1)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "end_time")
	assert.NotContains(t, err.Error(), "no active subscription")
}

func TestExpireDueSubscriptionsRollsBackOnFollowupQueryError(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		extraColumn string
		missing     string
	}{
		{name: "active subscription lookup", missing: "upgrade_group"},
		{name: "expired subscription lookup", extraColumn: ", upgrade_group TEXT", missing: "downgrade_group"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := useIsolatedSubscriptionDatabase(t)
			require.NoError(t, db.AutoMigrate(&User{}))
			require.NoError(t, db.Exec(`CREATE TABLE user_subscriptions (
				id INTEGER PRIMARY KEY,
				user_id INTEGER,
				status TEXT,
				end_time BIGINT,
				updated_at BIGINT`+testCase.extraColumn+`
			)`).Error)
			const userID = 991020
			require.NoError(t, db.Create(&User{Id: userID, Username: "expiration-query-error"}).Error)
			require.NoError(t, db.Exec(
				"INSERT INTO user_subscriptions (id, user_id, status, end_time, updated_at) VALUES (?, ?, ?, ?, ?)",
				1, userID, "active", time.Now().Add(-time.Minute).Unix(), common.GetTimestamp(),
			).Error)

			count, err := ExpireDueSubscriptions(10)
			require.Error(t, err)
			assert.Contains(t, strings.ToLower(err.Error()), testCase.missing)
			assert.Zero(t, count)
			var status string
			require.NoError(t, db.Raw("SELECT status FROM user_subscriptions WHERE id = ?", 1).Scan(&status).Error)
			assert.Equal(t, "active", status, "the expiration update must roll back with its follow-up query")
		})
	}
}

func TestSubscriptionUsageOverflowDoesNotMutateLedger(t *testing.T) {
	db := useIsolatedSubscriptionDatabase(t)
	require.NoError(t, db.AutoMigrate(&User{}, &SubscriptionPlan{}, &UserSubscription{}, &SubscriptionPreConsumeRecord{}))
	const (
		userID = 991030
		planID = 991031
	)
	require.NoError(t, db.Create(&User{Id: userID, Username: "subscription-overflow"}).Error)
	require.NoError(t, db.Create(&SubscriptionPlan{
		Id: planID, Title: "unlimited", Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1,
		Enabled: true, TotalAmount: 0, QuotaResetPeriod: SubscriptionResetNever,
	}).Error)
	sub := &UserSubscription{
		UserId: userID, PlanId: planID, AmountTotal: 0, AmountUsed: math.MaxInt64,
		Status: "active", EndTime: time.Now().Add(time.Hour).Unix(),
	}
	require.NoError(t, db.Create(sub).Error)

	err := PostConsumeUserSubscriptionDelta(sub.Id, 1)
	require.ErrorContains(t, err, "subscription usage overflow")
	_, err = PreConsumeUserSubscription("overflow-preconsume", userID, "test", 0, 1)
	require.ErrorContains(t, err, "subscription usage overflow")

	var got UserSubscription
	require.NoError(t, db.First(&got, sub.Id).Error)
	assert.Equal(t, int64(math.MaxInt64), got.AmountUsed)
	var markerCount int64
	require.NoError(t, db.Model(&SubscriptionPreConsumeRecord{}).
		Where("request_id = ?", "overflow-preconsume").Count(&markerCount).Error)
	assert.Zero(t, markerCount)
}
