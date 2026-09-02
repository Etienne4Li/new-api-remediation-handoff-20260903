package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetChannelExcludesFailedChannelWithoutMemoryCache(t *testing.T) {
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
			require.NoError(t, sqlDB.Close())
		}
	})

	const modelName = "db-retry-excludes-failed-channel"
	weight := uint(100)
	channels := []struct {
		id       int
		priority int64
	}{
		{id: 2301, priority: 10},
		{id: 2302, priority: 10},
		{id: 2303, priority: 0},
	}
	for _, candidate := range channels {
		channelID := candidate.id
		priority := candidate.priority
		require.NoError(t, db.Create(&Channel{
			Id:       channelID,
			Type:     constant.ChannelTypeOpenAI,
			Key:      fmt.Sprintf("key-%d", channelID),
			Status:   common.ChannelStatusEnabled,
			Name:     fmt.Sprintf("channel-%d", channelID),
			Weight:   &weight,
			Models:   modelName,
			Group:    "default",
			Priority: &priority,
		}).Error)
		require.NoError(t, db.Create(&Ability{
			Group:     "default",
			Model:     modelName,
			ChannelId: channelID,
			Enabled:   true,
			Priority:  &priority,
			Weight:    weight,
		}).Error)
	}

	first, err := GetChannel("default", modelName, 0, "/v1/responses", nil)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, int64(10), first.GetPriority())
	second, err := GetChannel(
		"default",
		modelName,
		1,
		"/v1/responses",
		map[int]struct{}{first.Id: {}},
	)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.NotEqual(t, first.Id, second.Id)
	assert.Equal(t, int64(10), second.GetPriority())
}

func TestGetChannelSkipsUnsupportedHighPriorityEndpoint(t *testing.T) {
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
			require.NoError(t, sqlDB.Close())
		}
	})

	const modelName = "db-capability-fallback"
	weight := uint(100)
	highPriority := int64(20)
	lowPriority := int64(0)
	// PaLM has no Claude Messages adaptor; it must not mask the lower OpenAI
	// compatible channel when /v1/messages is requested.
	require.NoError(t, db.Create(&Channel{
		Id: 2401, Type: constant.ChannelTypePaLM, Key: "key-palm", Status: common.ChannelStatusEnabled,
		Name: "palm", Weight: &weight, Models: modelName, Group: "default", Priority: &highPriority,
	}).Error)
	require.NoError(t, db.Create(&Ability{
		Group: "default", Model: modelName, ChannelId: 2401, Enabled: true, Priority: &highPriority, Weight: weight,
	}).Error)
	require.NoError(t, db.Create(&Channel{
		Id: 2402, Type: constant.ChannelTypeOpenAI, Key: "key-openai", Status: common.ChannelStatusEnabled,
		Name: "openai", Weight: &weight, Models: modelName, Group: "default", Priority: &lowPriority,
	}).Error)
	require.NoError(t, db.Create(&Ability{
		Group: "default", Model: modelName, ChannelId: 2402, Enabled: true, Priority: &lowPriority, Weight: weight,
	}).Error)

	selected, err := GetChannel("default", modelName, 0, "/v1/messages", nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 2402, selected.Id)
}
