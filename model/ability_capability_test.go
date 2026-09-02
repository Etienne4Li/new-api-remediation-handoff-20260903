package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetChannelPropagatesCapabilityLookupError(t *testing.T) {
	originalDB := DB
	originalGroupCol := commonGroupCol
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	commonGroupCol = "`group`"
	t.Cleanup(func() {
		DB = originalDB
		commonGroupCol = originalGroupCol
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	const modelName = "capability-lookup-error"
	priority := int64(1)
	weight := uint(100)
	require.NoError(t, db.Create(&Channel{
		Id: 2501, Type: constant.ChannelTypeOpenAI, Key: "key", Status: common.ChannelStatusEnabled,
		Name: "channel", Weight: &weight, Models: modelName, Group: "default", Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&Ability{
		Group: "default", Model: modelName, ChannelId: 2501, Enabled: true, Priority: &priority, Weight: weight,
	}).Error)

	forcedErr := errors.New("forced capability lookup failure")
	channelLookups := 0
	const callbackName = "test:fail_capability_lookup"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && strings.Trim(tx.Statement.Table, "`\"") == "channels" {
			channelLookups++
			if channelLookups == 1 {
				tx.AddError(forcedErr)
			}
		}
	}))
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})

	selected, err := GetChannel("default", modelName, 0, "/v1/messages", nil)
	require.ErrorIs(t, err, forcedErr)
	require.Nil(t, selected)
	require.Equal(t, 1, channelLookups)
}
