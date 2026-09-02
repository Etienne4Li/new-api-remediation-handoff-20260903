package model

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type TopUp struct {
	Id              int     `json:"id"`
	UserId          int     `json:"user_id" gorm:"index"`
	Amount          int64   `json:"amount"`
	Money           float64 `json:"money"`
	TradeNo         string  `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string  `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	CreateTime      int64   `json:"create_time"`
	CompleteTime    int64   `json:"complete_time"`
	Status          string  `json:"status"`
	// SettlementResolution records the operator action required after a
	// provider payment was accepted but wallet crediting was not possible.
	// Keeping it separate from Status preserves the immutable payment fact.
	SettlementResolution string `json:"settlement_resolution" gorm:"type:varchar(32);default:'';index:idx_top_ups_settlement_resolution"`
	// SettlementErrorCode is a stable, queryable reason for paid_uncredited
	// orders. ProviderPayload retains only a secret-backed audit digest.
	SettlementErrorCode string `json:"settlement_error_code" gorm:"type:varchar(64);default:''"`

	// CreditedQuota and the provider fields are immutable checkout snapshots.
	// They are deliberately hidden from API responses; settlement must never
	// recompute a credit from mutable pricing settings or trust callback data.
	CreditedQuota      int     `json:"-" gorm:"type:bigint;default:0"`
	ProviderTradeNo    *string `json:"-" gorm:"type:varchar(255);index:idx_top_ups_provider_trade_no,length:191"`
	ProviderMerchantID string  `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderOrderName  string  `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderAmount     string  `json:"-" gorm:"type:varchar(64);default:''"`
	ProviderProductID  string  `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderStoreID    string  `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderCheckoutID string  `json:"-" gorm:"type:varchar(255);default:''"`
	// ProviderSubscriptionID identifies the recurring subscription created by
	// a provider checkout. It is optional for one-time payments and for legacy
	// callbacks that do not echo the subscription object.
	ProviderSubscriptionID string `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderCurrency       string `json:"-" gorm:"type:varchar(16);default:''"`
	ProviderEventID        string `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderKeyFingerprint string `json:"-" gorm:"type:varchar(64);default:''"`
	ProviderPayload        string `json:"-" gorm:"type:text"`
}

const (
	PaymentMethodStripe       = "stripe"
	PaymentMethodCreem        = "creem"
	PaymentMethodWaffo        = "waffo"
	PaymentMethodWaffoPancake = "waffo_pancake"
	PaymentMethodBalance      = "balance"
)

// TopUpStatusPaidUncredited is kept in the model package as well as common so
// callers that already depend on model-level payment constants do not need to
// import the common package merely to inspect an order state.
const TopUpStatusPaidUncredited = common.TopUpStatusPaidUncredited

const (
	TopUpSettlementResolutionRefundRequired = "refund_required"
	TopUpSettlementResolutionCreditRequired = "credit_required"
	// TopUpSettlementResolutionCreditPending is written in the short window
	// after provider evidence is committed and before the first wallet-credit
	// attempt runs.  It is deliberately distinct from credit_required: the
	// latter records a deterministic business failure (for example a full
	// wallet), while credit_pending means the process may have crashed before
	// it could classify the outcome.  Both states are retryable.
	TopUpSettlementResolutionCreditPending = "credit_pending"
	TopUpSettlementErrorWalletQuota        = "wallet_quota_limit_exceeded"
	TopUpSettlementErrorUserNotFound       = "user_not_found"
)

// Compatibility aliases for integrations that use the shorter resolution
// names. They intentionally resolve to the same persisted value.
const SettlementResolutionRefundRequired = TopUpSettlementResolutionRefundRequired

const (
	PaymentProviderEpay         = "epay"
	PaymentProviderStripe       = "stripe"
	PaymentProviderCreem        = "creem"
	PaymentProviderWaffo        = "waffo"
	PaymentProviderWaffoPancake = "waffo_pancake"
	PaymentProviderBalance      = "balance"
)

var (
	ErrPaymentMethodMismatch    = errors.New("payment method mismatch")
	ErrTopUpNotFound            = errors.New("topup not found")
	ErrTopUpUserNotFound        = errors.New("topup user not found")
	ErrTopUpStatusInvalid       = errors.New("topup status invalid")
	ErrInvalidTopUpQuota        = errors.New("invalid top-up quota")
	ErrTopUpQuotaLimitExceeded  = errors.New("top-up quota limit exceeded")
	ErrWalletQuotaLimitExceeded = errors.New("wallet quota limit exceeded")
	// ErrTopUpSettlementResolutionInvalid prevents a credit retry from
	// overriding an operator/provider resolution such as refund_required or a
	// future manual_reconciliation state.  Resolution changes and retries must
	// serialize on the same order row lock.
	ErrTopUpSettlementResolutionInvalid = errors.New("top-up settlement resolution is not creditable")
	ErrEpayCallbackInvalid              = errors.New("invalid epay callback")
	ErrEpayCallbackMismatch             = errors.New("epay callback does not match order")
	ErrEpayProviderTradeConflict        = errors.New("epay provider trade number conflict")
	// ErrProviderCheckoutConflict means an order already has a different
	// provider checkout identifier. Checkout identity is immutable once it has
	// been observed, so callers must not overwrite it with a stale value.
	ErrProviderCheckoutConflict = errors.New("provider checkout id conflict")
	// ErrEpayOrderSnapshotMissing is returned for legacy orders that predate
	// the immutable provider/credit snapshot. Such rows must be reconciled
	// against the provider before an HTTP callback can grant quota.
	ErrEpayOrderSnapshotMissing = errors.New("epay order snapshot missing")
)

// EpayCallback is the allow-listed, signature-verified subset of an EPay
// callback that is allowed to reach settlement. Controllers must construct it
// only after verifying the raw request and merchant id.
type EpayCallback struct {
	ServiceTradeNo           string
	ProviderTradeNo          string
	PaymentMethod            string
	Name                     string
	Money                    string
	MerchantID               string
	TradeStatus              string
	RawPayload               string
	SignatureKeyFingerprint  string
	SignatureUsedPreviousKey bool
}

func (callback EpayCallback) validate() error {
	if strings.TrimSpace(callback.ServiceTradeNo) == "" ||
		strings.TrimSpace(callback.ProviderTradeNo) == "" ||
		strings.TrimSpace(callback.PaymentMethod) == "" ||
		strings.TrimSpace(callback.Name) == "" ||
		strings.TrimSpace(callback.Money) == "" ||
		strings.TrimSpace(callback.MerchantID) == "" ||
		strings.TrimSpace(callback.TradeStatus) == "" ||
		strings.TrimSpace(callback.SignatureKeyFingerprint) == "" {
		return ErrEpayCallbackInvalid
	}
	return nil
}

// EpayKeyFingerprint identifies the signing key used for an order without
// persisting the key itself. It lets a short-lived previous-key verifier accept
// only orders that were actually created while that key was current.
func EpayKeyFingerprint(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return fmt.Sprintf("%x", common.Sha256Raw([]byte("epay-signing-key-v1\x00"+key)))
}

func epayLegacySuccessAckAllowed(tradeNo string) bool {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return false
	}
	for _, allowed := range strings.Split(os.Getenv("EPAY_LEGACY_SUCCESS_ACK_TRADE_NOS"), ",") {
		if strings.TrimSpace(allowed) == tradeNo {
			return true
		}
	}
	return false
}

// ValidateForController exposes the common required-field validation to the
// HTTP adapter while keeping settlement-specific checks in this package.
func (callback EpayCallback) ValidateForController() error {
	return callback.validate()
}

func (topUp *TopUp) Insert() error {
	var err error
	err = DB.Create(topUp).Error
	return err
}

func topUpQuotaMaxCurrent(creditedQuota int) (int, error) {
	if creditedQuota <= 0 || creditedQuota > common.MaxWalletQuota {
		return 0, ErrInvalidTopUpQuota
	}
	return common.MaxWalletQuota - creditedQuota, nil
}

// topUpHasSettlementSnapshot reports whether any of the immutable checkout
// fields has been populated. A row with no CreditedQuota but only part of a
// snapshot is not a legacy row: falling back to mutable Amount/Money in that
// case could grant a different entitlement than the checkout represented.
func topUpHasSettlementSnapshot(topUp *TopUp) bool {
	if topUp == nil {
		return true
	}
	if topUp.CreditedQuota != 0 {
		return true
	}
	if topUp.ProviderTradeNo != nil && strings.TrimSpace(*topUp.ProviderTradeNo) != "" {
		return true
	}
	for _, value := range []string{
		topUp.ProviderMerchantID,
		topUp.ProviderOrderName,
		topUp.ProviderAmount,
		topUp.ProviderProductID,
		topUp.ProviderStoreID,
		topUp.ProviderCheckoutID,
		topUp.ProviderSubscriptionID,
		topUp.ProviderCurrency,
		topUp.ProviderEventID,
		topUp.ProviderKeyFingerprint,
		topUp.ProviderPayload,
	} {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// topUpQuotaForSettlement resolves the entitlement for compatibility
// settlement paths. New checkouts persist CreditedQuota and must use it
// verbatim. Historical rows are allowed to use their provider-specific
// formula only when every snapshot field is empty; all derived values still
// pass the wallet-safe strict conversion and upper-bound checks.
func topUpQuotaForSettlement(topUp *TopUp, provider string) (int, error) {
	if topUp == nil {
		return 0, ErrTopUpNotFound
	}
	if strings.TrimSpace(topUp.PaymentProvider) != strings.TrimSpace(provider) {
		return 0, ErrPaymentMethodMismatch
	}

	if topUp.CreditedQuota > 0 {
		if topUp.CreditedQuota > common.MaxWalletQuota {
			return 0, ErrInvalidTopUpQuota
		}
		return topUp.CreditedQuota, nil
	}
	if topUpHasSettlementSnapshot(topUp) {
		return 0, ErrProviderSnapshotMissing
	}

	var derived decimal.Decimal
	switch provider {
	case PaymentProviderStripe:
		// Legacy Stripe rows store the charged major-unit amount in Money.
		derived = decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.GetQuotaPerUnit()))
	case PaymentProviderCreem:
		// Legacy Creem rows store the product quota directly in Amount.
		derived = decimal.NewFromInt(topUp.Amount)
	case PaymentProviderEpay, PaymentProviderWaffo, PaymentProviderWaffoPancake:
		// Legacy Waffo rows store a display-unit amount in Amount.
		derived = decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.GetQuotaPerUnit()))
	default:
		return 0, ErrPaymentMethodMismatch
	}

	quota, err := common.WalletQuotaFromDecimalStrict(derived)
	if err != nil || quota <= 0 || quota > common.MaxWalletQuota {
		return 0, ErrInvalidTopUpQuota
	}
	return quota, nil
}

// ValidateTopUpQuotaCapacity performs the user-facing pre-payment check. The
// settlement path repeats the same invariant with an atomic conditional
// update, because the wallet balance can change after checkout creation.
func ValidateTopUpQuotaCapacity(userId int, creditedQuota int) error {
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return err
	}

	var user User
	if err := DB.Select("quota").Where("id = ?", userId).First(&user).Error; err != nil {
		return err
	}
	if user.Quota > maxCurrentQuota {
		return ErrTopUpQuotaLimitExceeded
	}
	return nil
}

// creditTopUpQuota atomically enforces the wallet ceiling while adding quota.
// Keeping the predicate and increment in one UPDATE prevents two
// concurrent callbacks from both passing a separate read/check.
func creditTopUpQuota(tx *gorm.DB, userId int, creditedQuota int, updates map[string]interface{}) error {
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return err
	}

	updateFields := make(map[string]interface{}, len(updates)+1)
	for key, value := range updates {
		updateFields[key] = value
	}
	updateFields["quota"] = gorm.Expr("quota + ?", creditedQuota)

	result := tx.Model(&User{}).
		Where("id = ? AND quota <= ?", userId, maxCurrentQuota).
		Updates(updateFields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		_, err := stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityUser, userId, getUserCacheKey(userId))
		return err
	}

	var count int64
	if err := tx.Model(&User{}).Where("id = ?", userId).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return ErrTopUpQuotaLimitExceeded
}

func isTopUpPaidUncredited(topUp *TopUp) bool {
	return topUp != nil && strings.EqualFold(strings.TrimSpace(topUp.Status), TopUpStatusPaidUncredited)
}

// topUpCreditFailureCode identifies failures that are safe to turn into a
// durable paid_uncredited state. Only deterministic business outcomes belong
// here; driver/transaction errors must bubble up so the provider retries.
func topUpCreditFailureCode(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrTopUpQuotaLimitExceeded):
		return TopUpSettlementErrorWalletQuota, true
	case errors.Is(err, gorm.ErrRecordNotFound):
		return TopUpSettlementErrorUserNotFound, true
	default:
		return "", false
	}
}

func markTopUpPaidUncredited(topUp *TopUp, errorCode string) {
	if topUp == nil {
		return
	}
	topUp.Status = TopUpStatusPaidUncredited
	if strings.TrimSpace(errorCode) == TopUpSettlementErrorUserNotFound {
		topUp.SettlementResolution = TopUpSettlementResolutionRefundRequired
	} else if strings.TrimSpace(errorCode) == "" {
		// Evidence has been committed, but no wallet attempt has completed yet.
		// Keep this explicit so reconciliation can distinguish a crash window
		// from a classified quota/user failure.
		topUp.SettlementResolution = TopUpSettlementResolutionCreditPending
	} else {
		topUp.SettlementResolution = TopUpSettlementResolutionCreditRequired
	}
	topUp.SettlementErrorCode = strings.TrimSpace(errorCode)
	topUp.CompleteTime = common.GetTimestamp()
}

// settleTopUpCreditTx performs the wallet-side part of a payment settlement
// while the order transaction is still open. The user row is locked before the
// ceiling check, so a concurrent debit/credit cannot turn a deterministic
// quota-limit result into a false success. It returns a non-empty error code
// only for business outcomes that are safe to persist as paid_uncredited;
// database/driver failures are returned as errors and roll the whole payment
// transaction back.
func settleTopUpCreditTx(tx *gorm.DB, topUp *TopUp, updates map[string]interface{}) (string, error) {
	if topUp == nil {
		return "", ErrTopUpNotFound
	}
	return settleTopUpCreditWithQuotaTx(tx, topUp, topUp.CreditedQuota, updates)
}

func settleTopUpCreditWithQuotaTx(tx *gorm.DB, topUp *TopUp, creditedQuota int, updates map[string]interface{}) (string, error) {
	if tx == nil || topUp == nil {
		return "", ErrTopUpNotFound
	}
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return "", err
	}
	var user User
	if err := lockForUpdate(tx).Select("id", "quota").Where("id = ?", topUp.UserId).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TopUpSettlementErrorUserNotFound, nil
		}
		return "", err
	}
	if user.Quota > maxCurrentQuota {
		return TopUpSettlementErrorWalletQuota, nil
	}
	if err := creditTopUpQuota(tx, topUp.UserId, creditedQuota, updates); err != nil {
		if code, ok := topUpCreditFailureCode(err); ok {
			return code, nil
		}
		return "", err
	}
	return "", nil
}

func clearTopUpSettlementResolution(topUp *TopUp) {
	if topUp == nil {
		return
	}
	topUp.SettlementResolution = ""
	topUp.SettlementErrorCode = ""
}

type topUpCreditAttempt struct {
	AlreadyCompleted bool
	Credited         bool
	PendingCode      string
	UserID           int
	CreditedQuota    int
	Provider         string
	Money            float64
	PaymentMethod    string
}

func topUpCreditError(code string) error {
	switch strings.TrimSpace(code) {
	case TopUpSettlementErrorWalletQuota:
		return ErrTopUpQuotaLimitExceeded
	case TopUpSettlementErrorUserNotFound:
		return ErrTopUpUserNotFound
	default:
		return ErrTopUpStatusInvalid
	}
}

// retryPaidUncreditedTopUpWithUpdates retries only the wallet side of a
// provider payment that has already been durably authenticated. It never
// re-validates provider money and never changes a pending order into a paid
// order. The returned PendingCode means the order was saved in
// paid_uncredited and the caller may safely ACK the provider event.
func retryPaidUncreditedTopUpWithUpdates(tradeNo string, updates map[string]interface{}) (attempt topUpCreditAttempt, err error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return attempt, ErrTopUpNotFound
	}
	if DB == nil {
		return attempt, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		var topUp TopUp
		if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&topUp).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		attempt.UserID = topUp.UserId
		attempt.CreditedQuota = topUp.CreditedQuota
		attempt.Provider = topUp.PaymentProvider
		attempt.Money = topUp.Money
		attempt.PaymentMethod = topUp.PaymentMethod
		if topUp.Status == common.TopUpStatusSuccess {
			attempt.AlreadyCompleted = true
			return nil
		}
		if !isTopUpPaidUncredited(&topUp) {
			return ErrTopUpStatusInvalid
		}
		// A paid order can be left in several operator-resolution states.  Only
		// the two creditable states may enter the wallet mutation path.  Empty is
		// accepted for legacy rows created before SettlementResolution existed;
		// treating it as credit_pending lets an interrupted first attempt be
		// recovered without allowing an explicit refund/manual decision to be
		// overwritten.
		switch resolution := strings.ToLower(strings.TrimSpace(topUp.SettlementResolution)); resolution {
		case "", TopUpSettlementResolutionCreditPending, TopUpSettlementResolutionCreditRequired:
			// retryable credit states
		case TopUpSettlementResolutionRefundRequired:
			return ErrTopUpSettlementResolutionInvalid
		default:
			return ErrTopUpSettlementResolutionInvalid
		}
		if topUp.CreditedQuota <= 0 || topUp.CreditedQuota > common.MaxWalletQuota {
			return ErrInvalidTopUpQuota
		}
		code, err := settleTopUpCreditTx(tx, &topUp, updates)
		if err != nil {
			return err
		}
		if code != "" {
			attempt.PendingCode = code
			topUp.SettlementErrorCode = code
			if code == TopUpSettlementErrorUserNotFound {
				topUp.SettlementResolution = TopUpSettlementResolutionRefundRequired
			} else {
				topUp.SettlementResolution = TopUpSettlementResolutionCreditRequired
			}
			if err := tx.Save(&topUp).Error; err != nil {
				return err
			}
			return nil
		}
		topUp.Status = common.TopUpStatusSuccess
		clearTopUpSettlementResolution(&topUp)
		if topUp.CompleteTime == 0 {
			topUp.CompleteTime = common.GetTimestamp()
		}
		if err := tx.Save(&topUp).Error; err != nil {
			return err
		}
		attempt.Credited = true
		return nil
	})
	if err != nil {
		return attempt, err
	}
	return attempt, nil
}

// RetryPaidUncreditedTopUp is the operator-facing retry primitive. A retry
// after success is idempotent; a still-full wallet leaves the durable payment
// state untouched and returns ErrTopUpQuotaLimitExceeded.
func RetryPaidUncreditedTopUp(tradeNo string) error {
	attempt, err := retryPaidUncreditedTopUpWithUpdates(tradeNo, nil)
	if err != nil {
		return err
	}
	if attempt.PendingCode != "" {
		return topUpCreditError(attempt.PendingCode)
	}
	if attempt.Credited {
		syncCreditUserQuotaCache(attempt.UserID, attempt.CreditedQuota, "paid_uncredited topup retry")
		RecordTopupLog(attempt.UserID, fmt.Sprintf("已付款充值补入账，充值额度: %v，支付金额：%f", logger.FormatQuota(attempt.CreditedQuota), attempt.Money), "", attempt.PaymentMethod, attempt.Provider)
	}
	return nil
}

func (topUp *TopUp) Update() error {
	var err error
	err = DB.Save(topUp).Error
	return err
}

// SetTopUpProviderCheckoutID records the checkout identifier without saving a
// caller's potentially stale order snapshot. A webhook may settle the order
// between checkout creation and this write; updating one column keeps that
// settlement state intact. The empty-or-same predicate also makes retries
// idempotent and prevents a second checkout from being silently rebound.
func SetTopUpProviderCheckoutID(tradeNo string, provider string, checkoutID string) error {
	tradeNo = strings.TrimSpace(tradeNo)
	provider = strings.TrimSpace(provider)
	checkoutID = strings.TrimSpace(checkoutID)
	if tradeNo == "" || provider == "" || checkoutID == "" {
		return ErrProviderSettlementInvalid
	}

	result := DB.Model(&TopUp{}).
		Where("trade_no = ? AND payment_provider = ?", tradeNo, provider).
		Where("provider_checkout_id IS NULL OR provider_checkout_id = '' OR provider_checkout_id = ?", checkoutID).
		Update("provider_checkout_id", checkoutID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}

	// Distinguish a missing/mismatched order from a checkout conflict. A
	// concurrent identical writer may have committed the same value while the
	// update reported no changed rows, which is an idempotent success.
	var current TopUp
	err := DB.Select("payment_provider, provider_checkout_id").Where("trade_no = ?", tradeNo).First(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrTopUpNotFound
	}
	if err != nil {
		return err
	}
	if current.PaymentProvider != provider {
		return ErrPaymentMethodMismatch
	}
	if strings.TrimSpace(current.ProviderCheckoutID) == checkoutID {
		return nil
	}
	return ErrProviderCheckoutConflict
}

func GetTopUpById(id int) *TopUp {
	var topUp *TopUp
	var err error
	err = DB.Where("id = ?", id).First(&topUp).Error
	if err != nil {
		return nil
	}
	return topUp
}

func GetTopUpByTradeNo(tradeNo string) *TopUp {
	var topUp *TopUp
	var err error
	err = DB.Where("trade_no = ?", tradeNo).First(&topUp).Error
	if err != nil {
		return nil
	}
	return topUp
}

// GetTopUpByTradeNoWithError is the error-preserving variant for webhook and
// settlement boundaries. The legacy getter above intentionally keeps its
// pointer-only API for callers that treat a missing row as nil, but using it
// at an externally-triggered payment boundary would collapse a transient
// database outage into "order not found" and incorrectly acknowledge a
// provider event.
func GetTopUpByTradeNoWithError(tradeNo string) (*TopUp, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return nil, ErrTopUpNotFound
	}
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	var topUp TopUp
	if err := DB.Where("trade_no = ?", tradeNo).First(&topUp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTopUpNotFound
		}
		return nil, err
	}
	return &topUp, nil
}

func UpdatePendingTopUpStatus(tradeNo string, expectedPaymentProvider string, targetStatus string) error {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			return err
		}
		if expectedPaymentProvider != "" && topUp.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}

		topUp.Status = targetStatus
		return tx.Save(topUp).Error
	})
}

// validateEpayOrderCallback checks the fields that must be bound to the
// immutable checkout snapshot. Older rows may not have the snapshot columns;
// in that case the amount/type checks still apply and the controller's
// merchant check remains mandatory.
func validateEpayOrderCallback(paymentMethod string, money float64, providerTradeNo *string, providerMerchantID string, providerOrderName string, providerAmount string, providerKeyFingerprint string, callback EpayCallback) error {
	if err := callback.validate(); err != nil {
		return err
	}
	if callback.PaymentMethod != paymentMethod {
		return ErrPaymentMethodMismatch
	}
	if providerTradeNo != nil && strings.TrimSpace(*providerTradeNo) != callback.ProviderTradeNo {
		return ErrEpayProviderTradeConflict
	}
	if providerMerchantID != "" && providerMerchantID != callback.MerchantID {
		return ErrEpayCallbackMismatch
	}
	if providerOrderName != "" && providerOrderName != callback.Name {
		return ErrEpayCallbackMismatch
	}
	if callback.SignatureUsedPreviousKey && strings.TrimSpace(providerKeyFingerprint) != callback.SignatureKeyFingerprint {
		return ErrEpayCallbackMismatch
	}
	actualMoney, err := decimal.NewFromString(strings.TrimSpace(callback.Money))
	if err != nil || actualMoney.LessThanOrEqual(decimal.Zero) {
		return ErrEpayCallbackInvalid
	}
	// EPay amounts are decimal currency values. Reject values with more than
	// two fractional digits instead of silently rounding an underpayment.
	if !actualMoney.Equal(actualMoney.Round(2)) {
		return ErrEpayCallbackMismatch
	}
	expectedMoney := decimal.NewFromFloat(money).Round(2)
	if providerAmount != "" {
		if expected, parseErr := decimal.NewFromString(strings.TrimSpace(providerAmount)); parseErr == nil {
			expectedMoney = expected.Round(2)
		} else {
			return ErrEpayCallbackMismatch
		}
	}
	if !actualMoney.Equal(expectedMoney) {
		return ErrEpayCallbackMismatch
	}
	return nil
}

// RechargeEpayVerified atomically settles an EPay callback after the
// controller has verified its signature and merchant id. Every provider
// transaction number is persisted and protected by a unique composite index,
// so one provider payment cannot be bound to two local orders.
func RechargeEpayVerified(callback EpayCallback, callerIp string) (alreadyDone bool, err error) {
	outcome, err := RechargeEpayVerifiedWithOutcome(callback, callerIp)
	return outcome != EpaySettlementCompleted, err
}

type EpaySettlementOutcome int

const (
	EpaySettlementCompleted EpaySettlementOutcome = iota
	EpaySettlementAlreadyCompleted
	EpaySettlementLegacySuccessAcknowledged
	// EpaySettlementPaidUncredited is shared by all subscription webhook
	// adapters (the historical enum name is retained for API compatibility).
	// The provider payment is durably recorded, but entitlement creation needs
	// operator retry or refund; callbacks must be acknowledged as handled.
	EpaySettlementPaidUncredited
)

// RechargeEpayVerifiedWithOutcome exposes the legacy acknowledgement outcome
// so the HTTP adapter can emit a high-priority operational warning instead of
// presenting it as an ordinary idempotent retry.
func RechargeEpayVerifiedWithOutcome(callback EpayCallback, callerIp string) (EpaySettlementOutcome, error) {
	return rechargeEpayVerified(callback, callerIp, false)
}

// rechargeEpayVerified contains the common settlement transaction. The
// allowLegacySnapshot flag is intentionally reachable only through the
// trusted internal compatibility wrapper below; HTTP callbacks always pass
// false and therefore fail closed for pre-snapshot orders.
func rechargeEpayVerified(callback EpayCallback, callerIp string, allowLegacySnapshot bool) (outcome EpaySettlementOutcome, err error) {
	if err := callback.validate(); err != nil {
		return EpaySettlementCompleted, err
	}
	if callback.TradeStatus != "TRADE_SUCCESS" {
		return EpaySettlementCompleted, ErrEpayCallbackInvalid
	}
	tradeNo := strings.TrimSpace(callback.ServiceTradeNo)
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var quotaToAdd int
	topUp := &TopUp{}
	evidenceCommitted := false
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTopUpNotFound
			}
			// Preserve driver/connectivity errors so webhook adapters can return a
			// retryable failure instead of acknowledging an unsettled payment.
			return err
		}
		if topUp.PaymentProvider != PaymentProviderEpay {
			return ErrPaymentMethodMismatch
		}
		missingSnapshot := topUp.CreditedQuota <= 0 ||
			strings.TrimSpace(topUp.ProviderMerchantID) == "" ||
			strings.TrimSpace(topUp.ProviderOrderName) == "" ||
			strings.TrimSpace(topUp.ProviderAmount) == "" ||
			strings.TrimSpace(topUp.ProviderKeyFingerprint) == ""
		if !allowLegacySnapshot && missingSnapshot {
			if topUp.Status == common.TopUpStatusSuccess && epayLegacySuccessAckAllowed(tradeNo) {
				if validationErr := validateEpayOrderCallback(topUp.PaymentMethod, topUp.Money, nil, "", "", "", "", callback); validationErr != nil {
					common.SysError(fmt.Sprintf("allowlisted legacy EPay success callback mismatch trade_no=%s provider_trade_no=%s error=%v", tradeNo, callback.ProviderTradeNo, validationErr))
				}
				outcome = EpaySettlementLegacySuccessAcknowledged
				return nil
			}
			return ErrEpayOrderSnapshotMissing
		}
		// The trusted compatibility seam may be called again after it has
		// completed a fully legacy row. The first call records the provider
		// transaction, which is settlement evidence but not a complete checkout
		// snapshot; recognize the exact same transaction as a read-only retry
		// before the partial-snapshot guard below.
		if allowLegacySnapshot && missingSnapshot && topUp.Status == common.TopUpStatusSuccess {
			if topUp.ProviderTradeNo == nil || strings.TrimSpace(*topUp.ProviderTradeNo) != callback.ProviderTradeNo {
				return ErrEpayProviderTradeConflict
			}
			if err := validateEpayOrderCallback(topUp.PaymentMethod, topUp.Money, topUp.ProviderTradeNo, topUp.ProviderMerchantID, topUp.ProviderOrderName, topUp.ProviderAmount, topUp.ProviderKeyFingerprint, callback); err != nil {
				return err
			}
			if err := bindPaymentEventTx(tx, PaymentProviderEpay, callback.ProviderTradeNo, tradeNo, PaymentEventOrderTopUp, callback.RawPayload); err != nil {
				return err
			}
			outcome = EpaySettlementAlreadyCompleted
			return nil
		}
		if allowLegacySnapshot && missingSnapshot && topUpHasSettlementSnapshot(topUp) {
			// The internal compatibility seam may settle a genuinely old row,
			// but must not turn a partially-written modern snapshot into a
			// mutable-pricing fallback.
			return ErrEpayOrderSnapshotMissing
		}
		if err := validateEpayOrderCallback(topUp.PaymentMethod, topUp.Money, topUp.ProviderTradeNo, topUp.ProviderMerchantID, topUp.ProviderOrderName, topUp.ProviderAmount, topUp.ProviderKeyFingerprint, callback); err != nil {
			return err
		}
		if topUp.Status == common.TopUpStatusSuccess {
			// A completed order must already carry the provider transaction that
			// granted it. Do not let a later validly signed transaction be treated
			// as an idempotent duplicate merely because the local status is success.
			if topUp.ProviderTradeNo == nil || strings.TrimSpace(*topUp.ProviderTradeNo) != callback.ProviderTradeNo {
				return ErrEpayProviderTradeConflict
			}
			if err := bindPaymentEventTx(tx, PaymentProviderEpay, callback.ProviderTradeNo, tradeNo, PaymentEventOrderTopUp, callback.RawPayload); err != nil {
				return err
			}
			outcome = EpaySettlementAlreadyCompleted
			return nil
		}
		if isTopUpPaidUncredited(topUp) {
			if topUp.ProviderTradeNo == nil || strings.TrimSpace(*topUp.ProviderTradeNo) != callback.ProviderTradeNo {
				return ErrEpayProviderTradeConflict
			}
			if err := bindPaymentEventTx(tx, PaymentProviderEpay, callback.ProviderTradeNo, tradeNo, PaymentEventOrderTopUp, callback.RawPayload); err != nil {
				return err
			}
			outcome = EpaySettlementPaidUncredited
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}

		// The trusted compatibility seam is intentionally the only path that can
		// complete a fully legacy row from mutable historical fields. HTTP
		// callbacks always have an immutable snapshot and use the evidence-first
		// state machine below.
		if allowLegacySnapshot && missingSnapshot {
			if err := bindPaymentEventTx(tx, PaymentProviderEpay, callback.ProviderTradeNo, tradeNo, PaymentEventOrderTopUp, callback.RawPayload); err != nil {
				return err
			}
			var other TopUp
			if err := tx.Where("payment_provider = ? AND provider_trade_no = ? AND trade_no <> ?", PaymentProviderEpay, callback.ProviderTradeNo, tradeNo).First(&other).Error; err == nil {
				return ErrEpayProviderTradeConflict
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var quotaErr error
			quotaToAdd, quotaErr = topUpQuotaForSettlement(topUp, PaymentProviderEpay)
			if quotaErr != nil {
				return quotaErr
			}
			providerTradeNo := callback.ProviderTradeNo
			topUp.ProviderTradeNo = &providerTradeNo
			if topUp.ProviderPayload == "" && callback.RawPayload != "" {
				topUp.ProviderPayload = callback.RawPayload
			}
			topUp.CompleteTime = common.GetTimestamp()
			topUp.Status = common.TopUpStatusSuccess
			if err := tx.Save(topUp).Error; err != nil {
				return err
			}
			return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil)
		}

		if err := bindPaymentEventTx(tx, PaymentProviderEpay, callback.ProviderTradeNo, tradeNo, PaymentEventOrderTopUp, callback.RawPayload); err != nil {
			return err
		}
		// A unique index is the final race-safe guard. The explicit lookup gives
		// callers a stable domain error for the usual (non-racing) conflict.
		var other TopUp
		if err := tx.Where("payment_provider = ? AND provider_trade_no = ? AND trade_no <> ?", PaymentProviderEpay, callback.ProviderTradeNo, tradeNo).First(&other).Error; err == nil {
			return ErrEpayProviderTradeConflict
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		// Only the trusted compatibility seam may settle a fully legacy row.
		// Modern rows use the immutable CreditedQuota snapshot; the helper also
		// applies the wallet upper bound to the legacy formula.
		var quotaErr error
		quotaToAdd, quotaErr = topUpQuotaForSettlement(topUp, PaymentProviderEpay)
		if quotaErr != nil {
			return quotaErr
		}
		if topUp.ProviderTradeNo == nil {
			providerTradeNo := callback.ProviderTradeNo
			topUp.ProviderTradeNo = &providerTradeNo
		}
		if topUp.ProviderPayload == "" && callback.RawPayload != "" {
			topUp.ProviderPayload = callback.RawPayload
		}
		markTopUpPaidUncredited(topUp, "")
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}
		evidenceCommitted = true
		outcome = EpaySettlementPaidUncredited
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrTopUpNotFound) && !errors.Is(err, ErrPaymentMethodMismatch) && !errors.Is(err, ErrTopUpStatusInvalid) && !errors.Is(err, ErrEpayCallbackInvalid) && !errors.Is(err, ErrEpayCallbackMismatch) && !errors.Is(err, ErrEpayProviderTradeConflict) && !errors.Is(err, ErrEpayOrderSnapshotMissing) {
			common.SysError("epay topup failed: " + err.Error())
		}
		return EpaySettlementCompleted, err
	}
	if outcome == EpaySettlementLegacySuccessAcknowledged {
		common.SysError(fmt.Sprintf("allowlisted legacy EPay success callback acknowledged without settlement trade_no=%s provider_trade_no=%s", tradeNo, callback.ProviderTradeNo))
		return outcome, nil
	}
	if outcome == EpaySettlementAlreadyCompleted {
		return outcome, nil
	}
	if outcome == EpaySettlementPaidUncredited {
		attempt, creditErr := retryPaidUncreditedTopUpWithUpdates(tradeNo, nil)
		if attempt.AlreadyCompleted {
			return EpaySettlementAlreadyCompleted, nil
		}
		if attempt.Credited {
			quotaToAdd = attempt.CreditedQuota
			syncCreditUserQuotaCache(attempt.UserID, quotaToAdd, "epay topup")
			common.SysLog(fmt.Sprintf("易支付充值成功 trade_no=%s user_id=%d quota_to_add=%d money=%.2f", tradeNo, attempt.UserID, quotaToAdd, topUp.Money))
			RecordTopupLog(attempt.UserID, fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%f", logger.LogQuota(quotaToAdd), topUp.Money), callerIp, topUp.PaymentMethod, PaymentProviderEpay)
			return EpaySettlementCompleted, nil
		}
		// The provider evidence transaction has committed. Even a transient
		// failure in the independent credit attempt is therefore safe to ACK: the
		// durable paid_uncredited row is now visible to the operator retry path.
		common.SysError(fmt.Sprintf("易支付充值已付款但未入账 trade_no=%s provider_trade_no=%s error=%v evidence_committed=%t", tradeNo, callback.ProviderTradeNo, creditErr, evidenceCommitted || !attempt.AlreadyCompleted))
		return EpaySettlementPaidUncredited, nil
	}
	syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, "epay topup")

	common.SysLog(fmt.Sprintf("易支付充值成功 trade_no=%s user_id=%d quota_to_add=%d money=%.2f", topUp.TradeNo, topUp.UserId, quotaToAdd, topUp.Money))
	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%f", logger.LogQuota(quotaToAdd), topUp.Money), callerIp, topUp.PaymentMethod, PaymentProviderEpay)
	return EpaySettlementCompleted, nil
}

// RechargeEpay is kept as a compatibility seam for trusted internal callers
// and older tests. HTTP callbacks must use RechargeEpayVerified; this wrapper
// derives a callback only from the already-persisted local order and therefore
// cannot be fed provider-controlled amount/name/merchant fields.
func RechargeEpay(tradeNo string, actualPaymentMethod string, callerIp string) (alreadyDone bool, err error) {
	topUp := GetTopUpByTradeNo(tradeNo)
	if topUp == nil {
		return false, ErrTopUpNotFound
	}
	providerTradeNo := "internal:" + tradeNo
	if topUp.ProviderTradeNo != nil {
		providerTradeNo = *topUp.ProviderTradeNo
	}
	outcome, err := rechargeEpayVerified(EpayCallback{
		ServiceTradeNo:          tradeNo,
		ProviderTradeNo:         providerTradeNo,
		PaymentMethod:           actualPaymentMethod,
		Name:                    firstNonEmpty(topUp.ProviderOrderName, "internal-order"),
		Money:                   decimal.NewFromFloat(topUp.Money).Round(2).StringFixed(2),
		MerchantID:              firstNonEmpty(topUp.ProviderMerchantID, "internal"),
		TradeStatus:             "TRADE_SUCCESS",
		SignatureKeyFingerprint: firstNonEmpty(topUp.ProviderKeyFingerprint, "internal"),
	}, callerIp, true)
	return outcome != EpaySettlementCompleted, err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func Recharge(referenceId string, customerId string, callerIp string) (err error) {
	result, err := settleLegacyTopUp(referenceId, PaymentProviderStripe, map[string]interface{}{
		"stripe_customer": customerId,
	})
	if err != nil {
		common.SysError("topup failed: " + err.Error())
		return err
	}
	if result.AlreadyCompleted || !result.Credited {
		return nil
	}
	syncCreditUserQuotaCache(result.UserID, result.CreditedQuota, "stripe topup")
	RecordTopupLog(result.UserID, fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%d", logger.FormatQuota(result.CreditedQuota), int64(result.Money)), callerIp, result.PaymentMethod, PaymentMethodStripe)
	return nil
}

// topUpQueryWindowSeconds 限制充值记录查询的时间窗口（秒）。
const topUpQueryWindowSeconds int64 = 30 * 24 * 60 * 60

// topUpQueryCutoff 返回允许查询的最早 create_time（秒级 Unix 时间戳）。
func topUpQueryCutoff() int64 {
	return common.GetTimestamp() - topUpQueryWindowSeconds
}

// topUpUserHistoryQuery scopes a user's history to the normal recent window,
// while keeping a provider-authenticated paid_uncredited order visible until
// it is reconciled.  Hiding that terminal-but-uncredited state after 30 days
// would make the payment permanently invisible to both the user and support
// staff even though the provider evidence is retained locally.
func topUpUserHistoryQuery(query *gorm.DB, userId int) *gorm.DB {
	return query.Where(
		"user_id = ? AND (create_time >= ? OR status = ?)",
		userId,
		topUpQueryCutoff(),
		common.TopUpStatusPaidUncredited,
	)
}

func GetUserTopUps(userId int, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	// Start transaction
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Get a bounded total within the transaction.  LIMIT on Count does not
	// bound aggregate work, so probe only primary keys.
	total, err = boundedPrimaryKeyCount(
		topUpUserHistoryQuery(tx.Model(&TopUp{}), userId),
		searchTopUpCountHardLimit,
	)
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated topups within same transaction
	err = topUpUserHistoryQuery(tx.Model(&TopUp{}), userId).
		Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Commit transaction
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return topups, total, nil
}

// GetAllTopUps 获取全平台的充值记录（管理员使用，不限制时间窗口）
func GetAllTopUps(pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	return getAllTopUpsForRole(pageInfo, 0, false)
}

// GetAllTopUpsForRole returns only orders owned by users the calling
// administrator may manage. This keeps peer-admin and root financial records
// behind the same hierarchy boundary as user and subscription management.
func GetAllTopUpsForRole(pageInfo *common.PageInfo, actorRole int) (topups []*TopUp, total int64, err error) {
	return getAllTopUpsForRole(pageInfo, actorRole, true)
}

func getAllTopUpsForRole(pageInfo *common.PageInfo, actorRole int, roleScoped bool) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{})
	if roleScoped {
		query = applyTopUpRoleVisibility(tx, query, actorRole)
	}
	if total, err = boundedPrimaryKeyCount(query, searchTopUpCountHardLimit); err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return topups, total, nil
}

// searchTopUpCountHardLimit 搜索充值记录时 COUNT 的安全上限，
// 防止对超大表执行无界 COUNT 触发 DoS。
const searchTopUpCountHardLimit = 10000

// SearchUserTopUps 按订单号搜索某用户的充值记录
func SearchUserTopUps(userId int, keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := topUpUserHistoryQuery(tx.Model(&TopUp{}), userId)
	if keyword != "" {
		pattern, perr := sanitizeLikePattern(keyword)
		if perr != nil {
			tx.Rollback()
			return nil, 0, perr
		}
		query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}

	if total, err = boundedPrimaryKeyCount(query, searchTopUpCountHardLimit); err != nil {
		tx.Rollback()
		common.SysError("failed to count search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// SearchAllTopUps 按订单号搜索全平台充值记录（管理员使用，不限制时间窗口）
func SearchAllTopUps(keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	return searchAllTopUpsForRole(keyword, pageInfo, 0, false)
}

// SearchAllTopUpsForRole is the role-scoped counterpart of SearchAllTopUps.
func SearchAllTopUpsForRole(keyword string, pageInfo *common.PageInfo, actorRole int) (topups []*TopUp, total int64, err error) {
	return searchAllTopUpsForRole(keyword, pageInfo, actorRole, true)
}

func searchAllTopUpsForRole(keyword string, pageInfo *common.PageInfo, actorRole int, roleScoped bool) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{})
	if roleScoped {
		query = applyTopUpRoleVisibility(tx, query, actorRole)
	}
	if keyword != "" {
		pattern, perr := sanitizeLikePattern(keyword)
		if perr != nil {
			tx.Rollback()
			return nil, 0, perr
		}
		query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}

	if total, err = boundedPrimaryKeyCount(query, searchTopUpCountHardLimit); err != nil {
		tx.Rollback()
		common.SysError("failed to count search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

func applyTopUpRoleVisibility(tx *gorm.DB, query *gorm.DB, actorRole int) *gorm.DB {
	if !common.IsValidateRole(actorRole) || actorRole < common.RoleAdminUser {
		return query.Where("1 = 0")
	}
	visibleUsers := tx.Unscoped().Model(&User{}).Select("id").Where("role IN ?", []int{
		common.RoleGuestUser,
		common.RoleCommonUser,
		common.RoleAdminUser,
		common.RoleRootUser,
	})
	if actorRole != common.RoleRootUser {
		visibleUsers = visibleUsers.Where("role < ?", actorRole)
	}
	return query.Where("user_id IN (?)", visibleUsers)
}

// ManualCompleteTopUp 管理员手动完成订单并给用户充值
func ManualCompleteTopUp(tradeNo string, callerIp string) error {
	if tradeNo == "" {
		return errors.New("未提供订单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var userId int
	var quotaToAdd int
	var payMoney float64
	var paymentMethod string

	err := DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		// 行级锁，避免并发补单
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return errors.New("充值订单不存在")
		}

		// 幂等处理：已成功直接返回
		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("订单状态不是待支付，无法补单")
		}

		// Modern orders freeze the exact entitlement at checkout time. Use that
		// snapshot for every provider (including Creem, whose Amount is a
		// user-facing quantity and must not be multiplied again). Only legacy
		// rows without CreditedQuota use the historical provider-specific
		// derivation; those rows are already an explicit operator/manual path.
		if topUp.CreditedQuota > 0 {
			quotaToAdd = topUp.CreditedQuota
		} else {
			var quotaErr error
			switch topUp.PaymentProvider {
			case PaymentProviderStripe:
				quotaToAdd, quotaErr = common.WalletQuotaFromDecimalStrict(
					decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.GetQuotaPerUnit())),
				)
			case PaymentProviderCreem:
				// Historical Creem orders store Amount as the already-computed
				// quota from CreemProduct. Multiplying it by QuotaPerUnit would
				// inflate a manual completion by that factor.
				quotaToAdd, quotaErr = common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(topUp.Amount))
			default:
				quotaToAdd, quotaErr = common.WalletQuotaFromDecimalStrict(
					decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.GetQuotaPerUnit())),
				)
			}
			if quotaErr != nil {
				return ErrInvalidTopUpQuota
			}
		}
		if quotaToAdd <= 0 || quotaToAdd > common.MaxWalletQuota {
			return ErrInvalidTopUpQuota
		}

		// 标记完成
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		// 增加用户额度（立即写库，保持一致性）
		if err := creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil); err != nil {
			return err
		}

		userId = topUp.UserId
		payMoney = topUp.Money
		paymentMethod = topUp.PaymentMethod
		return nil
	})

	if err != nil {
		return err
	}

	// 事务外记录日志，避免阻塞
	syncCreditUserQuotaCache(userId, quotaToAdd, "manual topup")
	RecordTopupLog(userId, fmt.Sprintf("管理员补单成功，充值金额: %v，支付金额：%f", logger.FormatQuota(quotaToAdd), payMoney), callerIp, paymentMethod, "admin")
	return nil
}
func RechargeCreem(referenceId string, customerEmail string, customerName string, callerIp string) (err error) {
	// customerName is retained for source compatibility; Creem historically
	// used it only while creating the checkout.  The settlement helper applies
	// the email update atomically with the wallet mutation in phase two.
	_ = customerName
	updateFields := map[string]interface{}{}
	if strings.TrimSpace(customerEmail) != "" {
		// The map is consumed by creditTopUpQuota only after it has locked the
		// user and passed the wallet-cap predicate.
		updateFields["email"] = customerEmail
	}
	result, err := settleLegacyTopUp(referenceId, PaymentProviderCreem, updateFields)
	if err != nil {
		common.SysError("creem topup failed: " + err.Error())
		return err
	}
	if result.AlreadyCompleted || !result.Credited {
		return nil
	}
	syncCreditUserQuotaCache(result.UserID, result.CreditedQuota, "creem topup")
	RecordTopupLog(result.UserID, fmt.Sprintf("使用Creem充值成功，充值额度: %v，支付金额：%.2f", result.CreditedQuota, result.Money), callerIp, result.PaymentMethod, PaymentMethodCreem)
	return nil
}

func RechargeWaffo(tradeNo string, callerIp string) (err error) {
	result, err := settleLegacyTopUp(tradeNo, PaymentProviderWaffo, nil)
	if err != nil {
		common.SysError("waffo topup failed: " + err.Error())
		return err
	}
	if result.AlreadyCompleted || !result.Credited {
		return nil
	}
	syncCreditUserQuotaCache(result.UserID, result.CreditedQuota, "waffo topup")
	if result.CreditedQuota > 0 {
		RecordTopupLog(result.UserID, fmt.Sprintf("Waffo充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(result.CreditedQuota), result.Money), callerIp, result.PaymentMethod, PaymentMethodWaffo)
	}
	return nil
}

func RechargeWaffoPancake(tradeNo string) (err error) {
	result, err := settleLegacyTopUp(tradeNo, PaymentProviderWaffoPancake, nil)
	if err != nil {
		common.SysError("waffo pancake topup failed: " + err.Error())
		return err
	}
	if result.AlreadyCompleted || !result.Credited {
		return nil
	}
	syncCreditUserQuotaCache(result.UserID, result.CreditedQuota, "waffo pancake topup")
	if result.CreditedQuota > 0 {
		RecordLog(result.UserID, LogTypeTopup, fmt.Sprintf("Waffo Pancake充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(result.CreditedQuota), result.Money))
	}
	return nil
}
