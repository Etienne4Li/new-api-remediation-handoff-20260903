package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newSetupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Option{}, &Setup{}))
	return db
}

func TestInitializeSetupCommitsBootstrapAsOneOperation(t *testing.T) {
	previousDB := DB
	previousOptions := common.OptionMap
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptions
	})

	DB = newSetupTestDB(t)
	common.OptionMap = map[string]string{}
	hashed := "hashed-password"
	initialized, err := InitializeSetup(SetupInitParams{
		Username:           "root",
		HashedPassword:     hashed,
		SelfUseModeEnabled: true,
		DemoSiteEnabled:    false,
		Version:            "test",
	})
	require.NoError(t, err)
	assert.True(t, initialized)

	var roots []User
	require.NoError(t, DB.Where("role = ?", common.RoleRootUser).Find(&roots).Error)
	require.Len(t, roots, 1)
	assert.Equal(t, "root", roots[0].Username)
	assert.Equal(t, hashed, roots[0].Password)

	var setup Setup
	require.NoError(t, DB.First(&setup).Error)
	assert.Equal(t, uint(1), setup.ID)
	assert.Equal(t, "test", setup.Version)

	var options []Option
	require.NoError(t, DB.Where("key IN ?", []string{"SelfUseModeEnabled", "DemoSiteEnabled"}).Find(&options).Error)
	require.Len(t, options, 2)
	assert.Equal(t, "true", optionValue(options, "SelfUseModeEnabled"))
	assert.Equal(t, "false", optionValue(options, "DemoSiteEnabled"))
	assert.Equal(t, "true", common.OptionMap["SelfUseModeEnabled"])
}

func TestInitializeSetupSecondCallerIsNoOp(t *testing.T) {
	previousDB := DB
	previousOptions := common.OptionMap
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptions
	})

	DB = newSetupTestDB(t)
	common.OptionMap = map[string]string{}
	first, err := InitializeSetup(SetupInitParams{Username: "root", HashedPassword: "hash", Version: "v1"})
	require.NoError(t, err)
	require.True(t, first)

	second, err := InitializeSetup(SetupInitParams{Username: "other", HashedPassword: "different", Version: "v2"})
	require.NoError(t, err)
	assert.False(t, second)

	var rootCount int64
	require.NoError(t, DB.Model(&User{}).Where("role = ?", common.RoleRootUser).Count(&rootCount).Error)
	assert.EqualValues(t, 1, rootCount)
	var setupCount int64
	require.NoError(t, DB.Model(&Setup{}).Count(&setupCount).Error)
	assert.EqualValues(t, 1, setupCount)
	var root User
	require.NoError(t, DB.Where("role = ?", common.RoleRootUser).First(&root).Error)
	assert.Equal(t, "root", root.Username)
}

func TestInitializeSetupRollsBackRootWhenBootstrapWriteFails(t *testing.T) {
	previousDB := DB
	previousOptions := common.OptionMap
	t.Cleanup(func() {
		DB = previousDB
		common.OptionMap = previousOptions
	})

	// Do not migrate Option. The transaction must roll back the root insert
	// rather than leaving a root user without setup/options.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &Setup{}))
	DB = db
	common.OptionMap = map[string]string{}

	initialized, err := InitializeSetup(SetupInitParams{Username: "root", HashedPassword: "hash"})
	assert.False(t, initialized)
	assert.Error(t, err)

	var rootCount int64
	require.NoError(t, DB.Model(&User{}).Where("role = ?", common.RoleRootUser).Count(&rootCount).Error)
	assert.Zero(t, rootCount)
	var setupCount int64
	require.NoError(t, DB.Model(&Setup{}).Count(&setupCount).Error)
	assert.Zero(t, setupCount)
}

func optionValue(options []Option, key string) string {
	for _, option := range options {
		if option.Key == key {
			return option.Value
		}
	}
	return ""
}
