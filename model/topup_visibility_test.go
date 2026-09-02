package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUserTopUpHistoryRetainsOldPaidUncreditedOrders(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previousDB := DB
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(&TopUp{}))

	oldCreateTime := time.Now().Unix() - topUpQueryWindowSeconds - 1
	rows := []TopUp{
		{UserId: 42, TradeNo: "old-pending", CreateTime: oldCreateTime, Status: common.TopUpStatusPending},
		{UserId: 42, TradeNo: "old-success", CreateTime: oldCreateTime, Status: common.TopUpStatusSuccess},
		{UserId: 42, TradeNo: "old-paid-uncredited", CreateTime: oldCreateTime, Status: common.TopUpStatusPaidUncredited},
		{UserId: 42, TradeNo: "recent-success", CreateTime: time.Now().Unix(), Status: common.TopUpStatusSuccess},
		{UserId: 7, TradeNo: "other-user-paid-uncredited", CreateTime: oldCreateTime, Status: common.TopUpStatusPaidUncredited},
	}
	require.NoError(t, db.Create(&rows).Error)

	page := &common.PageInfo{Page: 1, PageSize: 20}
	items, total, err := GetUserTopUps(42, page)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, items, 2)
	// Explicitly assert the statuses rather than relying only on ordering (the
	// in-memory database may assign ids differently across dialects).
	seen := map[string]string{}
	for _, item := range items {
		seen[item.TradeNo] = item.Status
	}
	assert.Equal(t, common.TopUpStatusPaidUncredited, seen["old-paid-uncredited"])
	assert.Equal(t, common.TopUpStatusSuccess, seen["recent-success"])
	_, oldPendingPresent := seen["old-pending"]
	_, oldSuccessPresent := seen["old-success"]
	assert.False(t, oldPendingPresent)
	assert.False(t, oldSuccessPresent)

	searchItems, searchTotal, err := SearchUserTopUps(42, "old-paid-uncredited", page)
	require.NoError(t, err)
	assert.EqualValues(t, 1, searchTotal)
	require.Len(t, searchItems, 1)
	assert.Equal(t, "old-paid-uncredited", searchItems[0].TradeNo)
}
