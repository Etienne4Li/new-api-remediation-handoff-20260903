package controller

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

var errEpayCallbackRejected = errors.New("epay callback rejected")

// collectEpayParams accepts the two forms EPay documents (GET query and
// application/x-www-form-urlencoded POST) without merging attacker-controlled
// duplicate values from both locations.
func collectEpayParams(c *gin.Context) (map[string]string, error) {
	if c.Request.Method == http.MethodPost {
		if err := c.Request.ParseForm(); err != nil {
			return nil, err
		}
		for _, values := range c.Request.PostForm {
			if len(values) != 1 {
				// Do not silently choose one of duplicate form values. Different
				// proxies/frameworks choose first/last differently, which can
				// turn a signed payload into an ambiguous order assertion.
				return nil, errEpayCallbackRejected
			}
		}
		return lo.Reduce(lo.Keys(c.Request.PostForm), func(result map[string]string, key string, _ int) map[string]string {
			result[key] = c.Request.PostForm.Get(key)
			return result
		}, map[string]string{}), nil
	}
	for _, values := range c.Request.URL.Query() {
		if len(values) != 1 {
			return nil, errEpayCallbackRejected
		}
	}
	return lo.Reduce(lo.Keys(c.Request.URL.Query()), func(result map[string]string, key string, _ int) map[string]string {
		result[key] = c.Request.URL.Query().Get(key)
		return result
	}, map[string]string{}), nil
}

// verifyEpayCallback is the single EPay verification boundary used by normal
// top-ups and subscriptions. SDK Verify only checks the MD5 value, so this
// helper additionally enforces the algorithm, merchant, required fields and a
// sane currency amount before any model settlement call is possible.
func verifyEpayCallback(c *gin.Context) (model.EpayCallback, *epay.VerifyRes, error) {
	paymentConfig := operation_setting.GetPaymentRuntimeConfig()
	var callback model.EpayCallback
	params, err := collectEpayParams(c)
	if err != nil || len(params) == 0 {
		return callback, nil, errEpayCallbackRejected
	}
	if strings.TrimSpace(paymentConfig.PayAddress) == "" || strings.TrimSpace(paymentConfig.EpayID) == "" || strings.TrimSpace(paymentConfig.EpayKey) == "" {
		return callback, nil, errEpayCallbackRejected
	}

	// go-epay v0.0.4 ignores the incoming sign_type while calculating MD5;
	// accepting another algorithm label would make protocol migration ambiguous.
	if !strings.EqualFold(strings.TrimSpace(params["sign_type"]), "MD5") {
		return callback, nil, errEpayCallbackRejected
	}
	required := []string{"pid", "type", "out_trade_no", "trade_no", "name", "money", "trade_status", "sign", "sign_type"}
	for _, key := range required {
		value := strings.TrimSpace(params[key])
		if value == "" || len(value) > 512 {
			return callback, nil, errEpayCallbackRejected
		}
	}
	if strings.TrimSpace(params["pid"]) != strings.TrimSpace(paymentConfig.EpayID) {
		return callback, nil, errEpayCallbackRejected
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(params["money"]))
	if err != nil || amount.LessThanOrEqual(decimal.Zero) || !amount.Equal(amount.Round(2)) {
		return callback, nil, errEpayCallbackRejected
	}

	verifiedKey := strings.TrimSpace(paymentConfig.EpayKey)
	usedPreviousKey := false
	verifyInfo, err := verifyEpayWithKeyConfig(params, verifiedKey, paymentConfig)
	if (err != nil || verifyInfo == nil || !verifyInfo.VerifyStatus) && previousEpayKeyAvailableFor(paymentConfig.EpayKey) {
		// A previous key is an explicit, short-lived rotation bridge. Verify
		// against a fresh map copy because go-epay's Verify mutates sign and
		// sign_type while calculating its digest.
		verifiedKey = strings.TrimSpace(os.Getenv("EPAY_PREVIOUS_KEY"))
		usedPreviousKey = true
		verifyInfo, err = verifyEpayWithKeyConfig(params, verifiedKey, paymentConfig)
	}
	if err != nil || verifyInfo == nil || !verifyInfo.VerifyStatus {
		return callback, verifyInfo, errEpayCallbackRejected
	}
	callback = model.EpayCallback{
		ServiceTradeNo:  strings.TrimSpace(verifyInfo.ServiceTradeNo),
		ProviderTradeNo: strings.TrimSpace(verifyInfo.TradeNo),
		PaymentMethod:   strings.TrimSpace(verifyInfo.Type),
		Name:            strings.TrimSpace(verifyInfo.Name),
		Money:           strings.TrimSpace(verifyInfo.Money),
		MerchantID:      strings.TrimSpace(params["pid"]),
		TradeStatus:     strings.TrimSpace(verifyInfo.TradeStatus),
		// VerifyRes excludes the signature and merchant fields from its JSON
		// representation; retaining it is sufficient for audit without putting
		// the secret-bearing callback URL into the database.
		RawPayload:               common.GetJsonString(verifyInfo),
		SignatureKeyFingerprint:  model.EpayKeyFingerprint(verifiedKey),
		SignatureUsedPreviousKey: usedPreviousKey,
	}
	if err := callback.ValidateForController(); err != nil {
		return model.EpayCallback{}, verifyInfo, fmt.Errorf("%w: %v", errEpayCallbackRejected, err)
	}
	return callback, verifyInfo, nil
}

// previousEpayKeyAvailable intentionally requires an explicit Unix deadline.
// A rotation bridge without an expiry would silently become a second
// permanent credential and defeat key rotation.
func previousEpayKeyAvailable() bool {
	return previousEpayKeyAvailableFor(operation_setting.GetPaymentRuntimeConfig().EpayKey)
}

func previousEpayKeyAvailableFor(currentKey string) bool {
	previous := strings.TrimSpace(os.Getenv("EPAY_PREVIOUS_KEY"))
	deadline, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("EPAY_PREVIOUS_KEY_UNTIL")), 10, 64)
	return previous != "" && previous != strings.TrimSpace(currentKey) && err == nil && deadline > time.Now().Unix()
}

func verifyEpayWithKey(params map[string]string, key string) (*epay.VerifyRes, error) {
	paymentConfig := operation_setting.GetPaymentRuntimeConfig()
	return verifyEpayWithKeyConfig(params, key, paymentConfig)
}

func verifyEpayWithKeyConfig(params map[string]string, key string, paymentConfig operation_setting.PaymentRuntimeConfig) (*epay.VerifyRes, error) {
	client, err := epay.NewClient(&epay.Config{
		PartnerID: paymentConfig.EpayID,
		Key:       key,
	}, paymentConfig.PayAddress)
	if err != nil {
		return nil, err
	}
	paramsCopy := make(map[string]string, len(params))
	for name, value := range params {
		paramsCopy[name] = value
	}
	return client.Verify(paramsCopy)
}

func writeEpayFailure(c *gin.Context) {
	if _, err := c.Writer.Write([]byte("fail")); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 webhook 响应写入失败 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), err.Error()))
	}
}
