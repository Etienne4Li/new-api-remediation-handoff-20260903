package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestRefundSubscriptionPreConsumeIsAtomicWithMarker deliberately makes the
// marker write fail after the subscription row has been updated. A correct
// implementation must roll the usage update back with the marker; otherwise a
// retry can refund the same reservation more than once.
func TestRefundSubscriptionPreConsumeIsAtomicWithMarker(t *testing.T) {
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

	require.NoError(t, db.AutoMigrate(&User{}, &UserSubscription{}, &SubscriptionPreConsumeRecord{}))
	sub := &UserSubscription{
		UserId:      1,
		PlanId:      1,
		AmountTotal: 1000,
		AmountUsed:  100,
		Status:      "active",
	}
	require.NoError(t, db.Create(&User{Id: sub.UserId, Username: "atomic-refund-user"}).Error)
	require.NoError(t, db.Create(sub).Error)
	record := &SubscriptionPreConsumeRecord{
		RequestId:          "atomic-refund-request",
		UserId:             sub.UserId,
		UserSubscriptionId: sub.Id,
		PreConsumed:        40,
		Status:             "consumed",
	}
	require.NoError(t, db.Create(record).Error)

	// Force the final marker write to fail. The transaction must also roll back
	// the subscription usage decrement performed immediately before it.
	require.NoError(t, db.Exec(`
CREATE TRIGGER fail_refund_marker
BEFORE UPDATE OF status ON subscription_pre_consume_records
WHEN NEW.status = 'refunded'
BEGIN
  SELECT RAISE(ABORT, 'forced marker failure');
END`).Error)

	err = RefundSubscriptionPreConsume(record.RequestId)
	require.ErrorContains(t, err, "forced marker failure")
	var gotSub UserSubscription
	require.NoError(t, db.First(&gotSub, sub.Id).Error)
	assert.EqualValues(t, 100, gotSub.AmountUsed, "usage decrement must roll back with marker failure")
	var gotRecord SubscriptionPreConsumeRecord
	require.NoError(t, db.First(&gotRecord, record.Id).Error)
	assert.Equal(t, "consumed", gotRecord.Status)

	require.NoError(t, db.Exec("DROP TRIGGER fail_refund_marker").Error)
	require.NoError(t, RefundSubscriptionPreConsume(record.RequestId))
	require.NoError(t, db.First(&gotSub, sub.Id).Error)
	assert.EqualValues(t, 60, gotSub.AmountUsed)
	require.NoError(t, db.First(&gotRecord, record.Id).Error)
	assert.Equal(t, "refunded", gotRecord.Status)

	// The marker remains the idempotency fence on retries.
	require.NoError(t, RefundSubscriptionPreConsume(record.RequestId))
	require.NoError(t, db.First(&gotSub, sub.Id).Error)
	assert.EqualValues(t, 60, gotSub.AmountUsed)
}

func TestRefundSubscriptionPreConsumeRejectsUsageUnderflow(t *testing.T) {
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

	require.NoError(t, db.AutoMigrate(&User{}, &UserSubscription{}, &SubscriptionPreConsumeRecord{}))
	sub := &UserSubscription{UserId: 2, PlanId: 1, AmountTotal: 1000, AmountUsed: 10, Status: "active"}
	require.NoError(t, db.Create(&User{Id: sub.UserId, Username: "underflow-refund-user"}).Error)
	require.NoError(t, db.Create(sub).Error)
	record := &SubscriptionPreConsumeRecord{
		RequestId: "underflow-refund-request", UserId: sub.UserId,
		UserSubscriptionId: sub.Id, PreConsumed: 40, Status: "consumed",
	}
	require.NoError(t, db.Create(record).Error)

	err = RefundSubscriptionPreConsume(record.RequestId)
	require.ErrorContains(t, err, "subscription usage underflow")
	var gotSub UserSubscription
	require.NoError(t, db.First(&gotSub, sub.Id).Error)
	assert.EqualValues(t, 10, gotSub.AmountUsed)
	var gotRecord SubscriptionPreConsumeRecord
	require.NoError(t, db.First(&gotRecord, record.Id).Error)
	assert.Equal(t, "consumed", gotRecord.Status)
}

func TestBillingOperationRefundClosesReservationAtomically(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9201, 0)
	sub := &UserSubscription{Id: 9201, UserId: user.Id, AmountTotal: 1000, AmountUsed: 100, Status: "active"}
	require.NoError(t, DB.Create(sub).Error)
	token := billingOperationTestToken(t, 9201, user.Id, 60)
	token.UsedQuota = 40
	require.NoError(t, DB.Model(token).Update("used_quota", token.UsedQuota).Error)
	record := &SubscriptionPreConsumeRecord{
		RequestId:          "billing-operation-atomic-refund",
		UserId:             user.Id,
		UserSubscriptionId: sub.Id,
		PreConsumed:        40,
		Status:             "consumed",
	}
	require.NoError(t, DB.Create(record).Error)
	spec := BillingOperationSpec{
		RequestID:         record.RequestId,
		Component:         "subscription_refund",
		UserID:            user.Id,
		TokenID:           token.Id,
		SubscriptionID:    sub.Id,
		TokenDelta:        -record.PreConsumed,
		SubscriptionDelta: -record.PreConsumed,
	}

	require.NoError(t, ApplyBillingOperationAndMarkSubscriptionPreConsumeRefunded(spec, record.RequestId))
	require.NoError(t, ApplyBillingOperationAndMarkSubscriptionPreConsumeRefunded(spec, record.RequestId))

	var gotSub UserSubscription
	require.NoError(t, DB.First(&gotSub, sub.Id).Error)
	assert.EqualValues(t, 60, gotSub.AmountUsed)
	var gotToken Token
	require.NoError(t, DB.First(&gotToken, token.Id).Error)
	assert.Equal(t, 100, gotToken.RemainQuota)
	assert.Zero(t, gotToken.UsedQuota)
	var gotRecord SubscriptionPreConsumeRecord
	require.NoError(t, DB.First(&gotRecord, record.Id).Error)
	assert.Equal(t, "refunded", gotRecord.Status)
}

func TestBillingOperationRefundRejectsPendingOperationForClosedReservation(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9202, 0)
	sub := &UserSubscription{Id: 9202, UserId: user.Id, AmountTotal: 1000, AmountUsed: 60, Status: "active"}
	require.NoError(t, DB.Create(sub).Error)
	record := &SubscriptionPreConsumeRecord{
		RequestId:          "billing-operation-pending-closed-refund",
		UserId:             user.Id,
		UserSubscriptionId: sub.Id,
		PreConsumed:        40,
		Status:             "refunded",
	}
	require.NoError(t, DB.Create(record).Error)
	spec := BillingOperationSpec{
		RequestID:         record.RequestId,
		Component:         "subscription_refund",
		UserID:            user.Id,
		SubscriptionID:    sub.Id,
		SubscriptionDelta: -record.PreConsumed,
	}
	normalized, err := normalizeBillingOperationSpec(spec)
	require.NoError(t, err)
	pending := billingOperationFromSpec(normalized)
	require.NoError(t, DB.Create(&pending).Error)

	err = ApplyBillingOperationAndMarkSubscriptionPreConsumeRefunded(spec, record.RequestId)
	require.ErrorIs(t, err, ErrBillingOperationConflict)

	var gotSub UserSubscription
	require.NoError(t, DB.First(&gotSub, sub.Id).Error)
	assert.EqualValues(t, 60, gotSub.AmountUsed, "a closed reservation must not drive a new refund")
	var gotOperation BillingOperation
	require.NoError(t, DB.First(&gotOperation, pending.Id).Error)
	assert.Equal(t, BillingOperationPending, gotOperation.Status)
}
