package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/cachex"
	"github.com/samber/hot"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Subscription duration units
const (
	SubscriptionDurationYear   = "year"
	SubscriptionDurationMonth  = "month"
	SubscriptionDurationDay    = "day"
	SubscriptionDurationHour   = "hour"
	SubscriptionDurationCustom = "custom"
)

// Subscription quota reset period
const (
	SubscriptionResetNever   = "never"
	SubscriptionResetDaily   = "daily"
	SubscriptionResetWeekly  = "weekly"
	SubscriptionResetMonthly = "monthly"
	SubscriptionResetCustom  = "custom"
)

var (
	ErrSubscriptionOrderNotFound      = errors.New("subscription order not found")
	ErrSubscriptionOrderStatusInvalid = errors.New("subscription order status invalid")
	// ErrSubscriptionPurchaseLimitExceeded is returned by the entitlement
	// creator when an order has been paid but the plan's per-user purchase cap
	// prevents a new entitlement from being created.  Payment settlement wraps
	// this condition in a durable paid_uncredited order state instead of
	// returning it to the provider as a retryable transport failure.
	ErrSubscriptionPurchaseLimitExceeded = errors.New("subscription purchase limit exceeded")
	// ErrSubscriptionOrderPaidUncredited identifies a paid order that still
	// needs operator action (retry the grant or issue a refund).  Webhook
	// adapters use the settlement outcome, not this error, to acknowledge a
	// provider callback after the durable state has been committed.
	ErrSubscriptionOrderPaidUncredited = errors.New("subscription order paid but entitlement not credited")
	// ErrSubscriptionOrderConflict indicates that a subscription settlement
	// would collide with an existing one-time top-up row.  The two ledgers must
	// never silently overwrite one another, even on legacy databases where the
	// trade_no uniqueness constraint was not enforced across tables.
	ErrSubscriptionOrderConflict              = errors.New("subscription order conflicts with existing top-up")
	ErrSubscriptionEntitlementSnapshotMissing = errors.New("subscription entitlement snapshot missing")
	ErrSubscriptionEntitlementSnapshotInvalid = errors.New("subscription entitlement snapshot invalid")
	// ErrSubscriptionPreConsumeConflict means a request id was replayed with
	// a different owner or amount.  Reusing the existing marker in that case
	// could make the caller believe a different reservation was applied.
	ErrSubscriptionPreConsumeConflict = errors.New("subscription pre-consume request conflicts with existing marker")
)

const (
	subscriptionPlanCacheNamespace     = "new-api:subscription_plan:v1"
	subscriptionPlanInfoCacheNamespace = "new-api:subscription_plan_info:v1"
)

// SubscriptionOrderStatusPaidUncredited is a terminal payment state: the
// provider payment was authenticated and persisted, but no local entitlement
// was granted.  It intentionally remains distinct from the normal success
// state so operators can reconcile or retry it without replaying the payment.
const SubscriptionOrderStatusPaidUncredited = "paid_uncredited"

// SubscriptionSettlementResolutionRefundRequired marks a paid_uncredited
// order whose safe default remediation is a provider refund.  Resolution is a
// separate field rather than another order status so the payment fact remains
// immutable while the operator action can progress independently.
const SubscriptionSettlementResolutionRefundRequired = "refund_required"

var (
	subscriptionPlanCacheOnce     sync.Once
	subscriptionPlanInfoCacheOnce sync.Once

	subscriptionPlanCache     *cachex.HybridCache[SubscriptionPlan]
	subscriptionPlanInfoCache *cachex.HybridCache[SubscriptionPlanInfo]
)

func subscriptionPlanCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_TTL", 300)
	if ttlSeconds <= 0 {
		ttlSeconds = 300
	}
	return time.Duration(ttlSeconds) * time.Second
}

func subscriptionPlanInfoCacheTTL() time.Duration {
	ttlSeconds := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_TTL", 120)
	if ttlSeconds <= 0 {
		ttlSeconds = 120
	}
	return time.Duration(ttlSeconds) * time.Second
}

func subscriptionPlanCacheCapacity() int {
	capacity := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_CACHE_CAP", 5000)
	if capacity <= 0 {
		capacity = 5000
	}
	return capacity
}

func subscriptionPlanInfoCacheCapacity() int {
	capacity := common.GetEnvOrDefault("SUBSCRIPTION_PLAN_INFO_CACHE_CAP", 10000)
	if capacity <= 0 {
		capacity = 10000
	}
	return capacity
}

func getSubscriptionPlanCache() *cachex.HybridCache[SubscriptionPlan] {
	subscriptionPlanCacheOnce.Do(func() {
		ttl := subscriptionPlanCacheTTL()
		subscriptionPlanCache = cachex.NewHybridCache[SubscriptionPlan](cachex.HybridCacheConfig[SubscriptionPlan]{
			Namespace: cachex.Namespace(subscriptionPlanCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SubscriptionPlan]{},
			Memory: func() *hot.HotCache[string, SubscriptionPlan] {
				return hot.NewHotCache[string, SubscriptionPlan](hot.LRU, subscriptionPlanCacheCapacity()).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return subscriptionPlanCache
}

func getSubscriptionPlanInfoCache() *cachex.HybridCache[SubscriptionPlanInfo] {
	subscriptionPlanInfoCacheOnce.Do(func() {
		ttl := subscriptionPlanInfoCacheTTL()
		subscriptionPlanInfoCache = cachex.NewHybridCache[SubscriptionPlanInfo](cachex.HybridCacheConfig[SubscriptionPlanInfo]{
			Namespace: cachex.Namespace(subscriptionPlanInfoCacheNamespace),
			Redis:     common.RDB,
			RedisEnabled: func() bool {
				return common.RedisEnabled && common.RDB != nil
			},
			RedisCodec: cachex.JSONCodec[SubscriptionPlanInfo]{},
			Memory: func() *hot.HotCache[string, SubscriptionPlanInfo] {
				return hot.NewHotCache[string, SubscriptionPlanInfo](hot.LRU, subscriptionPlanInfoCacheCapacity()).
					WithTTL(ttl).
					WithJanitor().
					Build()
			},
		})
	})
	return subscriptionPlanInfoCache
}

func subscriptionPlanCacheKey(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

func InvalidateSubscriptionPlanCache(planId int) {
	if planId <= 0 {
		return
	}
	cache := getSubscriptionPlanCache()
	_, _ = cache.DeleteMany([]string{subscriptionPlanCacheKey(planId)})
	infoCache := getSubscriptionPlanInfoCache()
	_ = infoCache.Purge()
}

// Subscription plan
type SubscriptionPlan struct {
	Id int `json:"id"`

	Title    string `json:"title" gorm:"type:varchar(128);not null"`
	Subtitle string `json:"subtitle" gorm:"type:varchar(255);default:''"`

	// Display money amount (follow existing code style: float64 for money)
	PriceAmount float64 `json:"price_amount" gorm:"type:decimal(10,6);not null;default:0"`
	Currency    string  `json:"currency" gorm:"type:varchar(8);not null;default:'USD'"`

	DurationUnit  string `json:"duration_unit" gorm:"type:varchar(16);not null;default:'month'"`
	DurationValue int    `json:"duration_value" gorm:"type:int;not null;default:1"`
	CustomSeconds int64  `json:"custom_seconds" gorm:"type:bigint;not null;default:0"`

	Enabled   bool `json:"enabled" gorm:"default:true"`
	SortOrder int  `json:"sort_order" gorm:"type:int;default:0"`

	AllowBalancePay *bool `json:"allow_balance_pay"`

	// Allow falling back to wallet balance after subscription quota is exhausted (empty = true)
	AllowWalletOverflow *bool `json:"allow_wallet_overflow"`

	StripePriceId         string `json:"stripe_price_id" gorm:"type:varchar(128);default:''"`
	CreemProductId        string `json:"creem_product_id" gorm:"type:varchar(128);default:''"`
	WaffoPancakeProductId string `json:"waffo_pancake_product_id" gorm:"type:varchar(128);default:''"`

	// Max purchases per user (0 = unlimited)
	MaxPurchasePerUser int `json:"max_purchase_per_user" gorm:"type:int;default:0"`

	// Upgrade user group after purchase (empty = no change)
	UpgradeGroup string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`

	// Downgrade user group on expiry (empty = revert to the group held before purchase)
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`

	// Total quota (amount in quota units, 0 = unlimited)
	TotalAmount int64 `json:"total_amount" gorm:"type:bigint;not null;default:0"`

	// Quota reset period for plan
	QuotaResetPeriod        string `json:"quota_reset_period" gorm:"type:varchar(16);default:'never'"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds" gorm:"type:bigint;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func (p *SubscriptionPlan) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	p.CreatedAt = now
	p.UpdatedAt = now
	return nil
}

func (p *SubscriptionPlan) BeforeUpdate(tx *gorm.DB) error {
	p.UpdatedAt = common.GetTimestamp()
	return nil
}

func (p *SubscriptionPlan) NormalizeDefaults() {
	if p.AllowBalancePay == nil {
		p.AllowBalancePay = common.GetPointer(true)
	}
	if p.AllowWalletOverflow == nil {
		p.AllowWalletOverflow = common.GetPointer(true)
	}
}

// Subscription order (payment -> webhook -> create UserSubscription)
type SubscriptionOrder struct {
	Id     int     `json:"id"`
	UserId int     `json:"user_id" gorm:"index"`
	PlanId int     `json:"plan_id" gorm:"index"`
	Money  float64 `json:"money"`

	TradeNo         string `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	Status          string `json:"status"`
	CreateTime      int64  `json:"create_time"`
	CompleteTime    int64  `json:"complete_time"`
	// SettlementResolution records operator remediation for a paid order that
	// could not receive its entitlement (for example, refund_required). It is
	// separate from Status so the paid fact is never reverted to pending.
	SettlementResolution string `json:"settlement_resolution" gorm:"type:varchar(32);default:'';index"`
	// SettlementErrorCode is a stable, queryable reason for a durable
	// paid_uncredited state; ProviderPayload retains only an audit digest.
	SettlementErrorCode string `json:"settlement_error_code" gorm:"type:varchar(64);default:''"`

	ProviderPayload string `json:"-" gorm:"type:text"`

	// Immutable provider checkout snapshot. EPay callbacks are accepted only
	// when these values match the order created before redirecting the user.
	ProviderTradeNo    *string `json:"-" gorm:"type:varchar(255);index:idx_subscription_orders_provider_trade_no,length:191"`
	ProviderMerchantID string  `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderOrderName  string  `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderAmount     string  `json:"-" gorm:"type:varchar(64);default:''"`
	ProviderProductID  string  `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderStoreID    string  `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderCheckoutID string  `json:"-" gorm:"type:varchar(255);default:''"`
	// ProviderSubscriptionID is the recurring provider object created by this
	// checkout. It is metadata only until lifecycle/renewal settlement is
	// implemented; storing it now enables safe reconciliation without granting
	// any additional entitlement.
	ProviderSubscriptionID string `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderCurrency       string `json:"-" gorm:"type:varchar(16);default:''"`
	ProviderEventID        string `json:"-" gorm:"type:varchar(255);default:''"`
	ProviderKeyFingerprint string `json:"-" gorm:"type:varchar(64);default:''"`

	// EntitlementSnapshot is the immutable promise made at checkout. It is
	// versioned JSON so settlement remains independent of later plan edits.
	EntitlementSnapshot string `json:"-" gorm:"type:text"`
}

const SubscriptionEntitlementSnapshotVersion = 1

// SubscriptionEntitlementSnapshot contains every plan field that can change
// the entitlement granted by a paid order. Provider product identifiers are
// included so webhook adapters can bind the paid object to the same checkout.
type SubscriptionEntitlementSnapshot struct {
	Version                 int    `json:"version"`
	PlanID                  int    `json:"plan_id"`
	PlanUpdatedAt           int64  `json:"plan_updated_at"`
	Title                   string `json:"title"`
	PriceAmount             string `json:"price_amount"`
	Currency                string `json:"currency"`
	DurationUnit            string `json:"duration_unit"`
	DurationValue           int    `json:"duration_value"`
	CustomSeconds           int64  `json:"custom_seconds"`
	TotalAmount             int64  `json:"total_amount"`
	QuotaResetPeriod        string `json:"quota_reset_period"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds"`
	UpgradeGroup            string `json:"upgrade_group"`
	DowngradeGroup          string `json:"downgrade_group"`
	AllowWalletOverflow     bool   `json:"allow_wallet_overflow"`
	MaxPurchasePerUser      int    `json:"max_purchase_per_user"`
	EnabledAtPurchase       bool   `json:"enabled_at_purchase"`
	StripePriceID           string `json:"stripe_price_id"`
	CreemProductID          string `json:"creem_product_id"`
	WaffoPancakeProductID   string `json:"waffo_pancake_product_id"`
}

func buildSubscriptionEntitlementSnapshot(plan *SubscriptionPlan) (*SubscriptionEntitlementSnapshot, error) {
	if plan == nil || plan.Id <= 0 {
		return nil, ErrSubscriptionEntitlementSnapshotInvalid
	}
	plan.NormalizeDefaults()
	if math.IsInf(plan.PriceAmount, 0) || math.IsNaN(plan.PriceAmount) {
		return nil, ErrSubscriptionEntitlementSnapshotInvalid
	}
	price := decimal.NewFromFloat(plan.PriceAmount)
	if price.IsNegative() {
		return nil, ErrSubscriptionEntitlementSnapshotInvalid
	}
	snapshot := &SubscriptionEntitlementSnapshot{
		Version:                 SubscriptionEntitlementSnapshotVersion,
		PlanID:                  plan.Id,
		PlanUpdatedAt:           plan.UpdatedAt,
		Title:                   strings.TrimSpace(plan.Title),
		PriceAmount:             price.StringFixed(6),
		Currency:                strings.TrimSpace(plan.Currency),
		DurationUnit:            strings.TrimSpace(plan.DurationUnit),
		DurationValue:           plan.DurationValue,
		CustomSeconds:           plan.CustomSeconds,
		TotalAmount:             plan.TotalAmount,
		QuotaResetPeriod:        NormalizeResetPeriod(plan.QuotaResetPeriod),
		QuotaResetCustomSeconds: plan.QuotaResetCustomSeconds,
		UpgradeGroup:            strings.TrimSpace(plan.UpgradeGroup),
		DowngradeGroup:          strings.TrimSpace(plan.DowngradeGroup),
		AllowWalletOverflow:     plan.AllowWalletOverflow != nil && *plan.AllowWalletOverflow,
		MaxPurchasePerUser:      plan.MaxPurchasePerUser,
		EnabledAtPurchase:       plan.Enabled,
		StripePriceID:           strings.TrimSpace(plan.StripePriceId),
		CreemProductID:          strings.TrimSpace(plan.CreemProductId),
		WaffoPancakeProductID:   strings.TrimSpace(plan.WaffoPancakeProductId),
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// BuildSubscriptionEntitlementSnapshot creates a serialized, immutable plan
// snapshot for a checkout order.
func BuildSubscriptionEntitlementSnapshot(plan *SubscriptionPlan) (string, error) {
	snapshot, err := buildSubscriptionEntitlementSnapshot(plan)
	if err != nil {
		return "", err
	}
	data, err := common.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("marshal subscription entitlement snapshot: %w", err)
	}
	return string(data), nil
}

func (s *SubscriptionEntitlementSnapshot) Validate() error {
	if s == nil || s.Version != SubscriptionEntitlementSnapshotVersion || s.PlanID <= 0 {
		return ErrSubscriptionEntitlementSnapshotInvalid
	}
	price, err := decimal.NewFromString(strings.TrimSpace(s.PriceAmount))
	if err != nil || price.IsNegative() || strings.TrimSpace(s.Currency) == "" {
		return ErrSubscriptionEntitlementSnapshotInvalid
	}
	switch strings.TrimSpace(s.DurationUnit) {
	case SubscriptionDurationYear, SubscriptionDurationMonth, SubscriptionDurationDay, SubscriptionDurationHour:
		if s.DurationValue <= 0 {
			return ErrSubscriptionEntitlementSnapshotInvalid
		}
	case SubscriptionDurationCustom:
		if s.CustomSeconds <= 0 {
			return ErrSubscriptionEntitlementSnapshotInvalid
		}
	default:
		return ErrSubscriptionEntitlementSnapshotInvalid
	}
	if s.TotalAmount < 0 || s.MaxPurchasePerUser < 0 || s.QuotaResetCustomSeconds < 0 {
		return ErrSubscriptionEntitlementSnapshotInvalid
	}
	period := NormalizeResetPeriod(s.QuotaResetPeriod)
	if period != strings.TrimSpace(s.QuotaResetPeriod) ||
		(period == SubscriptionResetCustom && s.QuotaResetCustomSeconds <= 0) {
		return ErrSubscriptionEntitlementSnapshotInvalid
	}
	if len(s.Title) > 128 || len(s.Currency) > 8 || len(s.UpgradeGroup) > 64 || len(s.DowngradeGroup) > 64 {
		return ErrSubscriptionEntitlementSnapshotInvalid
	}
	return nil
}

func (s *SubscriptionEntitlementSnapshot) ToPlan() (*SubscriptionPlan, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	price, err := decimal.NewFromString(strings.TrimSpace(s.PriceAmount))
	if err != nil {
		return nil, ErrSubscriptionEntitlementSnapshotInvalid
	}
	priceFloat := price.InexactFloat64()
	if math.IsNaN(priceFloat) || math.IsInf(priceFloat, 0) || priceFloat < 0 {
		return nil, ErrSubscriptionEntitlementSnapshotInvalid
	}
	allowOverflow := s.AllowWalletOverflow
	return &SubscriptionPlan{
		Id:                      s.PlanID,
		Title:                   s.Title,
		PriceAmount:             priceFloat,
		Currency:                s.Currency,
		DurationUnit:            s.DurationUnit,
		DurationValue:           s.DurationValue,
		CustomSeconds:           s.CustomSeconds,
		Enabled:                 s.EnabledAtPurchase,
		AllowWalletOverflow:     &allowOverflow,
		MaxPurchasePerUser:      s.MaxPurchasePerUser,
		UpgradeGroup:            s.UpgradeGroup,
		DowngradeGroup:          s.DowngradeGroup,
		TotalAmount:             s.TotalAmount,
		QuotaResetPeriod:        s.QuotaResetPeriod,
		QuotaResetCustomSeconds: s.QuotaResetCustomSeconds,
		StripePriceId:           s.StripePriceID,
		CreemProductId:          s.CreemProductID,
		WaffoPancakeProductId:   s.WaffoPancakeProductID,
		UpdatedAt:               s.PlanUpdatedAt,
	}, nil
}

func (o *SubscriptionOrder) SetEntitlementSnapshot(plan *SubscriptionPlan) error {
	snapshot, err := BuildSubscriptionEntitlementSnapshot(plan)
	if err != nil {
		return err
	}
	o.EntitlementSnapshot = snapshot
	return nil
}

func (o *SubscriptionOrder) GetEntitlementSnapshot() (*SubscriptionEntitlementSnapshot, error) {
	if o == nil || strings.TrimSpace(o.EntitlementSnapshot) == "" {
		return nil, ErrSubscriptionEntitlementSnapshotMissing
	}
	var snapshot SubscriptionEntitlementSnapshot
	if err := common.Unmarshal([]byte(o.EntitlementSnapshot), &snapshot); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubscriptionEntitlementSnapshotInvalid, err)
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (o *SubscriptionOrder) Insert() error {
	if o.CreateTime == 0 {
		o.CreateTime = common.GetTimestamp()
	}
	return DB.Create(o).Error
}

func (o *SubscriptionOrder) Update() error {
	return DB.Save(o).Error
}

// SetSubscriptionOrderProviderCheckoutID updates only the checkout identity.
// The order object held by a payment request can be stale if a webhook settles
// it while the provider API call is in flight; a full Save would then roll the
// order back to pending and erase settlement fields.
func SetSubscriptionOrderProviderCheckoutID(tradeNo string, provider string, checkoutID string) error {
	tradeNo = strings.TrimSpace(tradeNo)
	provider = strings.TrimSpace(provider)
	checkoutID = strings.TrimSpace(checkoutID)
	if tradeNo == "" || provider == "" || checkoutID == "" {
		return ErrProviderSettlementInvalid
	}

	result := DB.Model(&SubscriptionOrder{}).
		Where("trade_no = ? AND payment_provider = ?", tradeNo, provider).
		Where("provider_checkout_id IS NULL OR provider_checkout_id = '' OR provider_checkout_id = ?", checkoutID).
		Update("provider_checkout_id", checkoutID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}

	var current SubscriptionOrder
	err := DB.Select("payment_provider, provider_checkout_id").Where("trade_no = ?", tradeNo).First(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrSubscriptionOrderNotFound
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

func GetSubscriptionOrderByTradeNo(tradeNo string) *SubscriptionOrder {
	if tradeNo == "" {
		return nil
	}
	var order SubscriptionOrder
	if err := DB.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		return nil
	}
	return &order
}

// GetSubscriptionOrderByTradeNoWithError preserves database errors for
// externally-triggered payment callbacks. A pointer-only lookup cannot tell
// a missing order from a transient database failure; acknowledging the latter
// would permanently lose a valid provider notification.
func GetSubscriptionOrderByTradeNoWithError(tradeNo string) (*SubscriptionOrder, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return nil, ErrSubscriptionOrderNotFound
	}
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	var order SubscriptionOrder
	if err := DB.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSubscriptionOrderNotFound
		}
		return nil, err
	}
	return &order, nil
}

// User subscription instance
type UserSubscription struct {
	Id     int `json:"id"`
	UserId int `json:"user_id" gorm:"index;index:idx_user_sub_active,priority:1"`
	PlanId int `json:"plan_id" gorm:"index"`

	AmountTotal int64 `json:"amount_total" gorm:"type:bigint;not null;default:0"`
	AmountUsed  int64 `json:"amount_used" gorm:"type:bigint;not null;default:0"`

	StartTime int64  `json:"start_time" gorm:"bigint"`
	EndTime   int64  `json:"end_time" gorm:"bigint;index;index:idx_user_sub_active,priority:3"`
	Status    string `json:"status" gorm:"type:varchar(32);index;index:idx_user_sub_active,priority:2"` // active/expired/cancelled
	// ProviderSubscriptionID binds recurring provider events to this entitlement.
	ProviderSubscriptionID string `json:"-" gorm:"type:varchar(255);index:idx_user_subscriptions_provider_subscription_id,length:191"`
	// ProviderSubscriptionProvider prevents an ID collision between gateways
	// (for example a Stripe and Creem object that happen to use the same ID).
	// It is empty for admin/balance subscriptions and for legacy rows created
	// before recurring-provider binding was introduced.
	ProviderSubscriptionProvider string `json:"-" gorm:"type:varchar(50);index"`
	// ProviderLifecycleEventTime is the provider-reported event creation time
	// of the last accepted lifecycle event. It is separate from UpdatedAt,
	// which also changes for local maintenance and quota consumption.
	ProviderLifecycleEventTime int64 `json:"-" gorm:"type:bigint;default:0"`
	// SubscriptionOrderTradeNo links this entitlement to the immutable local
	// checkout order. It is populated for paid orders so a later webhook that
	// finally reveals the provider's recurring subscription ID can bind that ID
	// to the exact entitlement instead of guessing among same-user/same-plan
	// rows. Empty values are retained for admin/balance and legacy rows.
	SubscriptionOrderTradeNo string `json:"-" gorm:"type:varchar(255);index:idx_user_subscriptions_subscription_order_trade_no,length:191"`

	Source string `json:"source" gorm:"type:varchar(32);default:'order'"` // order/admin

	// Quota reset settings are copied from the plan at entitlement creation.
	// Plan definitions remain editable, but changing a plan must not silently
	// change the reset cadence promised to subscriptions that already exist.
	// Empty values are retained for legacy rows created before these columns
	// existed; reset helpers then conservatively fall back to the current plan.
	QuotaResetPeriod        string `json:"quota_reset_period" gorm:"type:varchar(16);default:''"`
	QuotaResetCustomSeconds int64  `json:"quota_reset_custom_seconds" gorm:"type:bigint;default:0"`

	LastResetTime int64 `json:"last_reset_time" gorm:"type:bigint;default:0"`
	NextResetTime int64 `json:"next_reset_time" gorm:"type:bigint;default:0;index"`

	UpgradeGroup  string `json:"upgrade_group" gorm:"type:varchar(64);default:''"`
	PrevUserGroup string `json:"prev_user_group" gorm:"type:varchar(64);default:''"`

	// Downgrade target group on expiry (snapshot from plan; empty = revert to PrevUserGroup)
	DowngradeGroup string `json:"downgrade_group" gorm:"type:varchar(64);default:''"`

	// Whether wallet fallback is allowed after this subscription's quota is exhausted (snapshot from plan)
	AllowWalletOverflow bool `json:"allow_wallet_overflow"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func normalizeSubscriptionLifecycleStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "paid", "trialing":
		return "active"
	case "past_due", "unpaid", "incomplete", "incomplete_expired":
		return "past_due"
	case "canceled", "cancelled":
		return "cancelled"
	case "expired":
		return "expired"
	default:
		return ""
	}
}

func subscriptionLifecycleStatusRank(status string) int {
	switch normalizeSubscriptionLifecycleStatus(status) {
	case "active":
		return 1
	case "past_due":
		return 2
	case "cancelled":
		return 3
	case "expired":
		return 4
	default:
		return 0
	}
}

func isTerminalSubscriptionLifecycleStatus(status string) bool {
	switch normalizeSubscriptionLifecycleStatus(status) {
	case "cancelled", "expired":
		return true
	default:
		return false
	}
}

// SubscriptionLifecycleOptions carries lifecycle intent that cannot be
// represented by a provider status alone. In particular, Stripe and Creem
// may report a cancellation request while the customer is still entitled to
// use the service through the end of the paid period. Keeping this intent
// explicit prevents a webhook adapter from accidentally revoking access early.
//
// ImmediateRevoke takes precedence over CancellationAtPeriodEnd when both are
// set. The latter is deliberately a bool rather than inferred from a future
// periodEnd: a provider can send a future period boundary on an immediate
// cancellation event as well.
type SubscriptionLifecycleOptions struct {
	ImmediateRevoke         bool
	CancellationAtPeriodEnd bool
	// RenewalOnly marks an active/paid lifecycle delivery as a billing
	// renewal rather than a generic status update. A past_due entitlement may
	// only be recovered by such an event when the provider reports a strictly
	// newer period boundary, unless AllowSamePeriodRecovery is explicitly set
	// together with verified invoice evidence.
	RenewalOnly bool
	// AllowSamePeriodRecovery permits a verified payment event to recover a
	// past_due subscription whose period boundary is exactly unchanged. It
	// never permits an older boundary and it never resets AmountUsed; callers
	// must pass non-nil SubscriptionLifecycleEvidence in the same transaction.
	AllowSamePeriodRecovery bool
	// PaymentBinding is present only for a verified positive payment event such
	// as Stripe invoice.payment_succeeded. The provider subscription is resolved
	// first; its immutable local order link then owns every supplied object alias.
	// Legacy entitlements without an order link are intentionally left unbound
	// for manual reconciliation.
	PaymentBinding *ProviderPaymentBindingScope
	// ProviderScope is present for every account-scoped lifecycle delivery,
	// including failures and cancellations that carry no positive payment
	// aliases. The immutable Subscription alias must resolve to this
	// entitlement's exact order before the event can mutate local state.
	ProviderScope *ProviderLifecycleScope
}

// ApplySubscriptionLifecycleEvent applies a verified recurring-provider event.
// PaymentEvent is also used as the event-id fence, so webhook retries cannot
// reset quota or mutate state twice.
func ApplySubscriptionLifecycleEvent(provider, eventID, providerSubscriptionID, status string, periodEnd int64, payload string) error {
	return ApplySubscriptionLifecycleEventAt(provider, eventID, providerSubscriptionID, status, 0, periodEnd, payload)
}

// SubscriptionLifecycleEvidence is the provider-side recurring price proof
// carried by invoice events. It is intentionally separate from the lifecycle
// status so non-invoice legacy events can continue to use the ID-only API.
// Invoice evidence is checked against the immutable SubscriptionOrder snapshot
// inside the same transaction that mutates UserSubscription.
type SubscriptionLifecycleEvidence struct {
	Amount    string
	Currency  string
	ProductID string
}

// ApplySubscriptionLifecycleEventAt is the timestamp-aware lifecycle entry
// point used by provider webhooks. Provider event times make delivery order
// irrelevant; a stale event is recorded for idempotency/audit but cannot
// overwrite a newer state. A zero eventTime is accepted only for legacy
// callers and is handled conservatively (terminal states are sticky).
func ApplySubscriptionLifecycleEventAt(provider, eventID, providerSubscriptionID, status string, eventTime int64, periodEnd int64, payload string) error {
	return ApplySubscriptionLifecycleEventAtWithEvidence(provider, eventID, providerSubscriptionID, status, eventTime, periodEnd, nil, payload)
}

// ApplySubscriptionLifecycleEventAtWithEvidence is the timestamp-aware
// lifecycle entry point with optional invoice price evidence. When evidence is
// supplied, exactly one successful local subscription order must bind the
// provider subscription ID and all provider amount/currency/product fields
// must match that order's immutable checkout snapshot.
func ApplySubscriptionLifecycleEventAtWithEvidence(provider, eventID, providerSubscriptionID, status string, eventTime int64, periodEnd int64, evidence *SubscriptionLifecycleEvidence, payload string) error {
	return ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(provider, eventID, providerSubscriptionID, status, eventTime, periodEnd, evidence, payload, SubscriptionLifecycleOptions{})
}

// ApplySubscriptionLifecycleEventAtWithOptions is the lifecycle entry point
// for adapters that do not carry invoice price evidence.
func ApplySubscriptionLifecycleEventAtWithOptions(provider, eventID, providerSubscriptionID, status string, eventTime int64, periodEnd int64, payload string, options SubscriptionLifecycleOptions) error {
	return ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(provider, eventID, providerSubscriptionID, status, eventTime, periodEnd, nil, payload, options)
}

// ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions applies a verified
// lifecycle event while preserving the provider's cancellation intent.
func ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(provider, eventID, providerSubscriptionID, status string, eventTime int64, periodEnd int64, evidence *SubscriptionLifecycleEvidence, payload string, options SubscriptionLifecycleOptions) error {
	provider = strings.TrimSpace(provider)
	eventID = strings.TrimSpace(eventID)
	providerSubscriptionID = strings.TrimSpace(providerSubscriptionID)
	status = normalizeSubscriptionLifecycleStatus(status)
	if provider == "" || eventID == "" || providerSubscriptionID == "" || status == "" || eventTime < 0 || periodEnd < 0 {
		return ErrProviderSettlementInvalid
	}
	if DB == nil {
		return fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	normalizedProvider := normalizeProviderPaymentBindingValue(provider)
	if normalizedProvider == PaymentProviderStripe && options.ProviderScope == nil {
		// Stripe lifecycle deliveries must carry the independently derived
		// account/environment scope. A positive-payment binding is additional
		// evidence, not a substitute for the lifecycle delivery's own scope.
		return ErrProviderPaymentBindingInvalid
	}
	providerScope := options.ProviderScope
	if providerScope == nil && options.PaymentBinding != nil {
		// Preserve the compatibility path for providers that have not migrated to
		// an explicit lifecycle scope. Stripe is rejected above before this
		// fallback can be reached.
		providerScope = &ProviderLifecycleScope{
			ProviderAccountID:   options.PaymentBinding.ProviderAccountID,
			ProviderEnvironment: options.PaymentBinding.ProviderEnvironment,
		}
	}
	providerAccountID := ""
	providerEnvironment := ""
	if providerScope != nil {
		providerAccountID = strings.TrimSpace(providerScope.ProviderAccountID)
		providerEnvironment = normalizeProviderPaymentBindingValue(providerScope.ProviderEnvironment)
		if !validProviderPaymentScope(normalizedProvider, providerAccountID, providerEnvironment) {
			return ErrProviderPaymentBindingInvalid
		}
	}
	if options.ProviderScope != nil && options.PaymentBinding != nil &&
		(providerAccountID != strings.TrimSpace(options.PaymentBinding.ProviderAccountID) ||
			providerEnvironment != normalizeProviderPaymentBindingValue(options.PaymentBinding.ProviderEnvironment)) {
		return ErrProviderPaymentBindingConflict
	}
	refreshGroupUserID := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		eventKey, ok := PaymentEventKey(provider, eventID)
		if !ok {
			return ErrProviderSettlementInvalid
		}
		var existing PaymentEvent
		lookupErr := tx.Where("event_key = ?", eventKey).First(&existing).Error
		if lookupErr == nil {
			if existing.Provider != provider || existing.ProviderTradeNo != eventID ||
				existing.OrderTradeNo != providerSubscriptionID || existing.OrderKind != "subscription_lifecycle" {
				return ErrEpayProviderTradeConflict
			}
			return nil
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		// Resolve through the durable provider+ID ledger. This prevents a Stripe
		// callback from selecting a Creem entitlement when both gateways happen
		// to use the same opaque subscription ID. Legacy rows are handled by the
		// resolver's conservative provider-scoped fallback.
		subPtr, err := resolveSubscriptionProviderEntitlementTx(tx, provider, providerSubscriptionID)
		if err != nil {
			// Keep the distinction between an unknown ID and a database failure;
			// callers may safely acknowledge an unrelated provider event only for
			// this explicit not-found result.
			return err
		}
		sub := *subPtr
		boundProvider := strings.TrimSpace(sub.ProviderSubscriptionProvider)
		if boundProvider != "" && !strings.EqualFold(boundProvider, provider) {
			return ErrProviderEventConflict
		}
		orderTradeNo := strings.TrimSpace(sub.SubscriptionOrderTradeNo)
		if providerScope != nil {
			if orderTradeNo == "" {
				return ErrProviderPaymentBindingNotFound
			}
			subscriptionBinding, err := findProviderPaymentBindingTx(
				tx,
				provider,
				providerAccountID,
				providerEnvironment,
				ProviderPaymentObjectSubscription,
				providerSubscriptionID,
			)
			if err != nil {
				return err
			}
			if !providerPaymentBindingOwns(subscriptionBinding, orderTradeNo, PaymentEventOrderSubscription) {
				return ErrProviderPaymentBindingConflict
			}
		}
		if evidence != nil {
			if err := validateSubscriptionLifecycleEvidenceTx(tx, provider, providerSubscriptionID, *evidence); err != nil {
				return err
			}
		}
		// Record the provider event only after the owner rows have been resolved
		// and locked. Checkout settlement follows the same order -> user ->
		// subscription -> binding -> event sequence; inserting the event before
		// resolution used to create an event/index -> order lock inversion that
		// could deadlock a concurrent checkout completion.
		if err := bindPaymentEventTx(tx, provider, eventID, providerSubscriptionID, "subscription_lifecycle", payload); err != nil {
			return err
		}
		if options.PaymentBinding != nil {
			if !options.RenewalOnly || evidence == nil {
				return ErrProviderPaymentBindingInvalid
			}
			if orderTradeNo == "" {
				return ErrProviderPaymentBindingNotFound
			}
			if err := validateProviderPaymentBindingScope(
				provider,
				options.PaymentBinding.ProviderAccountID,
				options.PaymentBinding.ProviderEnvironment,
				options.PaymentBinding.Objects,
			); err != nil {
				return err
			}
			checkoutBinding, err := findProviderPaymentBindingTx(
				tx,
				provider,
				options.PaymentBinding.ProviderAccountID,
				options.PaymentBinding.ProviderEnvironment,
				ProviderPaymentObjectSubscription,
				providerSubscriptionID,
			)
			if err != nil {
				return err
			}
			if !providerPaymentBindingOwns(checkoutBinding, orderTradeNo, PaymentEventOrderSubscription) {
				return ErrProviderPaymentBindingConflict
			}
			if err := bindProviderPaymentBindingsTx(tx, ProviderPaymentBindingBatch{
				Provider:            provider,
				ProviderAccountID:   options.PaymentBinding.ProviderAccountID,
				ProviderEnvironment: options.PaymentBinding.ProviderEnvironment,
				OrderTradeNo:        orderTradeNo,
				OrderKind:           PaymentEventOrderSubscription,
				Objects:             options.PaymentBinding.Objects,
			}); err != nil {
				return err
			}
		}
		// Bind legacy rows on the first verified event, but only after the
		// uniqueness check above. This prevents a cross-provider collision from
		// being silently claimed by whichever webhook arrived first.
		updates := map[string]interface{}{}
		if boundProvider == "" {
			updates["provider_subscription_provider"] = provider
		}

		// Provider event ordering/state rules:
		//   * terminal local states never get resurrected by an active/past_due
		//     delivery; a new checkout creates a new entitlement;
		//   * known older events are ignored (but their event fence is retained);
		//   * equal-time events resolve toward the more restrictive state;
		//   * timestamp-less legacy active events require fresh period evidence.
		// A scheduled cancellation is still an active entitlement. Providers may
		// encode that request as status=canceled (Stripe) or
		// status=scheduled_cancel (Creem), so resolve it only after loading the
		// local row and obtaining a safe period boundary.
		lifecyclePeriodEnd := periodEnd
		periodEndCancellation := options.CancellationAtPeriodEnd && !options.ImmediateRevoke
		if periodEndCancellation {
			if lifecyclePeriodEnd <= 0 {
				// Older payloads omitted the boundary. The checkout snapshot's
				// local end time is a safe fallback; an unlimited/unknown end time
				// cannot be kept active indefinitely on an unverified boundary.
				lifecyclePeriodEnd = sub.EndTime
			}
			if lifecyclePeriodEnd <= 0 {
				return ErrProviderSettlementInvalid
			}
			now := getDBTimestampFrom(tx)
			if lifecyclePeriodEnd <= now {
				// The scheduled boundary has already passed by the time the
				// webhook is processed. Apply the terminal transition now.
				options.CancellationAtPeriodEnd = false
				options.ImmediateRevoke = true
				// Once the promised period boundary is in the past, model the
				// entitlement as expired regardless of whether the provider sent
				// a canceled/active status alongside the scheduled-cancel flag.
				status = "expired"
			} else {
				// Keep access active through the provider boundary even when the
				// provider's status field says canceled.
				status = "active"
			}
		}
		if options.ImmediateRevoke && !isTerminalSubscriptionLifecycleStatus(status) {
			status = "cancelled"
		}

		currentStatus := normalizeSubscriptionLifecycleStatus(sub.Status)
		// A generic active status update is not payment proof.  Providers may
		// include a future current_period_end on subscription.updated payloads
		// even when the corresponding invoice is still unpaid.  Do not let such
		// an event extend an already-active entitlement or reset its consumed
		// quota; only an explicitly identified renewal event may advance the
		// period.  Keep the provider/event fence (and any legacy provider binding)
		// durable, while leaving the entitlement fields untouched.
		if status == "active" && currentStatus == "active" &&
			!periodEndCancellation && lifecyclePeriodEnd > sub.EndTime && !options.RenewalOnly {
			if len(updates) == 0 {
				return nil
			}
			return tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(updates).Error
		}
		// An active status alone is not payment proof. In particular, a
		// provider can deliver an old/same-period "active" update after a
		// payment failure. Treating that update as a recovery would resurrect
		// access without a new billing period. A same-period recovery is allowed
		// only for an explicitly opted-in, evidence-backed payment event; the
		// normal renewal path below still requires a strictly later boundary and
		// is the only path that resets period-scoped usage.
		if status == "active" && currentStatus == "past_due" {
			// Only an explicitly identified renewal event may advance a
			// past_due entitlement. A generic subscription.updated payload can
			// carry a future period boundary even while its invoice is unpaid.
			periodAdvanced := options.RenewalOnly && lifecyclePeriodEnd > sub.EndTime
			samePeriodRecovery := lifecyclePeriodEnd > 0 &&
				lifecyclePeriodEnd == sub.EndTime &&
				evidence != nil && options.RenewalOnly && options.AllowSamePeriodRecovery
			// A scheduled cancellation only describes the end of an existing
			// period; it is never payment evidence and therefore cannot recover a
			// past_due entitlement, even when its boundary happens to be newer.
			if periodEndCancellation || !periodAdvanced && !samePeriodRecovery {
				// Keep the event fence (the provider delivery was observed), but do
				// not change the local entitlement. An older boundary is never a
				// valid recovery, even when payment evidence is present.
				if len(updates) == 0 {
					return nil
				}
				return tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(updates).Error
			}
		}
		currentTime := sub.ProviderLifecycleEventTime
		ignore := false
		if isTerminalSubscriptionLifecycleStatus(currentStatus) &&
			subscriptionLifecycleStatusRank(status) < subscriptionLifecycleStatusRank(currentStatus) {
			ignore = true
		} else if eventTime > 0 && currentTime > 0 && eventTime < currentTime {
			ignore = true
		} else if eventTime == 0 {
			// An un-timestamped legacy delivery cannot prove that it is newer.
			// Permit an initial active -> past_due transition, a terminal
			// transition, or an active renewal with a strictly later period end.
			// A timestamp-less active event can never recover a past_due/terminal
			// subscription because its freshness cannot be proven.
			canApply := currentStatus == "" ||
				(status == "past_due" && currentStatus == "active") ||
				(isTerminalSubscriptionLifecycleStatus(status) &&
					subscriptionLifecycleStatusRank(status) >= subscriptionLifecycleStatusRank(currentStatus)) ||
				(status == "active" && currentStatus == "active" && periodEnd > sub.EndTime)
			if !canApply && status != currentStatus {
				ignore = true
			}
		} else if eventTime > 0 && currentTime == eventTime &&
			subscriptionLifecycleStatusRank(status) < subscriptionLifecycleStatusRank(currentStatus) {
			ignore = true
		}
		if ignore {
			if len(updates) == 0 {
				return nil
			}
			return tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(updates).Error
		}

		updates["status"] = status
		if eventTime > 0 && (currentTime == 0 || eventTime >= currentTime) {
			updates["provider_lifecycle_event_time"] = eventTime
		}
		if status == "active" && lifecyclePeriodEnd > 0 {
			if periodEndCancellation {
				// Cancellation shortens (or, for a legacy unlimited row,
				// establishes) the access window. It is not a renewal, so quota
				// already consumed in the current period must be preserved. Never
				// extend a locally recorded boundary: provider cancellation data can
				// be stale or refer to a different billing period, and an extension
				// would grant access beyond the entitlement promised at checkout.
				if sub.EndTime == 0 || lifecyclePeriodEnd < sub.EndTime {
					updates["end_time"] = lifecyclePeriodEnd
				}
			} else if lifecyclePeriodEnd > sub.EndTime {
				// A successful renewal advances the period and resets the
				// period-scoped quota. Keep the historical behaviour for normal
				// active events.
				updates["end_time"] = lifecyclePeriodEnd
				updates["amount_used"] = int64(0)
			}
		}
		if isTerminalSubscriptionLifecycleStatus(status) {
			// The normal active-subscription queries intentionally exclude
			// cancelled/expired rows. End the local access window as well so a
			// provider-side revoke/refund cannot leave a future-dated entitlement
			// visible indefinitely, and apply the same group downgrade used by the
			// admin/expiry paths.
			now := common.GetTimestamp()
			if sub.EndTime == 0 || sub.EndTime > now {
				updates["end_time"] = now
			}
			target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
			if err != nil {
				return err
			}
			if target != "" {
				refreshGroupUserID = sub.UserId
			}
		}
		return tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).Updates(updates).Error
	})
	if err != nil {
		return err
	}
	if refreshGroupUserID > 0 {
		refreshSubscriptionUserGroupCache(refreshGroupUserID, "subscription lifecycle terminal transition")
	}
	return nil
}

func validateSubscriptionLifecycleEvidenceTx(tx *gorm.DB, provider, providerSubscriptionID string, evidence SubscriptionLifecycleEvidence) error {
	if tx == nil {
		return ErrProviderSettlementInvalid
	}
	evidence.Amount = strings.TrimSpace(evidence.Amount)
	evidence.Currency = strings.ToUpper(strings.TrimSpace(evidence.Currency))
	evidence.ProductID = strings.TrimSpace(evidence.ProductID)
	if evidence.Amount == "" || evidence.Currency == "" || evidence.ProductID == "" {
		return ErrProviderSnapshotMissing
	}
	if amount, err := decimal.NewFromString(evidence.Amount); err != nil || !amount.IsPositive() {
		return ErrProviderSettlementInvalid
	}
	var orders []SubscriptionOrder
	if err := lockForUpdate(tx).
		Where("payment_provider = ? AND provider_subscription_id = ? AND status = ?", provider, providerSubscriptionID, common.TopUpStatusSuccess).
		Find(&orders).Error; err != nil {
		return err
	}
	if len(orders) == 0 {
		return ErrProviderSnapshotMissing
	}
	if len(orders) > 1 {
		return ErrProviderEventConflict
	}
	order := &orders[0]
	if strings.TrimSpace(order.ProviderAmount) == "" || strings.TrimSpace(order.ProviderCurrency) == "" || strings.TrimSpace(order.ProviderProductID) == "" {
		return ErrProviderSnapshotMissing
	}
	if !compareProviderAmount(order.ProviderAmount, evidence.Amount) ||
		strings.ToUpper(strings.TrimSpace(order.ProviderCurrency)) != evidence.Currency ||
		strings.TrimSpace(order.ProviderProductID) != evidence.ProductID {
		return ErrProviderSnapshotMismatch
	}
	// The entitlement snapshot is itself validated here so a hand-edited or
	// partially migrated successful order cannot become the source of recurring
	// access merely because its provider columns happen to match.
	if _, err := order.GetEntitlementSnapshot(); err != nil {
		return err
	}
	return nil
}

func (s *UserSubscription) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	s.CreatedAt = now
	s.UpdatedAt = now
	return nil
}

func (s *UserSubscription) BeforeUpdate(tx *gorm.DB) error {
	s.UpdatedAt = common.GetTimestamp()
	return nil
}

type SubscriptionSummary struct {
	Subscription *UserSubscription `json:"subscription"`
}

type SubscriptionResetResult struct {
	PlanId           int    `json:"plan_id"`
	MatchedCount     int    `json:"matched_count"`
	ResetCount       int    `json:"reset_count"`
	UserCount        int    `json:"user_count"`
	AdvanceResetTime bool   `json:"advance_reset_time"`
	PlanTitle        string `json:"-"`
	AffectedUserIds  []int  `json:"-"`
}

func calcPlanEndTime(start time.Time, plan *SubscriptionPlan) (int64, error) {
	if plan == nil {
		return 0, errors.New("plan is nil")
	}
	if plan.DurationValue <= 0 && plan.DurationUnit != SubscriptionDurationCustom {
		return 0, errors.New("duration_value must be > 0")
	}
	switch plan.DurationUnit {
	case SubscriptionDurationYear:
		return start.AddDate(plan.DurationValue, 0, 0).Unix(), nil
	case SubscriptionDurationMonth:
		return start.AddDate(0, plan.DurationValue, 0).Unix(), nil
	case SubscriptionDurationDay:
		return start.Add(time.Duration(plan.DurationValue) * 24 * time.Hour).Unix(), nil
	case SubscriptionDurationHour:
		return start.Add(time.Duration(plan.DurationValue) * time.Hour).Unix(), nil
	case SubscriptionDurationCustom:
		if plan.CustomSeconds <= 0 {
			return 0, errors.New("custom_seconds must be > 0")
		}
		return start.Add(time.Duration(plan.CustomSeconds) * time.Second).Unix(), nil
	default:
		return 0, fmt.Errorf("invalid duration_unit: %s", plan.DurationUnit)
	}
}

func NormalizeResetPeriod(period string) string {
	switch strings.TrimSpace(period) {
	case SubscriptionResetDaily, SubscriptionResetWeekly, SubscriptionResetMonthly, SubscriptionResetCustom:
		return strings.TrimSpace(period)
	default:
		return SubscriptionResetNever
	}
}

func calcNextResetTime(base time.Time, plan *SubscriptionPlan, endUnix int64) int64 {
	if plan == nil {
		return 0
	}
	period := NormalizeResetPeriod(plan.QuotaResetPeriod)
	if period == SubscriptionResetNever {
		return 0
	}
	var next time.Time
	switch period {
	case SubscriptionResetDaily:
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, 1)
	case SubscriptionResetWeekly:
		// Align to next Monday 00:00
		weekday := int(base.Weekday()) // Sunday=0
		// Convert to Monday=1..Sunday=7
		if weekday == 0 {
			weekday = 7
		}
		daysUntil := 8 - weekday
		next = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, base.Location()).
			AddDate(0, 0, daysUntil)
	case SubscriptionResetMonthly:
		// Align to first day of next month 00:00
		next = time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, base.Location()).
			AddDate(0, 1, 0)
	case SubscriptionResetCustom:
		if plan.QuotaResetCustomSeconds <= 0 {
			return 0
		}
		next = base.Add(time.Duration(plan.QuotaResetCustomSeconds) * time.Second)
	default:
		return 0
	}
	if endUnix > 0 && next.Unix() > endUnix {
		return 0
	}
	return next.Unix()
}

func GetSubscriptionPlanById(id int) (*SubscriptionPlan, error) {
	// This entry point is used by checkout handlers.  A checkout must snapshot
	// the currently committed price/product/enabled state, so do not serve a
	// potentially stale process/Redis cache value here.  Internal read-heavy
	// paths may call getSubscriptionPlanByIdTx(nil, id) explicitly when a cache
	// trade-off is acceptable.
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	return getSubscriptionPlanByIdTx(DB, id)
}

func getSubscriptionPlanByIdTx(tx *gorm.DB, id int) (*SubscriptionPlan, error) {
	if id <= 0 {
		return nil, errors.New("invalid plan id")
	}
	// A transaction must always read through its own handle.  Looking in the
	// process/Redis cache first can return a value from before the transaction
	// started (or from another transaction altogether), so callers such as the
	// balance purchase and quota-reset paths would apply stale price, enabled,
	// or entitlement settings.  Worse, caching the result of a transactional
	// query would publish uncommitted data that survives a rollback.  The cache
	// is therefore intentionally limited to non-transactional reads below.
	key := subscriptionPlanCacheKey(id)
	if tx == nil && key != "" {
		if cached, found, err := getSubscriptionPlanCache().Get(key); err == nil && found {
			cached.NormalizeDefaults()
			return &cached, nil
		}
	}
	var plan SubscriptionPlan
	query := DB
	if tx != nil {
		query = tx
	}
	if query == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	if err := query.Where("id = ?", id).First(&plan).Error; err != nil {
		return nil, err
	}
	plan.NormalizeDefaults()
	if tx == nil {
		_ = getSubscriptionPlanCache().SetWithTTL(key, plan, subscriptionPlanCacheTTL())
	}
	return &plan, nil
}

func CountUserSubscriptionsByPlan(userId int, planId int) (int64, error) {
	if userId <= 0 || planId <= 0 {
		return 0, errors.New("invalid userId or planId")
	}
	var count int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", userId, planId).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func getUserGroupByIdTx(tx *gorm.DB, userId int) (string, error) {
	if userId <= 0 {
		return "", errors.New("invalid userId")
	}
	if tx == nil {
		tx = DB
	}
	var group string
	if err := lockForUpdate(tx).Model(&User{}).Where("id = ?", userId).Select(commonGroupCol).Find(&group).Error; err != nil {
		return "", err
	}
	return group, nil
}

func downgradeUserGroupForSubscriptionTx(tx *gorm.DB, sub *UserSubscription, now int64) (string, error) {
	if tx == nil || sub == nil {
		return "", errors.New("invalid downgrade args")
	}
	downgradeGroup := strings.TrimSpace(sub.DowngradeGroup)
	upgradeGroup := strings.TrimSpace(sub.UpgradeGroup)
	// Nothing to do if neither an explicit downgrade target nor an upgrade snapshot exists.
	if downgradeGroup == "" && upgradeGroup == "" {
		return "", nil
	}
	currentGroup, err := getUserGroupByIdTx(tx, sub.UserId)
	if err != nil {
		return "", err
	}
	// If another active upgraded subscription exists, keep the current group.
	var activeSub UserSubscription
	activeQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND id <> ? AND upgrade_group <> ''",
		sub.UserId, "active", now, sub.Id).
		Order("end_time desc, id desc").
		Limit(1).
		Find(&activeSub)
	if activeQuery.Error == nil && activeQuery.RowsAffected > 0 {
		return "", nil
	}
	// Determine the downgrade target: an explicit downgrade group takes precedence,
	// otherwise revert to the group held before purchase (legacy behavior).
	target := downgradeGroup
	if target == "" {
		// Legacy behavior: only revert when the subscription actually elevated the user.
		if currentGroup != upgradeGroup {
			return "", nil
		}
		target = strings.TrimSpace(sub.PrevUserGroup)
	}
	if target == "" || target == currentGroup {
		return "", nil
	}
	if err := tx.Model(&User{}).Where("id = ?", sub.UserId).
		Update("group", target).Error; err != nil {
		return "", err
	}
	return target, nil
}

func CreateUserSubscriptionFromPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string) (*UserSubscription, error) {
	return createUserSubscriptionFromPlanTx(tx, userId, plan, source, "")
}

// createUserSubscriptionFromPlanTx is the order-aware variant used by paid
// settlement. Keeping the historical public helper's signature avoids forcing
// admin/balance callers to invent an order identity.
func createUserSubscriptionFromPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, source string, orderTradeNo string) (*UserSubscription, error) {
	if tx == nil {
		return nil, errors.New("tx is nil")
	}
	if plan == nil || plan.Id == 0 {
		return nil, errors.New("invalid plan")
	}
	if userId <= 0 {
		return nil, errors.New("invalid user id")
	}
	// This helper is also exposed through CreateUserSubscriptionFromPlanTx and
	// may be called by a future code path that does not know the surrounding
	// billing protocol. Acquire the user fence at the boundary so the purchase
	// cap check and any group upgrade below always follow user -> subscription.
	// Existing callers already hold this lock; row locks are re-entrant within a
	// transaction on the supported databases.
	var user User
	if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&user).Error; err != nil {
		return nil, err
	}
	if plan.MaxPurchasePerUser > 0 {
		var count int64
		if err := tx.Model(&UserSubscription{}).
			Where("user_id = ? AND plan_id = ?", userId, plan.Id).
			Count(&count).Error; err != nil {
			return nil, err
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			return nil, fmt.Errorf("%w: 已达到该套餐购买上限", ErrSubscriptionPurchaseLimitExceeded)
		}
	}
	nowUnix := getDBTimestampFrom(tx)
	now := time.Unix(nowUnix, 0)
	endUnix, err := calcPlanEndTime(now, plan)
	if err != nil {
		return nil, err
	}
	resetBase := now
	nextReset := calcNextResetTime(resetBase, plan, endUnix)
	lastReset := int64(0)
	if nextReset > 0 {
		lastReset = now.Unix()
	}
	upgradeGroup := strings.TrimSpace(plan.UpgradeGroup)
	prevGroup := ""
	if upgradeGroup != "" {
		currentGroup, err := getUserGroupByIdTx(tx, userId)
		if err != nil {
			return nil, err
		}
		if currentGroup != upgradeGroup {
			prevGroup = currentGroup
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("group", upgradeGroup).Error; err != nil {
				return nil, err
			}
		}
	}
	allowWalletOverflow := true
	if plan.AllowWalletOverflow != nil {
		allowWalletOverflow = *plan.AllowWalletOverflow
	}
	sub := &UserSubscription{
		UserId:                   userId,
		PlanId:                   plan.Id,
		AmountTotal:              plan.TotalAmount,
		AmountUsed:               0,
		StartTime:                now.Unix(),
		EndTime:                  endUnix,
		Status:                   "active",
		Source:                   source,
		SubscriptionOrderTradeNo: strings.TrimSpace(orderTradeNo),
		QuotaResetPeriod:         NormalizeResetPeriod(plan.QuotaResetPeriod),
		QuotaResetCustomSeconds:  plan.QuotaResetCustomSeconds,
		LastResetTime:            lastReset,
		NextResetTime:            nextReset,
		UpgradeGroup:             upgradeGroup,
		PrevUserGroup:            prevGroup,
		DowngradeGroup:           strings.TrimSpace(plan.DowngradeGroup),
		AllowWalletOverflow:      allowWalletOverflow,
		CreatedAt:                common.GetTimestamp(),
		UpdatedAt:                common.GetTimestamp(),
	}
	if err := tx.Create(sub).Error; err != nil {
		return nil, err
	}
	return sub, nil
}

func refreshSubscriptionUserGroupCache(userId int, operation string) {
	if err := RefreshUserGroupCache(userId); err != nil {
		common.SysError(fmt.Sprintf("failed to refresh user group cache after %s for user %d: %v", operation, userId, err))
	}
}

// applySubscriptionSettlementEvidence copies the already-validated provider
// facts onto the order before any entitlement work is attempted.  Keeping this
// operation ahead of the MaxPurchasePerUser check means a paid-but-uncredited
// order retains enough evidence for reconciliation even when no subscription
// row can be created.
func applySubscriptionSettlementEvidence(order *SubscriptionOrder, providerPayload string, epayCallback *EpayCallback, settlement *ProviderSettlement) {
	if order == nil {
		return
	}
	if strings.TrimSpace(providerPayload) != "" {
		order.ProviderPayload = providerPayload
	}
	if epayCallback != nil {
		if order.ProviderTradeNo == nil {
			providerTradeNo := strings.TrimSpace(epayCallback.ProviderTradeNo)
			if providerTradeNo != "" {
				order.ProviderTradeNo = &providerTradeNo
			}
		}
		return
	}
	if settlement == nil {
		return
	}
	providerTradeNo := strings.TrimSpace(settlement.ProviderTradeNo)
	if providerTradeNo != "" {
		order.ProviderTradeNo = &providerTradeNo
	}
	order.ProviderEventID = firstNonEmptyProviderValue(order.ProviderEventID, settlement.ProviderEventID, settlement.ProviderTradeNo)
	order.ProviderProductID = firstNonEmptyProviderValue(order.ProviderProductID, settlement.ProductID)
	order.ProviderStoreID = firstNonEmptyProviderValue(order.ProviderStoreID, settlement.StoreID)
	order.ProviderCheckoutID = firstNonEmptyProviderValue(order.ProviderCheckoutID, settlement.CheckoutID)
	order.ProviderSubscriptionID = firstNonEmptyProviderValue(order.ProviderSubscriptionID, settlement.ProviderSubscriptionID)
	order.ProviderCurrency = firstNonEmptyProviderValue(order.ProviderCurrency, settlement.Currency)
	order.ProviderMerchantID = firstNonEmptyProviderValue(order.ProviderMerchantID, settlement.MerchantID)
	order.ProviderAmount = firstNonEmptyProviderValue(order.ProviderAmount, settlement.Amount)
}

func isSubscriptionOrderPaidUncredited(order *SubscriptionOrder) bool {
	return order != nil && strings.EqualFold(strings.TrimSpace(order.Status), SubscriptionOrderStatusPaidUncredited)
}

// CompleteSubscriptionOrderEpay settles a strictly verified EPay callback.
// Callback fields are checked against the immutable order snapshot inside the
// same transaction that grants the subscription.
func CompleteSubscriptionOrderEpay(callback EpayCallback) error {
	_, err := CompleteSubscriptionOrderEpayWithOutcome(callback)
	return err
}

func CompleteSubscriptionOrderEpayWithOutcome(callback EpayCallback) (EpaySettlementOutcome, error) {
	if err := callback.validate(); err != nil {
		return EpaySettlementCompleted, err
	}
	if callback.TradeStatus != "TRADE_SUCCESS" {
		return EpaySettlementCompleted, ErrEpayCallbackInvalid
	}
	return completeSubscriptionOrder(callback.ServiceTradeNo, callback.RawPayload, PaymentProviderEpay, callback.PaymentMethod, &callback, nil)
}

// Complete a subscription order (idempotent). Creates a UserSubscription snapshot from the plan.
// expectedPaymentProvider guards against cross-gateway callback attacks (empty skips the check).
// actualPaymentMethod is an assertion about the local checkout method; it is
// never allowed to overwrite a persisted method.
func CompleteSubscriptionOrder(tradeNo string, providerPayload string, expectedPaymentProvider string, actualPaymentMethod string) error {
	_, err := completeSubscriptionOrder(tradeNo, providerPayload, expectedPaymentProvider, actualPaymentMethod, nil, nil)
	return err
}

// CompleteSubscriptionOrderVerified settles a provider callback only after
// comparing it with the immutable checkout snapshot. This is the entry point
// for Stripe, Creem, Waffo, and Pancake webhooks.
func CompleteSubscriptionOrderVerified(settlement ProviderSettlement) error {
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(settlement)
	return err
}

// CompleteSubscriptionOrderVerifiedWithOutcome is the webhook-facing variant
// that distinguishes a normal grant from a durable paid_uncredited terminal
// state.  A paid_uncredited outcome returns a nil error so signed provider
// callbacks can be ACKed after the transaction commits; operators can later
// call RetryPaidUncreditedSubscriptionOrder or issue a refund.
func CompleteSubscriptionOrderVerifiedWithOutcome(settlement ProviderSettlement) (EpaySettlementOutcome, error) {
	settlement = settlement.normalized()
	return completeSubscriptionOrder(settlement.OrderTradeNo, settlement.Payload, settlement.Provider, "", nil, &settlement)
}

func completeSubscriptionOrder(tradeNo string, providerPayload string, expectedPaymentProvider string, actualPaymentMethod string, epayCallback *EpayCallback, providerSettlement *ProviderSettlement) (outcome EpaySettlementOutcome, err error) {
	if tradeNo == "" {
		return EpaySettlementCompleted, errors.New("tradeNo is empty")
	}
	if providerSettlement != nil {
		if providerSettlement.OrderTradeNo != tradeNo {
			return EpaySettlementCompleted, ErrProviderSnapshotMismatch
		}
		if err := providerSettlement.validate(); err != nil {
			return EpaySettlementCompleted, err
		}
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	var logUserId int
	var logPlanTitle string
	var logMoney float64
	var logPaymentMethod string
	var upgradeGroup string
	err = DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			// Keep transient database/driver errors visible to webhook callers;
			// collapsing them into "not found" can lose a valid notification.
			return err
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if providerSettlement != nil {
			if order.PaymentProvider != providerSettlement.Provider {
				return ErrPaymentMethodMismatch
			}
			if order.Status == common.TopUpStatusPending {
				if err := ValidateSubscriptionProviderSnapshot(&order, *providerSettlement); err != nil {
					return err
				}
				if err := ensureProviderTradeNoAvailableTx(tx, providerSettlement.Provider, providerSettlement.ProviderTradeNo, tradeNo); err != nil {
					return err
				}
				if err := bindPaymentEventTx(tx, providerSettlement.Provider, providerSettlement.ProviderTradeNo, tradeNo, PaymentEventOrderSubscription, providerSettlement.Payload); err != nil {
					return err
				}
			} else if order.Status == common.TopUpStatusSuccess {
				// Idempotency must not weaken provider verification.  Every
				// delivery, including a replay after the order was completed,
				// is compared with the immutable amount/currency/product/
				// merchant/checkout snapshot before it is acknowledged.
				if err := ValidateSubscriptionProviderSnapshot(&order, *providerSettlement); err != nil {
					return err
				}
				if err := ensureProviderTradeNoAvailableTx(tx, providerSettlement.Provider, providerSettlement.ProviderTradeNo, tradeNo); err != nil {
					return err
				}
				if order.ProviderTradeNo == nil || strings.TrimSpace(*order.ProviderTradeNo) != providerSettlement.ProviderTradeNo {
					return ErrProviderEventConflict
				}
				if strings.TrimSpace(order.ProviderSubscriptionID) != "" && providerSettlement.ProviderSubscriptionID != "" &&
					strings.TrimSpace(order.ProviderSubscriptionID) != providerSettlement.ProviderSubscriptionID {
					return ErrProviderEventConflict
				}
				if strings.TrimSpace(order.ProviderSubscriptionID) == "" && providerSettlement.ProviderSubscriptionID != "" {
					if err := bindSubscriptionProviderIDTx(tx, &order, providerSettlement.ProviderSubscriptionID, providerSettlement.Provider); err != nil {
						return err
					}
				}
				if err := bindPaymentEventTx(tx, providerSettlement.Provider, providerSettlement.ProviderTradeNo, tradeNo, PaymentEventOrderSubscription, providerSettlement.Payload); err != nil {
					return err
				}
				if err := bindProviderSettlementPaymentObjectsTx(tx, *providerSettlement, PaymentEventOrderSubscription); err != nil {
					return err
				}
			} else if isSubscriptionOrderPaidUncredited(&order) {
				// The payment fact and provider evidence were committed by the first
				// delivery.  Replays must still pass the immutable snapshot checks,
				// but must not insert another PaymentEvent or attempt entitlement
				// creation.  A nil error makes the signed webhook an idempotent ACK.
				if err := ValidateSubscriptionProviderSnapshot(&order, *providerSettlement); err != nil {
					return err
				}
				if err := ensureProviderTradeNoAvailableTx(tx, providerSettlement.Provider, providerSettlement.ProviderTradeNo, tradeNo); err != nil {
					return err
				}
				if order.ProviderTradeNo == nil || strings.TrimSpace(*order.ProviderTradeNo) != providerSettlement.ProviderTradeNo {
					return ErrProviderEventConflict
				}
				if strings.TrimSpace(order.ProviderSubscriptionID) != "" && providerSettlement.ProviderSubscriptionID != "" &&
					strings.TrimSpace(order.ProviderSubscriptionID) != providerSettlement.ProviderSubscriptionID {
					return ErrProviderEventConflict
				}
				if strings.TrimSpace(order.ProviderSubscriptionID) == "" && providerSettlement.ProviderSubscriptionID != "" {
					// Preserve a recurring identity revealed by a later replay for
					// reconciliation, but never use it to grant a second entitlement.
					if err := bindSubscriptionProviderIDTx(tx, &order, providerSettlement.ProviderSubscriptionID, providerSettlement.Provider); err != nil {
						return err
					}
				}
				// A legacy deployment may have committed the paid_uncredited order
				// before the shared payment-event ledger was installed.  Re-bind the
				// verified transaction on every replay so the idempotency fence is
				// repaired without granting another entitlement.  A conflicting
				// existing event remains a hard failure.
				if err := bindPaymentEventTx(tx, providerSettlement.Provider, providerSettlement.ProviderTradeNo, tradeNo, PaymentEventOrderSubscription, providerSettlement.Payload); err != nil {
					return err
				}
				if err := bindProviderSettlementPaymentObjectsTx(tx, *providerSettlement, PaymentEventOrderSubscription); err != nil {
					return err
				}
				// A first delivery may have created the zero-quota history mirror
				// before the provider exposed its recurring subscription ID.  Replay
				// must reconcile that mirror in the same transaction as the order and
				// event fence; otherwise a later entitlement retry sees a false
				// cross-ledger conflict (or loses the provider identity entirely).
				if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
					return err
				}
			}
		}
		if epayCallback != nil {
			// A callback must be tied to the checkout snapshot created before the
			// user left for EPay. Legacy rows without these fields cannot be
			// authenticated against a mutable plan/order and are therefore held
			// for provider-side reconciliation instead of being auto-completed.
			missingSnapshot := strings.TrimSpace(order.ProviderMerchantID) == "" ||
				strings.TrimSpace(order.ProviderOrderName) == "" ||
				strings.TrimSpace(order.ProviderAmount) == "" ||
				strings.TrimSpace(order.ProviderKeyFingerprint) == ""
			if missingSnapshot {
				if order.Status == common.TopUpStatusSuccess && epayLegacySuccessAckAllowed(tradeNo) {
					if validationErr := validateEpayOrderCallback(order.PaymentMethod, order.Money, nil, "", "", "", "", *epayCallback); validationErr != nil {
						common.SysError(fmt.Sprintf("allowlisted legacy EPay subscription callback mismatch trade_no=%s provider_trade_no=%s error=%v", tradeNo, epayCallback.ProviderTradeNo, validationErr))
					}
					outcome = EpaySettlementLegacySuccessAcknowledged
					return nil
				}
				return ErrEpayOrderSnapshotMissing
			}
			if epayCallback.ServiceTradeNo != tradeNo {
				return ErrEpayCallbackMismatch
			}
			if err := validateEpayOrderCallback(order.PaymentMethod, order.Money, order.ProviderTradeNo, order.ProviderMerchantID, order.ProviderOrderName, order.ProviderAmount, order.ProviderKeyFingerprint, *epayCallback); err != nil {
				return err
			}
			// Re-bind the event on every verified callback.  This is idempotent for
			// the original delivery and repairs a missing ledger row on a
			// paid_uncredited order left by an older deployment; a conflicting
			// provider transaction still fails closed.
			if err := bindPaymentEventTx(tx, PaymentProviderEpay, epayCallback.ProviderTradeNo, tradeNo, PaymentEventOrderSubscription, epayCallback.RawPayload); err != nil {
				return err
			}
		}
		if actualPaymentMethod != "" && order.PaymentMethod != actualPaymentMethod {
			return ErrPaymentMethodMismatch
		}
		if order.Status == common.TopUpStatusSuccess {
			if epayCallback != nil {
				if order.ProviderTradeNo == nil || strings.TrimSpace(*order.ProviderTradeNo) != epayCallback.ProviderTradeNo {
					return ErrEpayProviderTradeConflict
				}
			}
			outcome = EpaySettlementAlreadyCompleted
			return nil
		}
		if isSubscriptionOrderPaidUncredited(&order) {
			if epayCallback != nil && (order.ProviderTradeNo == nil || strings.TrimSpace(*order.ProviderTradeNo) != epayCallback.ProviderTradeNo) {
				return ErrEpayProviderTradeConflict
			}
			outcome = EpaySettlementPaidUncredited
			return nil
		}
		if order.Status != common.TopUpStatusPending {
			return ErrSubscriptionOrderStatusInvalid
		}
		entitlement, err := order.GetEntitlementSnapshot()
		if err != nil {
			return err
		}
		plan, err := entitlement.ToPlan()
		if err != nil {
			return err
		}
		if plan.Id != order.PlanId {
			return ErrSubscriptionEntitlementSnapshotInvalid
		}
		// Persist authenticated provider facts before entitlement creation. If
		// the purchase cap rejects the grant, this transaction records a durable
		// paid_uncredited order rather than rolling back to pending.
		applySubscriptionSettlementEvidence(&order, providerPayload, epayCallback, providerSettlement)
		// 锁定用户行：并发完成同一用户的不同订单（包括多实例部署下）时，
		// 使 CreateUserSubscriptionFromPlanTx 的 MaxPurchasePerUser 检查按用户串行。
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", order.UserId).First(&userRow).Error; err != nil {
			return err
		}
		subscription, err := createUserSubscriptionFromPlanTx(tx, order.UserId, plan, "order", order.TradeNo)
		if err != nil {
			if errors.Is(err, ErrSubscriptionPurchaseLimitExceeded) && (providerSettlement != nil || epayCallback != nil) {
				// A durable paid_uncredited state is valid only after an
				// authenticated provider callback.  Internal/legacy callers that
				// invoke CompleteSubscriptionOrder without provider evidence have
				// not established payment and must receive the original limit error
				// so an unpaid order is never mislabeled as paid.
				if providerSettlement == nil && epayCallback == nil {
					return err
				}
				order.Status = SubscriptionOrderStatusPaidUncredited
				order.SettlementResolution = SubscriptionSettlementResolutionRefundRequired
				order.SettlementErrorCode = "max_purchase_per_user"
				order.CompleteTime = common.GetTimestamp()
				if providerSettlement != nil {
					if bindingErr := bindProviderSettlementPaymentObjectsTx(tx, *providerSettlement, PaymentEventOrderSubscription); bindingErr != nil {
						return bindingErr
					}
				}
				if mirrorErr := upsertSubscriptionTopUpTx(tx, &order); mirrorErr != nil {
					// A legacy cross-ledger collision must not erase the durable paid
					// order state. Keep the original provider evidence on the order and
					// let reconciliation handle the missing history mirror; genuine DB
					// failures remain retryable.
					if !errors.Is(mirrorErr, ErrSubscriptionOrderConflict) {
						return mirrorErr
					}
					common.SysError(fmt.Sprintf("subscription paid_uncredited mirror conflict trade_no=%s error=%v", order.TradeNo, mirrorErr))
				}
				if saveErr := tx.Save(&order).Error; saveErr != nil {
					return saveErr
				}
				outcome = EpaySettlementPaidUncredited
				logUserId = order.UserId
				logPlanTitle = plan.Title
				logMoney = order.Money
				logPaymentMethod = order.PaymentMethod
				return nil
			}
			return err
		}
		if providerSettlement != nil && strings.TrimSpace(providerSettlement.ProviderSubscriptionID) != "" {
			if err := bindSubscriptionProviderIDTx(tx, &order,
				strings.TrimSpace(providerSettlement.ProviderSubscriptionID),
				strings.TrimSpace(providerSettlement.Provider)); err != nil {
				return err
			}
			// bindSubscriptionProviderIDTx updates the persisted row and the
			// in-memory entitlement copy used below.
			subscription.ProviderSubscriptionID = strings.TrimSpace(providerSettlement.ProviderSubscriptionID)
			subscription.ProviderSubscriptionProvider = strings.TrimSpace(providerSettlement.Provider)
		}
		if providerSettlement != nil {
			if err := bindProviderSettlementPaymentObjectsTx(tx, *providerSettlement, PaymentEventOrderSubscription); err != nil {
				return err
			}
		}
		if subscription.PrevUserGroup != "" {
			upgradeGroup = strings.TrimSpace(subscription.UpgradeGroup)
		}
		if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
			return err
		}
		order.Status = common.TopUpStatusSuccess
		order.SettlementResolution = ""
		order.SettlementErrorCode = ""
		order.CompleteTime = common.GetTimestamp()
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		logUserId = order.UserId
		logPlanTitle = plan.Title
		logMoney = order.Money
		logPaymentMethod = order.PaymentMethod
		return nil
	})
	if err != nil {
		return EpaySettlementCompleted, err
	}
	if outcome == EpaySettlementLegacySuccessAcknowledged {
		common.SysError(fmt.Sprintf("allowlisted legacy EPay subscription callback acknowledged without settlement trade_no=%s provider_trade_no=%s", tradeNo, epayCallback.ProviderTradeNo))
		return outcome, nil
	}
	if outcome == EpaySettlementAlreadyCompleted {
		return outcome, nil
	}
	if outcome == EpaySettlementPaidUncredited {
		common.SysError(fmt.Sprintf("subscription payment authenticated but entitlement remains uncredited trade_no=%s user_id=%d resolution=%s error_code=%s", tradeNo, logUserId, SubscriptionSettlementResolutionRefundRequired, "max_purchase_per_user"))
		return outcome, nil
	}
	if upgradeGroup != "" && logUserId > 0 {
		refreshSubscriptionUserGroupCache(logUserId, "subscription payment completion")
	}
	if logUserId > 0 {
		msg := fmt.Sprintf("订阅购买成功，套餐: %s，支付金额: %.2f，支付方式: %s", logPlanTitle, logMoney, logPaymentMethod)
		RecordLog(logUserId, LogTypeTopup, msg)
	}
	return EpaySettlementCompleted, nil
}

// RetryPaidUncreditedSubscriptionOrder retries only the entitlement side of a
// payment that was already authenticated and durably recorded.  It never
// re-validates or replays provider money, and it never changes a pending order
// into a paid order.  The order and user rows are locked together so an admin
// retry cannot race a concurrent grant for the same user/plan.
//
// ErrSubscriptionPurchaseLimitExceeded is returned when the cap is still
// exhausted; the order remains paid_uncredited and can be retried later (or
// refunded according to SettlementResolution).
func RetryPaidUncreditedSubscriptionOrder(tradeNo string) error {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return ErrSubscriptionOrderNotFound
	}
	if DB == nil {
		return fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	var (
		logUserID int
		logTitle  string
	)
	err := DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if order.Status == common.TopUpStatusSuccess {
			return nil
		}
		if !isSubscriptionOrderPaidUncredited(&order) {
			return ErrSubscriptionOrderStatusInvalid
		}
		entitlement, err := order.GetEntitlementSnapshot()
		if err != nil {
			return err
		}
		plan, err := entitlement.ToPlan()
		if err != nil {
			return err
		}
		if plan.Id != order.PlanId {
			return ErrSubscriptionEntitlementSnapshotInvalid
		}
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", order.UserId).First(&userRow).Error; err != nil {
			return err
		}

		// A crash cannot leave a committed entitlement and an uncommitted order
		// update because both operations are in one transaction, but inspect the
		// order link defensively so an operator can safely retry after a legacy
		// partial repair.
		var linked []UserSubscription
		if err := lockForUpdate(tx).Where("subscription_order_trade_no = ?", tradeNo).Find(&linked).Error; err != nil {
			return err
		}
		if len(linked) > 1 {
			return ErrProviderEventConflict
		}
		var subscription *UserSubscription
		if len(linked) == 1 {
			subscription = &linked[0]
			if subscription.UserId != order.UserId || subscription.PlanId != order.PlanId {
				// The order link is a durable accounting identity.  If a legacy
				// repair attached a row belonging to another user/plan, fail closed
				// rather than marking the paid order successful for the wrong
				// entitlement.
				return ErrProviderEventConflict
			}
		} else {
			subscription, err = createUserSubscriptionFromPlanTx(tx, order.UserId, plan, "order", order.TradeNo)
			if err != nil {
				return err
			}
		}
		if strings.TrimSpace(order.ProviderSubscriptionID) != "" {
			// Route every recurring identity mutation through the same ledger
			// primitive used by webhook settlement.  A direct UPDATE here would
			// bypass the provider+ID and one-entitlement uniqueness fences, so an
			// operator retry could silently steal an identity already bound to a
			// different entitlement (or leave the denormalized columns ahead of the
			// durable ledger).
			if err := bindSubscriptionProviderIDTx(tx, &order, strings.TrimSpace(order.ProviderSubscriptionID), strings.TrimSpace(order.PaymentProvider)); err != nil {
				return err
			}
		}
		if err := upsertSubscriptionTopUpTx(tx, &order); err != nil {
			return err
		}
		order.Status = common.TopUpStatusSuccess
		order.SettlementResolution = ""
		order.SettlementErrorCode = ""
		if order.CompleteTime == 0 {
			order.CompleteTime = common.GetTimestamp()
		}
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		logUserID = order.UserId
		logTitle = plan.Title
		return nil
	})
	if err != nil {
		return err
	}
	if logUserID > 0 {
		refreshSubscriptionUserGroupCache(logUserID, "paid_uncredited subscription retry")
		common.SysLog(fmt.Sprintf("paid_uncredited subscription entitlement restored trade_no=%s user_id=%d plan=%s", tradeNo, logUserID, logTitle))
	}
	return nil
}

// bindSubscriptionProviderIDTx repairs the common provider-expansion race:
// some checkout events settle before the webhook payload includes the
// recurring subscription object, while a later replay does include it.  The
// order and its exact entitlement are linked by SubscriptionOrderTradeNo, so
// the ID can be filled without guessing among a user's other subscriptions.
// Legacy rows without that link are left untouched and can be reconciled by
// an operator; silently claiming an arbitrary same-plan row would be unsafe.
func bindSubscriptionProviderIDTx(tx *gorm.DB, order *SubscriptionOrder, providerSubscriptionID, provider string) error {
	if tx == nil || order == nil {
		return ErrProviderSettlementInvalid
	}
	providerSubscriptionID = strings.TrimSpace(providerSubscriptionID)
	provider = strings.TrimSpace(provider)
	if providerSubscriptionID == "" || provider == "" {
		return ErrProviderSettlementInvalid
	}
	if existing := strings.TrimSpace(order.ProviderSubscriptionID); existing != "" {
		if existing != providerSubscriptionID {
			return ErrProviderEventConflict
		}
	}
	var matches []UserSubscription
	query := lockForUpdate(tx).
		Where("subscription_order_trade_no = ?", strings.TrimSpace(order.TradeNo)).
		Find(&matches)
	if query.Error != nil {
		return query.Error
	}
	if len(matches) > 1 {
		return ErrProviderEventConflict
	}
	if len(matches) == 1 {
		// The ledger's unique identity/entitlement fences are the authoritative
		// concurrency guard. Keep this call inside the caller's transaction so
		// order, entitlement, and binding commit or roll back together.
		return bindSubscriptionProviderIDLedgerTx(tx, order, &matches[0], providerSubscriptionID, provider)
	}
	// There is no order-linked entitlement to carry the identity yet (the
	// paid_uncredited/legacy reconciliation case).  We may retain the provider
	// ID on the order for operator visibility, but only after proving that no
	// existing ledger or raw legacy row already claims the same provider/id.  A
	// blind order update would create a second claim that later retries could not
	// reconcile deterministically.
	if err := ensureUnlinkedProviderSubscriptionIDAvailableTx(tx, providerSubscriptionID, provider); err != nil {
		return err
	}
	// Persist the order identity even when the entitlement is a legacy row that
	// cannot be safely linked. This gives reconciliation a durable provider
	// reference without granting or moving access.
	order.ProviderSubscriptionID = providerSubscriptionID
	return tx.Model(&SubscriptionOrder{}).Where("id = ?", order.Id).
		Update("provider_subscription_id", providerSubscriptionID).Error
}

// ensureUnlinkedProviderSubscriptionIDAvailableTx checks the identity fence
// before bindSubscriptionProviderIDTx records a provider ID on an order that
// has no linked entitlement yet.  Distinct providers may legitimately reuse an
// opaque ID, but an unnamespaced/same-provider legacy row is ambiguous and must
// block the write.  A ledger row is always authoritative; even a dangling row
// is a conflict requiring reconciliation rather than a reason to overwrite it.
func ensureUnlinkedProviderSubscriptionIDAvailableTx(tx *gorm.DB, providerSubscriptionID, provider string) error {
	if tx == nil {
		return ErrProviderSettlementInvalid
	}
	provider = normalizeSubscriptionBindingProvider(provider)
	providerSubscriptionID = strings.TrimSpace(providerSubscriptionID)
	key, ok := SubscriptionProviderBindingKey(provider, providerSubscriptionID)
	if !ok {
		return ErrProviderSettlementInvalid
	}
	var binding SubscriptionProviderBindingRecord
	if err := lockForUpdate(tx).Where("provider_subscription_key = ?", key).First(&binding).Error; err == nil {
		return ErrProviderEventConflict
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var rawMatches []UserSubscription
	if err := lockForUpdate(tx).
		Where("provider_subscription_id = ?", providerSubscriptionID).
		Find(&rawMatches).Error; err != nil {
		return err
	}
	for _, candidate := range rawMatches {
		candidateProvider := normalizeSubscriptionBindingProvider(candidate.ProviderSubscriptionProvider)
		if candidateProvider == "" || candidateProvider == provider {
			return ErrProviderEventConflict
		}
	}
	return nil
}

func upsertSubscriptionTopUpTx(tx *gorm.DB, order *SubscriptionOrder) error {
	if tx == nil || order == nil {
		return errors.New("invalid subscription order")
	}
	now := common.GetTimestamp()
	var topup TopUp
	if err := lockForUpdate(tx).Where("trade_no = ?", order.TradeNo).First(&topup).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			topup = TopUp{
				UserId:                 order.UserId,
				Amount:                 0,
				Money:                  order.Money,
				TradeNo:                order.TradeNo,
				PaymentMethod:          order.PaymentMethod,
				PaymentProvider:        order.PaymentProvider,
				CreateTime:             order.CreateTime,
				CompleteTime:           now,
				Status:                 common.TopUpStatusSuccess,
				ProviderTradeNo:        order.ProviderTradeNo,
				ProviderSubscriptionID: order.ProviderSubscriptionID,
				ProviderMerchantID:     order.ProviderMerchantID,
				ProviderOrderName:      order.ProviderOrderName,
				ProviderAmount:         order.ProviderAmount,
				ProviderProductID:      order.ProviderProductID,
				ProviderStoreID:        order.ProviderStoreID,
				ProviderCheckoutID:     order.ProviderCheckoutID,
				ProviderCurrency:       order.ProviderCurrency,
				ProviderEventID:        order.ProviderEventID,
				ProviderKeyFingerprint: order.ProviderKeyFingerprint,
				ProviderPayload:        order.ProviderPayload,
			}
			return tx.Create(&topup).Error
		}
		return err
	}
	// A top-up row with the same trade number is a separate financial ledger.
	// It may be the exact mirror written by an older deployment, but it must
	// never be overwritten or converted from a pending/other-user order merely
	// to make a subscription visible in history. Validate every immutable
	// identity field before treating an existing success row as an idempotent
	// mirror.
	if topup.UserId != order.UserId || topup.PaymentProvider != order.PaymentProvider || topup.PaymentMethod != order.PaymentMethod ||
		topup.Status != common.TopUpStatusSuccess || topup.CreditedQuota != 0 || !subscriptionMirrorMoneyEqual(topup.Money, order.Money) ||
		!subscriptionMirrorProviderIdentityEqual(&topup, order) {
		return ErrSubscriptionOrderConflict
	}
	// Provider payloads can reveal optional recurring identity fields only on a
	// later webhook.  Fill an empty mirror field, but never overwrite a
	// non-empty value with a different one.  This keeps the history row an
	// immutable evidence mirror while allowing safe forward completion.
	if strings.TrimSpace(topup.ProviderSubscriptionID) == "" && strings.TrimSpace(order.ProviderSubscriptionID) != "" {
		if err := tx.Model(&TopUp{}).Where("id = ?", topup.Id).
			Update("provider_subscription_id", strings.TrimSpace(order.ProviderSubscriptionID)).Error; err != nil {
			return err
		}
	}
	return nil
}

func subscriptionMirrorMoneyEqual(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
		return false
	}
	return decimal.NewFromFloat(a).Equal(decimal.NewFromFloat(b))
}

func subscriptionMirrorProviderIdentityEqual(topup *TopUp, order *SubscriptionOrder) bool {
	if topup == nil || order == nil {
		return false
	}
	if (topup.ProviderTradeNo == nil) != (order.ProviderTradeNo == nil) {
		return false
	}
	if topup.ProviderTradeNo != nil && strings.TrimSpace(*topup.ProviderTradeNo) != strings.TrimSpace(*order.ProviderTradeNo) {
		return false
	}
	// An older mirror may legitimately lack a field that a later verified
	// callback supplies.  That one-way empty -> populated transition is handled
	// by upsertSubscriptionTopUpTx; a conflicting pair remains rejected.
	providerSubscriptionMatches := strings.TrimSpace(topup.ProviderSubscriptionID) == strings.TrimSpace(order.ProviderSubscriptionID) ||
		(strings.TrimSpace(topup.ProviderSubscriptionID) == "" && strings.TrimSpace(order.ProviderSubscriptionID) != "")
	return providerSubscriptionMatches &&
		topup.ProviderMerchantID == order.ProviderMerchantID &&
		topup.ProviderOrderName == order.ProviderOrderName &&
		topup.ProviderAmount == order.ProviderAmount &&
		topup.ProviderProductID == order.ProviderProductID &&
		topup.ProviderStoreID == order.ProviderStoreID &&
		topup.ProviderCheckoutID == order.ProviderCheckoutID &&
		topup.ProviderCurrency == order.ProviderCurrency &&
		topup.ProviderEventID == order.ProviderEventID &&
		topup.ProviderKeyFingerprint == order.ProviderKeyFingerprint &&
		topup.ProviderPayload == order.ProviderPayload
}

func ExpireSubscriptionOrder(tradeNo string, expectedPaymentProvider string) error {
	if tradeNo == "" {
		return errors.New("tradeNo is empty")
	}
	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var order SubscriptionOrder
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSubscriptionOrderNotFound
			}
			return err
		}
		if expectedPaymentProvider != "" && order.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if order.Status != common.TopUpStatusPending {
			return nil
		}
		order.Status = common.TopUpStatusExpired
		order.CompleteTime = common.GetTimestamp()
		return tx.Save(&order).Error
	})
}

// Admin bind (no payment). Creates a UserSubscription from a plan.
func AdminBindSubscription(userId int, planId int, sourceNote string) (string, error) {
	if userId <= 0 || planId <= 0 {
		return "", errors.New("invalid userId or planId")
	}
	// Load the plan through the same transaction that creates the entitlement.
	// Reading it before opening the transaction can return a stale cache entry
	// while an administrator is editing the plan, causing a manual bind to use
	// old quota/duration/group settings.  It also made the operation's plan read
	// and subscription write observe different database snapshots.
	var plan *SubscriptionPlan
	groupChanged := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		loadedPlan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		plan = loadedPlan
		// 与 CompleteSubscriptionOrder 一致：先锁用户行，再做购买次数检查。
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&userRow).Error; err != nil {
			return err
		}
		subscription, err := CreateUserSubscriptionFromPlanTx(tx, userId, loadedPlan, "admin")
		if err == nil {
			groupChanged = subscription.PrevUserGroup != ""
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if groupChanged {
		refreshSubscriptionUserGroupCache(userId, "admin subscription creation")
		return fmt.Sprintf("用户分组将升级到 %s", plan.UpgradeGroup), nil
	}
	return "", nil
}

func calcSubscriptionBalanceQuota(priceAmount float64) (int, error) {
	if priceAmount <= 0 {
		return 0, nil
	}
	quotaPerUnit := common.GetQuotaPerUnit()
	if quotaPerUnit <= 0 {
		return 0, errors.New("额度单位配置错误")
	}
	quota := decimal.NewFromFloat(priceAmount).
		Mul(decimal.NewFromFloat(quotaPerUnit)).
		Ceil()
	return common.WalletQuotaFromDecimalStrict(quota)
}

// PurchaseSubscriptionWithBalance creates a subscription by deducting the user's wallet quota.
func PurchaseSubscriptionWithBalance(userId int, planId int) error {
	if userId <= 0 || planId <= 0 {
		return errors.New("invalid userId or planId")
	}

	var logPlanTitle string
	var logMoney float64
	var chargedQuota int
	var upgradeGroup string
	var quotaCacheMutationID string
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		if !plan.Enabled {
			return errors.New("套餐未启用")
		}
		if plan.PriceAmount < 0 {
			return errors.New("套餐价格不能为负数")
		}
		if plan.AllowBalancePay != nil && !*plan.AllowBalancePay {
			return errors.New("该套餐不允许使用余额兑换")
		}

		requiredQuota, err := calcSubscriptionBalanceQuota(plan.PriceAmount)
		if err != nil {
			return err
		}

		var user User
		if err := lockForUpdate(tx).Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		if requiredQuota > 0 && user.Quota < requiredQuota {
			return errors.New("余额不足")
		}
		if requiredQuota > 0 {
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("quota", gorm.Expr("quota - ?", requiredQuota)).Error; err != nil {
				return err
			}
			quotaCacheMutationID, err = stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityUser, userId, getUserCacheKey(userId))
			if err != nil {
				return err
			}
		}

		subscription, err := CreateUserSubscriptionFromPlanTx(tx, userId, plan, PaymentMethodBalance)
		if err != nil {
			return err
		}

		now := common.GetTimestamp()
		tradeNo := fmt.Sprintf("SUBBALUSR%dNO%s%d", userId, common.GetRandomString(6), time.Now().UnixNano())
		order := &SubscriptionOrder{
			UserId:          userId,
			PlanId:          plan.Id,
			Money:           plan.PriceAmount,
			TradeNo:         tradeNo,
			PaymentMethod:   PaymentMethodBalance,
			PaymentProvider: PaymentProviderBalance,
			Status:          common.TopUpStatusSuccess,
			CreateTime:      now,
			CompleteTime:    now,
			ProviderPayload: fmt.Sprintf("charged_quota=%d", requiredQuota),
		}
		if err := order.SetEntitlementSnapshot(plan); err != nil {
			return err
		}
		if err := tx.Create(order).Error; err != nil {
			return err
		}

		logPlanTitle = plan.Title
		logMoney = plan.PriceAmount
		chargedQuota = requiredQuota
		if subscription.PrevUserGroup != "" {
			upgradeGroup = strings.TrimSpace(subscription.UpgradeGroup)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if chargedQuota > 0 {
		cacheSafe := true
		if err := cacheDecrUserQuota(userId, int64(chargedQuota)); err != nil {
			common.SysLog("failed to decrease user quota cache after subscription balance purchase: " + err.Error())
			// The wallet transaction has already committed.  A cache miss or
			// malformed hash must not leave a stale snapshot authoritative until
			// its normal TTL expires: the next balance read could otherwise show
			// an old (or zero) amount after a successful subscription purchase.
			// Reuse the durable fence/repair path used by the other wallet ledger
			// mutations; it invalidates and rehydrates synchronously when Redis is
			// available, and records a retry marker when it is not.
			if repairErr := repairUserQuotaCache(userId); repairErr != nil {
				cacheSafe = false
				recordQuotaCacheRepair(QuotaCacheRepairEntityUser, userId, getUserCacheKey(userId), repairErr)
			}
		}
		if cacheSafe {
			if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityUser, userId, getUserCacheKey(userId), quotaCacheMutationID); err != nil {
				common.SysLog("failed to complete subscription wallet cache repair: " + err.Error())
			}
		}
	}
	if upgradeGroup != "" {
		refreshSubscriptionUserGroupCache(userId, "subscription balance purchase")
	}
	msg := fmt.Sprintf("使用余额购买订阅成功，套餐: %s，支付金额: %.2f，扣除额度: %d", logPlanTitle, logMoney, chargedQuota)
	RecordLog(userId, LogTypeTopup, msg)
	return nil
}

// GetAllActiveUserSubscriptions returns all active subscriptions for a user.
func GetAllActiveUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var subs []UserSubscription
	err := DB.Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Order("end_time desc, id desc").
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

// HasActiveUserSubscription returns whether the user has any active subscription.
// This is a lightweight existence check to avoid heavy pre-consume transactions.
func HasActiveUserSubscription(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var count int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// UserActiveSubscriptionsAllowWalletOverflow returns whether wallet balance may be used
// after the user's subscription quota is exhausted. A single active subscription that
// disallows wallet overflow (allow_wallet_overflow = false) blocks the fallback.
func UserActiveSubscriptionsAllowWalletOverflow(userId int) (bool, error) {
	if userId <= 0 {
		return false, errors.New("invalid userId")
	}
	now := common.GetTimestamp()
	var strictCount int64
	if err := DB.Model(&UserSubscription{}).
		Where("user_id = ? AND status = ? AND end_time > ? AND allow_wallet_overflow = ?",
			userId, "active", now, false).
		Count(&strictCount).Error; err != nil {
		return false, err
	}
	return strictCount == 0, nil
}

// GetAllUserSubscriptions returns all subscriptions (active and expired) for a user.
func GetAllUserSubscriptions(userId int) ([]SubscriptionSummary, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	var subs []UserSubscription
	err := DB.Where("user_id = ?", userId).
		Order("end_time desc, id desc").
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return buildSubscriptionSummaries(subs), nil
}

// GetUserSubscriptionOwnerID returns the account that owns a subscription.
// Controllers use this projection before administrator-only mutations so the
// same target-role boundary as the rest of user management can be enforced.
func GetUserSubscriptionOwnerID(userSubscriptionId int) (int, error) {
	if userSubscriptionId <= 0 {
		return 0, errors.New("invalid userSubscriptionId")
	}
	var sub UserSubscription
	if err := DB.Select("user_id").Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
		return 0, err
	}
	return sub.UserId, nil
}

func buildSubscriptionSummaries(subs []UserSubscription) []SubscriptionSummary {
	if len(subs) == 0 {
		return []SubscriptionSummary{}
	}
	result := make([]SubscriptionSummary, 0, len(subs))
	for _, sub := range subs {
		subCopy := sub
		result = append(result, SubscriptionSummary{
			Subscription: &subCopy,
		})
	}
	return result
}

// AdminInvalidateUserSubscription marks a user subscription as cancelled and ends it immediately.
func AdminInvalidateUserSubscription(userSubscriptionId int) (string, error) {
	if userSubscriptionId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	var owner struct {
		UserId int `gorm:"column:user_id"`
	}
	if err := DB.Model(&UserSubscription{}).
		Select("user_id").Where("id = ?", userSubscriptionId).First(&owner).Error; err != nil {
		return "", err
	}
	now := common.GetTimestamp()
	cacheGroup := ""
	downgradeGroup := ""
	var userId int
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Billing and pre-consume transactions lock the user before any of the
		// user's subscriptions. Follow that order here as well; locking the
		// subscription first and then downgrading the user could form a cycle.
		var user User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", owner.UserId).First(&user).Error; err != nil {
			return err
		}
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
			return err
		}
		if sub.UserId != owner.UserId {
			return errors.New("user subscription owner changed while acquiring locks")
		}
		userId = sub.UserId
		if err := tx.Model(&sub).Updates(map[string]interface{}{
			"status":     "cancelled",
			"end_time":   now,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
		if err != nil {
			return err
		}
		if target != "" {
			cacheGroup = target
			downgradeGroup = target
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if cacheGroup != "" && userId > 0 {
		refreshSubscriptionUserGroupCache(userId, "admin subscription update")
	}
	if downgradeGroup != "" {
		return fmt.Sprintf("用户分组将回退到 %s", downgradeGroup), nil
	}
	return "", nil
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func AdminDeleteUserSubscription(userSubscriptionId int) (string, error) {
	if userSubscriptionId <= 0 {
		return "", errors.New("invalid userSubscriptionId")
	}
	var owner struct {
		UserId int `gorm:"column:user_id"`
	}
	if err := DB.Model(&UserSubscription{}).
		Select("user_id").Where("id = ?", userSubscriptionId).First(&owner).Error; err != nil {
		return "", err
	}
	now := common.GetTimestamp()
	cacheGroup := ""
	downgradeGroup := ""
	var userId int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", owner.UserId).First(&user).Error; err != nil {
			return err
		}
		var sub UserSubscription
		if err := lockForUpdate(tx).
			Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
			return err
		}
		if sub.UserId != owner.UserId {
			return errors.New("user subscription owner changed while acquiring locks")
		}
		userId = sub.UserId
		target, err := downgradeUserGroupForSubscriptionTx(tx, &sub, now)
		if err != nil {
			return err
		}
		if target != "" {
			cacheGroup = target
			downgradeGroup = target
		}
		if err := tx.Where("id = ?", userSubscriptionId).Delete(&UserSubscription{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if cacheGroup != "" && userId > 0 {
		refreshSubscriptionUserGroupCache(userId, "admin subscription deletion")
	}
	if downgradeGroup != "" {
		return fmt.Sprintf("用户分组将回退到 %s", downgradeGroup), nil
	}
	return "", nil
}

func resetUserSubscriptionTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64, advanceResetTime bool) error {
	if tx == nil || sub == nil || plan == nil {
		return errors.New("invalid reset args")
	}
	sub.AmountUsed = 0
	if advanceResetTime {
		nextReset := calcNextResetTime(time.Unix(now, 0), subscriptionResetPlan(sub, plan), sub.EndTime)
		sub.NextResetTime = nextReset
		if nextReset > 0 {
			sub.LastResetTime = now
		} else {
			sub.LastResetTime = 0
		}
	}
	return tx.Save(sub).Error
}

// subscriptionResetPlan overlays the immutable reset settings captured on a
// subscription onto the current plan object.  Duration, quota and group
// values are already copied to UserSubscription at creation; reset cadence
// must follow the same rule.  Rows created before the snapshot columns were
// introduced have an empty period and deliberately fall back to the current
// plan for backward compatibility.
func subscriptionResetPlan(sub *UserSubscription, plan *SubscriptionPlan) *SubscriptionPlan {
	if sub == nil || plan == nil || strings.TrimSpace(sub.QuotaResetPeriod) == "" {
		return plan
	}
	snapshot := *plan
	snapshot.QuotaResetPeriod = NormalizeResetPeriod(sub.QuotaResetPeriod)
	snapshot.QuotaResetCustomSeconds = sub.QuotaResetCustomSeconds
	return &snapshot
}

func buildSubscriptionResetResult(plan *SubscriptionPlan, subs []UserSubscription, advanceResetTime bool) *SubscriptionResetResult {
	userIds := make([]int, 0, len(subs))
	seenUsers := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if _, ok := seenUsers[sub.UserId]; ok {
			continue
		}
		seenUsers[sub.UserId] = struct{}{}
		userIds = append(userIds, sub.UserId)
	}
	return &SubscriptionResetResult{
		PlanId:           plan.Id,
		MatchedCount:     len(subs),
		ResetCount:       len(subs),
		UserCount:        len(userIds),
		AdvanceResetTime: advanceResetTime,
		PlanTitle:        plan.Title,
		AffectedUserIds:  userIds,
	}
}

func adminResetUserSubscriptionsByPlanTx(tx *gorm.DB, userId int, plan *SubscriptionPlan, now int64, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if tx == nil || plan == nil {
		return nil, errors.New("invalid reset args")
	}
	var subs []UserSubscription
	if err := lockForUpdate(tx).
		Where("user_id = ? AND plan_id = ? AND status = ? AND end_time > ?", userId, plan.Id, "active", now).
		Order("end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, errors.New("该用户没有有效的此套餐订阅")
	}
	for i := range subs {
		if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
			return nil, err
		}
	}
	return buildSubscriptionResetResult(plan, subs, advanceResetTime), nil
}

func adminResetPlanSubscriptionsTx(tx *gorm.DB, plan *SubscriptionPlan, now int64, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if tx == nil || plan == nil {
		return nil, errors.New("invalid reset args")
	}
	var subs []UserSubscription
	if err := lockForUpdate(tx).
		Where("plan_id = ? AND status = ? AND end_time > ?", plan.Id, "active", now).
		Order("user_id asc, end_time asc, id asc").
		Find(&subs).Error; err != nil {
		return nil, err
	}
	for i := range subs {
		if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
			return nil, err
		}
	}
	return buildSubscriptionResetResult(plan, subs, advanceResetTime), nil
}

func AdminResetUserSubscriptionsByPlan(userId int, planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if userId <= 0 || planId <= 0 {
		return nil, errors.New("invalid userId or planId")
	}
	var result *SubscriptionResetResult
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		result, err = adminResetUserSubscriptionsByPlanTx(tx, userId, plan, now, advanceResetTime)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func AdminResetPlanSubscriptions(planId int, advanceResetTime bool) (*SubscriptionResetResult, error) {
	if planId <= 0 {
		return nil, errors.New("invalid planId")
	}
	var result *SubscriptionResetResult
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}
		result, err = adminResetPlanSubscriptionsTx(tx, plan, now, advanceResetTime)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// AdminResetPlanSubscriptionsForRole resets only subscriptions whose owners
// are below the calling administrator's role. Plan-wide operations otherwise
// let an ordinary administrator mutate peer-admin and root entitlements even
// though every user-specific management endpoint denies those targets.
func AdminResetPlanSubscriptionsForRole(planId int, advanceResetTime bool, actorRole int) (*SubscriptionResetResult, error) {
	if planId <= 0 || !common.IsValidateRole(actorRole) || actorRole < common.RoleAdminUser {
		return nil, errors.New("invalid planId or actorRole")
	}
	var result *SubscriptionResetResult
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		plan, err := getSubscriptionPlanByIdTx(tx, planId)
		if err != nil {
			return err
		}

		allowedOwners := tx.Model(&User{}).Select("id").Where("role IN ?", []int{
			common.RoleGuestUser,
			common.RoleCommonUser,
			common.RoleAdminUser,
			common.RoleRootUser,
		})
		if actorRole != common.RoleRootUser {
			allowedOwners = allowedOwners.Where("role < ?", actorRole)
		}

		var subs []UserSubscription
		if err := lockForUpdate(tx).
			Where("plan_id = ? AND status = ? AND end_time > ?", plan.Id, "active", now).
			Where("user_id IN (?)", allowedOwners).
			Order("user_id asc, end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return err
		}
		for i := range subs {
			if err := resetUserSubscriptionTx(tx, &subs[i], plan, now, advanceResetTime); err != nil {
				return err
			}
		}
		result = buildSubscriptionResetResult(plan, subs, advanceResetTime)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type SubscriptionPreConsumeResult struct {
	UserSubscriptionId int
	PreConsumed        int64
	AmountTotal        int64
	AmountUsedBefore   int64
	AmountUsedAfter    int64
}

// ExpireDueSubscriptions marks expired subscriptions and handles group downgrade.
func ExpireDueSubscriptions(limit int) (int, error) {
	if DB == nil {
		return 0, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	if limit <= 0 {
		limit = 200
	}
	now := GetDBTimestamp()
	var subs []UserSubscription
	if err := DB.Where("status = ? AND end_time > 0 AND end_time <= ?", "active", now).
		Order("end_time asc, id asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	expiredCount := 0
	userIds := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if sub.UserId > 0 {
			userIds[sub.UserId] = struct{}{}
		}
	}
	for userId := range userIds {
		cacheGroup := ""
		expiredForUser := 0
		err := DB.Transaction(func(tx *gorm.DB) error {
			// Serialize every subscription transition for this user behind the
			// same user-row fence used by billing. The bulk update below acquires
			// subscription locks only after this point.
			var user User
			if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&user).Error; err != nil {
				return err
			}
			res := tx.Model(&UserSubscription{}).
				Where("user_id = ? AND status = ? AND end_time > 0 AND end_time <= ?", userId, "active", now).
				Updates(map[string]interface{}{
					"status":     "expired",
					"updated_at": common.GetTimestamp(),
				})
			if res.Error != nil {
				return res.Error
			}
			expiredForUser = int(res.RowsAffected)

			// If there's an active upgraded subscription, keep current group.
			var activeSub UserSubscription
			activeQuery := tx.Where("user_id = ? AND status = ? AND end_time > ? AND upgrade_group <> ''",
				userId, "active", now).
				Order("end_time desc, id desc").
				Limit(1).
				Find(&activeSub)
			if activeQuery.Error != nil {
				return activeQuery.Error
			}
			if activeQuery.RowsAffected > 0 {
				return nil
			}

			// Find the most recently expired subscription that defines a group transition
			// (an explicit downgrade target or an upgrade snapshot to revert).
			var lastExpired UserSubscription
			expiredQuery := tx.Where("user_id = ? AND status = ? AND (downgrade_group <> '' OR upgrade_group <> '')",
				userId, "expired").
				Order("end_time desc, id desc").
				Limit(1).
				Find(&lastExpired)
			if expiredQuery.Error != nil {
				return expiredQuery.Error
			}
			if expiredQuery.RowsAffected == 0 {
				return nil
			}
			currentGroup, err := getUserGroupByIdTx(tx, userId)
			if err != nil {
				return err
			}
			// An explicit downgrade group takes precedence; otherwise revert to the
			// group held before purchase (legacy behavior, only when the subscription
			// actually elevated the user).
			target := strings.TrimSpace(lastExpired.DowngradeGroup)
			if target == "" {
				upgradeGroup := strings.TrimSpace(lastExpired.UpgradeGroup)
				prevGroup := strings.TrimSpace(lastExpired.PrevUserGroup)
				if upgradeGroup == "" || prevGroup == "" {
					return nil
				}
				if currentGroup != upgradeGroup {
					return nil
				}
				target = prevGroup
			}
			if target == "" || target == currentGroup {
				return nil
			}
			if err := tx.Model(&User{}).Where("id = ?", userId).
				Update("group", target).Error; err != nil {
				return err
			}
			cacheGroup = target
			return nil
		})
		if err != nil {
			return expiredCount, err
		}
		expiredCount += expiredForUser
		if cacheGroup != "" {
			refreshSubscriptionUserGroupCache(userId, "subscription expiration")
		}
	}
	return expiredCount, nil
}

// SubscriptionPreConsumeRecord stores idempotent pre-consume operations per request.
type SubscriptionPreConsumeRecord struct {
	Id                 int    `json:"id"`
	RequestId          string `json:"request_id" gorm:"type:varchar(64);uniqueIndex"`
	UserId             int    `json:"user_id" gorm:"index"`
	UserSubscriptionId int    `json:"user_subscription_id" gorm:"index"`
	PreConsumed        int64  `json:"pre_consumed" gorm:"type:bigint;not null;default:0"`
	Status             string `json:"status" gorm:"type:varchar(32);index"` // consumed/refund_pending/refunded
	CreatedAt          int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt          int64  `json:"updated_at" gorm:"bigint;index"`
}

func (r *SubscriptionPreConsumeRecord) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	r.CreatedAt = now
	r.UpdatedAt = now
	return nil
}

func (r *SubscriptionPreConsumeRecord) BeforeUpdate(tx *gorm.DB) error {
	r.UpdatedAt = common.GetTimestamp()
	return nil
}

func maybeResetUserSubscriptionWithPlanTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64) error {
	if tx == nil || sub == nil || plan == nil {
		return errors.New("invalid reset args")
	}
	plan = subscriptionResetPlan(sub, plan)
	if sub.NextResetTime > 0 && sub.NextResetTime > now {
		return nil
	}
	if NormalizeResetPeriod(plan.QuotaResetPeriod) == SubscriptionResetNever {
		return nil
	}
	baseUnix := sub.LastResetTime
	if baseUnix <= 0 {
		baseUnix = sub.StartTime
	}
	base := time.Unix(baseUnix, 0)
	next := calcNextResetTime(base, plan, sub.EndTime)
	advanced := false
	for next > 0 && next <= now {
		advanced = true
		base = time.Unix(next, 0)
		next = calcNextResetTime(base, plan, sub.EndTime)
	}
	if !advanced {
		if sub.NextResetTime == 0 && next > 0 {
			sub.NextResetTime = next
			sub.LastResetTime = base.Unix()
			return tx.Save(sub).Error
		}
		return nil
	}
	sub.AmountUsed = 0
	sub.LastResetTime = base.Unix()
	sub.NextResetTime = next
	return tx.Save(sub).Error
}

// PreConsumeUserSubscription pre-consumes from any active subscription total quota.
func PreConsumeUserSubscription(requestId string, userId int, modelName string, quotaType int, amount int64) (*SubscriptionPreConsumeResult, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	if strings.TrimSpace(requestId) == "" {
		return nil, errors.New("requestId is empty")
	}
	if amount <= 0 || amount > int64(common.MaxQuota) {
		return nil, errors.New("amount is out of range")
	}
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	now := GetDBTimestamp()

	returnValue := &SubscriptionPreConsumeResult{}

	err := DB.Transaction(func(tx *gorm.DB) error {
		// Serialize reservations for a user before looking up the request marker.
		// A unique-index race is not recoverable inside a PostgreSQL transaction
		// after a duplicate INSERT aborts the transaction; the user-row lock
		// gives concurrent retries a portable, dialect-safe fence instead.
		var user User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		var existing SubscriptionPreConsumeRecord
		query := lockForUpdate(tx).Where("request_id = ?", requestId).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if existing.Status == "refunded" || existing.Status == subscriptionPreConsumeStatusRefundPending {
				return errors.New("subscription pre-consume already refunded")
			}
			if existing.UserId != userId || existing.PreConsumed != amount {
				return ErrSubscriptionPreConsumeConflict
			}
			var sub UserSubscription
			if err := tx.Where("id = ?", existing.UserSubscriptionId).First(&sub).Error; err != nil {
				return err
			}
			returnValue.UserSubscriptionId = sub.Id
			returnValue.PreConsumed = existing.PreConsumed
			returnValue.AmountTotal = sub.AmountTotal
			returnValue.AmountUsedBefore = sub.AmountUsed
			returnValue.AmountUsedAfter = sub.AmountUsed
			return nil
		}

		var subs []UserSubscription
		if err := lockForUpdate(tx).
			Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
			Order("end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return err
		}
		if len(subs) == 0 {
			return errors.New("no active subscription")
		}
		for _, candidate := range subs {
			sub := candidate
			plan, err := getSubscriptionPlanByIdTx(tx, sub.PlanId)
			if err != nil {
				return err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
				return err
			}
			usedBefore := sub.AmountUsed
			if usedBefore < 0 {
				return fmt.Errorf("subscription usage is negative, used=%d", usedBefore)
			}
			if sub.AmountTotal > 0 {
				if usedBefore > sub.AmountTotal {
					return fmt.Errorf("subscription used exceeds total, used=%d total=%d", usedBefore, sub.AmountTotal)
				}
				remain := sub.AmountTotal - usedBefore
				if remain < amount {
					continue
				}
			} else if usedBefore > math.MaxInt64-amount {
				return fmt.Errorf("subscription usage overflow, used=%d delta=%d", usedBefore, amount)
			}
			record := &SubscriptionPreConsumeRecord{
				RequestId:          requestId,
				UserId:             userId,
				UserSubscriptionId: sub.Id,
				PreConsumed:        amount,
				Status:             "consumed",
			}
			if err := tx.Create(record).Error; err != nil {
				var dup SubscriptionPreConsumeRecord
				if err2 := tx.Where("request_id = ?", requestId).First(&dup).Error; err2 == nil {
					if dup.Status == "refunded" {
						return errors.New("subscription pre-consume already refunded")
					}
					returnValue.UserSubscriptionId = sub.Id
					returnValue.PreConsumed = dup.PreConsumed
					returnValue.AmountTotal = sub.AmountTotal
					returnValue.AmountUsedBefore = sub.AmountUsed
					returnValue.AmountUsedAfter = sub.AmountUsed
					return nil
				}
				return err
			}
			sub.AmountUsed += amount
			if err := tx.Save(&sub).Error; err != nil {
				return err
			}
			returnValue.UserSubscriptionId = sub.Id
			returnValue.PreConsumed = amount
			returnValue.AmountTotal = sub.AmountTotal
			returnValue.AmountUsedBefore = usedBefore
			returnValue.AmountUsedAfter = sub.AmountUsed
			return nil
		}
		return fmt.Errorf("subscription quota insufficient, need=%d", amount)
	})
	if err != nil {
		return nil, err
	}
	return returnValue, nil
}

// PreConsumeUserSubscriptionAndToken reserves a subscription amount and the
// corresponding token quota in one database transaction.  The older
// PreConsumeUserSubscription API predates the token ledger and intentionally
// remains available for non-relay callers; BillingSession uses this combined
// seam so a process crash cannot commit the subscription marker while leaving
// the token reservation in an unknown state.
//
// The token mutation is represented by a BillingOperation row with a stable
// request/component key.  Existing rows are validated and replayed, making a
// retry after a transport/database ambiguity safe.  Cache synchronization is
// performed only after the outer transaction commits.
func PreConsumeUserSubscriptionAndToken(requestId string, userId int, modelName string, quotaType int, amount int64, tokenID int, tokenKey string, tokenUnlimited, playground bool) (*SubscriptionPreConsumeResult, error) {
	if userId <= 0 {
		return nil, errors.New("invalid userId")
	}
	requestId = strings.TrimSpace(requestId)
	if requestId == "" {
		return nil, errors.New("requestId is empty")
	}
	if amount <= 0 || amount > int64(common.MaxQuota) {
		return nil, errors.New("amount is out of range")
	}
	if !playground && tokenID <= 0 {
		return nil, errors.New("token id is required")
	}
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}

	now := GetDBTimestamp()
	result := &SubscriptionPreConsumeResult{}
	operation := BillingOperationSpec{
		RequestID:           requestId,
		Component:           "subscription_preconsume_token",
		UserID:              userId,
		TokenID:             tokenID,
		TokenKey:            strings.TrimSpace(tokenKey),
		TokenDelta:          amount,
		TokenUnlimited:      tokenUnlimited,
		RequireTokenBalance: !tokenUnlimited && !playground,
	}
	if playground {
		operation.TokenDelta = 0
		operation.RequireTokenBalance = false
	}
	normalized, err := normalizeBillingOperationSpec(operation)
	if err != nil {
		return nil, err
	}

	var appliedNow bool
	err = DB.Transaction(func(tx *gorm.DB) error {
		// All request-lifecycle operations acquire the owner before their journal
		// row.  Keeping subscription pre-consume on the same user -> operation
		// order prevents a settle/refund transaction that already owns the user
		// fence from deadlocking with a pre-consume retry that owns the operation
		// row. The user is known from the authenticated request, so no discovery
		// read is needed before taking this portable serialization point.
		var user User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&user).Error; err != nil {
			return err
		}
		operationRow, err := lockBillingOperationTx(tx, normalized)
		if err != nil {
			return err
		}

		var existing SubscriptionPreConsumeRecord
		query := lockForUpdate(tx).Where("request_id = ?", requestId).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			if existing.Status == "refunded" || existing.Status == subscriptionPreConsumeStatusRefundPending {
				return errors.New("subscription pre-consume already refunded")
			}
			if existing.UserId != userId || existing.PreConsumed != amount {
				return ErrSubscriptionPreConsumeConflict
			}
			var sub UserSubscription
			if err := lockForUpdate(tx).Where("id = ?", existing.UserSubscriptionId).First(&sub).Error; err != nil {
				return err
			}
			result.UserSubscriptionId = sub.Id
			result.PreConsumed = existing.PreConsumed
			result.AmountTotal = sub.AmountTotal
			result.AmountUsedBefore = sub.AmountUsed
			result.AmountUsedAfter = sub.AmountUsed
			if operationRow.Status == BillingOperationApplied {
				// The marker and token operation were committed by an earlier
				// attempt. The immutable request identity makes this a replay; do
				// not apply the token delta a second time.
				return nil
			}
			appliedNow, err = applyBillingOperationTx(tx, normalized)
			return err
		}
		if operationRow.Status == BillingOperationApplied {
			// An applied token operation without its subscription marker cannot
			// be safely guessed at. Leave the durable row visible for manual
			// reconciliation instead of granting a free subscription period.
			return ErrBillingOperationConflict
		}

		var subs []UserSubscription
		if err := lockForUpdate(tx).
			Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
			Order("end_time asc, id asc").
			Find(&subs).Error; err != nil {
			return err
		}
		if len(subs) == 0 {
			return errors.New("no active subscription")
		}
		for _, candidate := range subs {
			sub := candidate
			plan, err := getSubscriptionPlanByIdTx(tx, sub.PlanId)
			if err != nil {
				return err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &sub, plan, now); err != nil {
				return err
			}
			usedBefore := sub.AmountUsed
			if usedBefore < 0 {
				return fmt.Errorf("subscription usage is negative, used=%d", usedBefore)
			}
			if sub.AmountTotal > 0 {
				if usedBefore > sub.AmountTotal {
					return fmt.Errorf("subscription used exceeds total, used=%d total=%d", usedBefore, sub.AmountTotal)
				}
				if sub.AmountTotal-usedBefore < amount {
					continue
				}
			} else if usedBefore > math.MaxInt64-amount {
				return fmt.Errorf("subscription usage overflow, used=%d delta=%d", usedBefore, amount)
			}
			record := &SubscriptionPreConsumeRecord{
				RequestId:          requestId,
				UserId:             userId,
				UserSubscriptionId: sub.Id,
				PreConsumed:        amount,
				Status:             "consumed",
			}
			if err := tx.Create(record).Error; err != nil {
				// The user-row lock normally prevents this race. If a legacy
				// database lacks the expected index, surface the error rather than
				// guessing whether the token was already charged.
				return err
			}
			sub.AmountUsed += amount
			if err := tx.Save(&sub).Error; err != nil {
				return err
			}
			result.UserSubscriptionId = sub.Id
			result.PreConsumed = amount
			result.AmountTotal = sub.AmountTotal
			result.AmountUsedBefore = usedBefore
			result.AmountUsedAfter = sub.AmountUsed
			appliedNow, err = applyBillingOperationTx(tx, normalized)
			return err
		}
		return fmt.Errorf("subscription quota insufficient, need=%d", amount)
	})
	if err != nil {
		return nil, err
	}
	syncBillingOperationCaches(normalized, appliedNow)
	return result, nil
}

// RefundSubscriptionPreConsume is idempotent and refunds pre-consumed subscription quota by requestId.
func RefundSubscriptionPreConsume(requestId string) error {
	if strings.TrimSpace(requestId) == "" {
		return errors.New("requestId is empty")
	}
	if DB == nil {
		return fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		// Keep the same lock protocol as durable billing operations: user first,
		// then reservation marker, then subscription. Read only the owner id
		// before taking the user lock; the locked marker below remains the source
		// of truth and is revalidated after the lock is acquired.
		var owner struct {
			UserId int `gorm:"column:user_id"`
		}
		if err := tx.Model(&SubscriptionPreConsumeRecord{}).
			Select("user_id").Where("request_id = ?", requestId).First(&owner).Error; err != nil {
			return err
		}
		if owner.UserId > 0 {
			var user User
			if err := lockForUpdate(tx).Select("id").Where("id = ?", owner.UserId).First(&user).Error; err != nil {
				return err
			}
		}
		var record SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).
			Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == "refunded" {
			return nil
		}
		if record.Status == subscriptionPreConsumeStatusRefundPending {
			// A durable refund intent owns this reservation.  The dedicated
			// BillingOperation path must finish it; applying the historical
			// marker-only refund here could double-debit the token ledger.
			return ErrBillingOperationConflict
		}
		if record.PreConsumed <= 0 {
			record.Status = "refunded"
			return tx.Save(&record).Error
		}
		// Keep the subscription update and the idempotency marker in this same
		// transaction. Calling PostConsumeUserSubscriptionDelta here would open a
		// second transaction on DB: the quota change could commit while the
		// marker update later rolled back (or the process could exit between the
		// two commits), making a retry refund the same reservation twice. It also
		// deadlocks SQLite configurations that allow only one open connection.
		if err := postConsumeUserSubscriptionDeltaTx(tx, record.UserSubscriptionId, -record.PreConsumed); err != nil {
			return err
		}
		record.Status = "refunded"
		return tx.Save(&record).Error
	})
}

// MarkSubscriptionPreConsumeRefunded marks a previously consumed reservation
// as refunded without changing the subscription usage.  It is used after a
// durable BillingOperation has atomically applied the subscription + token
// refund.  Keeping this marker-only step separate lets a retry repair a crash
// between the ledger transaction and the reservation marker update without
// applying the subscription delta twice.
func MarkSubscriptionPreConsumeRefunded(requestId string) error {
	requestId = strings.TrimSpace(requestId)
	if requestId == "" {
		return errors.New("requestId is empty")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var record SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).
			Where("request_id = ?", requestId).First(&record).Error; err != nil {
			return err
		}
		if record.Status == "refunded" {
			return nil
		}
		if record.Status != "consumed" {
			return fmt.Errorf("invalid subscription pre-consume status %q", record.Status)
		}
		record.Status = "refunded"
		return tx.Save(&record).Error
	})
}

// ResetDueSubscriptions resets subscriptions whose next_reset_time has passed.
func ResetDueSubscriptions(limit int) (int, error) {
	if DB == nil {
		return 0, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	if limit <= 0 {
		limit = 200
	}
	now := GetDBTimestamp()
	var subs []UserSubscription
	if err := DB.Where("next_reset_time > 0 AND next_reset_time <= ? AND status = ?", now, "active").
		Order("next_reset_time asc").
		Limit(limit).
		Find(&subs).Error; err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}
	resetCount := 0
	for _, sub := range subs {
		subCopy := sub
		plan, err := getSubscriptionPlanByIdTx(nil, sub.PlanId)
		if err != nil {
			return resetCount, err
		}
		if plan == nil {
			return resetCount, errors.New("subscription plan is nil")
		}
		err = DB.Transaction(func(tx *gorm.DB) error {
			var locked UserSubscription
			if err := lockForUpdate(tx).
				Where("id = ? AND next_reset_time > 0 AND next_reset_time <= ?", subCopy.Id, now).
				First(&locked).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &locked, plan, now); err != nil {
				return err
			}
			resetCount++
			return nil
		})
		if err != nil {
			return resetCount, err
		}
	}
	return resetCount, nil
}

// CleanupSubscriptionPreConsumeRecords removes only records whose refund has
// already been durably committed.  A consumed record is itself the recovery
// marker for an in-flight request: deleting it before the caller has refunded
// the subscription quota would make a post-crash retry impossible and strand
// the reserved amount forever.  Such records may be retained until an
// explicit reconciliation process proves that they are safe to archive.
func CleanupSubscriptionPreConsumeRecords(olderThanSeconds int64) (int64, error) {
	if DB == nil {
		return 0, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	if olderThanSeconds <= 0 {
		olderThanSeconds = 7 * 24 * 3600
	}
	cutoff := GetDBTimestamp() - olderThanSeconds
	res := DB.Where("updated_at < ? AND status = ?", cutoff, "refunded").Delete(&SubscriptionPreConsumeRecord{})
	return res.RowsAffected, res.Error
}

type SubscriptionPlanInfo struct {
	PlanId    int
	PlanTitle string
}

func GetSubscriptionPlanInfoByUserSubscriptionId(userSubscriptionId int) (*SubscriptionPlanInfo, error) {
	if userSubscriptionId <= 0 {
		return nil, errors.New("invalid userSubscriptionId")
	}
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	cacheKey := fmt.Sprintf("sub:%d", userSubscriptionId)
	if cached, found, err := getSubscriptionPlanInfoCache().Get(cacheKey); err == nil && found {
		return &cached, nil
	}
	var sub UserSubscription
	if err := DB.Where("id = ?", userSubscriptionId).First(&sub).Error; err != nil {
		return nil, err
	}
	plan, err := getSubscriptionPlanByIdTx(nil, sub.PlanId)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, errors.New("subscription plan is nil")
	}
	info := &SubscriptionPlanInfo{
		PlanId:    sub.PlanId,
		PlanTitle: plan.Title,
	}
	_ = getSubscriptionPlanInfoCache().SetWithTTL(cacheKey, *info, subscriptionPlanInfoCacheTTL())
	return info, nil
}

// Update subscription used amount by delta (positive consume more, negative refund).
func PostConsumeUserSubscriptionDelta(userSubscriptionId int, delta int64) error {
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return nil
	}
	if DB == nil {
		return fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		return postConsumeUserSubscriptionDeltaTx(tx, userSubscriptionId, delta)
	})
}

// postConsumeUserSubscriptionDeltaTx applies a subscription usage delta using
// the caller's transaction. Callers that need to update another idempotency
// record (for example, refunding a pre-consume reservation) must use this
// helper so both writes commit or roll back together.
func postConsumeUserSubscriptionDeltaTx(tx *gorm.DB, userSubscriptionId int, delta int64) error {
	if tx == nil {
		return errors.New("transaction is nil")
	}
	if userSubscriptionId <= 0 {
		return errors.New("invalid userSubscriptionId")
	}
	if delta == 0 {
		return nil
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).
		Where("id = ?", userSubscriptionId).
		First(&sub).Error; err != nil {
		return err
	}
	if sub.AmountUsed < 0 {
		return fmt.Errorf("subscription usage is negative, used=%d", sub.AmountUsed)
	}
	if delta < 0 && delta < -sub.AmountUsed {
		// Never silently clamp a refund below zero. A pre-consume marker can be
		// stale (for example after an administrator adjustment or a prior
		// reconciliation); clamping would erase unrelated, legitimate usage and
		// make a retry appear successful while the ledgers diverge. Keep the
		// transaction uncommitted so the caller can reconcile the marker.
		return fmt.Errorf("subscription usage underflow, used=%d delta=%d", sub.AmountUsed, delta)
	}
	if delta > 0 && sub.AmountUsed > math.MaxInt64-delta {
		return fmt.Errorf("subscription usage overflow, used=%d delta=%d", sub.AmountUsed, delta)
	}
	newUsed := sub.AmountUsed + delta
	if sub.AmountTotal > 0 && newUsed > sub.AmountTotal {
		return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, sub.AmountTotal)
	}
	sub.AmountUsed = newUsed
	return tx.Save(&sub).Error
}
