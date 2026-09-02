package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic("failed to open test db: " + err.Error())
	}
	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to get sql.DB: " + err.Error())
	}
	sqlDB.SetMaxOpenConns(1)

	model.DB = db
	model.LOG_DB = db

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.SetLogConsumeEnabled(true)

	if err := db.AutoMigrate(
		&model.Task{},
		&model.User{},
		&model.Token{},
		&model.Log{},
		&model.Channel{},
		&model.Midjourney{},
		&model.MidjourneySubmitIntent{},
		&model.TopUp{},
		&model.SubscriptionPlan{},
		&model.UserSubscription{},
		&model.SubscriptionPreConsumeRecord{},
		&model.BillingOperation{},
		&model.SystemTask{},
		&model.SystemTaskLock{},
	); err != nil {
		panic("failed to migrate: " + err.Error())
	}

	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Seed helpers
// ---------------------------------------------------------------------------

func truncate(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM tasks")
		model.DB.Exec("DELETE FROM users")
		model.DB.Exec("DELETE FROM tokens")
		model.DB.Exec("DELETE FROM logs")
		model.DB.Exec("DELETE FROM channels")
		model.DB.Exec("DELETE FROM midjourneys")
		model.DB.Exec("DELETE FROM midjourney_submit_intents")
		model.DB.Exec("DELETE FROM top_ups")
		model.DB.Exec("DELETE FROM subscription_plans")
		model.DB.Exec("DELETE FROM user_subscriptions")
		model.DB.Exec("DELETE FROM subscription_pre_consume_records")
		model.DB.Exec("DELETE FROM billing_operations")
		model.DB.Exec("DELETE FROM system_task_locks")
		model.DB.Exec("DELETE FROM system_tasks")
	})
}

func seedUser(t *testing.T, id int, quota int) {
	t.Helper()
	// aff_code is globally unique in the production schema.  The service
	// fixtures create more than one user per test, so an empty default would
	// make otherwise unrelated billing tests fail on the uniqueness index.
	user := &model.User{Id: id, Username: fmt.Sprintf("test_user_%d", id), AffCode: fmt.Sprintf("test-aff-%d", id), Quota: quota, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(user).Error)
}

func seedToken(t *testing.T, id int, userId int, key string, remainQuota int) {
	t.Helper()
	token := &model.Token{
		Id:          id,
		UserId:      userId,
		Key:         key,
		Name:        "test_token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: remainQuota,
		UsedQuota:   0,
	}
	require.NoError(t, model.DB.Create(token).Error)
}

func seedSubscription(t *testing.T, id int, userId int, amountTotal int64, amountUsed int64) {
	t.Helper()
	// Pre-consume validation resolves the plan snapshot even in billing-only
	// tests. Keep the fixture self-contained by creating a minimal plan with
	// the same stable ID as the subscription row.
	plan := &model.SubscriptionPlan{
		Id:            id,
		Title:         "test-plan",
		DurationUnit:  model.SubscriptionDurationMonth,
		DurationValue: 1,
		TotalAmount:   amountTotal,
		Enabled:       true,
	}
	require.NoError(t, model.DB.Create(plan).Error)
	sub := &model.UserSubscription{
		Id:          id,
		UserId:      userId,
		PlanId:      id,
		AmountTotal: amountTotal,
		AmountUsed:  amountUsed,
		Status:      "active",
		StartTime:   time.Now().Unix(),
		EndTime:     time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	require.NoError(t, model.DB.Create(sub).Error)
}

func seedChannel(t *testing.T, id int) {
	t.Helper()
	ch := &model.Channel{Id: id, Name: "test_channel", Key: "sk-test", Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(ch).Error)
}

func seedChargedAccounting(t *testing.T, userID, channelID, tokenID, quota, requestCount int) {
	t.Helper()
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
		"used_quota":    quota,
		"request_count": requestCount,
	}).Error)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).
		Update("used_quota", quota).Error)
	if tokenID > 0 {
		require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).
			Update("used_quota", quota).Error)
	}
}

func makeTask(userId, channelId, quota, tokenId int, billingSource string, subscriptionId int) *model.Task {
	return &model.Task{
		TaskID:    "task_" + time.Now().Format("150405.000"),
		UserId:    userId,
		ChannelId: channelId,
		Quota:     quota,
		Status:    model.TaskStatus(model.TaskStatusInProgress),
		Group:     "default",
		Data:      json.RawMessage(`{}`),
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Properties: model.Properties{
			OriginModelName: "test-model",
		},
		PrivateData: model.TaskPrivateData{
			BillingSource:  billingSource,
			SubscriptionId: subscriptionId,
			TokenId:        tokenId,
			BillingContext: &model.TaskBillingContext{
				ModelPrice:      0.02,
				GroupRatio:      1.0,
				OriginModelName: "test-model",
			},
		},
	}
}

func TestPriceDataOtherRatiosFilterAndSnapshot(t *testing.T) {
	priceData := types.PriceData{}

	priceData.AddOtherRatio("zero", 0)
	priceData.AddOtherRatio("negative", -0.5)
	priceData.AddOtherRatio("nan", math.NaN())
	priceData.AddOtherRatio("inf", math.Inf(1))
	priceData.AddOtherRatio("one", 1)
	priceData.AddOtherRatio("positive", 2.5)

	ratios := priceData.OtherRatios()
	require.Len(t, ratios, 2)
	assert.Equal(t, 1.0, ratios["one"])
	assert.Equal(t, 2.5, ratios["positive"])
	assert.True(t, priceData.HasOtherRatio("one"))
	assert.False(t, priceData.HasOtherRatio("zero"))

	ratios["positive"] = 99
	ratios["new"] = 3
	nextSnapshot := priceData.OtherRatios()
	assert.Equal(t, 2.5, nextSnapshot["positive"])
	assert.NotContains(t, nextSnapshot, "new")
}

func TestCalculateTaskQuotaByTokensUsesSubmissionRatioSnapshot(t *testing.T) {
	const modelName = "task-terminal-ratio-snapshot"
	originalModelRatios := ratio_setting.ModelRatio2JSONString()
	originalGroupRatios := ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalModelRatios))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatios))
	})

	// Live prices change while the provider task is running. Terminal billing
	// must keep the exact model/group/other multipliers captured at submission.
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"`+modelName+`":100}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":50}`))
	task := &model.Task{
		Group: "default",
		PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{
			OriginModelName: modelName,
			ModelRatio:      2,
			GroupRatio:      3,
			OtherRatios:     map[string]float64{"duration": 4},
		}},
	}

	quota, _, clamp, ok := calculateTaskQuotaByTokens(task, 5)
	require.True(t, ok)
	assert.Equal(t, 120, quota)
	assert.Nil(t, clamp)
}

func TestPriceDataReplaceAndApplyOtherRatios(t *testing.T) {
	priceData := types.PriceData{}

	replaced := priceData.ReplaceOtherRatios(map[string]float64{
		"zero":     0,
		"negative": -3,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
		"one":      1,
		"duration": 2,
		"size":     1.5,
	})

	require.True(t, replaced)
	assert.Equal(t, 3.0, priceData.OtherRatioMultiplier())
	assert.Equal(t, 30.0, priceData.ApplyOtherRatiosToFloat(10))
	assert.Equal(t, 10.0, priceData.RemoveOtherRatiosFromFloat(30))
	assert.True(t, decimal.NewFromInt(30).Equal(priceData.ApplyOtherRatiosToDecimal(decimal.NewFromInt(10))))

	replaced = priceData.ReplaceOtherRatios(map[string]float64{
		"zero": 0,
		"nan":  math.NaN(),
	})

	require.False(t, replaced)
	assert.Nil(t, priceData.OtherRatios())
	assert.Equal(t, 1.0, priceData.OtherRatioMultiplier())
}

func TestTaskBillingOtherFiltersHistoricalOtherRatios(t *testing.T) {
	task := makeTask(1, 1, 100, 0, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.OtherRatios = map[string]float64{
		"seconds":  2,
		"identity": 1,
		"zero":     0,
		"negative": -1,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
	}

	other := taskBillingOther(task)

	assert.Equal(t, 2.0, other["seconds"])
	assert.Equal(t, 1.0, other["identity"])
	assert.NotContains(t, other, "zero")
	assert.NotContains(t, other, "negative")
	assert.NotContains(t, other, "nan")
	assert.NotContains(t, other, "inf")
}

func TestTaskBillingContextPriceDataFiltersMultiplier(t *testing.T) {
	priceData := taskBillingContextPriceData(&model.TaskBillingContext{
		OtherRatios: map[string]float64{
			"seconds":  2,
			"size":     3,
			"identity": 1,
			"zero":     0,
			"negative": -1,
			"nan":      math.NaN(),
			"inf":      math.Inf(1),
		},
	})

	require.NotNil(t, priceData)
	assert.Equal(t, 6.0, priceData.OtherRatioMultiplier())
	assert.Equal(t, map[string]float64{
		"seconds":  2,
		"size":     3,
		"identity": 1,
	}, priceData.OtherRatios())
}

// ---------------------------------------------------------------------------
// Read-back helpers
// ---------------------------------------------------------------------------

func getUserQuota(t *testing.T, id int) int {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("quota").Where("id = ?", id).First(&user).Error)
	return user.Quota
}

func getUserUsageAccounting(t *testing.T, id int) (int, int) {
	t.Helper()
	var user model.User
	require.NoError(t, model.DB.Select("used_quota", "request_count").Where("id = ?", id).First(&user).Error)
	return user.UsedQuota, user.RequestCount
}

func getChannelUsedQuota(t *testing.T, id int) int64 {
	t.Helper()
	var channel model.Channel
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", id).First(&channel).Error)
	return channel.UsedQuota
}

func getTokenRemainQuota(t *testing.T, id int) int {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.Select("remain_quota").Where("id = ?", id).First(&token).Error)
	return token.RemainQuota
}

func getTokenUsedQuota(t *testing.T, id int) int {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.Select("used_quota").Where("id = ?", id).First(&token).Error)
	return token.UsedQuota
}

func getSubscriptionUsed(t *testing.T, id int) int64 {
	t.Helper()
	var sub model.UserSubscription
	require.NoError(t, model.DB.Select("amount_used").Where("id = ?", id).First(&sub).Error)
	return sub.AmountUsed
}

func getTaskQuota(t *testing.T, id int64) int {
	t.Helper()
	var task model.Task
	require.NoError(t, model.DB.Select("quota").Where("id = ?", id).First(&task).Error)
	return task.Quota
}

func getMidjourneyTask(t *testing.T, id int) model.Midjourney {
	t.Helper()
	var task model.Midjourney
	require.NoError(t, model.DB.First(&task, id).Error)
	return task
}

func getLastLog(t *testing.T) *model.Log {
	t.Helper()
	var log model.Log
	err := model.LOG_DB.Order("id desc").First(&log).Error
	if err != nil {
		return nil
	}
	return &log
}

func countLogs(t *testing.T) int64 {
	t.Helper()
	var count int64
	model.LOG_DB.Model(&model.Log{}).Count(&count)
	return count
}

func countBillingOperations(t *testing.T, component string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, model.DB.Model(&model.BillingOperation{}).
		Where("component = ?", component).Count(&count).Error)
	return count
}

// ===========================================================================
// Legacy Midjourney billing tests
// ===========================================================================

func TestPrepareMidjourneyTaskBillingKeepsUnbilledMarkerClear(t *testing.T) {
	task := &model.Midjourney{Quota: 900, TokenId: 7, BillingChannelId: 8}

	prepared, err := PrepareMidjourneyTaskBilling(&relaycommon.RelayInfo{}, task, 900, false)

	require.NoError(t, err)
	assert.False(t, prepared)
	assert.Zero(t, task.Quota)
	assert.Zero(t, task.TokenId)
	assert.Zero(t, task.BillingChannelId)
}

func TestSettleMidjourneyTaskBillingRequiresPersistedTask(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 49, 49, 49
	const initialUserQuota, initialTokenQuota, chargedQuota = 10000, 5000, 3000
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-midjourney-unpersisted", initialTokenQuota)
	seedChannel(t, channelID)

	relayInfo := &relaycommon.RelayInfo{
		UserId:    userID,
		TokenId:   tokenID,
		TokenKey:  "sk-midjourney-unpersisted",
		UserQuota: initialUserQuota,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channelID,
		},
	}
	task := &model.Midjourney{UserId: userID, ChannelId: channelID}
	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, chargedQuota, true)
	require.NoError(t, err)
	require.True(t, prepared)

	billed, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)

	require.Error(t, err)
	assert.False(t, billed)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
}

func TestMidjourneyRefundRestoresEveryAccountingElementOnBillingChannel(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, billingChannelID, executionChannelID = 50, 50, 50, 51
	const initialUserQuota, initialTokenQuota, chargedQuota = 10000, 5000, 3000
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-midjourney", initialTokenQuota)
	seedChannel(t, billingChannelID)
	seedChannel(t, executionChannelID)

	relayInfo := &relaycommon.RelayInfo{
		UserId:     userID,
		TokenId:    tokenID,
		TokenKey:   "sk-midjourney",
		UserQuota:  initialUserQuota,
		UsingGroup: "default",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: billingChannelID,
		},
	}
	task := &model.Midjourney{
		UserId:    userID,
		Action:    "IMAGINE",
		MjId:      "mj-accounting-refund",
		ChannelId: executionChannelID,
		Progress:  "0%",
	}

	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, chargedQuota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	assert.Equal(t, chargedQuota, task.Quota)
	assert.Zero(t, task.TokenId)
	assert.Equal(t, billingChannelID, task.BillingChannelId)
	require.NoError(t, task.Insert())

	billed, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)
	require.NoError(t, err)
	require.True(t, billed)
	assert.Equal(t, initialUserQuota-chargedQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-chargedQuota, getTokenRemainQuota(t, tokenID))
	persisted := getMidjourneyTask(t, task.Id)
	assert.Equal(t, chargedQuota, persisted.Quota)
	assert.Equal(t, tokenID, persisted.TokenId)
	assert.Equal(t, billingChannelID, persisted.BillingChannelId)

	seedChargedAccounting(t, userID, billingChannelID, tokenID, chargedQuota, 1)

	assert.True(t, RefundMidjourneyQuota(ctx, task, "构图失败"))
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, billingChannelID))
	assert.Zero(t, getChannelUsedQuota(t, executionChannelID))

	persisted = getMidjourneyTask(t, task.Id)
	assert.Zero(t, persisted.Quota)
	assert.Equal(t, tokenID, persisted.TokenId)
	assert.Equal(t, billingChannelID, persisted.BillingChannelId)
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
	assert.Equal(t, chargedQuota, log.Quota)
	assert.Equal(t, tokenID, log.TokenId)
	assert.Equal(t, billingChannelID, log.ChannelId)

	assert.True(t, RefundMidjourneyQuota(ctx, task, "duplicate poll"))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestSettleMidjourneyTaskBillingFundingFailureClearsMarkers(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 52, 52, 52
	const initialUserQuota, initialTokenQuota, chargedQuota = 10000, 5000, 3000
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-midjourney-funding-failure", initialTokenQuota)
	seedChannel(t, channelID)

	relayInfo := &relaycommon.RelayInfo{
		UserId:    userID,
		TokenId:   tokenID,
		TokenKey:  "sk-midjourney-funding-failure",
		UserQuota: initialUserQuota,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channelID,
		},
	}
	task := &model.Midjourney{UserId: userID, MjId: "mj-funding-failure", ChannelId: channelID}
	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, chargedQuota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	require.NoError(t, task.Insert())

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_midjourney_user_update
		BEFORE UPDATE ON users
		WHEN OLD.id = 52
		BEGIN
			SELECT RAISE(ABORT, 'forced user quota failure');
		END;
	`).Error)
	t.Cleanup(func() {
		model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_user_update")
	})

	billed, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)

	require.Error(t, err)
	assert.False(t, billed)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	persisted := getMidjourneyTask(t, task.Id)
	assert.Zero(t, persisted.Quota)
	assert.Zero(t, persisted.TokenId)
	assert.Zero(t, persisted.BillingChannelId)
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Zero(t, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Zero(t, countLogs(t))
}

func TestSettleMidjourneyTaskBillingTokenFailureCompensatesFunding(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID = 53, 53, 53
	const initialUserQuota, initialTokenQuota, chargedQuota = 10000, 5000, 3000
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "sk-midjourney-token-failure", initialTokenQuota)
	seedChannel(t, channelID)

	relayInfo := &relaycommon.RelayInfo{
		UserId:    userID,
		TokenId:   tokenID,
		TokenKey:  "sk-midjourney-token-failure",
		UserQuota: initialUserQuota,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channelID,
		},
	}
	task := &model.Midjourney{UserId: userID, MjId: "mj-token-failure", ChannelId: channelID}
	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, chargedQuota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	require.NoError(t, task.Insert())

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_midjourney_token_update
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 53
		BEGIN
			SELECT RAISE(ABORT, 'forced token quota failure');
		END;
	`).Error)
	t.Cleanup(func() {
		model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_token_update")
	})

	billed, err := SettleMidjourneyTaskBilling(relayInfo, task, prepared)

	require.Error(t, err)
	// The token write failed, so the funding write must be compensated before
	// the durable marker is cleared. No ledger should remain charged.
	assert.False(t, billed)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	persisted := getMidjourneyTask(t, task.Id)
	assert.Zero(t, persisted.Quota)
	assert.Zero(t, persisted.TokenId)
	assert.Zero(t, persisted.BillingChannelId)
	assert.Zero(t, countLogs(t))
}

func TestPrepareMidjourneyTaskBillingRejectsSubscriptionBeforeCharge(t *testing.T) {
	task := &model.Midjourney{Quota: 900, TokenId: 7, BillingChannelId: 8}
	relayInfo := &relaycommon.RelayInfo{BillingSource: BillingSourceSubscription, SubscriptionId: 1}

	prepared, err := PrepareMidjourneyTaskBilling(relayInfo, task, 900, true)

	require.Error(t, err)
	assert.False(t, prepared)
	assert.Zero(t, task.Quota)
	assert.Zero(t, task.TokenId)
	assert.Zero(t, task.BillingChannelId)
}

func TestConvertSimpleChangeParamsRejectsMalformedInputWithoutPanic(t *testing.T) {
	tests := []string{"", "task", "task ", "task x", "task u", "task u0", "task u5", "task v9", "task u1 extra"}
	for _, input := range tests {
		assert.NotPanics(t, func() {
			assert.Nil(t, ConvertSimpleChangeParams(input))
		}, input)
	}
}

func TestConvertSimpleChangeParamsAcceptsSupportedActions(t *testing.T) {
	upscale := ConvertSimpleChangeParams("task-1 u2")
	require.NotNil(t, upscale)
	assert.Equal(t, "task-1", upscale.TaskId)
	assert.Equal(t, constant.MjActionUpscale, upscale.Action)
	assert.Equal(t, 2, upscale.Index)

	variation := ConvertSimpleChangeParams("  task-2   v4  ")
	require.NotNil(t, variation)
	assert.Equal(t, constant.MjActionVariation, variation.Action)
	assert.Equal(t, 4, variation.Index)

	reroll := ConvertSimpleChangeParams("task-3 r")
	require.NotNil(t, reroll)
	assert.Equal(t, constant.MjActionReRoll, reroll.Action)
	assert.Zero(t, reroll.Index)
}

func TestCoverPlusActionToNormalActionRejectsMalformedCustomIDWithoutPanic(t *testing.T) {
	inputs := []string{"", "MJ", "MJ::JOB", "MJ::JOB::upsample", "MJ::JOB::variation", "MJ::JOB::upsample::x", "MJ::JOB::variation::x"}
	for _, customID := range inputs {
		assert.NotPanics(t, func() {
			response := CoverPlusActionToNormalAction(&dto.MidjourneyRequest{CustomId: customID})
			require.NotNil(t, response)
			assert.NotEqual(t, 0, response.Code)
		}, customID)
	}
}

func TestCoverPlusActionToNormalActionParsesValidCustomID(t *testing.T) {
	request := &dto.MidjourneyRequest{CustomId: "MJ::JOB::upsample::2::job-id"}
	assert.Nil(t, CoverPlusActionToNormalAction(request))
	assert.Equal(t, constant.MjActionUpscale, request.Action)
	assert.Equal(t, 2, request.Index)
}

func TestRefundMidjourneyQuotaUsesLegacyChannelFallbackWithoutTokenAdjustment(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 54, 54, 54
	const walletAfterCharge, tokenQuota, chargedQuota = 7000, 5000, 3000
	seedUser(t, userID, walletAfterCharge)
	seedToken(t, tokenID, userID, "sk-midjourney-legacy", tokenQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, 0, chargedQuota, 1)
	task := &model.Midjourney{
		UserId:    userID,
		MjId:      "mj-legacy-fallback",
		Action:    "IMAGINE",
		ChannelId: channelID,
		Quota:     chargedQuota,
		TokenId:   0,
		Progress:  "0%",
	}
	require.NoError(t, task.Insert())

	assert.True(t, RefundMidjourneyQuota(ctx, task, "legacy failure"))

	assert.Equal(t, walletAfterCharge+chargedQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, channelID, log.ChannelId)
	assert.Zero(t, log.TokenId)
}

func TestRefundMidjourneyQuotaTokenFailureKeepsFundingAndMarker(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 55, 55, 55
	const walletAfterCharge, tokenRemain, chargedQuota = 7000, 5000, 3000
	seedUser(t, userID, walletAfterCharge)
	seedToken(t, tokenID, userID, "sk-midjourney-token-refund-failure", tokenRemain)
	require.NoError(t, model.DecreaseTokenQuota(tokenID, "sk-midjourney-token-refund-failure", chargedQuota))
	seedChannel(t, channelID)
	task := &model.Midjourney{
		UserId: userID, MjId: "mj-token-refund-failure", Action: "IMAGINE",
		ChannelId: channelID, Quota: chargedQuota, TokenId: tokenID,
		BillingChannelId: channelID, Progress: "0%",
	}
	require.NoError(t, task.Insert())

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_midjourney_token_refund
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 55
		BEGIN
			SELECT RAISE(ABORT, 'forced token refund failure');
		END;
	`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_token_refund") })

	assert.False(t, RefundMidjourneyQuota(ctx, task, "token unavailable"))
	assert.Equal(t, walletAfterCharge, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain-chargedQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, chargedQuota, getTokenUsedQuota(t, tokenID))
	assert.Equal(t, chargedQuota, task.Quota)
	assert.Equal(t, chargedQuota, getMidjourneyTask(t, task.Id).Quota)
	assert.Zero(t, countLogs(t))

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_token_refund").Error)
	assert.True(t, RefundMidjourneyQuota(ctx, task, "token retry"))
	assert.Equal(t, walletAfterCharge+chargedQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	assert.Zero(t, getMidjourneyTask(t, task.Id).Quota)
}

func TestRefundMidjourneyQuotaFundingFailureCompensatesToken(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 56, 56, 56
	const walletAfterCharge, tokenRemain, chargedQuota = 7000, 5000, 3000
	seedUser(t, userID, walletAfterCharge)
	seedToken(t, tokenID, userID, "sk-midjourney-funding-refund-failure", tokenRemain)
	require.NoError(t, model.DecreaseTokenQuota(tokenID, "sk-midjourney-funding-refund-failure", chargedQuota))
	seedChannel(t, channelID)
	task := &model.Midjourney{
		UserId: userID, MjId: "mj-funding-refund-failure", Action: "IMAGINE",
		ChannelId: channelID, Quota: chargedQuota, TokenId: tokenID,
		BillingChannelId: channelID, Progress: "0%",
	}
	require.NoError(t, task.Insert())

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_midjourney_funding_refund
		BEFORE UPDATE ON users
		WHEN OLD.id = 56
		BEGIN
			SELECT RAISE(ABORT, 'forced funding refund failure');
		END;
	`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_funding_refund") })

	assert.False(t, RefundMidjourneyQuota(ctx, task, "funding unavailable"))
	assert.Equal(t, walletAfterCharge, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain-chargedQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, chargedQuota, getTokenUsedQuota(t, tokenID))
	assert.Equal(t, chargedQuota, getMidjourneyTask(t, task.Id).Quota)
	assert.Zero(t, countLogs(t))

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_funding_refund").Error)
	assert.True(t, RefundMidjourneyQuota(ctx, task, "funding retry"))
	assert.Equal(t, walletAfterCharge+chargedQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	assert.Zero(t, getMidjourneyTask(t, task.Id).Quota)
}

// ===========================================================================
// RefundTaskQuota tests
// ===========================================================================

func TestRefundTaskQuota_Wallet(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 1, 1, 1
	const initQuota, preConsumed = 10000, 3000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-test-key", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)

	assert.True(t, RefundTaskQuota(ctx, task, "task failed: upstream error"))

	// User quota should increase by preConsumed
	assert.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))

	// Token remain_quota should increase, used_quota should decrease
	assert.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))

	// A refund log should be created
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
	assert.Equal(t, preConsumed, log.Quota)
	assert.Equal(t, "test-model", log.ModelName)
	assert.Zero(t, task.Quota)
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestRefundTaskQuota_Subscription(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, subID = 2, 2, 2, 1
	const preConsumed = 2000
	const subTotal, subUsed int64 = 100000, 50000
	const tokenRemain = 8000

	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-sub-key", tokenRemain)
	seedChannel(t, channelID)
	seedSubscription(t, subID, userID, subTotal, subUsed)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subID)
	require.NoError(t, model.DB.Create(task).Error)

	assert.True(t, RefundTaskQuota(ctx, task, "subscription task failed"))

	// Subscription used should decrease by preConsumed
	assert.Equal(t, subUsed-int64(preConsumed), getSubscriptionUsed(t, subID))

	// Token should also be refunded
	assert.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestRefundTaskQuota_ZeroQuota(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 3
	seedUser(t, userID, 5000)

	task := makeTask(userID, 0, 0, 0, BillingSourceWallet, 0)

	assert.True(t, RefundTaskQuota(ctx, task, "zero quota task"))

	// No change to user quota
	assert.Equal(t, 5000, getUserQuota(t, userID))

	// No log created
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRefundTaskQuota_NoToken(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 4, 4
	const initQuota, preConsumed = 10000, 1500

	seedUser(t, userID, initQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, 0, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0) // TokenId=0
	require.NoError(t, model.DB.Create(task).Error)

	assert.True(t, RefundTaskQuota(ctx, task, "no token task failed"))

	// User quota refunded
	assert.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))

	// Log created
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestRefundTaskQuota_FundingFailureKeepsAccountingAndPendingMarker(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID, preConsumed = 5, 5, 1200
	seedUser(t, userID, 5000)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, 0, preConsumed, 1)
	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceSubscription, 9999)
	task.Status = model.TaskStatusFailure
	require.NoError(t, model.DB.Create(task).Error)

	assert.False(t, RefundTaskQuota(ctx, task, "subscription missing"))
	assert.Equal(t, 5000, getUserQuota(t, userID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, preConsumed, getTaskQuota(t, task.ID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, preConsumed, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(preConsumed), getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRefundTaskQuota_TokenFailureKeepsFundingAndPendingMarker(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 6, 6, 6
	const walletAfterCharge, tokenRemain, preConsumed = 10000, 5000, 1200
	seedUser(t, userID, walletAfterCharge)
	seedToken(t, tokenID, userID, "sk-task-token-failure", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_task_token_refund
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 6
		BEGIN
			SELECT RAISE(ABORT, 'forced task token refund failure');
		END;
	`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_task_token_refund") })

	assert.False(t, RefundTaskQuota(ctx, task, "token unavailable"))
	// Token failure must leave every other ledger untouched and retain the
	// durable marker so a later retry can complete the refund.
	assert.Equal(t, walletAfterCharge, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, preConsumed, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(preConsumed), getChannelUsedQuota(t, channelID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, preConsumed, getTaskQuota(t, task.ID))
	assert.Equal(t, int64(0), countLogs(t))

	// Once the transient token failure is removed, the same task can be
	// retried without a duplicate funding refund.
	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_task_token_refund").Error)
	assert.True(t, RefundTaskQuota(ctx, task, "token retry"))
	assert.Equal(t, walletAfterCharge+preConsumed, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestRefundTaskQuota_FundingFailureCompensatesTokenRefund(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 7, 7, 7
	const walletAfterCharge, tokenRemain, preConsumed = 10000, 5000, 1300
	seedUser(t, userID, walletAfterCharge)
	seedToken(t, tokenID, userID, "sk-task-funding-failure", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_task_funding_refund
		BEFORE UPDATE ON users
		WHEN OLD.id = 7
		BEGIN
			SELECT RAISE(ABORT, 'forced task funding refund failure');
		END;
	`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_task_funding_refund") })

	assert.False(t, RefundTaskQuota(ctx, task, "funding unavailable"))
	// The token is first refunded, then restored when funding fails. This keeps
	// the whole operation retryable and avoids a partial token refund.
	assert.Equal(t, walletAfterCharge, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, getTokenUsedQuota(t, tokenID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, preConsumed, getTaskQuota(t, task.ID))
	assert.Equal(t, int64(0), countLogs(t))

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_task_funding_refund").Error)
	assert.True(t, RefundTaskQuota(ctx, task, "funding retry"))
	assert.Equal(t, walletAfterCharge+preConsumed, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	assert.Zero(t, getTaskQuota(t, task.ID))
}

func TestRefundTaskQuotaRejectsInvalidQuotaWithoutMutatingLedgers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		quota  int
		modern bool
	}{
		{name: "legacy negative", quota: -500},
		{name: "modern negative", quota: -500, modern: true},
		{name: "legacy above limit", quota: common.MaxQuota + 1},
		{name: "modern above limit", quota: common.MaxQuota + 1, modern: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			const userID, tokenID, channelID = 708, 708, 708
			const userQuota, tokenRemain, tokenUsed = 8_000, 4_000, 600
			seedUser(t, userID, userQuota)
			seedToken(t, tokenID, userID, "sk-invalid-task-refund", tokenRemain)
			seedChannel(t, channelID)
			require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", tokenUsed).Error)
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]any{
				"used_quota": 700,
			}).Error)
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("used_quota", 700).Error)

			task := makeTask(userID, channelID, tc.quota, tokenID, BillingSourceWallet, 0)
			task.TaskID = "invalid-task-refund-" + tc.name
			task.Status = model.TaskStatusFailure
			task.SubmitTime = time.Now().Unix()
			task.BillingSettlementState = model.TaskBillingSettlementComplete
			if tc.modern {
				task.BillingRequestId = "invalid-task-refund-request-" + tc.name
			}
			require.NoError(t, model.DB.Create(task).Error)

			assert.False(t, RefundTaskQuota(context.Background(), task, "invalid quota"))
			assert.Equal(t, userQuota, getUserQuota(t, userID))
			assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
			assert.Equal(t, tokenUsed, getTokenUsedQuota(t, tokenID))
			used, _ := getUserUsageAccounting(t, userID)
			assert.Equal(t, 700, used)
			assert.Equal(t, int64(700), getChannelUsedQuota(t, channelID))
			assert.Equal(t, tc.quota, getTaskQuota(t, task.ID))
			assert.Zero(t, countLogs(t))
		})
	}
}

func TestRefundTaskQuotaRejectsInvalidFundingSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		subID  int
	}{
		{name: "subscription id missing", source: BillingSourceSubscription},
		{name: "unknown source", source: "corrupt-source", subID: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			const userID, channelID, chargedQuota = 709, 709, 500
			const walletQuota = 8_000
			seedUser(t, userID, walletQuota)
			seedChannel(t, channelID)
			seedChargedAccounting(t, userID, channelID, 0, chargedQuota, 1)
			task := makeTask(userID, channelID, chargedQuota, 0, tc.source, tc.subID)
			task.TaskID = "invalid-funding-task-refund-" + tc.name
			task.Status = model.TaskStatusFailure
			require.NoError(t, model.DB.Create(task).Error)

			assert.False(t, RefundTaskQuota(context.Background(), task, "invalid funding snapshot"))
			assert.Equal(t, walletQuota, getUserQuota(t, userID), "corrupt subscription metadata must not be reinterpreted as wallet funding")
			used, requests := getUserUsageAccounting(t, userID)
			assert.Equal(t, chargedQuota, used)
			assert.Equal(t, 1, requests)
			assert.Equal(t, int64(chargedQuota), getChannelUsedQuota(t, channelID))
			assert.Equal(t, chargedQuota, getTaskQuota(t, task.ID))
			assert.Zero(t, countLogs(t))
		})
	}
}

func TestRefundTaskQuotaModernRefundsFundingWhenTokenWasDeleted(t *testing.T) {
	truncate(t)
	const userID, missingTokenID, channelID = 710, 710, 710
	const initialQuota, chargedQuota = 10_000, 900
	seedUser(t, userID, initialQuota-chargedQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, 0, chargedQuota, 1)

	task := makeTask(userID, channelID, chargedQuota, missingTokenID, BillingSourceWallet, 0)
	task.TaskID = "modern-refund-deleted-token"
	task.Status = model.TaskStatusFailure
	task.SubmitTime = time.Now().Unix()
	task.BillingRequestId = "modern-refund-deleted-token-request"
	task.BillingPreConsumedQuota = chargedQuota
	task.BillingSettlementQuota = chargedQuota
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	require.NoError(t, model.DB.Create(task).Error)

	require.True(t, claimAndRefundTaskQuota(context.Background(), task, "token deleted"))
	assert.Equal(t, initialQuota, getUserQuota(t, userID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Zero(t, getTaskQuota(t, task.ID))
}

// Modern async tasks carry a stable BillingRequestId. Their refund must be
// replay-safe even when the task marker write fails after the financial
// operation has committed.
func TestRefundTaskQuotaModernIsIdempotentAfterTaskMarkerFailure(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 711, 711, 711
	const initialUserQuota, initialTokenQuota, chargedQuota = 10_000, 6_000, 1_250
	seedUser(t, userID, initialUserQuota-chargedQuota)
	seedToken(t, tokenID, userID, "sk-modern-refund-marker", initialTokenQuota-chargedQuota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, chargedQuota, 1)

	task := makeTask(userID, channelID, chargedQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "modern-refund-marker-failure"
	task.Status = model.TaskStatusFailure
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	task.BillingRequestId = "modern-refund-marker-failure-request"
	task.BillingPreConsumedQuota = chargedQuota
	task.BillingSettlementQuota = chargedQuota
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	task.BillingUsageRecorded = true
	require.NoError(t, model.DB.Create(task).Error)

	// Force only the quota-clearing UPDATE to fail. The durable operation and
	// retryable reconciliation state must still commit.
	require.NoError(t, model.DB.Exec(fmt.Sprintf(`
		CREATE TRIGGER fail_modern_task_refund_marker
		BEFORE UPDATE ON tasks
		WHEN OLD.id = %d AND NEW.quota = 0
		BEGIN
			SELECT RAISE(ABORT, 'forced modern task marker failure');
		END;
	`, task.ID)).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_modern_task_refund_marker") })

	assert.False(t, claimAndRefundTaskQuota(ctx, task, "first attempt"))
	// The operation is committed exactly once even though the task marker was
	// rejected. No ledger or usage counter should be changed by a retry.
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(0), countLogs(t), "log waits until the durable task marker is cleared")

	var pending model.Task
	require.NoError(t, model.DB.First(&pending, task.ID).Error)
	assert.Equal(t, model.TaskBillingReconcileRetryable, pending.BillingReconcileState)
	assert.Equal(t, chargedQuota, pending.Quota)
	var op model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", task.BillingRequestId, "task_refund").First(&op).Error)
	assert.Equal(t, model.BillingOperationApplied, op.Status)

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_modern_task_refund_marker").Error)
	candidates, refunded, pendingCount := reconcileTerminalTaskBilling(ctx, 100)
	assert.Equal(t, 1, candidates, "reconciliation should find the retryable marker")
	assert.Equal(t, 1, refunded)
	assert.Zero(t, pendingCount)

	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Zero(t, got.Quota)
	assert.Equal(t, model.TaskBillingReconcileComplete, got.BillingReconcileState)
	assert.Equal(t, int64(1), countLogs(t))

	// A second direct retry is a no-op, including the informational log.
	assert.True(t, RefundTaskQuota(ctx, &got, "duplicate retry"))
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, int64(1), countLogs(t))
}

func TestRefundTaskQuotaModernSubscriptionAndPlayground(t *testing.T) {
	t.Run("subscription", func(t *testing.T) {
		truncate(t)
		const userID, tokenID, channelID, subID = 712, 712, 712, 712
		const chargedQuota, initialSubUsed, initialTokenRemain = 900, 4_000, 5_000
		seedUser(t, userID, 0)
		seedToken(t, tokenID, userID, "sk-modern-sub-refund", initialTokenRemain-chargedQuota)
		seedSubscription(t, subID, userID, 20_000, initialSubUsed)
		seedChannel(t, channelID)
		seedChargedAccounting(t, userID, channelID, tokenID, chargedQuota, 1)
		task := makeTask(userID, channelID, chargedQuota, tokenID, BillingSourceSubscription, subID)
		task.TaskID = "modern-subscription-refund"
		task.Status = model.TaskStatusFailure
		task.SubmitTime = time.Now().Unix()
		task.BillingRequestId = "modern-subscription-refund-request"
		task.BillingSettlementState = model.TaskBillingSettlementComplete
		require.NoError(t, model.DB.Create(task).Error)
		require.True(t, claimAndRefundTaskQuota(context.Background(), task, "subscription failure"))
		assert.Equal(t, int64(initialSubUsed-chargedQuota), getSubscriptionUsed(t, subID))
		assert.Equal(t, initialTokenRemain, getTokenRemainQuota(t, tokenID))
		assert.Zero(t, getTokenUsedQuota(t, tokenID))
		assert.Zero(t, getUserQuota(t, userID))
		used, requests := getUserUsageAccounting(t, userID)
		assert.Zero(t, used)
		assert.Equal(t, 1, requests)
	})

	t.Run("playground", func(t *testing.T) {
		truncate(t)
		const userID, channelID, chargedQuota = 713, 713, 700
		seedUser(t, userID, 10_000-chargedQuota)
		seedChannel(t, channelID)
		seedChargedAccounting(t, userID, channelID, 0, chargedQuota, 1)
		task := makeTask(userID, channelID, chargedQuota, 0, BillingSourceWallet, 0)
		task.TaskID = "modern-playground-refund"
		task.Status = model.TaskStatusFailure
		task.SubmitTime = time.Now().Unix()
		task.BillingRequestId = "modern-playground-refund-request"
		task.BillingPlayground = true
		task.BillingSettlementState = model.TaskBillingSettlementComplete
		require.NoError(t, model.DB.Create(task).Error)
		require.True(t, claimAndRefundTaskQuota(context.Background(), task, "playground failure"))
		assert.Equal(t, 10_000, getUserQuota(t, userID))
		used, requests := getUserUsageAccounting(t, userID)
		assert.Zero(t, used)
		assert.Equal(t, 1, requests)
		assert.Zero(t, getChannelUsedQuota(t, channelID))
	})
}

func TestRefundTaskQuotaLegacyDurableReplaySafe(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID, quota = 9310, 9310, 9310, 40
	seedUser(t, userID, 1000-quota)
	seedToken(t, tokenID, userID, "legacy-durable-refund-token", 1000-quota)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", quota).Error)
	seedChannel(t, channelID)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update("used_quota", quota).Error)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("used_quota", quota).Error)
	task := makeTask(userID, channelID, quota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "legacy-durable-refund"
	task.Status = model.TaskStatusFailure
	task.SubmitTime = time.Now().Unix()
	require.NoError(t, model.DB.Create(task).Error)

	require.True(t, RefundTaskQuota(context.Background(), task, "legacy failure"))
	assert.Zero(t, task.Quota)
	assert.Equal(t, 1000, getUserQuota(t, userID))
	assert.Equal(t, 1000, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	used, _ := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	var op model.BillingOperation
	require.NoError(t, model.DB.Where("component = ?", legacyTaskRefundComponent).First(&op).Error)
	assert.Equal(t, model.BillingOperationApplied, op.Status)

	// A replay after process loss must not credit either ledger twice.
	var replay model.Task
	require.NoError(t, model.DB.First(&replay, task.ID).Error)
	assert.True(t, RefundTaskQuota(context.Background(), &replay, "duplicate retry"))
	assert.Equal(t, 1000, getUserQuota(t, userID))
	assert.Equal(t, 1000, getTokenRemainQuota(t, tokenID))
}

func TestRefundTaskQuotaLegacyDurableFailureKeepsRetryableMarker(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID, quota = 9311, 9311, 9311, 40
	seedUser(t, userID, 1000-quota)
	seedToken(t, tokenID, userID, "legacy-durable-refund-failure-token", 1000-quota)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", quota).Error)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, quota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "legacy-durable-refund-failure"
	task.Status = model.TaskStatusFailure
	task.SubmitTime = time.Now().Unix()
	require.NoError(t, model.DB.Create(task).Error)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_legacy_task_refund_operation
		BEFORE UPDATE OF status ON billing_operations
		WHEN NEW.status = 'applied'
		BEGIN
			SELECT RAISE(ABORT, 'forced legacy refund failure');
		END;
	`).Error)
	assert.False(t, RefundTaskQuota(context.Background(), task, "forced failure"))
	var pending model.Task
	require.NoError(t, model.DB.First(&pending, task.ID).Error)
	assert.Equal(t, model.TaskBillingReconcileManual, pending.BillingReconcileState)
	assert.Equal(t, quota, pending.Quota)
	assert.Equal(t, 1000-quota, getUserQuota(t, userID))
	require.NoError(t, model.DB.Exec("DROP TRIGGER fail_legacy_task_refund_operation").Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"billing_reconcile_state": model.TaskBillingReconcilePending,
		"billing_reconcile_until": 0,
	}).Error)
	pending.BillingReconcileState = model.TaskBillingReconcilePending
	pending.BillingReconcileUntil = 0
	require.True(t, RefundTaskQuota(context.Background(), &pending, "retry"))
	assert.Zero(t, pending.Quota)
	assert.Equal(t, 1000, getUserQuota(t, userID))
}

// ===========================================================================
// RecalculateTaskQuota tests
// ===========================================================================

func TestRecalculate_PositiveDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 10, 10, 10
	const initQuota, preConsumed = 10000, 2000
	const actualQuota = 3000 // under-charged by 1000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-recalc-pos", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)

	RecalculateTaskQuota(ctx, task, actualQuota, "adaptor adjustment")

	// User quota should decrease by the delta (1000 additional charge)
	assert.Equal(t, initQuota-(actualQuota-preConsumed), getUserQuota(t, userID))

	// Token should also be charged the delta
	assert.Equal(t, tokenRemain-(actualQuota-preConsumed), getTokenRemainQuota(t, tokenID))
	assert.Equal(t, actualQuota, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, actualQuota, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(actualQuota), getChannelUsedQuota(t, channelID))

	// task.Quota should be updated to actualQuota
	assert.Equal(t, actualQuota, task.Quota)

	// Log type should be Consume (additional charge)
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeConsume, log.Type)
	assert.Equal(t, actualQuota-preConsumed, log.Quota)
}

func TestRecalculate_NegativeDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 11, 11, 11
	const initQuota, preConsumed = 10000, 5000
	const actualQuota = 3000 // over-charged by 2000
	const tokenRemain = 5000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-recalc-neg", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)

	RecalculateTaskQuota(ctx, task, actualQuota, "adaptor adjustment")

	// User quota should increase by abs(delta) = 2000 (refund overpayment)
	assert.Equal(t, initQuota+(preConsumed-actualQuota), getUserQuota(t, userID))

	// Token should be refunded the difference
	assert.Equal(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))
	assert.Equal(t, actualQuota, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, actualQuota, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(actualQuota), getChannelUsedQuota(t, channelID))

	// task.Quota updated
	assert.Equal(t, actualQuota, task.Quota)

	// Log type should be Refund
	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
	assert.Equal(t, preConsumed-actualQuota, log.Quota)
}

func TestRecalculate_ZeroDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 12
	const initQuota, preConsumed = 10000, 3000

	seedUser(t, userID, initQuota)

	task := makeTask(userID, 0, preConsumed, 0, BillingSourceWallet, 0)

	RecalculateTaskQuota(ctx, task, preConsumed, "exact match")

	// No change to user quota
	assert.Equal(t, initQuota, getUserQuota(t, userID))

	// No log created (delta is zero)
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRecalculate_ActualQuotaZero(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID = 13
	const initQuota = 10000

	seedUser(t, userID, initQuota)

	task := makeTask(userID, 0, 5000, 0, BillingSourceWallet, 0)

	RecalculateTaskQuota(ctx, task, 0, "zero actual")

	// No change (early return)
	assert.Equal(t, initQuota, getUserQuota(t, userID))
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRecalculateRejectsInvalidTaskQuotaBounds(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	RecalculateTaskQuota(ctx, nil, 1, "nil task")
	for _, task := range []*model.Task{
		{TaskID: "negative-pre", Quota: -1},
		{TaskID: "oversized-pre", Quota: common.MaxQuota + 1},
	} {
		RecalculateTaskQuota(ctx, task, 1, "invalid pre")
	}
	task := &model.Task{TaskID: "oversized-actual", Quota: 1}
	RecalculateTaskQuota(ctx, task, common.MaxQuota+1, "invalid actual")
	assert.Equal(t, 1, task.Quota)
	assert.Equal(t, int64(0), countLogs(t))
}

func TestRecalculate_Subscription_NegativeDelta(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, subID = 14, 14, 14, 2
	const preConsumed = 5000
	const actualQuota = 2000 // over-charged by 3000
	const subTotal, subUsed int64 = 100000, 50000
	const tokenRemain = 8000

	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "sk-sub-recalc", tokenRemain)
	seedChannel(t, channelID)
	seedSubscription(t, subID, userID, subTotal, subUsed)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceSubscription, subID)

	RecalculateTaskQuota(ctx, task, actualQuota, "subscription over-charge")

	// Subscription used should decrease by delta (refund 3000)
	assert.Equal(t, subUsed-int64(preConsumed-actualQuota), getSubscriptionUsed(t, subID))

	// Token refunded
	assert.Equal(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))
	assert.Equal(t, actualQuota, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, actualQuota, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(actualQuota), getChannelUsedQuota(t, channelID))

	assert.Equal(t, actualQuota, task.Quota)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
}

func TestRecalculate_TokenFailureLeavesFundingAndQuotaUnchanged(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 15, 15, 15
	const walletAfterCharge, tokenRemain, preConsumed, actualQuota = 10000, 8000, 5000, 3000
	seedUser(t, userID, walletAfterCharge)
	seedToken(t, tokenID, userID, "sk-task-recalc-token-failure", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_task_recalc_token
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 15
		BEGIN
			SELECT RAISE(ABORT, 'forced task recalc token failure');
		END;
	`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_task_recalc_token") })

	RecalculateTaskQuota(ctx, task, actualQuota, "token failure")
	assert.Equal(t, walletAfterCharge, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, getTokenUsedQuota(t, tokenID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, preConsumed, getTaskQuota(t, task.ID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, preConsumed, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(preConsumed), getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(0), countLogs(t))

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_task_recalc_token").Error)
	RecalculateTaskQuota(ctx, task, actualQuota, "token retry")
	assert.Equal(t, walletAfterCharge+(preConsumed-actualQuota), getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))
	assert.Equal(t, actualQuota, getTokenUsedQuota(t, tokenID))
	assert.Equal(t, actualQuota, task.Quota)
}

// A terminal task may be re-polled after another request has spent the
// user's remaining wallet balance. Recalculation must fail closed instead of
// allowing the signed delta to drive the wallet negative.
func TestRecalculateRejectsWalletOverdraftAfterConcurrentSpend(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 16, 16
	const currentWallet, preConsumed, actualQuota = 50, 100, 200
	seedUser(t, userID, currentWallet)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)

	RecalculateTaskQuota(ctx, task, actualQuota, "wallet overdraft")

	assert.Equal(t, currentWallet, getUserQuota(t, userID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, preConsumed, getTaskQuota(t, task.ID))
	assert.Zero(t, countLogs(t))
}

// Two pollers can hold stale copies of the same task. The adjustment must be
// fenced by a durable operation key and the task quota CAS so the second
// replay cannot charge the ledgers twice.
func TestRecalculatePersistedTaskIsIdempotentAcrossStaleCopies(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 17, 17, 17
	const initialWallet, preConsumed, actualQuota = 10000, 100, 200
	const tokenRemain = 10000
	seedUser(t, userID, initialWallet)
	seedToken(t, tokenID, userID, "sk-recalc-idempotent", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	require.NoError(t, model.DB.Create(task).Error)
	var stale model.Task
	require.NoError(t, model.DB.First(&stale, task.ID).Error)

	RecalculateTaskQuota(ctx, task, actualQuota, "first adjustment")
	RecalculateTaskQuota(ctx, &stale, actualQuota, "stale replay")

	assert.Equal(t, initialWallet-(actualQuota-preConsumed), getUserQuota(t, userID))
	assert.Equal(t, tokenRemain-(actualQuota-preConsumed), getTokenRemainQuota(t, tokenID))
	assert.Equal(t, actualQuota, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, actualQuota, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(actualQuota), getChannelUsedQuota(t, channelID))
	assert.Equal(t, actualQuota, getTaskQuota(t, task.ID))
	assert.Equal(t, int64(1), countBillingOperations(t, "legacy_task_recalculate"))
	assert.Equal(t, int64(1), countLogs(t))
}

// ===========================================================================
// CAS + Billing integration tests
// Simulates the flow in updateVideoSingleTask (service/task_polling.go)
// ===========================================================================

// simulatePollBilling reproduces the CAS + billing logic from updateVideoSingleTask.
// It takes a persisted task (already in DB), applies the new status, and performs
// the conditional update + billing exactly as the polling loop does.
func simulatePollBilling(ctx context.Context, task *model.Task, newStatus model.TaskStatus, actualQuota int) {
	snap := task.Snapshot()

	shouldRefund := false
	shouldSettle := false
	quota := task.Quota

	task.Status = newStatus
	switch string(newStatus) {
	case model.TaskStatusSuccess:
		task.Progress = "100%"
		task.FinishTime = 9999
		shouldSettle = true
	case model.TaskStatusFailure:
		task.Progress = "100%"
		task.FinishTime = 9999
		task.FailReason = "upstream error"
		if quota != 0 {
			shouldRefund = true
		}
	default:
		task.Progress = "50%"
	}

	isDone := task.Status == model.TaskStatus(model.TaskStatusSuccess) || task.Status == model.TaskStatus(model.TaskStatusFailure)
	if isDone && snap.Status != task.Status {
		won, err := task.UpdateWithStatus(snap.Status)
		if err != nil {
			shouldRefund = false
			shouldSettle = false
		} else if !won {
			shouldRefund = false
			shouldSettle = false
		}
	} else if !snap.Equal(task.Snapshot()) {
		_, _ = task.UpdateWithStatus(snap.Status)
	}

	if shouldSettle && actualQuota > 0 {
		RecalculateTaskQuota(ctx, task, actualQuota, "test settle")
	}
	if shouldRefund {
		RefundTaskQuota(ctx, task, task.FailReason)
	}
}

func TestCASGuardedRefund_Win(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 20, 20, 20
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 6000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-refund-win", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusFailure), 0)

	// CAS wins: task in DB should now be FAILURE
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusFailure, reloaded.Status)
	assert.Zero(t, reloaded.Quota)

	// Refund should have happened
	assert.Equal(t, initQuota+preConsumed, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+preConsumed, getTokenRemainQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
}

func TestCASGuardedRefund_Lose(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 21, 21, 21
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 6000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-refund-lose", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	// Create task with IN_PROGRESS in DB
	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	// Simulate another process already transitioning to FAILURE
	model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusFailure)

	// Our process still has the old in-memory state (IN_PROGRESS) and tries to transition
	// task.Status is still IN_PROGRESS in the snapshot
	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusFailure), 0)

	// CAS lost: user quota should NOT change (no double refund)
	assert.Equal(t, initQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, preConsumed, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(preConsumed), getChannelUsedQuota(t, channelID))

	// No billing log should be created
	assert.Equal(t, int64(0), countLogs(t))
}

func TestCASGuardedSettle_Win(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 22, 22, 22
	const initQuota, preConsumed = 10000, 5000
	const actualQuota = 3000 // over-charged, should get partial refund
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-cas-settle-win", tokenRemain)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	require.NoError(t, model.DB.Create(task).Error)

	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusSuccess), actualQuota)

	// CAS wins: task should be SUCCESS
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, model.TaskStatusSuccess, reloaded.Status)

	// Settlement should refund the over-charge (5000 - 3000 = 2000 back to user)
	assert.Equal(t, initQuota+(preConsumed-actualQuota), getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+(preConsumed-actualQuota), getTokenRemainQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, actualQuota, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(actualQuota), getChannelUsedQuota(t, channelID))

	// task.Quota should be updated to actualQuota
	assert.Equal(t, actualQuota, task.Quota)
}

func TestNonTerminalUpdate_NoBilling(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, channelID = 23, 23
	const initQuota, preConsumed = 10000, 3000

	seedUser(t, userID, initQuota)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, 0, BillingSourceWallet, 0)
	task.Status = model.TaskStatus(model.TaskStatusInProgress)
	task.Progress = "20%"
	require.NoError(t, model.DB.Create(task).Error)

	// Simulate a non-terminal poll update (still IN_PROGRESS, progress changed)
	simulatePollBilling(ctx, task, model.TaskStatus(model.TaskStatusInProgress), 0)

	// User quota should NOT change
	assert.Equal(t, initQuota, getUserQuota(t, userID))

	// No billing log
	assert.Equal(t, int64(0), countLogs(t))

	// Task progress should be updated in DB
	var reloaded model.Task
	require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, "50%", reloaded.Progress)
}

// ===========================================================================
// Mock adaptor for settleTaskBillingOnComplete tests
// ===========================================================================

type mockAdaptor struct {
	adjustReturn int
}

func (m *mockAdaptor) Init(_ *relaycommon.RelayInfo) {}
func (m *mockAdaptor) FetchTask(string, string, map[string]any, string) (*http.Response, error) {
	return nil, nil
}
func (m *mockAdaptor) FetchTaskWithContext(context.Context, string, string, map[string]any, string) (*http.Response, error) {
	return nil, nil
}
func (m *mockAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) { return nil, nil }
func (m *mockAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return m.adjustReturn
}

// ===========================================================================
// PerCallBilling tests — settleTaskBillingOnComplete
// ===========================================================================

func TestSettle_PerCallBilling_SkipsAdaptorAdjust(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 30, 30, 30
	const initQuota, preConsumed = 10000, 5000
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-percall-adaptor", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.PerCallBilling = true

	adaptor := &mockAdaptor{adjustReturn: 2000}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Per-call: no adjustment despite adaptor returning 2000
	assert.Equal(t, initQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, int64(0), countLogs(t))
}

func TestSettle_PerCallBilling_SkipsTotalTokens(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 31, 31, 31
	const initQuota, preConsumed = 10000, 4000
	const tokenRemain = 7000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-percall-tokens", tokenRemain)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	task.PrivateData.BillingContext.PerCallBilling = true

	adaptor := &mockAdaptor{adjustReturn: 0}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess, TotalTokens: 9999}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Per-call: no recalculation by tokens
	assert.Equal(t, initQuota, getUserQuota(t, userID))
	assert.Equal(t, tokenRemain, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, preConsumed, task.Quota)
	assert.Equal(t, int64(0), countLogs(t))
}

func TestSettle_NonPerCallBilling_AppliesAdaptorAdjustment(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID = 32, 32, 32
	const initQuota, preConsumed = 10000, 5000
	const adaptorQuota = 3000
	const tokenRemain = 8000

	seedUser(t, userID, initQuota)
	seedToken(t, tokenID, userID, "sk-nonpercall-adj", tokenRemain)
	seedChannel(t, channelID)
	// Model the submit-time pre-consume so the token's used_quota and the
	// informational aggregates match task.Quota. The durable adjustment
	// correctly rejects a refund that would underflow an inconsistent token.
	seedChargedAccounting(t, userID, channelID, tokenID, preConsumed, 1)

	task := makeTask(userID, channelID, preConsumed, tokenID, BillingSourceWallet, 0)
	// PerCallBilling defaults to false

	adaptor := &mockAdaptor{adjustReturn: adaptorQuota}
	taskResult := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	settleTaskBillingOnComplete(ctx, adaptor, task, taskResult)

	// Non-per-call: adaptor adjustment applies (refund 2000)
	assert.Equal(t, initQuota+(preConsumed-adaptorQuota), getUserQuota(t, userID))
	assert.Equal(t, tokenRemain+(preConsumed-adaptorQuota), getTokenRemainQuota(t, tokenID))
	assert.Equal(t, adaptorQuota, task.Quota)

	log := getLastLog(t)
	require.NotNil(t, log)
	assert.Equal(t, model.LogTypeRefund, log.Type)
}
