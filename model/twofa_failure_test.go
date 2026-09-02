package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTwoFAVerificationFailsClosedWhenUsagePersistenceFails(t *testing.T) {
	originalDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}, &TwoFA{}))
	DB = db
	t.Cleanup(func() {
		DB = originalDB
		require.NoError(t, sqlDB.Close())
	})

	secret := "JBSWY3DPEHPK3PXP"
	user := User{Username: "twofa-fail-closed", Password: "password", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	factor := TwoFA{UserId: user.Id, Secret: secret, IsEnabled: true}
	require.NoError(t, db.Create(&factor).Error)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	forcedErr := errors.New("forced twofa usage update failure")
	const callbackName = "test:twofa_usage_update_failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && strings.Trim(tx.Statement.Table, "`\"") == "two_fas" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	loaded := TwoFA{}
	require.NoError(t, db.First(&loaded, factor.Id).Error)
	valid, verifyErr := loaded.ValidateTOTPAndUpdateUsage(code)
	require.False(t, valid)
	require.ErrorIs(t, verifyErr, forcedErr)
}
