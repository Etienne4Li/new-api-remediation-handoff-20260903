package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminSubscriptionTransitionsPreserveGroupDowngrade(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		apply  func(int) (string, error)
		status string
		gone   bool
	}{
		{name: "invalidate", apply: AdminInvalidateUserSubscription, status: "cancelled"},
		{name: "delete", apply: AdminDeleteUserSubscription, gone: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			truncateTables(t)
			user := &User{
				Id:       9301,
				Username: "admin-subscription-transition-" + testCase.name,
				Status:   common.UserStatusEnabled,
				Group:    "pro",
			}
			require.NoError(t, DB.Create(user).Error)
			sub := &UserSubscription{
				UserId:        user.Id,
				AmountTotal:   100,
				Status:        "active",
				EndTime:       time.Now().Add(time.Hour).Unix(),
				UpgradeGroup:  "pro",
				PrevUserGroup: "default",
			}
			require.NoError(t, DB.Create(sub).Error)

			_, err := testCase.apply(sub.Id)
			require.NoError(t, err)
			var gotUser User
			require.NoError(t, DB.First(&gotUser, user.Id).Error)
			assert.Equal(t, "default", gotUser.Group)
			if testCase.gone {
				var count int64
				require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", sub.Id).Count(&count).Error)
				assert.Zero(t, count)
				return
			}
			var gotSub UserSubscription
			require.NoError(t, DB.First(&gotSub, sub.Id).Error)
			assert.Equal(t, testCase.status, gotSub.Status)
		})
	}
}

func TestExpireDueSubscriptionsDoesNotCountRolledBackRows(t *testing.T) {
	truncateTables(t)
	user := &User{
		Id:       9302,
		Username: "expire-subscription-rollback",
		Status:   common.UserStatusEnabled,
		Group:    "pro",
	}
	require.NoError(t, DB.Create(user).Error)
	sub := &UserSubscription{
		UserId:        user.Id,
		AmountTotal:   100,
		Status:        "active",
		EndTime:       time.Now().Add(-time.Minute).Unix(),
		UpgradeGroup:  "pro",
		PrevUserGroup: "default",
	}
	require.NoError(t, DB.Create(sub).Error)
	require.NoError(t, DB.Exec(`
CREATE TRIGGER fail_expiration_group_update
BEFORE UPDATE OF "group" ON users
BEGIN
  SELECT RAISE(ABORT, 'forced group update failure');
END`).Error)
	t.Cleanup(func() { _ = DB.Exec("DROP TRIGGER IF EXISTS fail_expiration_group_update").Error })

	count, err := ExpireDueSubscriptions(10)
	require.ErrorContains(t, err, "forced group update failure")
	assert.Zero(t, count, "rolled-back subscription updates must not be reported as expired")
	var gotSub UserSubscription
	require.NoError(t, DB.First(&gotSub, sub.Id).Error)
	assert.Equal(t, "active", gotSub.Status)
}
