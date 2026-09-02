package operation_setting

import (
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

type PaymentSetting struct {
	AmountOptions  []int           `json:"amount_options"`
	AmountDiscount map[int]float64 `json:"amount_discount"` // 充值金额对应的折扣，例如 100 元 0.9 表示 100 元充值享受 9 折优惠

	ComplianceConfirmed    bool   `json:"compliance_confirmed"`
	ComplianceTermsVersion string `json:"compliance_terms_version"`
	ComplianceConfirmedAt  int64  `json:"compliance_confirmed_at"`
	ComplianceConfirmedBy  int    `json:"compliance_confirmed_by"`
	ComplianceConfirmedIP  string `json:"compliance_confirmed_ip"`
}

const CurrentComplianceTermsVersion = "v1"

// 默认配置
var paymentSetting = PaymentSetting{
	AmountOptions:  []int{10, 20, 50, 100, 200, 500},
	AmountDiscount: map[int]float64{},
}

// paymentSettingState is copy-on-write.  Readers can retain the returned
// pointer for the lifetime of a request while an administrator publishes a
// replacement; the old map/slice backing storage is never modified by the
// updater.  This is important because returning a pointer to a live struct
// would let reflection-based hot reloads race with map/slice reads.
var paymentSettingState atomic.Pointer[PaymentSetting]
var paymentSettingUpdateMu sync.Mutex

// paymentSettingFields is deliberately a distinct type with no methods. It
// lets the config package reuse its reflection parser against a private copy
// without recursively dispatching back to PaymentSetting.UpdateConfigMap.
type paymentSettingFields PaymentSetting

func init() {
	initial := clonePaymentSetting(&paymentSetting)
	paymentSettingState.Store(&initial)
	// 注册到全局配置管理器
	config.GlobalConfig.Register("payment_setting", &paymentSetting)
}

func clonePaymentSetting(source *PaymentSetting) PaymentSetting {
	if source == nil {
		return PaymentSetting{}
	}
	clone := *source
	if source.AmountOptions != nil {
		clone.AmountOptions = append([]int(nil), source.AmountOptions...)
	}
	if source.AmountDiscount != nil {
		clone.AmountDiscount = make(map[int]float64, len(source.AmountDiscount))
		for amount, discount := range source.AmountDiscount {
			clone.AmountDiscount[amount] = discount
		}
	}
	return clone
}

// currentPaymentSetting returns the immutable object currently published.
// It intentionally does not take OptionMapRWMutex: ConfigManager exports
// registered settings while that lock is held during bootstrap.
func currentPaymentSetting() *PaymentSetting {
	if current := paymentSettingState.Load(); current != nil {
		return current
	}
	initial := clonePaymentSetting(&paymentSetting)
	if paymentSettingState.CompareAndSwap(nil, &initial) {
		return &initial
	}
	return paymentSettingState.Load()
}

// GetPaymentSetting returns the currently published snapshot pointer. Treat
// the pointed-to value as read-only; use UpdatePaymentSetting to change it.
// The pointer remains valid after a hot update because publication replaces
// the whole object instead of mutating it in place.
func GetPaymentSetting() *PaymentSetting {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return currentPaymentSetting()
}

// GetPaymentSettingSnapshot returns a detached deep copy suitable for request
// handlers. In particular, callers cannot mutate the published discount map
// or amount slice accidentally.
func GetPaymentSettingSnapshot() PaymentSetting {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return clonePaymentSetting(currentPaymentSetting())
}

// UpdatePaymentSetting atomically publishes all fields changed by update.
// The callback receives a private deep copy, so a partially changed map/slice
// is never visible to readers.
func UpdatePaymentSetting(update func(*PaymentSetting)) {
	if update == nil {
		return
	}
	paymentSettingUpdateMu.Lock()
	defer paymentSettingUpdateMu.Unlock()
	next := clonePaymentSetting(currentPaymentSetting())
	update(&next)
	// Clone once more in case the callback installed caller-owned map/slice
	// values; published backing storage must remain immutable.
	published := clonePaymentSetting(&next)
	paymentSettingState.Store(&published)
}

// ConfigSnapshot implements config.ConfigSnapshotProvider. It must not take
// OptionMapRWMutex because ConfigManager.ExportAllConfigs can invoke it while
// holding that lock.
func (p *PaymentSetting) ConfigSnapshot() interface{} {
	return clonePaymentSetting(currentPaymentSetting())
}

// ValidateConfigMap implements config.ConfigMapUpdater and parses into an
// isolated alias so malformed persisted values cannot touch live state.
func (p *PaymentSetting) ValidateConfigMap(values map[string]string) error {
	staged := paymentSettingFields(clonePaymentSetting(currentPaymentSetting()))
	return config.ValidateConfigFromMap(&staged, values)
}

// UpdateConfigMap implements config.ConfigMapUpdater. Parsing happens before
// the copy-on-write publication, making a multi-field update all-or-nothing.
func (p *PaymentSetting) UpdateConfigMap(values map[string]string) error {
	paymentSettingUpdateMu.Lock()
	defer paymentSettingUpdateMu.Unlock()
	staged := paymentSettingFields(clonePaymentSetting(currentPaymentSetting()))
	if err := config.UpdateConfigFromMap(&staged, values); err != nil {
		return err
	}
	published := clonePaymentSetting((*PaymentSetting)(&staged))
	paymentSettingState.Store(&published)
	return nil
}

func IsPaymentComplianceConfirmed() bool {
	current := currentPaymentSetting()
	return current.ComplianceConfirmed && current.ComplianceTermsVersion == CurrentComplianceTermsVersion
}
