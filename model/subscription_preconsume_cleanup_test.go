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

func TestCleanupSubscriptionPreConsumeRecordsKeepsConsumedRecoveryMarkers(t *testing.T) {
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

	require.NoError(t, db.AutoMigrate(&SubscriptionPreConsumeRecord{}))
	old := GetDBTimestamp() - 10_000
	consumed := &SubscriptionPreConsumeRecord{
		RequestId:          "cleanup-consumed",
		UserId:             1,
		UserSubscriptionId: 1,
		PreConsumed:        25,
		Status:             "consumed",
		CreatedAt:          old,
		UpdatedAt:          old,
	}
	refunded := &SubscriptionPreConsumeRecord{
		RequestId:          "cleanup-refunded",
		UserId:             1,
		UserSubscriptionId: 1,
		PreConsumed:        25,
		Status:             "refunded",
		CreatedAt:          old,
		UpdatedAt:          old,
	}
	recentRefunded := &SubscriptionPreConsumeRecord{
		RequestId:          "cleanup-recent-refunded",
		UserId:             1,
		UserSubscriptionId: 1,
		PreConsumed:        25,
		Status:             "refunded",
		CreatedAt:          GetDBTimestamp(),
		UpdatedAt:          GetDBTimestamp(),
	}
	require.NoError(t, db.Create(consumed).Error)
	require.NoError(t, db.Create(refunded).Error)
	require.NoError(t, db.Create(recentRefunded).Error)
	// Model hooks stamp create/update times; move the two historical rows back
	// past the retention boundary after insertion.
	require.NoError(t, db.Model(&SubscriptionPreConsumeRecord{}).
		Where("request_id IN ?", []string{consumed.RequestId, refunded.RequestId}).
		Updates(map[string]interface{}{"created_at": old, "updated_at": old}).Error)

	deleted, err := CleanupSubscriptionPreConsumeRecords(3600)
	require.NoError(t, err)
	assert.EqualValues(t, int64(1), deleted)

	var gotConsumed, gotRecent SubscriptionPreConsumeRecord
	require.NoError(t, db.Where("request_id = ?", consumed.RequestId).First(&gotConsumed).Error)
	require.NoError(t, db.Where("request_id = ?", recentRefunded.RequestId).First(&gotRecent).Error)
	assert.Equal(t, "consumed", gotConsumed.Status)
	assert.Equal(t, "refunded", gotRecent.Status)
	var count int64
	require.NoError(t, db.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", refunded.RequestId).Count(&count).Error)
	assert.Zero(t, count, "only an already-refunded old marker may be removed")
}
