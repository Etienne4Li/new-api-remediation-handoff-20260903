package controller

import (
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/stretchr/testify/require"
)

func validEpayParams() map[string]string {
	return map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "TUC20260902",
		"trade_no":     "2026090212345678",
		"name":         "TUC10",
		"money":        "10.00",
		"trade_status": epay.StatusTradeSuccess,
		"sign":         "deadbeef",
		"sign_type":    "MD5",
	}
}

func verifiedEpayInfo(params map[string]string) *epay.VerifyRes {
	return &epay.VerifyRes{
		Type:           params["type"],
		TradeNo:        params["trade_no"],
		ServiceTradeNo: params["out_trade_no"],
		Name:           params["name"],
		Money:          params["money"],
		TradeStatus:    params["trade_status"],
		VerifyStatus:   true,
	}
}

func TestValidateEpayCallbackAcceptsWellFormedNotification(t *testing.T) {
	params := validEpayParams()
	require.NoError(t, validateEpayCallback(params, verifiedEpayInfo(params), "1001"))
	require.NoError(t, validateEpayCallback(params, verifiedEpayInfo(params), " 1001 "))
}

func TestValidateEpayCallbackRejectsUnverifiedSignature(t *testing.T) {
	params := validEpayParams()
	info := verifiedEpayInfo(params)
	info.VerifyStatus = false
	require.ErrorIs(t, validateEpayCallback(params, info, "1001"), errEpayCallbackUnverified)
	require.ErrorIs(t, validateEpayCallback(params, nil, "1001"), errEpayCallbackUnverified)
}

func TestValidateEpayCallbackRejectsForeignMerchant(t *testing.T) {
	params := validEpayParams()
	params["pid"] = "2002"
	require.ErrorIs(t, validateEpayCallback(params, verifiedEpayInfo(params), "1001"), errEpayMerchantMismatch)
}

func TestValidateEpayCallbackFailsClosedWithoutConfiguredMerchant(t *testing.T) {
	params := validEpayParams()
	require.ErrorIs(t, validateEpayCallback(params, verifiedEpayInfo(params), ""), errEpayMerchantUnconfigured)
}

func TestValidateEpayCallbackRequiresSettlementFields(t *testing.T) {
	for _, field := range epayCallbackRequiredParams {
		params := validEpayParams()
		params[field] = "  "
		require.ErrorIs(t, validateEpayCallback(params, verifiedEpayInfo(params), "1001"), errEpayCallbackMissingField, field)
	}
}

func TestValidateEpayCallbackRejectsBadMoney(t *testing.T) {
	for _, money := range []string{"0", "-1", "abc", "1.005", "", "1e3x"} {
		params := validEpayParams()
		params["money"] = money
		info := verifiedEpayInfo(params)
		err := validateEpayCallback(params, info, "1001")
		require.Error(t, err, money)
	}
}

func TestParseEpayMoney(t *testing.T) {
	for _, ok := range []string{"10", "10.5", "10.50", " 0.01 ", "1566.50"} {
		_, err := parseEpayMoney(ok)
		require.NoError(t, err, ok)
	}
	for _, bad := range []string{"0", "0.00", "-5", "5.123", "五", ""} {
		_, err := parseEpayMoney(bad)
		require.ErrorIs(t, err, errEpayInvalidMoney, bad)
	}
}
