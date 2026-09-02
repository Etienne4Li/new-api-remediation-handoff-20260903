package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSubscriptionLifecycleStatus(t *testing.T) {
	tests := map[string]string{
		"active":    "active",
		"paid":      "active",
		"past_due":  "past_due",
		"unpaid":    "past_due",
		"canceled":  "cancelled",
		"cancelled": "cancelled",
	}
	for input, expected := range tests {
		require.Equal(t, expected, normalizeSubscriptionLifecycleStatus(input))
	}
}

func TestApplySubscriptionLifecycleEventIsIdempotent(t *testing.T) {
	truncateTables(t)
	sub := &UserSubscription{UserId: 1, PlanId: 1, AmountTotal: 1000, Status: "active", EndTime: 100, ProviderSubscriptionID: "sub_lifecycle_1"}
	require.NoError(t, DB.Create(sub).Error)
	require.NoError(t, ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt_lifecycle_1", sub.ProviderSubscriptionID, "past_due", 0, "{}"))
	require.NoError(t, ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt_lifecycle_1", sub.ProviderSubscriptionID, "active", 200, "{}"))
	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "past_due", got.Status)
	require.EqualValues(t, int64(100), got.EndTime)
	var events int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ? AND order_kind = ?", PaymentProviderCreem, "subscription_lifecycle").Count(&events).Error)
	require.Equal(t, int64(1), events)
}

func TestApplySubscriptionLifecycleEventDoesNotResurrectCancelledSubscription(t *testing.T) {
	truncateTables(t)
	sub := &UserSubscription{
		UserId:                       2,
		PlanId:                       2,
		AmountTotal:                  1000,
		Status:                       "cancelled",
		EndTime:                      100,
		ProviderSubscriptionID:       "sub_terminal_1",
		ProviderSubscriptionProvider: PaymentProviderCreem,
	}
	require.NoError(t, DB.Create(sub).Error)

	require.NoError(t, ApplySubscriptionLifecycleEvent(
		PaymentProviderCreem,
		"evt_terminal_active",
		sub.ProviderSubscriptionID,
		"active",
		200,
		"{}",
	))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "cancelled", got.Status)
}

func TestApplySubscriptionLifecycleEventRejectsAmbiguousProviderSubscription(t *testing.T) {
	truncateTables(t)
	for _, userID := range []int{3, 4} {
		require.NoError(t, DB.Create(&UserSubscription{
			UserId:                       userID,
			PlanId:                       userID,
			AmountTotal:                  1000,
			Status:                       "active",
			EndTime:                      100,
			ProviderSubscriptionID:       "sub_ambiguous",
			ProviderSubscriptionProvider: PaymentProviderCreem,
		}).Error)
	}

	err := ApplySubscriptionLifecycleEvent(
		PaymentProviderCreem,
		"evt_ambiguous",
		"sub_ambiguous",
		"past_due",
		0,
		"{}",
	)
	require.Error(t, err)
}

func TestApplySubscriptionLifecycleEventUsesProviderEventTime(t *testing.T) {
	tests := []struct {
		name         string
		firstStatus  string
		firstTime    int64
		firstEnd     int64
		secondStatus string
		secondTime   int64
		secondEnd    int64
		wantStatus   string
		wantEnd      int64
		wantUsed     int64
		wantTime     int64
	}{
		{
			name:         "older failure cannot overwrite newer active renewal",
			firstStatus:  "active",
			firstTime:    200,
			firstEnd:     300,
			secondStatus: "past_due",
			secondTime:   100,
			secondEnd:    0,
			wantStatus:   "active",
			wantEnd:      300,
			wantUsed:     0,
			wantTime:     200,
		},
		{
			name:         "newer recovery is accepted",
			firstStatus:  "past_due",
			firstTime:    100,
			firstEnd:     0,
			secondStatus: "active",
			secondTime:   200,
			secondEnd:    300,
			wantStatus:   "active",
			wantEnd:      300,
			wantUsed:     0,
			wantTime:     200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			truncateTables(t)
			sub := &UserSubscription{
				UserId: 10, PlanId: 10, AmountTotal: 1000, AmountUsed: 75,
				Status: "active", EndTime: 100, ProviderSubscriptionID: "sub_ordered",
			}
			require.NoError(t, DB.Create(sub).Error)
			require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(PaymentProviderCreem, "evt_ordered_1", sub.ProviderSubscriptionID, tt.firstStatus, tt.firstTime, tt.firstEnd, "{}", SubscriptionLifecycleOptions{RenewalOnly: true}))
			require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(PaymentProviderCreem, "evt_ordered_2", sub.ProviderSubscriptionID, tt.secondStatus, tt.secondTime, tt.secondEnd, "{}", SubscriptionLifecycleOptions{RenewalOnly: true, AllowSamePeriodRecovery: true}))

			var got UserSubscription
			require.NoError(t, DB.First(&got, sub.Id).Error)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.wantEnd, got.EndTime)
			require.Equal(t, tt.wantUsed, got.AmountUsed)
			require.Equal(t, tt.wantTime, got.ProviderLifecycleEventTime)
		})
	}
}

func TestApplySubscriptionLifecycleEventGenericActiveCannotRevivePastDue(t *testing.T) {
	truncateTables(t)
	sub := &UserSubscription{
		UserId: 13, PlanId: 13, AmountTotal: 1000, AmountUsed: 55,
		Status: "past_due", EndTime: 100, ProviderSubscriptionID: "sub_generic_no_recovery",
	}
	require.NoError(t, DB.Create(sub).Error)
	require.NoError(t, ApplySubscriptionLifecycleEventAt(
		PaymentProviderCreem, "evt_generic_no_recovery", sub.ProviderSubscriptionID,
		"active", 200, 300, "{}",
	))
	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "past_due", got.Status)
	require.EqualValues(t, int64(55), got.AmountUsed)
	require.EqualValues(t, int64(100), got.EndTime)
}

func TestApplySubscriptionLifecycleEventGenericActiveDoesNotAdvanceActivePeriod(t *testing.T) {
	truncateTables(t)
	sub := &UserSubscription{
		UserId: 15, PlanId: 15, AmountTotal: 1000, AmountUsed: 63,
		Status: "active", EndTime: 100,
		ProviderSubscriptionID: "sub_generic_active_period",
	}
	require.NoError(t, DB.Create(sub).Error)

	// A generic subscription.updated-style active event may carry a future
	// boundary even though no paid renewal was observed. It must not extend the
	// entitlement or reset quota; only the explicit RenewalOnly path can do so.
	require.NoError(t, ApplySubscriptionLifecycleEventAt(
		PaymentProviderCreem,
		"evt_generic_active_period",
		sub.ProviderSubscriptionID,
		"active",
		200,
		300,
		"{}",
	))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "active", got.Status)
	require.EqualValues(t, int64(100), got.EndTime)
	require.EqualValues(t, int64(63), got.AmountUsed)
	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).
		Where("provider = ? AND provider_trade_no = ?", PaymentProviderCreem, "evt_generic_active_period").
		Count(&eventCount).Error)
	require.EqualValues(t, 1, eventCount, "the event fence must be retained even when entitlement mutation is rejected")
}

func TestApplySubscriptionLifecycleEventRenewalOnlyAdvancesActivePeriod(t *testing.T) {
	truncateTables(t)
	sub := &UserSubscription{
		UserId: 16, PlanId: 16, AmountTotal: 1000, AmountUsed: 63,
		Status: "active", EndTime: 100,
		ProviderSubscriptionID: "sub_explicit_active_period",
	}
	require.NoError(t, DB.Create(sub).Error)

	require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(
		PaymentProviderCreem,
		"evt_explicit_active_period",
		sub.ProviderSubscriptionID,
		"active",
		200,
		300,
		"{}",
		SubscriptionLifecycleOptions{RenewalOnly: true},
	))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "active", got.Status)
	require.EqualValues(t, int64(300), got.EndTime)
	require.Zero(t, got.AmountUsed, "a verified renewal starts a fresh period")
}

func TestApplySubscriptionLifecycleEventAllowsEvidenceBackedSamePeriodRecovery(t *testing.T) {
	truncateTables(t)
	const (
		userID = 14
		planID = 14
	)
	require.NoError(t, DB.Create(&User{Id: userID, Username: "same-period-recovery-user"}).Error)
	plan := &SubscriptionPlan{
		Id: planID, Title: "same-period-recovery-plan", PriceAmount: 10,
		Currency: "USD", DurationUnit: SubscriptionDurationMonth,
		DurationValue: 1, Enabled: true, TotalAmount: 1000,
		StripePriceId: "price_same_period",
	}
	require.NoError(t, DB.Create(plan).Error)
	const (
		tradeNo         = "same-period-recovery-order"
		providerSubID   = "sub_same_period_recovery"
		providerTradeNo = "pi_same_period_recovery"
	)
	order := &SubscriptionOrder{
		UserId: userID, PlanId: planID, Money: 10, TradeNo: tradeNo,
		PaymentMethod: PaymentMethodCreem, PaymentProvider: PaymentProviderCreem,
		Status: common.TopUpStatusSuccess, ProviderTradeNo: common.GetPointer(providerTradeNo),
		ProviderSubscriptionID: providerSubID, ProviderAmount: "10.00",
		ProviderCurrency: "USD", ProviderProductID: "price_same_period",
		ProviderMerchantID: "stripe", ProviderOrderName: plan.Title,
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, DB.Create(order).Error)
	sub := &UserSubscription{
		UserId: userID, PlanId: planID, AmountTotal: 1000, AmountUsed: 73,
		Status: "past_due", EndTime: 100, ProviderSubscriptionID: providerSubID,
		ProviderSubscriptionProvider: PaymentProviderCreem,
		SubscriptionOrderTradeNo:     tradeNo,
	}
	require.NoError(t, DB.Create(sub).Error)
	require.NoError(t, ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
		PaymentProviderCreem, "evt_same_period_recovery", providerSubID,
		"active", 200, 100,
		&SubscriptionLifecycleEvidence{Amount: "10.00", Currency: "USD", ProductID: "price_same_period"},
		"{}", SubscriptionLifecycleOptions{RenewalOnly: true, AllowSamePeriodRecovery: true},
	))
	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "active", got.Status)
	require.EqualValues(t, int64(100), got.EndTime)
	require.EqualValues(t, int64(73), got.AmountUsed, "same-period recovery must preserve consumed quota")
}

func TestApplySubscriptionLifecycleEventEvidenceSamePeriodEqualTimeKeepsRestrictiveState(t *testing.T) {
	truncateTables(t)
	const (
		userID      = 17
		planID      = 17
		tradeNo     = "same-period-tie-order"
		providerSub = "sub_same-period-tie"
		providerTxn = "pi_same-period-tie"
		eventTime   = int64(200)
		periodEnd   = int64(100)
	)
	require.NoError(t, DB.Create(&User{Id: userID, Username: "same-period-tie-user"}).Error)
	plan := &SubscriptionPlan{
		Id: planID, Title: "same-period-tie-plan", PriceAmount: 10,
		Currency: "USD", DurationUnit: SubscriptionDurationMonth,
		DurationValue: 1, Enabled: true, TotalAmount: 1000,
		StripePriceId: "price_same_period_tie",
	}
	require.NoError(t, DB.Create(plan).Error)
	order := &SubscriptionOrder{
		UserId: userID, PlanId: planID, Money: 10, TradeNo: tradeNo,
		PaymentMethod: PaymentMethodCreem, PaymentProvider: PaymentProviderCreem,
		Status: common.TopUpStatusSuccess, ProviderTradeNo: common.GetPointer(providerTxn),
		ProviderSubscriptionID: providerSub, ProviderAmount: "10.00",
		ProviderCurrency: "USD", ProviderProductID: "price_same_period_tie",
		ProviderMerchantID: "stripe", ProviderOrderName: plan.Title,
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, DB.Create(order).Error)
	sub := &UserSubscription{
		UserId: userID, PlanId: planID, AmountTotal: 1000, AmountUsed: 73,
		Status: "past_due", EndTime: periodEnd,
		ProviderSubscriptionID:       providerSub,
		ProviderSubscriptionProvider: PaymentProviderCreem,
		ProviderLifecycleEventTime:   eventTime,
		SubscriptionOrderTradeNo:     tradeNo,
	}
	require.NoError(t, DB.Create(sub).Error)

	// Equal-time events resolve toward the more restrictive state. Even with
	// valid invoice evidence and the explicit same-period opt-in, a payment
	// event tied at the same provider timestamp cannot outrank the recorded
	// past_due event; it must be reconciled by a later-timestamp delivery.
	require.NoError(t, ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
		PaymentProviderCreem, "evt_same_period_tie", providerSub,
		"active", eventTime, periodEnd,
		&SubscriptionLifecycleEvidence{Amount: "10.00", Currency: "USD", ProductID: "price_same_period_tie"},
		"{}", SubscriptionLifecycleOptions{RenewalOnly: true, AllowSamePeriodRecovery: true},
	))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "past_due", got.Status)
	require.EqualValues(t, periodEnd, got.EndTime)
	require.EqualValues(t, int64(73), got.AmountUsed)
	require.EqualValues(t, eventTime, got.ProviderLifecycleEventTime)
}

func TestApplySubscriptionLifecycleEventEqualTimePrefersTerminalState(t *testing.T) {
	truncateTables(t)
	sub := &UserSubscription{
		UserId: 11, PlanId: 11, AmountTotal: 1000, AmountUsed: 20,
		Status: "active", EndTime: 100, ProviderSubscriptionID: "sub_equal_time",
	}
	require.NoError(t, DB.Create(sub).Error)
	require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(
		PaymentProviderCreem, "evt_equal_active", sub.ProviderSubscriptionID,
		"active", 500, 200, "{}", SubscriptionLifecycleOptions{RenewalOnly: true},
	))
	require.NoError(t, ApplySubscriptionLifecycleEventAt(PaymentProviderCreem, "evt_equal_cancel", sub.ProviderSubscriptionID, "cancelled", 500, 0, "{}"))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "cancelled", got.Status)
	require.Equal(t, int64(200), got.EndTime)
}

func TestApplySubscriptionLifecycleEventWithoutTimestampCannotRecoverPastDue(t *testing.T) {
	truncateTables(t)
	sub := &UserSubscription{
		UserId: 12, PlanId: 12, AmountTotal: 1000, AmountUsed: 40,
		Status: "active", EndTime: 100, ProviderSubscriptionID: "sub_no_timestamp",
	}
	require.NoError(t, DB.Create(sub).Error)
	require.NoError(t, ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt_no_timestamp_failed", sub.ProviderSubscriptionID, "past_due", 0, "{}"))
	require.NoError(t, ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt_no_timestamp_active", sub.ProviderSubscriptionID, "active", 200, "{}"))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "past_due", got.Status)
	require.Equal(t, int64(100), got.EndTime)
}

// A provider can emit an active/recovered event for the same billing period
// after a transient payment failure.  A period boundary that is old or equal
// to the locally recorded boundary is not a renewal: accepting it blindly
// resurrects a past_due entitlement (and, in older implementations, could
// reset its consumed quota).  Only a strictly newer period may perform the
// normal renewal transition without additional payment evidence.
func TestApplySubscriptionLifecycleEventDoesNotRevivePastDueWithoutPeriodAdvance(t *testing.T) {
	tests := []struct {
		name       string
		periodEnd  int64
		wantEnd    int64
		wantStatus string
		wantUsed   int64
		renewal    bool
	}{
		{name: "older period", periodEnd: 90, wantEnd: 100, wantStatus: "past_due", wantUsed: 75},
		{name: "same period", periodEnd: 100, wantEnd: 100, wantStatus: "past_due", wantUsed: 75},
		{name: "newer period", periodEnd: 200, wantEnd: 200, wantStatus: "active", wantUsed: 0, renewal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			truncateTables(t)
			sub := &UserSubscription{
				UserId:      121,
				PlanId:      121,
				AmountTotal: 1000,
				AmountUsed:  75,
				Status:      "past_due", EndTime: 100,
				ProviderSubscriptionID: "sub_past_due_period",
			}
			require.NoError(t, DB.Create(sub).Error)
			options := SubscriptionLifecycleOptions{}
			if tt.renewal {
				options.RenewalOnly = true
			}
			require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(
				PaymentProviderCreem,
				"evt_past_due_period_"+tt.name,
				sub.ProviderSubscriptionID,
				"active",
				200,
				tt.periodEnd,
				"{}",
				options,
			))
			var got UserSubscription
			require.NoError(t, DB.First(&got, sub.Id).Error)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.wantEnd, got.EndTime)
			require.Equal(t, tt.wantUsed, got.AmountUsed)
		})
	}
}

func TestApplySubscriptionLifecycleEventDowngradesGroupOnTerminalTransition(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 20, Username: "lifecycle-group-user", Group: "vip", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(user).Error)
	sub := &UserSubscription{
		UserId: 20, PlanId: 20, AmountTotal: 1000, Status: "active", EndTime: 999999,
		UpgradeGroup: "vip", PrevUserGroup: "default", ProviderSubscriptionID: "sub_group_terminal",
	}
	require.NoError(t, DB.Create(sub).Error)

	require.NoError(t, ApplySubscriptionLifecycleEventAt(PaymentProviderCreem, "evt_group_cancel", sub.ProviderSubscriptionID, "cancelled", 500, 0, "{}"))

	var got User
	require.NoError(t, DB.Select("group").First(&got, user.Id).Error)
	require.Equal(t, "default", got.Group)
}

func TestApplySubscriptionLifecycleEventScheduledCancellationKeepsAccessUntilPeriodEnd(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	periodEnd := now + 3600
	sub := &UserSubscription{
		UserId: 30, PlanId: 30, AmountTotal: 1000, AmountUsed: 73,
		Status: "active", EndTime: periodEnd + 600,
		ProviderSubscriptionID: "sub_scheduled_cancel",
	}
	require.NoError(t, DB.Create(sub).Error)

	require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(
		PaymentProviderCreem,
		"evt_scheduled_cancel",
		sub.ProviderSubscriptionID,
		"cancelled",
		now+10,
		periodEnd,
		"{}",
		SubscriptionLifecycleOptions{CancellationAtPeriodEnd: true},
	))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "active", got.Status)
	require.Equal(t, periodEnd, got.EndTime)
	require.Equal(t, int64(73), got.AmountUsed, "scheduling cancellation must not reset current-period usage")
	active, err := HasActiveUserSubscription(sub.UserId)
	require.NoError(t, err)
	require.True(t, active)
}

func TestApplySubscriptionLifecycleEventScheduledCancellationNeverExtendsLocalBoundary(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	localEnd := now + 1800
	providerPeriodEnd := now + 3600
	sub := &UserSubscription{
		UserId:                 33,
		PlanId:                 33,
		AmountTotal:            1000,
		AmountUsed:             41,
		Status:                 "active",
		EndTime:                localEnd,
		ProviderSubscriptionID: "sub_scheduled_cancel_no_extend",
	}
	require.NoError(t, DB.Create(sub).Error)

	require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(
		PaymentProviderCreem,
		"evt_scheduled_cancel_no_extend",
		sub.ProviderSubscriptionID,
		"cancelled",
		now+10,
		providerPeriodEnd,
		"{}",
		SubscriptionLifecycleOptions{CancellationAtPeriodEnd: true},
	))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "active", got.Status)
	require.Equal(t, localEnd, got.EndTime, "scheduled cancellation must not extend the local entitlement boundary")
	require.Equal(t, int64(41), got.AmountUsed, "scheduling cancellation must not reset current-period usage")
}

func TestApplySubscriptionLifecycleEventImmediateRevokeEndsFutureEntitlement(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 31, Username: "immediate-revoke-user", Group: "vip", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(user).Error)
	sub := &UserSubscription{
		UserId: 31, PlanId: 31, AmountTotal: 1000, AmountUsed: 19,
		Status: "active", EndTime: time.Now().Unix() + 7200,
		UpgradeGroup: "vip", PrevUserGroup: "default",
		ProviderSubscriptionID: "sub_immediate_revoke",
	}
	require.NoError(t, DB.Create(sub).Error)

	require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(
		PaymentProviderCreem,
		"evt_refund_revoke",
		sub.ProviderSubscriptionID,
		"cancelled",
		time.Now().Unix()+10,
		time.Now().Unix()+7200,
		"{}",
		SubscriptionLifecycleOptions{ImmediateRevoke: true},
	))

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "cancelled", got.Status)
	require.LessOrEqual(t, got.EndTime, time.Now().Unix())
	require.Equal(t, int64(19), got.AmountUsed)
	var gotUser User
	require.NoError(t, DB.Select("group").First(&gotUser, user.Id).Error)
	require.Equal(t, "default", gotUser.Group)
}

func TestApplySubscriptionLifecycleEventScheduledCancellationPastBoundaryRevokes(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	sub := &UserSubscription{
		UserId: 32, PlanId: 32, AmountTotal: 1000, AmountUsed: 5,
		Status: "active", EndTime: now + 3600,
		ProviderSubscriptionID: "sub_scheduled_already_due",
	}
	require.NoError(t, DB.Create(sub).Error)
	require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(
		PaymentProviderCreem,
		"evt_scheduled_already_due",
		sub.ProviderSubscriptionID,
		"cancelled",
		now+10,
		now-1,
		"{}",
		SubscriptionLifecycleOptions{CancellationAtPeriodEnd: true},
	))
	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	require.Equal(t, "expired", got.Status)
	require.LessOrEqual(t, got.EndTime, time.Now().Unix())
}
