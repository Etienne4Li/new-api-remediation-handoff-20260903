package operation_setting

import (
	"github.com/QuantumNous/new-api/setting/config"

	"github.com/shopspring/decimal"
)

type PaymentSetting struct {
	AmountOptions  []int           `json:"amount_options"`
	AmountDiscount map[int]float64 `json:"amount_discount"` // 充值金额对应的折扣，例如 100 元 0.9 表示 100 元充值享受 9 折优惠

	// TopupBonusEnabled/TopupBonus drive the "top up N, get extra quota"
	// promotion. It is deliberately orthogonal to AmountDiscount: a discount
	// lowers the price the user pays, a bonus leaves the price alone and
	// credits extra quota on top. Both may be configured at the same time, in
	// which case the discount is applied to the payable amount first and the
	// bonus is then derived from the quota that actually gets credited.
	TopupBonusEnabled bool            `json:"topup_bonus_enabled"`
	TopupBonus        map[int]float64 `json:"topup_bonus"` // 充值门槛对应的赠送比例，例如 100 -> 0.01 表示充值满 100 额外赠送 1%

	ComplianceConfirmed    bool   `json:"compliance_confirmed"`
	ComplianceTermsVersion string `json:"compliance_terms_version"`
	ComplianceConfirmedAt  int64  `json:"compliance_confirmed_at"`
	ComplianceConfirmedBy  int    `json:"compliance_confirmed_by"`
	ComplianceConfirmedIP  string `json:"compliance_confirmed_ip"`
}

const CurrentComplianceTermsVersion = "v1"

// maxTopupBonusRatio caps one tier's bonus at 100% of the top-up.
//
// The map is hand-edited by an administrator and the value is a fraction, not
// a percentage. Reading a fat-fingered "2" as 200% would hand out two extra
// top-ups for free, so anything above the cap is treated as a misconfiguration
// and the tier is dropped rather than clamped: granting nothing is the safe
// failure, granting 100% is not.
const maxTopupBonusRatio = 1.0

// 默认配置
var paymentSetting = PaymentSetting{
	AmountOptions:  []int{10, 20, 50, 100, 200, 500},
	AmountDiscount: map[int]float64{},
	// Off by default. The promotion costs real quota, so it only starts once an
	// administrator turns it on and configures the tiers.
	TopupBonusEnabled: false,
	TopupBonus:        map[int]float64{},
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("payment_setting", &paymentSetting)
}

func GetPaymentSetting() *PaymentSetting {
	return &paymentSetting
}

func IsPaymentComplianceConfirmed() bool {
	return paymentSetting.ComplianceConfirmed &&
		paymentSetting.ComplianceTermsVersion == CurrentComplianceTermsVersion
}

// TopupBonusTiers returns the usable bonus tiers: threshold (in the same amount
// unit the recharge form asks for) -> ratio of that amount granted as extra
// quota. Non-positive thresholds, non-positive ratios and ratios above
// maxTopupBonusRatio are dropped.
//
// It returns nil when the promotion is switched off or nothing usable is
// configured, so "disabled" and "empty" are a single case for every caller —
// the settlement path, the top-up info endpoint and the wallet UI all key off
// the same emptiness check and therefore cannot disagree about whether the
// promotion is running.
func TopupBonusTiers() map[int]float64 {
	if !paymentSetting.TopupBonusEnabled || len(paymentSetting.TopupBonus) == 0 {
		return nil
	}
	tiers := make(map[int]float64, len(paymentSetting.TopupBonus))
	for threshold, ratio := range paymentSetting.TopupBonus {
		if threshold <= 0 || ratio <= 0 || ratio > maxTopupBonusRatio {
			continue
		}
		tiers[threshold] = ratio
	}
	if len(tiers) == 0 {
		return nil
	}
	return tiers
}

// TopupBonusRatio returns the ratio that applies to a top-up of the given
// amount: the one configured for the highest threshold the amount reaches.
// Thresholds are inclusive, so exactly 100 already earns the 100 tier, and an
// amount that clears no threshold earns zero.
func TopupBonusRatio(amount decimal.Decimal) decimal.Decimal {
	tiers := TopupBonusTiers()
	if len(tiers) == 0 || amount.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}

	// Every surviving threshold is positive, so 0 is a safe "nothing matched"
	// sentinel for the running maximum.
	bestThreshold := 0
	bestRatio := 0.0
	for threshold, ratio := range tiers {
		if amount.LessThan(decimal.NewFromInt(int64(threshold))) {
			continue
		}
		if threshold > bestThreshold {
			bestThreshold = threshold
			bestRatio = ratio
		}
	}
	if bestThreshold == 0 {
		return decimal.Zero
	}
	return decimal.NewFromFloat(bestRatio)
}
