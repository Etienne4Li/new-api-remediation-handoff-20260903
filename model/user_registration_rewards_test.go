package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func configureRegistrationRewardsForTest(t *testing.T, newUser, invitee, inviter int) {
	t.Helper()
	previousGeneral := common.GetGeneralRuntimeConfig()
	previousPayment := operation_setting.GetPaymentSettingSnapshot()
	common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) {
		cfg.QuotaForNewUser = newUser
		cfg.QuotaForInvitee = invitee
		cfg.QuotaForInviter = inviter
	})
	operation_setting.UpdatePaymentSetting(func(cfg *operation_setting.PaymentSetting) {
		cfg.ComplianceConfirmed = true
		cfg.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	})
	t.Cleanup(func() {
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { *cfg = previousGeneral })
		operation_setting.UpdatePaymentSetting(func(cfg *operation_setting.PaymentSetting) { *cfg = previousPayment })
	})
}

func TestRegistrationRewardsCommitAtomicallyAndFinalizeIsIdempotent(t *testing.T) {
	truncateTables(t)
	configureRegistrationRewardsForTest(t, 100, 25, 50)

	inviter := &User{Id: 992001, Username: "atomic-registration-inviter", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(inviter).Error)
	invitee := &User{
		Username: "atomic-registration-invitee", DisplayName: "invitee",
		Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
	}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return invitee.InsertWithTx(tx, inviter.Id)
	}))

	var gotInvitee User
	require.NoError(t, DB.First(&gotInvitee, invitee.Id).Error)
	assert.Equal(t, 125, gotInvitee.Quota)
	assert.Equal(t, inviter.Id, gotInvitee.InviterId)
	var gotInviter User
	require.NoError(t, DB.First(&gotInviter, inviter.Id).Error)
	assert.Equal(t, 1, gotInviter.AffCount)
	assert.Equal(t, 50, gotInviter.AffQuota)
	assert.Equal(t, 50, gotInviter.AffHistoryQuota)

	invitee.FinalizeOAuthUserCreation(inviter.Id)
	invitee.FinalizeOAuthUserCreation(inviter.Id)
	require.NoError(t, DB.First(&gotInvitee, invitee.Id).Error)
	assert.Equal(t, 125, gotInvitee.Quota, "finalization must not reapply the invitee reward")
	require.NoError(t, DB.First(&gotInviter, inviter.Id).Error)
	assert.Equal(t, 1, gotInviter.AffCount, "finalization must not reapply the inviter reward")
	assert.Equal(t, 50, gotInviter.AffQuota)

	var inviteeRewardLogs int64
	require.NoError(t, LOG_DB.Model(&Log{}).
		Where("user_id = ? AND content LIKE ?", invitee.Id, "使用邀请码赠送 %").
		Count(&inviteeRewardLogs).Error)
	assert.EqualValues(t, 1, inviteeRewardLogs, "repeated finalization must not duplicate success logs")
	var inviterRewardLogs int64
	require.NoError(t, LOG_DB.Model(&Log{}).
		Where("user_id = ? AND content LIKE ?", inviter.Id, "邀请用户赠送 %").
		Count(&inviterRewardLogs).Error)
	assert.EqualValues(t, 1, inviterRewardLogs)
}

func TestRegistrationRewardFailureRollsBackUserAndBothLedgers(t *testing.T) {
	truncateTables(t)
	configureRegistrationRewardsForTest(t, 100, 25, 50)

	inviter := &User{Id: 992002, Username: "rollback-registration-inviter", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(inviter).Error)
	require.NoError(t, DB.Exec(`
CREATE TRIGGER fail_registration_inviter_reward
BEFORE UPDATE OF aff_quota ON users
WHEN OLD.id = 992002
BEGIN
  SELECT RAISE(ABORT, 'forced inviter reward failure');
END`).Error)
	t.Cleanup(func() { _ = DB.Exec("DROP TRIGGER IF EXISTS fail_registration_inviter_reward").Error })

	invitee := &User{
		Username: "rollback-registration-invitee", DisplayName: "invitee",
		Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		return invitee.InsertWithTx(tx, inviter.Id)
	})
	require.ErrorContains(t, err, "forced inviter reward failure")

	var inviteeCount int64
	require.NoError(t, DB.Model(&User{}).Where("username = ?", invitee.Username).Count(&inviteeCount).Error)
	assert.Zero(t, inviteeCount, "the account and invitee reward must roll back together")
	var gotInviter User
	require.NoError(t, DB.First(&gotInviter, inviter.Id).Error)
	assert.Zero(t, gotInviter.AffCount)
	assert.Zero(t, gotInviter.AffQuota)
	assert.Zero(t, gotInviter.AffHistoryQuota)
	assert.Zero(t, invitee.creationQuotaForNewUser)
	assert.Zero(t, invitee.creationQuotaForInvitee)
	assert.Zero(t, invitee.creationQuotaForInviter)
}

func TestRegistrationRejectsMissingInviterBeforeGrantingInviteeReward(t *testing.T) {
	truncateTables(t)
	configureRegistrationRewardsForTest(t, 100, 25, 0)

	invitee := &User{
		Username: "missing-registration-inviter", DisplayName: "invitee",
		Status: common.UserStatusEnabled, Role: common.RoleCommonUser,
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		return invitee.InsertWithTx(tx, 999999)
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var count int64
	require.NoError(t, DB.Model(&User{}).Where("username = ?", invitee.Username).Count(&count).Error)
	assert.Zero(t, count)
}
