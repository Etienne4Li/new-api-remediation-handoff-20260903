package controller

import (
	"errors"
	"strings"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/shopspring/decimal"
)

// EPay callback hardening (audit P0-01).
//
// A valid signature only proves the caller knows the merchant key. Before any
// settlement we additionally require that the notification names our merchant
// id, carries every field we settle on, and states a sane currency amount. The
// amount itself is compared against the locked local order inside the model
// transaction (see model.RechargeEpay / model.CompleteSubscriptionOrderWithPaidMoney).

var (
	errEpayCallbackUnverified   = errors.New("epay callback not verified")
	errEpayCallbackMissingField = errors.New("epay callback missing required field")
	errEpayMerchantMismatch     = errors.New("epay callback merchant id mismatch")
	errEpayInvalidMoney         = errors.New("epay callback money is not a positive amount with at most two decimals")
	errEpayMerchantUnconfigured = errors.New("epay merchant id is not configured")
)

var epayCallbackRequiredParams = []string{"pid", "type", "out_trade_no", "trade_no", "money", "trade_status", "sign"}

// validateEpayCallback enforces the callback-level invariants that do not need
// the local order. expectedMerchantID must be the configured EPay pid.
func validateEpayCallback(params map[string]string, verifyInfo *epay.VerifyRes, expectedMerchantID string) error {
	if verifyInfo == nil || !verifyInfo.VerifyStatus {
		return errEpayCallbackUnverified
	}
	for _, name := range epayCallbackRequiredParams {
		if strings.TrimSpace(params[name]) == "" {
			return errEpayCallbackMissingField
		}
	}
	expectedMerchantID = strings.TrimSpace(expectedMerchantID)
	if expectedMerchantID == "" {
		return errEpayMerchantUnconfigured
	}
	if strings.TrimSpace(params["pid"]) != expectedMerchantID {
		return errEpayMerchantMismatch
	}
	if _, err := parseEpayMoney(verifyInfo.Money); err != nil {
		return err
	}
	return nil
}

// parseEpayMoney accepts a positive decimal currency amount with at most two
// fractional digits. Anything else is rejected rather than rounded so an
// underpayment can never be normalised into a matching amount.
func parseEpayMoney(raw string) (decimal.Decimal, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || amount.LessThanOrEqual(decimal.Zero) || !amount.Equal(amount.Round(2)) {
		return decimal.Zero, errEpayInvalidMoney
	}
	return amount, nil
}
