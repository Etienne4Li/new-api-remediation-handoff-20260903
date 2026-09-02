package model

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newBoundedListCountDB(t *testing.T) (*gorm.DB, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.New(
			log.New(&output, "", 0),
			gormlogger.Config{LogLevel: gormlogger.Info},
		),
	})
	require.NoError(t, err)
	return db, &output
}

func assertNoAggregateCount(t *testing.T, output *bytes.Buffer) {
	t.Helper()
	assert.NotContains(t, strings.ToLower(output.String()), "count(*)")
}

func TestRedemptionListsUseBoundedCountProbe(t *testing.T) {
	db, output := newBoundedListCountDB(t)
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	require.NoError(t, db.AutoMigrate(&Redemption{}))

	rows := []Redemption{
		{Key: "bounded-redemption-00000000000001", Name: "alpha", Status: common.RedemptionCodeStatusEnabled},
		{Key: "bounded-redemption-00000000000002", Name: "beta", Status: common.RedemptionCodeStatusEnabled},
		{Key: "bounded-redemption-00000000000003", Name: "alpha-two", Status: common.RedemptionCodeStatusDisabled},
	}
	require.NoError(t, db.Create(&rows).Error)
	output.Reset()

	items, total, err := GetAllRedemptions(0, 2)
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.EqualValues(t, 3, total)
	assertNoAggregateCount(t, output)

	output.Reset()
	items, total, err = SearchRedemptions("alpha", "", 0, 2)
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.EqualValues(t, 2, total)
	assertNoAggregateCount(t, output)
}

func TestSearchUserTokensUsesBoundedCountProbe(t *testing.T) {
	db, output := newBoundedListCountDB(t)
	previousDB := DB
	DB = db
	previousMax := operation_setting.GetMaxUserTokens()
	t.Cleanup(func() {
		DB = previousDB
		operation_setting.UpdateTokenSetting(func(cfg *operation_setting.TokenSetting) {
			cfg.MaxUserTokens = previousMax
		})
	})
	require.NoError(t, db.AutoMigrate(&Token{}))
	operation_setting.UpdateTokenSetting(func(cfg *operation_setting.TokenSetting) { cfg.MaxUserTokens = 3 })

	for i := 0; i < 5; i++ {
		require.NoError(t, db.Create(&Token{
			UserId: 42,
			Key:    "bounded-token-" + string(rune('a'+i)),
			Name:   "token",
		}).Error)
	}
	output.Reset()

	items, total, err := SearchUserTokens(42, "", "", 0, 2)
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.EqualValues(t, 3, total)
	assertNoAggregateCount(t, output)
}

func TestTopUpListsUseBoundedCountProbe(t *testing.T) {
	db, output := newBoundedListCountDB(t)
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	require.NoError(t, db.AutoMigrate(&TopUp{}))

	for i := 0; i < 3; i++ {
		require.NoError(t, db.Create(&TopUp{
			UserId:     7,
			TradeNo:    "bounded-topup-" + string(rune('a'+i)),
			CreateTime: common.GetTimestamp(),
			Status:     common.TopUpStatusPending,
		}).Error)
	}
	page := &common.PageInfo{Page: 1, PageSize: 2}

	output.Reset()
	items, total, err := GetUserTopUps(7, page)
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.EqualValues(t, 3, total)
	assertNoAggregateCount(t, output)

	output.Reset()
	items, total, err = GetAllTopUps(page)
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.EqualValues(t, 3, total)
	assertNoAggregateCount(t, output)

	output.Reset()
	items, total, err = SearchUserTopUps(7, "bounded%", page)
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.EqualValues(t, 3, total)
	assertNoAggregateCount(t, output)

	output.Reset()
	items, total, err = SearchAllTopUps("bounded%", page)
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.EqualValues(t, 3, total)
	assertNoAggregateCount(t, output)
}

func TestAdminUserListsUseBoundedCountProbe(t *testing.T) {
	db, output := newBoundedListCountDB(t)
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	require.NoError(t, db.AutoMigrate(&User{}))

	// Keep the fixture just over the probe cap.  Every row below is visible to
	// an ordinary administrator and matches the search filters, so both admin
	// endpoints must return the cap instead of running an aggregate COUNT.
	rows := make([]User, userListCountHardLimit+1)
	for i := range rows {
		rows[i] = User{
			Username:    fmt.Sprintf("bounded-user-%05d", i),
			Password:    "password",
			DisplayName: "Bounded User",
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Group:       "bounded",
			AffCode:     fmt.Sprintf("bounded-aff-%05d", i),
		}
	}
	require.NoError(t, db.CreateInBatches(&rows, 500).Error)

	output.Reset()
	users, total, err := GetAllUsersForRole(
		&common.PageInfo{Page: 1, PageSize: 2},
		common.RoleAdminUser,
		NewUserSortOptions("id", "asc"),
	)
	require.NoError(t, err)
	assert.Len(t, users, 2)
	assert.EqualValues(t, userListCountHardLimit, total)
	queries := strings.ToLower(output.String())
	assert.Contains(t, queries, "select `id` from `users` where role < 10 limit 10001")
	assert.NotContains(t, queries, "count(*)")

	output.Reset()
	role := common.RoleCommonUser
	status := common.UserStatusEnabled
	users, total, err = SearchUsersForRole(
		"bounded-user",
		"bounded",
		&role,
		&status,
		common.RoleAdminUser,
		0,
		2,
		NewUserSortOptions("id", "asc"),
	)
	require.NoError(t, err)
	assert.Len(t, users, 2)
	assert.EqualValues(t, userListCountHardLimit, total)
	queries = strings.ToLower(output.String())
	assert.Contains(t, queries, "select `id` from `users`")
	assert.Contains(t, queries, "username like")
	assert.Contains(t, queries, "`group`")
	assert.Contains(t, queries, "limit 10001")
	assert.NotContains(t, queries, "count(*)")
}

func TestSearchUsersForRoleKeepsVisibilityAndSearchFilters(t *testing.T) {
	db, output := newBoundedListCountDB(t)
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	require.NoError(t, db.AutoMigrate(&User{}))

	users := []User{
		{
			Username: "special-visible", Password: "password", DisplayName: "Special",
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "target", AffCode: "special-visible-aff",
		},
		{
			Username: "special-other-group", Password: "password", DisplayName: "Special",
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "other", AffCode: "special-other-group-aff",
		},
		{
			Username: "special-disabled", Password: "password", DisplayName: "Special",
			Role: common.RoleCommonUser, Status: common.UserStatusDisabled, Group: "target", AffCode: "special-disabled-aff",
		},
		{
			Username: "special-admin", Password: "password", DisplayName: "Special",
			Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "target", AffCode: "special-admin-aff",
		},
		{
			Username: "special-root", Password: "password", DisplayName: "Special",
			Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "target", AffCode: "special-root-aff",
		},
	}
	require.NoError(t, db.Create(&users).Error)

	role := common.RoleCommonUser
	status := common.UserStatusEnabled
	output.Reset()
	items, total, err := SearchUsersForRole(
		"special", "target", &role, &status, common.RoleAdminUser, 0, 20,
		NewUserSortOptions("id", "asc"),
	)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "special-visible", items[0].Username)
	assert.EqualValues(t, 1, total)
	assert.NotContains(t, strings.ToLower(output.String()), "count(*)")
}

func TestAdminUserRoleReadEndpointsFailClosedWithoutDatabase(t *testing.T) {
	previousDB := DB
	DB = nil
	t.Cleanup(func() { DB = previousDB })

	_, _, err := GetAllUsersForRole(&common.PageInfo{Page: 1, PageSize: 10}, common.RoleAdminUser)
	require.ErrorIs(t, err, ErrDatabase)

	_, _, err = SearchUsersForRole("alice", "", nil, nil, common.RoleAdminUser, 0, 10)
	require.ErrorIs(t, err, ErrDatabase)

	_, err = GetUserByIdForAdmin(1)
	require.ErrorIs(t, err, ErrDatabase)
}

func TestAdminUserRoleReadEndpointsClampDirectPageBounds(t *testing.T) {
	truncateTables(t)
	rows := make([]User, common.MaxPageSize+5)
	for i := range rows {
		rows[i] = User{
			Username:    fmt.Sprintf("page-bound-user-%03d", i),
			Password:    "password",
			DisplayName: "Page Bound User",
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Group:       "page-bound",
			AffCode:     fmt.Sprintf("page-bound-aff-%03d", i),
		}
	}
	require.NoError(t, DB.CreateInBatches(&rows, 50).Error)

	items, total, err := GetAllUsersForRole(
		&common.PageInfo{Page: 1, PageSize: common.MaxPageSize * 10},
		common.RoleAdminUser,
		NewUserSortOptions("id", "asc"),
	)
	require.NoError(t, err)
	assert.EqualValues(t, common.MaxPageSize+5, total)
	assert.Len(t, items, common.MaxPageSize,
		"a direct model caller must not be able to disable the list LIMIT")

	items, total, err = SearchUsersForRole(
		"page-bound-user", "page-bound", nil, nil,
		common.RoleAdminUser, -1, common.MaxPageSize*10,
		NewUserSortOptions("id", "asc"),
	)
	require.NoError(t, err)
	assert.EqualValues(t, common.MaxPageSize+5, total)
	assert.Len(t, items, common.MaxPageSize,
		"a direct search caller must not be able to disable the list LIMIT")
}
