/**
此文件为旧版支付设置文件，如需增加新的参数、变量等，请在 payment_setting.go 中添加
This file is the old version of the payment settings file. If you need to add new parameters, variables, etc., please add them in payment_setting.go
*/

package operation_setting

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
)

var PayAddress = ""
var CustomCallbackAddress = ""
var EpayId = ""
var EpayKey = ""
var Price = 7.3
var MinTopUp = 1
var USDExchangeRate = 7.3

var PayMethods = []map[string]string{
	{
		"name": "支付宝",
		"icon": "SiAlipay",
		"type": "alipay",
	},
	{
		"name": "微信",
		"icon": "SiWechat",
		"type": "wxpay",
	},
	{
		"name":      "自定义1",
		"icon":      "LuCreditCard",
		"type":      "custom1",
		"min_topup": "50",
	},
}

// PaymentRuntimeConfig groups the legacy EPay/top-up settings that are
// consumed together while creating or verifying an order.  The exported
// scalar variables below remain for source compatibility; production code
// should take a copy with GetPaymentRuntimeConfig and use that copy for the
// whole request.
type PaymentRuntimeConfig struct {
	PayAddress            string
	CustomCallbackAddress string
	EpayID                string
	EpayKey               string
	Price                 float64
	MinTopUp              int
	USDExchangeRate       float64
	PayMethods            []map[string]string
}

var paymentRuntimeConfigMu sync.RWMutex

func clonePayMethods(methods []map[string]string) []map[string]string {
	if methods == nil {
		return nil
	}
	clone := make([]map[string]string, len(methods))
	for i, method := range methods {
		if method == nil {
			continue
		}
		clone[i] = make(map[string]string, len(method))
		for key, value := range method {
			clone[i][key] = value
		}
	}
	return clone
}

func paymentRuntimeConfigFromLegacyLocked() PaymentRuntimeConfig {
	return PaymentRuntimeConfig{
		PayAddress:            PayAddress,
		CustomCallbackAddress: CustomCallbackAddress,
		EpayID:                EpayId,
		EpayKey:               EpayKey,
		Price:                 Price,
		MinTopUp:              MinTopUp,
		USDExchangeRate:       USDExchangeRate,
		PayMethods:            clonePayMethods(PayMethods),
	}
}

func applyPaymentRuntimeConfigToLegacyLocked(cfg PaymentRuntimeConfig) {
	PayAddress = cfg.PayAddress
	CustomCallbackAddress = cfg.CustomCallbackAddress
	EpayId = cfg.EpayID
	EpayKey = cfg.EpayKey
	Price = cfg.Price
	MinTopUp = cfg.MinTopUp
	USDExchangeRate = cfg.USDExchangeRate
	PayMethods = clonePayMethods(cfg.PayMethods)
}

// GetPaymentRuntimeConfig returns a detached, request-safe copy. The option
// publication fence is acquired before the package lock, matching the lock
// order used by the other payment configuration snapshots.
func GetPaymentRuntimeConfig() PaymentRuntimeConfig {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	paymentRuntimeConfigMu.RLock()
	defer paymentRuntimeConfigMu.RUnlock()
	return paymentRuntimeConfigFromLegacyLocked()
}

// paymentRuntimeConfigSnapshotWithoutOptionLock is used while bootstrapping
// OptionMap itself (which already holds the writer lock).
func paymentRuntimeConfigSnapshotWithoutOptionLock() PaymentRuntimeConfig {
	paymentRuntimeConfigMu.RLock()
	defer paymentRuntimeConfigMu.RUnlock()
	return paymentRuntimeConfigFromLegacyLocked()
}

// UpdatePaymentRuntimeConfig atomically publishes related legacy payment
// fields. The callback mutates a private copy and may replace PayMethods;
// nested maps are copied before publication.
func UpdatePaymentRuntimeConfig(update func(*PaymentRuntimeConfig)) {
	if update == nil {
		return
	}
	paymentRuntimeConfigMu.Lock()
	defer paymentRuntimeConfigMu.Unlock()
	next := paymentRuntimeConfigFromLegacyLocked()
	update(&next)
	applyPaymentRuntimeConfigToLegacyLocked(next)
}

func UpdatePayMethodsByJsonString(jsonString string) error {
	var decoded []map[string]string
	if err := common.Unmarshal([]byte(jsonString), &decoded); err != nil {
		return err
	}
	if decoded == nil {
		decoded = make([]map[string]string, 0)
	}
	UpdatePaymentRuntimeConfig(func(cfg *PaymentRuntimeConfig) {
		cfg.PayMethods = decoded
	})
	return nil
}

func PayMethods2JsonString() string {
	// This helper is also called by InitOptionMap while OptionMapRWMutex is
	// already held for writing, so use the internal package-lock snapshot.
	methods := paymentRuntimeConfigSnapshotWithoutOptionLock().PayMethods
	jsonBytes, err := common.Marshal(methods)
	if err != nil {
		return "[]"
	}
	return string(jsonBytes)
}

func ContainsPayMethod(method string) bool {
	for _, payMethod := range GetPaymentRuntimeConfig().PayMethods {
		if payMethod["type"] == method {
			return true
		}
	}
	return false
}
