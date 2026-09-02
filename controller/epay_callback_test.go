package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEpayCallbackContext(t *testing.T, method string, params url.Values) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	path := "/api/user/epay/notify"
	var request *http.Request
	if method == http.MethodPost {
		request = httptest.NewRequest(method, path, strings.NewReader(params.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		request = httptest.NewRequest(method, path+"?"+params.Encode(), nil)
	}
	ctx.Request = request
	return ctx
}

func signedEpayCallbackValues(t *testing.T) url.Values {
	t.Helper()
	values := map[string]string{
		"pid":          "merchant-1",
		"type":         "alipay",
		"out_trade_no": "order-1",
		"trade_no":     "provider-1",
		"name":         "TUC2",
		"money":        "10.00",
		"trade_status": epay.StatusTradeSuccess,
		"sign_type":    "MD5",
	}
	ep := epay.GenerateParams(values, "test-key")
	result := make(url.Values, len(ep))
	for key, value := range ep {
		result.Set(key, value)
	}
	return result
}

func TestVerifyEpayCallbackRequiresSignedProtocolFieldsAndMerchant(t *testing.T) {
	originalAddress := operation_setting.PayAddress
	originalID := operation_setting.EpayId
	originalKey := operation_setting.EpayKey
	t.Cleanup(func() {
		operation_setting.PayAddress = originalAddress
		operation_setting.EpayId = originalID
		operation_setting.EpayKey = originalKey
	})
	operation_setting.PayAddress = "https://pay.example"
	operation_setting.EpayId = "merchant-1"
	operation_setting.EpayKey = "test-key"

	callback, verifyInfo, err := verifyEpayCallback(newEpayCallbackContext(t, http.MethodGet, signedEpayCallbackValues(t)))
	require.NoError(t, err)
	require.NotNil(t, verifyInfo)
	assert.Equal(t, "order-1", callback.ServiceTradeNo)
	assert.Equal(t, "provider-1", callback.ProviderTradeNo)
	assert.Equal(t, "merchant-1", callback.MerchantID)
	assert.Equal(t, model.EpayKeyFingerprint("test-key"), callback.SignatureKeyFingerprint)
	assert.False(t, callback.SignatureUsedPreviousKey)

	testCases := []struct {
		name   string
		mutate func(url.Values)
	}{
		{name: "wrong merchant", mutate: func(values url.Values) { values.Set("pid", "other-merchant"); resignEpayValues(values) }},
		{name: "missing provider transaction", mutate: func(values url.Values) { values.Del("trade_no"); resignEpayValues(values) }},
		{name: "unsupported signature algorithm", mutate: func(values url.Values) { values.Set("sign_type", "RSA") }},
		{name: "too many money digits", mutate: func(values url.Values) { values.Set("money", "10.001"); resignEpayValues(values) }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			values := signedEpayCallbackValues(t)
			tc.mutate(values)
			_, _, err := verifyEpayCallback(newEpayCallbackContext(t, http.MethodGet, values))
			require.Error(t, err)
		})
	}
}

func resignEpayValues(values url.Values) {
	params := make(map[string]string, len(values))
	for key := range values {
		params[key] = values.Get(key)
	}
	ep := epay.GenerateParams(params, "test-key")
	for key, value := range ep {
		values.Set(key, value)
	}
}

func TestCollectEpayParamsRejectsAmbiguousDuplicates(t *testing.T) {
	getValues := signedEpayCallbackValues(t)
	getValues.Add("trade_no", "provider-duplicate")
	_, err := collectEpayParams(newEpayCallbackContext(t, http.MethodGet, getValues))
	require.ErrorIs(t, err, errEpayCallbackRejected)

	postValues := signedEpayCallbackValues(t)
	postValues.Add("money", "11.00")
	_, err = collectEpayParams(newEpayCallbackContext(t, http.MethodPost, postValues))
	require.ErrorIs(t, err, errEpayCallbackRejected)
}

func TestVerifyEpayCallbackSupportsExplicitExpiringPreviousKey(t *testing.T) {
	originalAddress := operation_setting.PayAddress
	originalID := operation_setting.EpayId
	originalKey := operation_setting.EpayKey
	t.Cleanup(func() {
		operation_setting.PayAddress = originalAddress
		operation_setting.EpayId = originalID
		operation_setting.EpayKey = originalKey
	})
	operation_setting.PayAddress = "https://pay.example"
	operation_setting.EpayId = "merchant-1"
	operation_setting.EpayKey = "new-key"
	t.Setenv("EPAY_PREVIOUS_KEY", "test-key")
	t.Setenv("EPAY_PREVIOUS_KEY_UNTIL", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))

	callback, verifyInfo, err := verifyEpayCallback(newEpayCallbackContext(t, http.MethodGet, signedEpayCallbackValues(t)))
	require.NoError(t, err)
	require.NotNil(t, verifyInfo)
	assert.True(t, verifyInfo.VerifyStatus)
	assert.Equal(t, model.EpayKeyFingerprint("test-key"), callback.SignatureKeyFingerprint)
	assert.True(t, callback.SignatureUsedPreviousKey)

	t.Setenv("EPAY_PREVIOUS_KEY_UNTIL", strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
	_, _, err = verifyEpayCallback(newEpayCallbackContext(t, http.MethodGet, signedEpayCallbackValues(t)))
	require.Error(t, err)
}
