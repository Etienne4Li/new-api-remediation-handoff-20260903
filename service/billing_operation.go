package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// BuildSettlementOperationSpec returns the immutable financial operation that
// BillingSession.Settle will apply for an asynchronous task. Keeping this
// construction beside applyDurableBillingOperation prevents the marker-only
// preflight from drifting from the actual settlement deltas.
func (s *BillingSession) BuildSettlementOperationSpec(actualQuota int) (model.BillingOperationSpec, error) {
	if s == nil || s.relayInfo == nil || s.funding == nil {
		return model.BillingOperationSpec{}, errors.New("billing session is not initialized")
	}
	requestID := strings.TrimSpace(s.relayInfo.RequestId)
	if requestID == "" {
		return model.BillingOperationSpec{}, errors.New("billing request id is missing")
	}
	if actualQuota < 0 || actualQuota > common.MaxQuota {
		return model.BillingOperationSpec{}, fmt.Errorf("billing settlement quota is out of range: %d", actualQuota)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.preConsumedQuota < 0 || s.preConsumedQuota > common.MaxQuota {
		return model.BillingOperationSpec{}, fmt.Errorf("billing pre-consume quota is out of range: %d", s.preConsumedQuota)
	}
	delta := int64(actualQuota - s.preConsumedQuota)
	spec := model.BillingOperationSpec{
		RequestID:         requestID,
		Component:         "settle",
		UserID:            s.relayInfo.UserId,
		TokenID:           s.relayInfo.TokenId,
		TokenKey:          s.relayInfo.TokenKey,
		TokenUnlimited:    s.relayInfo.TokenUnlimited,
		WalletDelta:       0,
		TokenDelta:        delta,
		SubscriptionDelta: 0,
	}
	if s.relayInfo.IsPlayground {
		spec.TokenDelta = 0
	}
	switch funding := s.funding.(type) {
	case *WalletFunding:
		if funding == nil || funding.userId <= 0 {
			return model.BillingOperationSpec{}, errors.New("invalid wallet funding")
		}
		spec.UserID = funding.userId
		spec.WalletDelta = -delta
	case *SubscriptionFunding:
		if funding == nil || funding.userId <= 0 || funding.subscriptionId <= 0 {
			return model.BillingOperationSpec{}, errors.New("invalid subscription funding")
		}
		spec.UserID = funding.userId
		spec.SubscriptionID = funding.subscriptionId
		spec.SubscriptionDelta = delta
	default:
		return model.BillingOperationSpec{}, fmt.Errorf("unsupported funding source: %s", s.funding.Source())
	}
	return spec, nil
}

// applyDurableBillingOperation is the bridge between BillingSession's
// lifecycle state machine and model's database journal.  It returns used=false
// for legacy/test funding implementations that do not have a stable request
// identity; all production sessions created by NewBillingSession have one.
// A used=true result means the caller must not execute a second, non-idempotent
// ledger path even when err is non-nil.
func (s *BillingSession) applyDurableBillingOperation(component string, walletDelta, tokenDelta, subscriptionDelta int64, requireWalletBalance, requireTokenBalance bool) (used bool, err error) {
	if s == nil || s.relayInfo == nil || s.funding == nil {
		return false, nil
	}
	requestID := strings.TrimSpace(s.relayInfo.RequestId)
	if requestID == "" || model.DB == nil {
		return false, nil
	}
	// Test hooks deliberately model failures at individual component boundaries;
	// bypass the combined operation when one is installed so those tests and
	// legacy extension points retain their documented semantics.
	if tokenDelta != 0 && s.adjustTokenFn != nil {
		return false, nil
	}
	if requireWalletBalance && s.reserveFundingFn != nil {
		return false, nil
	}
	if requireTokenBalance && s.reserveTokenFn != nil {
		return false, nil
	}

	spec := model.BillingOperationSpec{
		RequestID:            requestID,
		Component:            component,
		UserID:               s.relayInfo.UserId,
		TokenID:              s.relayInfo.TokenId,
		TokenKey:             s.relayInfo.TokenKey,
		WalletDelta:          walletDelta,
		TokenDelta:           tokenDelta,
		SubscriptionDelta:    subscriptionDelta,
		RequireWalletBalance: requireWalletBalance,
		RequireTokenBalance:  requireTokenBalance,
		TokenUnlimited:       s.relayInfo.TokenUnlimited,
	}

	switch funding := s.funding.(type) {
	case *WalletFunding:
		if funding == nil || funding.userId <= 0 {
			return true, errors.New("invalid wallet funding")
		}
		if spec.UserID == 0 {
			spec.UserID = funding.userId
		}
		if spec.UserID != funding.userId {
			return true, model.ErrBillingOperationConflict
		}
	case *SubscriptionFunding:
		if funding == nil || funding.userId <= 0 {
			return true, errors.New("invalid subscription funding")
		}
		if spec.UserID == 0 {
			spec.UserID = funding.userId
		}
		if spec.UserID != funding.userId {
			return true, model.ErrBillingOperationConflict
		}
		// Keep the subscription identity on zero-delta settlement markers too.
		// BuildSettlementOperationSpec includes SubscriptionID for every
		// subscription settlement, and the immutable operation spec must match
		// across the direct path and a later task/reconciler replay even when no
		// quota adjustment was needed.
		if subscriptionDelta != 0 || component == "settle" {
			if funding.subscriptionId <= 0 {
				return true, errors.New("subscription funding is not reserved")
			}
			spec.SubscriptionID = funding.subscriptionId
		}
	default:
		return false, nil
	}

	return true, model.ApplyBillingOperation(spec)
}

// ensureAndApplyDurableWalletRefund records the refund intent in a separate
// transaction before attempting any ledger mutation.  If the apply transaction
// fails (including an ambiguous connection error), the pending marker remains
// available to the background reconciler; no non-idempotent fallback is safe.
func (s *BillingSession) ensureAndApplyDurableWalletRefund(wallet *WalletFunding) (used bool, err error) {
	if s == nil || s.relayInfo == nil || wallet == nil {
		return false, nil
	}
	requestID := strings.TrimSpace(s.relayInfo.RequestId)
	if requestID == "" || model.DB == nil || s.adjustTokenFn != nil || wallet.requestId == "" {
		return false, nil
	}
	if wallet.userId <= 0 || (wallet.consumed <= 0 && s.tokenConsumed <= 0) {
		return true, errors.New("invalid wallet refund state")
	}
	tokenDelta := -int64(s.tokenConsumed)
	if s.relayInfo.IsPlayground {
		tokenDelta = 0
	}
	spec := model.BillingOperationSpec{
		RequestID:      requestID,
		Component:      model.BillingOperationRefundComponent,
		UserID:         wallet.userId,
		TokenID:        s.relayInfo.TokenId,
		TokenKey:       s.relayInfo.TokenKey,
		WalletDelta:    int64(wallet.consumed),
		TokenDelta:     tokenDelta,
		TokenUnlimited: s.relayInfo.TokenUnlimited,
	}
	if err := model.EnsureBillingRefundOperation(spec); err != nil {
		return true, err
	}
	return true, model.ApplyBillingOperation(spec)
}

func (s *BillingSession) durableSubscriptionReservation(subscription *SubscriptionFunding) (*model.SubscriptionBillingReservation, error) {
	if s == nil || s.relayInfo == nil || subscription == nil {
		return nil, errors.New("billing session is not initialized")
	}
	requestID := strings.TrimSpace(s.relayInfo.RequestId)
	if requestID == "" || model.DB == nil || subscription.userId <= 0 || subscription.subscriptionId <= 0 || subscription.preConsumed <= 0 {
		return nil, errors.New("invalid subscription reservation state")
	}
	reservation, err := model.GetSubscriptionBillingReservation(requestID)
	if err != nil {
		return nil, err
	}
	if reservation.UserID != subscription.userId || reservation.UserID != s.relayInfo.UserId ||
		reservation.SubscriptionID != subscription.subscriptionId || reservation.TokenID != s.relayInfo.TokenId ||
		reservation.BaseSubscriptionQuota != subscription.preConsumed ||
		reservation.TokenUnlimited != s.relayInfo.TokenUnlimited ||
		reservation.SubscriptionQuota < reservation.BaseSubscriptionQuota ||
		reservation.SubscriptionQuota > int64(common.MaxQuota) || reservation.TokenQuota < 0 ||
		reservation.TokenQuota > int64(common.MaxQuota) {
		return nil, model.ErrBillingOperationConflict
	}
	if s.relayInfo.IsPlayground {
		if reservation.TokenQuota != 0 {
			return nil, model.ErrBillingOperationConflict
		}
	} else if reservation.TokenQuota != reservation.SubscriptionQuota {
		return nil, model.ErrBillingOperationConflict
	}
	return reservation, nil
}

func (s *BillingSession) restoreDurableSubscriptionReservation(subscription *SubscriptionFunding) error {
	reservation, err := s.durableSubscriptionReservation(subscription)
	if err != nil {
		return err
	}
	if reservation.Status != "consumed" && reservation.Status != "refund_pending" {
		return model.ErrBillingOperationConflict
	}
	extra := reservation.SubscriptionQuota - reservation.BaseSubscriptionQuota
	if reservation.AmountUsed < extra {
		return model.ErrBillingOperationConflict
	}
	s.preConsumedQuota = int(reservation.SubscriptionQuota)
	s.extraReserved = int(extra)
	s.tokenConsumed = int(reservation.TokenQuota)
	subscription.AmountTotal = reservation.AmountTotal
	// syncRelayInfo adds this request's extra reservation. Store the underlying
	// amount at the base-reservation point so a reconstructed session does not
	// report the durable top-ups twice.
	subscription.AmountUsedAfter = reservation.AmountUsed - extra
	s.syncRelayInfo()
	return nil
}

func (s *BillingSession) applyDurableSubscriptionReserve(subscription *SubscriptionFunding, targetQuota, delta int) (used bool, err error) {
	if s == nil || s.relayInfo == nil || subscription == nil {
		return false, nil
	}
	requestID := strings.TrimSpace(s.relayInfo.RequestId)
	if requestID == "" || model.DB == nil {
		return false, nil
	}
	if s.adjustTokenFn != nil || s.reserveFundingFn != nil || s.reserveTokenFn != nil {
		return false, nil
	}
	if subscription.userId <= 0 || subscription.subscriptionId <= 0 || delta <= 0 {
		return true, errors.New("invalid subscription reserve state")
	}
	tokenDelta := int64(delta)
	if s.relayInfo.IsPlayground {
		tokenDelta = 0
	}
	spec := model.BillingOperationSpec{
		RequestID:           requestID,
		Component:           fmt.Sprintf("reserve:%d", targetQuota),
		UserID:              subscription.userId,
		TokenID:             s.relayInfo.TokenId,
		TokenKey:            s.relayInfo.TokenKey,
		TokenDelta:          tokenDelta,
		SubscriptionID:      subscription.subscriptionId,
		SubscriptionDelta:   int64(delta),
		RequireTokenBalance: tokenDelta > 0 && !s.relayInfo.TokenUnlimited,
		TokenUnlimited:      s.relayInfo.TokenUnlimited,
	}
	return true, model.ApplySubscriptionReserveBillingOperation(spec)
}

// applyDurableSubscriptionRefund commits the base reservation, every applied
// Reserve top-up, the token refund, and the marker close in one transaction.
func (s *BillingSession) applyDurableSubscriptionRefund(subscription *SubscriptionFunding) (used bool, err error) {
	if s == nil || s.relayInfo == nil || subscription == nil {
		return false, nil
	}
	requestID := strings.TrimSpace(s.relayInfo.RequestId)
	if requestID == "" || model.DB == nil {
		return false, nil
	}
	if s.adjustTokenFn != nil {
		return false, nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		reservation, snapshotErr := s.durableSubscriptionReservation(subscription)
		if snapshotErr != nil {
			return true, snapshotErr
		}
		spec := model.BillingOperationSpec{
			RequestID:         requestID,
			Component:         "refund",
			UserID:            reservation.UserID,
			TokenID:           reservation.TokenID,
			TokenKey:          s.relayInfo.TokenKey,
			TokenDelta:        -reservation.TokenQuota,
			SubscriptionID:    reservation.SubscriptionID,
			SubscriptionDelta: -reservation.SubscriptionQuota,
			TokenUnlimited:    reservation.TokenUnlimited,
		}
		// Freeze the reservation and persist the exact refund intent before the
		// side-effecting transaction.  This closes the crash window between a
		// failed request and the first refund attempt, and prevents a concurrent
		// Reserve from changing the amount after the intent is recorded.
		if ensureErr := model.EnsureSubscriptionRefundOperation(spec, requestID); ensureErr != nil {
			return true, ensureErr
		}
		refundErr := model.ApplyBillingOperationAndMarkSubscriptionReservationRefunded(spec, requestID)
		if !errors.Is(refundErr, model.ErrBillingReservationChanged) {
			return true, refundErr
		}
	}
	return true, model.ErrBillingReservationChanged
}
