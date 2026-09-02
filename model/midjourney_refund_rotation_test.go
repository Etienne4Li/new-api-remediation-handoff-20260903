package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyMidjourneyRefundRow struct {
	Id         int
	Quota      int
	Status     string `gorm:"type:varchar(20);index"`
	Progress   string `gorm:"type:varchar(30);index"`
	FailReason string
}

func (legacyMidjourneyRefundRow) TableName() string {
	return "midjourneys"
}

func TestMidjourneyRefundRetryCursorMigratesLegacyRows(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() { DB = previousDB })

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	DB = db

	require.NoError(t, db.AutoMigrate(&legacyMidjourneyRefundRow{}))
	require.NoError(t, db.Create(&legacyMidjourneyRefundRow{
		Id: 1, Quota: 700, Status: "FAILURE", Progress: "100%", FailReason: "legacy failure",
	}).Error)

	require.NoError(t, db.AutoMigrate(&Midjourney{}))
	assert.True(t, db.Migrator().HasColumn(&Midjourney{}, "RefundRetryAt"))

	rows, err := GetMidjourneyTasksWithPendingRefundWithError(1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Zero(t, rows[0].RefundRetryAt)

	require.NoError(t, DeferMidjourneyRefundReconciliation(rows[0].Id, rows[0].RefundRetryAt))
	var deferred Midjourney
	require.NoError(t, db.Select("id", "quota", "refund_retry_at").First(&deferred, rows[0].Id).Error)
	assert.Equal(t, 700, deferred.Quota)
	assert.Positive(t, deferred.RefundRetryAt)
}
