package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func seedAdminRoleScopeRows(t *testing.T) (commonUser, peerAdmin, root User) {
	t.Helper()
	commonUser = User{Username: "scope-log-user", AffCode: "scope-log-user-aff", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	peerAdmin = User{Username: "scope-log-admin", AffCode: "scope-log-admin-aff", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}
	root = User{Username: "scope-log-root", AffCode: "scope-log-root-aff", Role: common.RoleRootUser, Status: common.UserStatusEnabled}
	for _, user := range []*User{&commonUser, &peerAdmin, &root} {
		require.NoError(t, DB.Create(user).Error)
	}
	return commonUser, peerAdmin, root
}

func TestAdminLogQueriesRespectRoleScope(t *testing.T) {
	truncateTables(t)
	commonUser, peerAdmin, root := seedAdminRoleScopeRows(t)
	for _, row := range []Log{
		{UserId: commonUser.Id, Username: commonUser.Username, Type: LogTypeConsume, Quota: 11, PromptTokens: 1, CompletionTokens: 2, CreatedAt: 100},
		{UserId: peerAdmin.Id, Username: peerAdmin.Username, Type: LogTypeConsume, Quota: 22, PromptTokens: 3, CompletionTokens: 4, CreatedAt: 101},
		{UserId: root.Id, Username: root.Username, Type: LogTypeConsume, Quota: 33, PromptTokens: 5, CompletionTokens: 6, CreatedAt: 102},
	} {
		require.NoError(t, LOG_DB.Create(&row).Error)
	}

	adminLogs, total, err := GetAllLogsForRole(LogTypeUnknown, 0, 0, "", "", "", 0, 20, 0, "", "", "", common.RoleAdminUser)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, adminLogs, 1)
	require.Equal(t, commonUser.Id, adminLogs[0].UserId)

	// Exact filters must not bypass the owner-role predicate.
	adminLogs, total, err = GetAllLogsForRole(LogTypeUnknown, 0, 0, "", root.Username, "", 0, 20, 0, "", "", "", common.RoleAdminUser)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, adminLogs)

	rootLogs, total, err := GetAllLogsForRole(LogTypeUnknown, 0, 0, "", "", "", 0, 20, 0, "", "", "", common.RoleRootUser)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, rootLogs, 3)

	stat, err := SumUsedQuotaForRole(LogTypeConsume, 0, 0, "", "", "", 0, "", common.RoleAdminUser)
	require.NoError(t, err)
	require.Equal(t, 11, stat.Quota)
	rootStat, err := SumUsedQuotaForRole(LogTypeConsume, 0, 0, "", "", "", 0, "", common.RoleRootUser)
	require.NoError(t, err)
	require.Equal(t, 66, rootStat.Quota)
}

func TestAdminQuotaDataQueriesRespectRoleScope(t *testing.T) {
	truncateTables(t)
	commonUser, peerAdmin, root := seedAdminRoleScopeRows(t)
	for _, row := range []QuotaData{
		{UserID: commonUser.Id, Username: commonUser.Username, ModelName: "m", CreatedAt: 100, Count: 1, Quota: 10, TokenUsed: 2, UseGroup: "g"},
		{UserID: peerAdmin.Id, Username: peerAdmin.Username, ModelName: "m", CreatedAt: 100, Count: 1, Quota: 20, TokenUsed: 3, UseGroup: "g"},
		{UserID: root.Id, Username: root.Username, ModelName: "m", CreatedAt: 100, Count: 1, Quota: 30, TokenUsed: 4, UseGroup: "g"},
	} {
		require.NoError(t, DB.Create(&row).Error)
	}

	rows, err := GetAllQuotaDatesForRole(0, 200, "", common.RoleAdminUser)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 10, rows[0].Quota)

	rows, err = GetAllQuotaDatesForRole(0, 200, root.Username, common.RoleAdminUser)
	require.NoError(t, err)
	require.Empty(t, rows)

	grouped, err := GetQuotaDataGroupByUserForRole(0, 200, common.RoleAdminUser)
	require.NoError(t, err)
	require.Len(t, grouped, 1)
	require.Equal(t, commonUser.Username, grouped[0].Username)

	// Invalid ordinal values must not inherit Root/Admin access.
	rows, err = GetAllQuotaDatesForRole(0, 200, "", 101)
	require.NoError(t, err)
	require.Empty(t, rows)
	flow, err := GetFlowQuotaData(0, 200, "", 0, 101)
	require.NoError(t, err)
	require.Empty(t, flow)
}
