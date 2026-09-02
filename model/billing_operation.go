package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BillingOperationStatus is deliberately small.  A billing operation is
// written and marked applied in one database transaction, so a committed
// pending row should never be observable during normal operation.  Keeping a
// pending state nevertheless lets an operator/reconciler safely finish rows
// inserted by an older deployment or by a manual repair.
type BillingOperationStatus string

const (
	BillingOperationPending BillingOperationStatus = "pending"
	BillingOperationApplied BillingOperationStatus = "applied"

	// BillingOperationRefundComponent is the durable, explicit refund intent
	// used by synchronous relay requests.  It is intentionally exported so the
	// service reconciler and operator tooling cannot drift to a different key.
	BillingOperationRefundComponent = "refund"
	// BillingOperationLegacyTaskRefundComponent identifies refunds synthesized
	// for task rows that predate BillingRequestId. It is deliberately separate
	// from the synchronous refund component because those rows do not own a
	// SubscriptionPreConsumeRecord marker.
	BillingOperationLegacyTaskRefundComponent = "legacy_task_refund"

	// Durable billing component names.  These values are part of the persisted
	// idempotency key; keep them stable when adding a new call site.  The
	// classifier below intentionally uses explicit names instead of broad
	// prefixes so an unrelated/provider-specific operation cannot accidentally
	// enter the request lifecycle fence.
	BillingOperationWalletRefundComponent                = "wallet_refund"
	BillingOperationSubscriptionRefundComponent          = "subscription_refund"
	BillingOperationTokenRefundComponent                 = "refund_token"
	BillingOperationExtraRefundComponent                 = "refund_extra"
	BillingOperationTaskRefundComponent                  = "task_refund"
	BillingOperationTerminalRefundComponent              = "terminal_refund"
	BillingOperationWalletPreConsumeComponent            = "wallet_preconsume"
	BillingOperationSubscriptionPreConsumeTokenComponent = "subscription_preconsume_token"
	BillingOperationPreConsumeComponent                  = "preconsume"
	BillingOperationPreConsumeTokenComponent             = "preconsume_token"
	BillingOperationSettleComponent                      = "settle"
	BillingOperationLegacySettleComponent                = "legacy_settle"
	BillingOperationWalletSettleComponent                = "wallet_settle"
	BillingOperationSubscriptionSettleComponent          = "subscription_settle"
	BillingOperationTerminalSettleComponent              = "terminal_settle"
	BillingOperationLegacyTaskRecalculateComponent       = "legacy_task_recalculate"
	BillingOperationViolationFeeComponent                = "violation_fee"
	BillingOperationSettleUsageComponent                 = "settle_usage"
	BillingOperationSyncSettleUsageComponent             = "sync_settle_usage"
	BillingOperationLegacySettleUsageComponent           = "legacy_settle_usage"
	BillingOperationTerminalSettleUsageComponent         = "terminal_settle_usage"

	// subscriptionPreConsumeStatusRefundPending freezes a reservation after a
	// caller has durably declared that the request failed, but before the refund
	// transaction has committed.  Reserve paths accept only "consumed", so a
	// retry cannot grow the amount after the refund snapshot was fenced.
	subscriptionPreConsumeStatusRefundPending = "refund_pending"
)

// billingOperationComponentClass describes the lifecycle role of a durable
// operation.  It is deliberately private: callers persist the string
// component, while the model owns the rules that decide which operations may
// coexist for one request.  Keeping the role separate from the component name
// is important for terminal task refunds, which are allowed to follow an
// already-applied submit settlement but ordinary request refunds are not.
type billingOperationComponentClass uint8

const (
	billingOperationClassUnknown billingOperationComponentClass = iota
	billingOperationClassCharge
	billingOperationClassSettlement
	billingOperationClassUsage
	billingOperationClassRefund
	billingOperationClassTerminalRefund
	// Independent post-adjustments (for example the CSAM violation fee) are
	// intentionally not fenced against request refunds/settlements.  The fee is
	// charged after the normal failure flow, including a refund, and therefore
	// must remain applicable even when another lifecycle component is already
	// applied for the request.
	billingOperationClassPostAdjustment
	billingOperationClassProviderReversal
)

var (
	// ErrBillingOperationConflict means that a request/component key was reused
	// with a different ledger or amount.  Treating that as an ordinary retry
	// would silently charge the wrong account, so callers must fail closed.
	ErrBillingOperationConflict = errors.New("billing operation key conflicts with an existing operation")
	// ErrBillingOperationInsufficient is returned when a conditional reserve
	// cannot be completed without overdrawing a wallet/token.
	ErrBillingOperationInsufficient = errors.New("billing operation balance insufficient")
	ErrBillingWalletInsufficient    = errors.New("billing wallet balance insufficient")
	ErrBillingTokenInsufficient     = errors.New("billing token balance insufficient")
	// ErrBillingOperationInvalid identifies malformed operation specifications.
	ErrBillingOperationInvalid = errors.New("invalid billing operation")
	// ErrBillingReservationChanged means a reserve committed between a
	// reservation snapshot and the transaction that tried to close it. The
	// caller may rebuild the snapshot and retry with the new exact totals.
	ErrBillingReservationChanged = errors.New("billing reservation changed")
	// ErrBillingTaskQuotaConflict means the task's durable quota changed while
	// a caller was trying to apply a stale terminal adjustment.  The ledger
	// operation must not be applied in that case: doing so would leave the
	// operation marker and task snapshot describing different baselines.
	ErrBillingTaskQuotaConflict = errors.New("task quota changed during billing operation")
	// ErrBillingUsageUnderflow means an informational usage reversal is larger
	// than the counter currently stored for the account/channel.  This is a
	// data-integrity conflict, not a value that can be safely clamped: the
	// enclosing BillingOperation transaction must roll back and remain
	// retryable/manual so an operator can reconstruct the missing history.
	ErrBillingUsageUnderflow = errors.New("billing usage counter underflow")
	// ErrBillingUsageOverflow is the corresponding guard for a corrupt counter
	// or an unexpectedly large positive adjustment.  It is kept distinct from
	// underflow so reconciliation and telemetry can tell the two cases apart.
	ErrBillingUsageOverflow = errors.New("billing usage counter overflow")
)

// BillingOperation is the durable idempotency fence for one logical billing
// component (for example preconsume, settle, or refund).  OperationKey is a
// SHA-256 digest of request_id and component; keeping the raw request ID and
// component as columns makes reconciliation/auditing practical without
// relying on a dialect-specific expression index.
type BillingOperation struct {
	Id int `json:"id"`

	OperationKey string `json:"operation_key" gorm:"type:char(64);not null;uniqueIndex"`
	RequestId    string `json:"request_id" gorm:"type:varchar(128);not null;index"`
	Component    string `json:"component" gorm:"type:varchar(96);not null"`

	UserId         int `json:"user_id" gorm:"index"`
	TokenId        int `json:"token_id" gorm:"index"`
	SubscriptionId int `json:"subscription_id" gorm:"index"`

	// WalletDelta is a direct users.quota delta: positive values credit the
	// wallet and negative values charge it.  TokenDelta and SubscriptionDelta
	// use billing semantics: positive values consume usage, negative values
	// refund usage.  The distinction mirrors the underlying schemas (tokens
	// store remain_quota and used_quota; subscriptions store amount_used).
	WalletDelta       int64 `json:"wallet_delta" gorm:"type:bigint;not null;default:0"`
	TokenDelta        int64 `json:"token_delta" gorm:"type:bigint;not null;default:0"`
	SubscriptionDelta int64 `json:"subscription_delta" gorm:"type:bigint;not null;default:0"`

	// Informational accounting deltas are journaled alongside financial
	// mutations. Task settlement/refund uses a separate idempotent component
	// (for example "settle_usage") so a crash cannot lose or double-count
	// used_quota/request counters after the financial operation has committed.
	UserUsedQuotaDelta    int64 `json:"user_used_quota_delta" gorm:"type:bigint;not null;default:0"`
	UserRequestCountDelta int64 `json:"user_request_count_delta" gorm:"type:bigint;not null;default:0"`
	ChannelId             int   `json:"channel_id" gorm:"index"`
	ChannelUsedQuotaDelta int64 `json:"channel_used_quota_delta" gorm:"type:bigint;not null;default:0"`

	RequireWalletBalance bool `json:"require_wallet_balance"`
	RequireTokenBalance  bool `json:"require_token_balance"`
	TokenUnlimited       bool `json:"token_unlimited"`

	Status    BillingOperationStatus `json:"status" gorm:"type:varchar(16);not null;index"`
	CreatedAt int64                  `json:"created_at" gorm:"bigint"`
	UpdatedAt int64                  `json:"updated_at" gorm:"bigint;index"`
}

func (o *BillingOperation) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	if o.CreatedAt == 0 {
		o.CreatedAt = now
	}
	o.UpdatedAt = now
	return nil
}

func (o *BillingOperation) BeforeUpdate(tx *gorm.DB) error {
	o.UpdatedAt = common.GetTimestamp()
	return nil
}

// BillingOperationSpec describes one atomic ledger mutation.  RequestID must
// be stable for the lifetime of a logical request; Component differentiates
// preconsume/settle/refund/reserve stages within that request.
type BillingOperationSpec struct {
	RequestID string
	Component string

	UserID         int
	TokenID        int
	TokenKey       string // used only for best-effort Redis synchronization
	SubscriptionID int

	WalletDelta       int64
	TokenDelta        int64
	SubscriptionDelta int64

	// Optional informational accounting deltas. They are applied in the same
	// transaction as the operation marker and are safe to replay. A non-zero
	// channel delta requires ChannelID; user deltas require UserID.
	UserUsedQuotaDelta    int64
	UserRequestCountDelta int64
	ChannelID             int
	ChannelUsedQuotaDelta int64

	RequireWalletBalance bool
	RequireTokenBalance  bool
	TokenUnlimited       bool
}

// SubscriptionBillingReservation is the durable total owned by one
// subscription request. The pre-consume marker stores the base reservation;
// applied reserve:* operations store monotonic top-ups made while streaming.
type SubscriptionBillingReservation struct {
	RequestID             string
	Status                string
	UserID                int
	TokenID               int
	SubscriptionID        int
	BaseSubscriptionQuota int64
	SubscriptionQuota     int64
	TokenQuota            int64
	TokenUnlimited        bool
	AmountTotal           int64
	AmountUsed            int64
}

// BillingOperationKey returns the stable, bounded key used by the unique
// database index.  The raw values are length-limited before hashing so a
// malformed client request cannot allocate an unbounded string in a marker.
func BillingOperationKey(requestID, component string) string {
	requestID = strings.TrimSpace(requestID)
	component = strings.TrimSpace(component)
	h := sha256.New()
	_, _ = h.Write([]byte(requestID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(component))
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeBillingOperationSpec(spec BillingOperationSpec) (BillingOperationSpec, error) {
	spec.RequestID = strings.TrimSpace(spec.RequestID)
	spec.Component = strings.TrimSpace(spec.Component)
	if spec.RequestID == "" || spec.Component == "" || len(spec.RequestID) > 128 || len(spec.Component) > 96 {
		return spec, ErrBillingOperationInvalid
	}
	if spec.UserID < 0 || spec.TokenID < 0 || spec.SubscriptionID < 0 {
		return spec, ErrBillingOperationInvalid
	}
	if spec.WalletDelta == math.MinInt64 || spec.TokenDelta == math.MinInt64 || spec.SubscriptionDelta == math.MinInt64 ||
		spec.UserUsedQuotaDelta == math.MinInt64 || spec.UserRequestCountDelta == math.MinInt64 || spec.ChannelUsedQuotaDelta == math.MinInt64 {
		return spec, ErrBillingOperationInvalid
	}
	// A single request is bounded by the same JavaScript-safe wallet domain as
	// all other wallet mutations.  This also prevents an inverse delta from
	// overflowing when a caller retries a failed operation.
	if absInt64Exceeds(spec.WalletDelta, int64(common.MaxWalletQuota)) ||
		// Token and user usage columns are int-sized in the deployed schema;
		// keep each operation inside the same bounded task-quota domain rather
		// than allowing a 64-bit request to overflow those counters.
		absInt64Exceeds(spec.TokenDelta, int64(common.MaxQuota)) ||
		absInt64Exceeds(spec.SubscriptionDelta, int64(common.MaxQuota)) {
		return spec, ErrBillingOperationInvalid
	}
	if absInt64Exceeds(spec.UserUsedQuotaDelta, int64(common.MaxQuota)) ||
		absInt64Exceeds(spec.ChannelUsedQuotaDelta, int64(common.MaxWalletQuota)) ||
		absInt64Exceeds(spec.UserRequestCountDelta, int64(math.MaxInt32)) {
		return spec, ErrBillingOperationInvalid
	}
	if spec.WalletDelta != 0 && spec.UserID <= 0 {
		return spec, ErrBillingOperationInvalid
	}
	if spec.TokenDelta != 0 && spec.TokenID <= 0 {
		return spec, ErrBillingOperationInvalid
	}
	if spec.SubscriptionDelta != 0 && spec.SubscriptionID <= 0 {
		return spec, ErrBillingOperationInvalid
	}
	if (spec.UserUsedQuotaDelta != 0 || spec.UserRequestCountDelta != 0) && spec.UserID <= 0 {
		return spec, ErrBillingOperationInvalid
	}
	if spec.ChannelID < 0 || (spec.ChannelUsedQuotaDelta != 0 && spec.ChannelID <= 0) {
		return spec, ErrBillingOperationInvalid
	}
	if spec.RequireWalletBalance && spec.WalletDelta >= 0 {
		return spec, ErrBillingOperationInvalid
	}
	if spec.RequireTokenBalance && spec.TokenDelta <= 0 {
		return spec, ErrBillingOperationInvalid
	}
	return spec, nil
}

func absInt64Exceeds(value, limit int64) bool {
	if value < 0 {
		return -value > limit
	}
	return value > limit
}

func billingOperationFromSpec(spec BillingOperationSpec) BillingOperation {
	return BillingOperation{
		OperationKey:          BillingOperationKey(spec.RequestID, spec.Component),
		RequestId:             spec.RequestID,
		Component:             spec.Component,
		UserId:                spec.UserID,
		TokenId:               spec.TokenID,
		SubscriptionId:        spec.SubscriptionID,
		WalletDelta:           spec.WalletDelta,
		TokenDelta:            spec.TokenDelta,
		SubscriptionDelta:     spec.SubscriptionDelta,
		UserUsedQuotaDelta:    spec.UserUsedQuotaDelta,
		UserRequestCountDelta: spec.UserRequestCountDelta,
		ChannelId:             spec.ChannelID,
		ChannelUsedQuotaDelta: spec.ChannelUsedQuotaDelta,
		RequireWalletBalance:  spec.RequireWalletBalance,
		RequireTokenBalance:   spec.RequireTokenBalance,
		TokenUnlimited:        spec.TokenUnlimited,
		Status:                BillingOperationPending,
	}
}

// EnsureBillingOperation persists an idempotency marker without applying any
// ledger mutation.  It is used to durably record an accepted asynchronous
// request before a task row (or the settlement itself) is written.  A later
// ApplyBillingOperation call can safely finish the pending marker; if the
// marker already exists, all immutable operation fields are validated so a
// reused request ID cannot silently change its accounting meaning.
func EnsureBillingOperation(spec BillingOperationSpec) error {
	return EnsureBillingOperations(spec)
}

// EnsureBillingRefundOperation durably records an explicit refund intent
// without touching any ledger.  A plain ApplyBillingOperation cannot provide
// this guarantee: when its first transaction fails, the marker insert is rolled
// back together with the ledger writes and a process restart has nothing to
// retry.  Callers must invoke this function after they have positively
// classified the request as failed and before attempting the refund.
//
// Refund intents are fenced against both pending and applied settlement
// operations.  A pending settlement is treated as ambiguous rather than
// guessed at; an operator/reconciler can resolve it once the outcome is known.
func EnsureBillingRefundOperation(spec BillingOperationSpec) error {
	normalized, err := normalizeBillingOperationSpec(spec)
	if err != nil {
		return err
	}
	if !isBillingRefundComponent(normalized.Component) || normalized.UserID <= 0 ||
		(normalized.WalletDelta == 0 && normalized.TokenDelta == 0 && normalized.SubscriptionDelta == 0) ||
		normalized.WalletDelta < 0 || normalized.TokenDelta > 0 || normalized.SubscriptionDelta > 0 {
		return ErrBillingOperationInvalid
	}
	// The generic synchronous `refund` component owns a
	// SubscriptionPreConsumeRecord. Preserve the stronger reservation fence for
	// that one path; task/terminal and component-specific refunds are marker-only
	// operations and must not be forced to invent a reservation row.
	if normalized.Component == BillingOperationRefundComponent && normalized.SubscriptionDelta != 0 {
		return EnsureSubscriptionRefundOperation(normalized, normalized.RequestID)
	}
	if DB == nil {
		return errors.New("billing operation database is not initialized")
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		// Refunds must serialize with all wallet/subscription mutations for the
		// same user. lockBillingUserTx intentionally skips token-only operations,
		// so take the user fence explicitly here even when WalletDelta is zero.
		if err := lockBillingUserByIDTx(tx, normalized.UserID); err != nil {
			return err
		}
		row, err := lockBillingOperationTx(tx, normalized)
		if err != nil {
			return err
		}
		if err := ensureBillingLifecycleOrderTx(tx, normalized,
			classifyBillingOperationComponent(normalized.Component), row.Status, nil); err != nil {
			return err
		}
		if row.Status != BillingOperationPending && row.Status != BillingOperationApplied {
			return fmt.Errorf("%w: unknown status %q", ErrBillingOperationConflict, row.Status)
		}
		return nil
	})
}

// EnsureSubscriptionRefundOperation records and freezes a subscription refund
// intent.  The reservation status transition and the pending BillingOperation
// row are committed together, so a failed/ambiguous refund can be replayed by
// a fresh process while concurrent Reserve calls are fenced out.
func EnsureSubscriptionRefundOperation(spec BillingOperationSpec, requestID string) error {
	normalized, err := normalizeBillingOperationSpec(spec)
	if err != nil {
		return err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || normalized.RequestID != requestID ||
		normalized.Component != BillingOperationRefundComponent || normalized.UserID <= 0 ||
		normalized.SubscriptionID <= 0 || normalized.SubscriptionDelta >= 0 || normalized.WalletDelta != 0 ||
		normalized.TokenDelta > 0 {
		return ErrBillingOperationInvalid
	}
	if DB == nil {
		return errors.New("billing operation database is not initialized")
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		if err := lockBillingUserByIDTx(tx, normalized.UserID); err != nil {
			return err
		}
		operationRow, err := lockBillingOperationTx(tx, normalized)
		if err != nil {
			return err
		}
		if err := ensureBillingLifecycleOrderTx(tx, normalized,
			classifyBillingOperationComponent(normalized.Component), operationRow.Status, nil); err != nil {
			return err
		}
		var marker SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).
			Where("request_id = ?", requestID).
			First(&marker).Error; err != nil {
			return err
		}
		if marker.UserId != normalized.UserID || marker.UserSubscriptionId != normalized.SubscriptionID {
			return ErrBillingOperationConflict
		}
		switch marker.Status {
		case "refunded":
			if operationRow.Status == BillingOperationApplied {
				return nil
			}
			// A closed reservation with a pending refund marker is an inconsistent
			// legacy state. Do not silently mark it applied or issue another refund.
			return ErrBillingOperationConflict
		case "consumed", subscriptionPreConsumeStatusRefundPending:
			reservation, reservationErr := getSubscriptionBillingReservationTx(tx, requestID, 0)
			if reservationErr != nil {
				return reservationErr
			}
			if reservation.UserID != normalized.UserID || reservation.SubscriptionID != normalized.SubscriptionID ||
				reservation.TokenID != normalized.TokenID || reservation.TokenUnlimited != normalized.TokenUnlimited ||
				reservation.SubscriptionQuota < 0 || normalized.SubscriptionDelta != -reservation.SubscriptionQuota ||
				normalized.TokenDelta != -reservation.TokenQuota {
				return ErrBillingReservationChanged
			}
			if operationRow.Status == BillingOperationApplied {
				// Repair a marker-only legacy crash where the ledger operation was
				// committed before the reservation status update.
				marker.Status = "refunded"
				return tx.Save(&marker).Error
			}
			if marker.Status == "consumed" {
				marker.Status = subscriptionPreConsumeStatusRefundPending
				if err := tx.Save(&marker).Error; err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("%w: invalid subscription pre-consume status %q", ErrBillingOperationConflict, marker.Status)
		}
	})
}

// EnsureBillingOperations atomically installs one or more pending operation
// markers.  Keeping marker creation in one transaction matters for async task
// recovery: the financial settlement and its informational usage operation
// must either both be discoverable by the reconciler or neither be advertised
// as durable. Existing applied/pending rows are accepted only when their full
// immutable specification matches.
func EnsureBillingOperations(specs ...BillingOperationSpec) error {
	if len(specs) == 0 {
		return nil
	}
	if DB == nil {
		return errors.New("billing operation database is not initialized")
	}
	normalized := make([]BillingOperationSpec, 0, len(specs))
	seen := make(map[string]BillingOperationSpec, len(specs))
	for _, spec := range specs {
		item, err := normalizeBillingOperationSpec(spec)
		if err != nil {
			return err
		}
		key := BillingOperationKey(item.RequestID, item.Component)
		if previous, ok := seen[key]; ok {
			if !billingOperationSpecsEqual(previous, item) {
				return ErrBillingOperationConflict
			}
			continue
		}
		seen[key] = item
		normalized = append(normalized, item)
	}
	if err := validateBillingOperationBatch(normalized); err != nil {
		return err
	}
	// Every operation transaction acquires user fences in this order. Sorting by
	// owner (then by stable operation key) prevents a multi-user batch from
	// locking user A while another batch locks user B and both later wait for the
	// other row. The operation -> user order is retained for compatibility with
	// the single-operation apply paths; only the order among a caller's specs is
	// normalized here.
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].UserID != normalized[j].UserID {
			return normalized[i].UserID < normalized[j].UserID
		}
		return BillingOperationKey(normalized[i].RequestID, normalized[i].Component) <
			BillingOperationKey(normalized[j].RequestID, normalized[j].Component)
	})
	batchOperationKeys := make(map[string]struct{}, len(normalized))
	for _, spec := range normalized {
		batchOperationKeys[BillingOperationKey(spec.RequestID, spec.Component)] = struct{}{}
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		for _, spec := range normalized {
			kind := classifyBillingOperationComponent(spec.Component)
			if kind != billingOperationClassUnknown && kind != billingOperationClassProviderReversal {
				if spec.UserID <= 0 {
					return ErrBillingOperationInvalid
				}
				// Lock the owner before the operation row. All other durable paths
				// follow this same order, allowing the peer lifecycle scan below to
				// use SELECT ... FOR UPDATE safely.
				if err := lockBillingUserByIDTx(tx, spec.UserID); err != nil {
					return err
				}
			}
			candidate := billingOperationFromSpec(spec)
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
				return err
			}
			var row BillingOperation
			if err := lockForUpdate(tx).
				Where("operation_key = ?", candidate.OperationKey).
				First(&row).Error; err != nil {
				return err
			}
			if !billingOperationMatches(&row, spec) {
				return ErrBillingOperationConflict
			}
			if row.Status != BillingOperationPending && row.Status != BillingOperationApplied {
				return fmt.Errorf("%w: unknown status %q", ErrBillingOperationConflict, row.Status)
			}
			// Marker-only lifecycle operations participate in the same fence as the
			// later side-effecting apply. Without this check a settlement/usage or
			// charge marker could be committed after a refund intent (or vice versa),
			// leaving both rows permanently blocked by the fail-closed apply checks.
			// Take the portable user lock before the check so concurrent intent
			// creation has a deterministic winner. Operation pairs in this same batch are
			// excluded from the database scan after validateBillingOperationBatch has
			// checked their pairwise compatibility (normally settle + usage).
			if kind != billingOperationClassUnknown && kind != billingOperationClassProviderReversal {
				if err := ensureBillingLifecycleOrderTx(tx, spec, kind, row.Status, batchOperationKeys); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// billingOperationSpecsEqual compares normalized specs without relying on the
// derived operation key. It is intentionally kept private; callers should use
// EnsureBillingOperations so normalization and bounds are always enforced.
func billingOperationSpecsEqual(a, b BillingOperationSpec) bool {
	return a.RequestID == b.RequestID &&
		a.Component == b.Component &&
		a.UserID == b.UserID &&
		a.TokenID == b.TokenID &&
		a.SubscriptionID == b.SubscriptionID &&
		a.WalletDelta == b.WalletDelta &&
		a.TokenDelta == b.TokenDelta &&
		a.SubscriptionDelta == b.SubscriptionDelta &&
		a.UserUsedQuotaDelta == b.UserUsedQuotaDelta &&
		a.UserRequestCountDelta == b.UserRequestCountDelta &&
		a.ChannelID == b.ChannelID &&
		a.ChannelUsedQuotaDelta == b.ChannelUsedQuotaDelta &&
		a.RequireWalletBalance == b.RequireWalletBalance &&
		a.RequireTokenBalance == b.RequireTokenBalance &&
		a.TokenUnlimited == b.TokenUnlimited
}

// GetBillingOperation loads one marker by its stable request/component
// identity.  It is primarily used by the pending-operation reconciler to
// enforce settlement-before-usage ordering.
func GetBillingOperation(requestID, component string) (*BillingOperation, error) {
	requestID = strings.TrimSpace(requestID)
	component = strings.TrimSpace(component)
	if requestID == "" || component == "" {
		return nil, ErrBillingOperationInvalid
	}
	if DB == nil {
		return nil, errors.New("billing operation database is not initialized")
	}
	var row BillingOperation
	if err := DB.Where("request_id = ? AND component = ?", requestID, component).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// GetSubscriptionBillingReservation rebuilds the full live reservation from
// its immutable base marker and every applied reserve:* journal entry.
func GetSubscriptionBillingReservation(requestID string) (*SubscriptionBillingReservation, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, ErrBillingOperationInvalid
	}
	if DB == nil {
		return nil, errors.New("billing operation database is not initialized")
	}
	return getSubscriptionBillingReservationTx(DB, requestID, 0)
}

func getSubscriptionBillingReservationTx(tx *gorm.DB, requestID string, ignoredPendingReserveID int) (*SubscriptionBillingReservation, error) {
	if tx == nil {
		return nil, ErrBillingOperationInvalid
	}
	var marker SubscriptionPreConsumeRecord
	if err := tx.Where("request_id = ?", requestID).First(&marker).Error; err != nil {
		return nil, err
	}
	if marker.Status != "consumed" && marker.Status != subscriptionPreConsumeStatusRefundPending && marker.Status != "refunded" {
		return nil, fmt.Errorf("%w: invalid subscription pre-consume status %q", ErrBillingOperationConflict, marker.Status)
	}
	if marker.UserId <= 0 || marker.UserSubscriptionId <= 0 || marker.PreConsumed <= 0 || marker.PreConsumed > int64(common.MaxQuota) {
		return nil, ErrBillingOperationConflict
	}

	var base BillingOperation
	if err := tx.Where("request_id = ? AND component = ?", requestID, "subscription_preconsume_token").First(&base).Error; err != nil {
		return nil, err
	}
	if base.Status != BillingOperationApplied || base.UserId != marker.UserId ||
		base.WalletDelta != 0 || base.SubscriptionId != 0 || base.SubscriptionDelta != 0 ||
		(base.TokenDelta != 0 && base.TokenDelta != marker.PreConsumed) {
		return nil, ErrBillingOperationConflict
	}

	var subscription UserSubscription
	if err := tx.Select("id", "user_id", "amount_total", "amount_used").
		Where("id = ?", marker.UserSubscriptionId).First(&subscription).Error; err != nil {
		return nil, err
	}
	if subscription.UserId != marker.UserId {
		return nil, ErrBillingOperationConflict
	}

	reservation := &SubscriptionBillingReservation{
		RequestID:             requestID,
		Status:                marker.Status,
		UserID:                marker.UserId,
		TokenID:               base.TokenId,
		SubscriptionID:        marker.UserSubscriptionId,
		BaseSubscriptionQuota: marker.PreConsumed,
		SubscriptionQuota:     marker.PreConsumed,
		TokenQuota:            base.TokenDelta,
		TokenUnlimited:        base.TokenUnlimited,
		AmountTotal:           subscription.AmountTotal,
		AmountUsed:            subscription.AmountUsed,
	}

	var reserves []BillingOperation
	if err := tx.Where("request_id = ? AND component LIKE ?", requestID, "reserve:%").
		Order("id asc").Find(&reserves).Error; err != nil {
		return nil, err
	}
	for _, reserve := range reserves {
		if reserve.Id == ignoredPendingReserveID && reserve.Status == BillingOperationPending {
			continue
		}
		if reserve.Status != BillingOperationApplied || reserve.UserId != reservation.UserID ||
			reserve.TokenId != reservation.TokenID || reserve.SubscriptionId != reservation.SubscriptionID ||
			reserve.WalletDelta != 0 || reserve.SubscriptionDelta <= 0 ||
			reserve.TokenUnlimited != reservation.TokenUnlimited {
			return nil, ErrBillingOperationConflict
		}
		target, err := strconv.ParseInt(strings.TrimPrefix(reserve.Component, "reserve:"), 10, 64)
		if err != nil || target <= 0 || target > int64(common.MaxQuota) {
			return nil, ErrBillingOperationConflict
		}
		if reservation.SubscriptionQuota > int64(common.MaxQuota)-reserve.SubscriptionDelta {
			return nil, ErrBillingOperationConflict
		}
		reservation.SubscriptionQuota += reserve.SubscriptionDelta
		if target != reservation.SubscriptionQuota {
			return nil, ErrBillingOperationConflict
		}
		expectedTokenDelta := reserve.SubscriptionDelta
		if base.TokenDelta == 0 {
			expectedTokenDelta = 0
		}
		if reserve.TokenDelta != expectedTokenDelta ||
			reserve.RequireTokenBalance != (expectedTokenDelta > 0 && !reserve.TokenUnlimited) {
			return nil, ErrBillingOperationConflict
		}
		if reservation.TokenQuota > int64(common.MaxQuota)-reserve.TokenDelta {
			return nil, ErrBillingOperationConflict
		}
		reservation.TokenQuota += reserve.TokenDelta
	}
	return reservation, nil
}

// GetPendingBillingOperations returns a bounded, deterministic batch of
// marker-only operations.  Callers must pass an explicit component allowlist;
// generic pre-consume/refund rows are deliberately excluded because their
// recovery semantics differ from async settlement.
func GetPendingBillingOperations(components []string, limit int) ([]BillingOperation, error) {
	if DB == nil {
		return nil, errors.New("billing operation database is not initialized")
	}
	allowed := make([]string, 0, len(components))
	seen := make(map[string]struct{}, len(components))
	for _, component := range components {
		component = strings.TrimSpace(component)
		if component == "" {
			continue
		}
		if _, ok := seen[component]; ok {
			continue
		}
		seen[component] = struct{}{}
		allowed = append(allowed, component)
	}
	if len(allowed) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var rows []BillingOperation
	err := DB.Where("status = ? AND component IN ?", BillingOperationPending, allowed).
		Order("updated_at asc, id asc").Limit(limit).Find(&rows).Error
	return rows, err
}

// DeferPendingBillingOperation moves a failed pending marker behind untouched
// work without changing its lifecycle status. The observed timestamp is a CAS
// fence: a concurrent apply or another worker's deferral is never overwritten.
// This keeps one permanently blocked marker from starving every later request
// in the bounded reconciliation queue.
func DeferPendingBillingOperation(id int, observedUpdatedAt int64) error {
	if DB == nil {
		return errors.New("billing operation database is not initialized")
	}
	if id <= 0 {
		return ErrBillingOperationInvalid
	}
	next := common.GetTimestamp() + 1
	if next <= observedUpdatedAt {
		if observedUpdatedAt == math.MaxInt64 {
			next = math.MaxInt64
		} else {
			next = observedUpdatedAt + 1
		}
	}
	return DB.Model(&BillingOperation{}).
		Where("id = ? AND status = ? AND updated_at = ?", id, BillingOperationPending, observedUpdatedAt).
		UpdateColumn("updated_at", next).Error
}

// HasPendingBillingOperationsWithError is the cheap scheduler probe
// corresponding to GetPendingBillingOperations. Returning the database error
// is important for schedulers: an outage must not be interpreted as an empty
// queue, otherwise durable billing operations can remain stranded indefinitely.
func HasPendingBillingOperationsWithError(components []string) (bool, error) {
	if DB == nil || len(components) == 0 {
		if DB == nil {
			return false, errors.New("database is not initialized")
		}
		return false, nil
	}
	allowed := make([]string, 0, len(components))
	for _, component := range components {
		component = strings.TrimSpace(component)
		if component != "" {
			allowed = append(allowed, component)
		}
	}
	if len(allowed) == 0 {
		return false, nil
	}
	var id int
	err := DB.Model(&BillingOperation{}).
		Where("status = ? AND component IN ?", BillingOperationPending, allowed).
		Limit(1).Pluck("id", &id).Error
	if err != nil {
		return false, err
	}
	return id != 0, nil
}

// HasPendingBillingOperations is retained for callers that use the historical
// boolean API. Scheduler code should use HasPendingBillingOperationsWithError.
func HasPendingBillingOperations(components []string) bool {
	pending, _ := HasPendingBillingOperationsWithError(components)
	return pending
}

func billingOperationMatches(row *BillingOperation, spec BillingOperationSpec) bool {
	if row == nil {
		return false
	}
	return row.OperationKey == BillingOperationKey(spec.RequestID, spec.Component) &&
		row.RequestId == spec.RequestID &&
		row.Component == spec.Component &&
		row.UserId == spec.UserID &&
		row.TokenId == spec.TokenID &&
		row.SubscriptionId == spec.SubscriptionID &&
		row.WalletDelta == spec.WalletDelta &&
		row.TokenDelta == spec.TokenDelta &&
		row.SubscriptionDelta == spec.SubscriptionDelta &&
		row.UserUsedQuotaDelta == spec.UserUsedQuotaDelta &&
		row.UserRequestCountDelta == spec.UserRequestCountDelta &&
		row.ChannelId == spec.ChannelID &&
		row.ChannelUsedQuotaDelta == spec.ChannelUsedQuotaDelta &&
		row.RequireWalletBalance == spec.RequireWalletBalance &&
		row.RequireTokenBalance == spec.RequireTokenBalance &&
		row.TokenUnlimited == spec.TokenUnlimited
}

// ApplySubscriptionReserveBillingOperation applies one monotonic subscription
// top-up only while the owning pre-consume marker is still open. The marker
// check shares the ledger transaction, so a stale process cannot reserve after
// another process has refunded the request.
func ApplySubscriptionReserveBillingOperation(spec BillingOperationSpec) error {
	normalized, err := normalizeBillingOperationSpec(spec)
	if err != nil {
		return err
	}
	target, parseErr := strconv.ParseInt(strings.TrimPrefix(normalized.Component, "reserve:"), 10, 64)
	if parseErr != nil || !strings.HasPrefix(normalized.Component, "reserve:") ||
		target <= 0 || target > int64(common.MaxQuota) || normalized.UserID <= 0 ||
		normalized.SubscriptionID <= 0 || normalized.WalletDelta != 0 ||
		normalized.SubscriptionDelta <= 0 || normalized.TokenDelta < 0 {
		return ErrBillingOperationInvalid
	}
	if DB == nil {
		return errors.New("billing operation database is not initialized")
	}

	var appliedNow bool
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockBillingUserTx(tx, normalized); err != nil {
			return err
		}
		operationRow, err := lockBillingOperationTx(tx, normalized)
		if err != nil {
			return err
		}
		var marker SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).Where("request_id = ?", normalized.RequestID).First(&marker).Error; err != nil {
			return err
		}
		if marker.Status != "consumed" || marker.UserId != normalized.UserID ||
			marker.UserSubscriptionId != normalized.SubscriptionID {
			return ErrBillingOperationConflict
		}
		if operationRow.Status == BillingOperationApplied {
			_, err := getSubscriptionBillingReservationTx(tx, normalized.RequestID, 0)
			return err
		}

		reservation, err := getSubscriptionBillingReservationTx(tx, normalized.RequestID, operationRow.Id)
		if err != nil {
			return err
		}
		if reservation.Status != "consumed" || reservation.UserID != normalized.UserID ||
			reservation.TokenID != normalized.TokenID || reservation.SubscriptionID != normalized.SubscriptionID ||
			reservation.TokenUnlimited != normalized.TokenUnlimited {
			return ErrBillingOperationConflict
		}
		if reservation.SubscriptionQuota >= target {
			return ErrBillingReservationChanged
		}
		required := target - reservation.SubscriptionQuota
		expectedTokenDelta := required
		if reservation.TokenQuota == 0 {
			expectedTokenDelta = 0
		}
		if normalized.SubscriptionDelta != required || normalized.TokenDelta != expectedTokenDelta ||
			normalized.RequireTokenBalance != (expectedTokenDelta > 0 && !normalized.TokenUnlimited) {
			return ErrBillingReservationChanged
		}
		appliedNow, err = applyBillingOperationTx(tx, normalized)
		return err
	})
	if err != nil {
		return err
	}
	syncBillingOperationCaches(normalized, appliedNow)
	return nil
}

// ApplyBillingOperation atomically applies all requested ledger deltas and
// records the operation as applied.  It is safe to call concurrently from
// multiple processes: the unique operation key and row lock ensure that only
// one transaction mutates the ledgers, while retries observe the applied row.
//
// Cache updates happen only after commit.  A cache miss is harmless (the next
// read hydrates from the database); a cache write failure invalidates the
// affected entry so stale quota cannot authorize a later request.
func ApplyBillingOperation(spec BillingOperationSpec) error {
	normalized, err := normalizeBillingOperationSpec(spec)
	if err != nil {
		return err
	}

	var appliedNow bool
	if DB == nil {
		return errors.New("billing operation database is not initialized")
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		var txErr error
		appliedNow, txErr = applyBillingOperationTx(tx, normalized)
		return txErr
	})
	if err != nil {
		return err
	}

	syncBillingOperationCaches(normalized, appliedNow)
	return nil
}

// ApplyBillingOperationAndMarkSubscriptionPreConsumeRefunded applies a
// subscription/token refund and closes its pre-consume marker in one
// transaction.  A plain ApplyBillingOperation followed by
// MarkSubscriptionPreConsumeRefunded leaves a crash window where the ledgers
// are already refunded but the reservation still says "consumed".  Keeping
// the marker update in this transaction makes both the first attempt and any
// retry after an ambiguous database response converge on the same state.
func ApplyBillingOperationAndMarkSubscriptionPreConsumeRefunded(spec BillingOperationSpec, requestID string) error {
	normalized, err := normalizeBillingOperationSpec(spec)
	if err != nil {
		return err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || normalized.RequestID != requestID {
		return ErrBillingOperationConflict
	}
	if normalized.SubscriptionID <= 0 || normalized.SubscriptionDelta >= 0 {
		return ErrBillingOperationInvalid
	}
	if DB == nil {
		return errors.New("billing operation database is not initialized")
	}

	var appliedNow bool
	err = DB.Transaction(func(tx *gorm.DB) error {
		// Keep the refund lock order identical to every other durable billing
		// path: owning user -> operation -> reservation marker ->
		// subscription/token rows. Discover a legacy owner before taking any
		// lock, then revalidate it against the locked marker below.
		userID := normalized.UserID
		if userID <= 0 {
			// A legacy caller may omit UserID even though the reservation carries
			// the authoritative owner.
			var owner struct {
				UserID int `gorm:"column:user_id"`
			}
			if err := tx.Model(&SubscriptionPreConsumeRecord{}).
				Select("user_id").Where("request_id = ?", requestID).First(&owner).Error; err != nil {
				return err
			}
			userID = owner.UserID
		}
		if userID <= 0 {
			return ErrBillingOperationInvalid
		}
		var user User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userID).First(&user).Error; err != nil {
			return err
		}
		operationRow, err := lockBillingOperationTx(tx, normalized)
		if err != nil {
			return err
		}
		var record SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).
			Where("request_id = ?", requestID).
			First(&record).Error; err != nil {
			return err
		}
		if record.UserSubscriptionId != normalized.SubscriptionID ||
			(normalized.UserID > 0 && record.UserId != normalized.UserID) ||
			normalized.SubscriptionDelta != -record.PreConsumed {
			return ErrBillingOperationConflict
		}
		switch record.Status {
		case "refunded":
			// A refunded reservation is already the source of truth.  Do not
			// apply a pending operation against it; an older path may have closed
			// the marker without carrying the durable operation row.
			if operationRow.Status == BillingOperationPending {
				return ErrBillingOperationConflict
			}
			return nil
		case "consumed", subscriptionPreConsumeStatusRefundPending:
			var txErr error
			appliedNow, txErr = applyBillingOperationTx(tx, normalized)
			if txErr != nil {
				return txErr
			}
			record.Status = "refunded"
			return tx.Save(&record).Error
		default:
			return fmt.Errorf("invalid subscription pre-consume status %q", record.Status)
		}
	})
	if err != nil {
		return err
	}
	syncBillingOperationCaches(normalized, appliedNow)
	return nil
}

// ApplyBillingOperationAndMarkSubscriptionReservationRefunded refunds the
// base subscription reservation plus every applied reserve:* top-up. The exact
// journal totals are revalidated while the user and pre-consume marker are
// locked, closing the race between a snapshot read and a concurrent Reserve.
func ApplyBillingOperationAndMarkSubscriptionReservationRefunded(spec BillingOperationSpec, requestID string) error {
	normalized, err := normalizeBillingOperationSpec(spec)
	if err != nil {
		return err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || normalized.RequestID != requestID ||
		normalized.UserID <= 0 || normalized.SubscriptionID <= 0 ||
		normalized.SubscriptionDelta >= 0 || normalized.TokenDelta > 0 {
		return ErrBillingOperationInvalid
	}
	if DB == nil {
		return errors.New("billing operation database is not initialized")
	}

	var appliedNow bool
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockBillingUserTx(tx, normalized); err != nil {
			return err
		}
		operationRow, err := lockBillingOperationTx(tx, normalized)
		if err != nil {
			return err
		}
		var marker SubscriptionPreConsumeRecord
		if err := lockForUpdate(tx).Where("request_id = ?", requestID).First(&marker).Error; err != nil {
			return err
		}
		if marker.UserId != normalized.UserID || marker.UserSubscriptionId != normalized.SubscriptionID {
			return ErrBillingOperationConflict
		}
		switch marker.Status {
		case "refunded":
			if operationRow.Status != BillingOperationApplied {
				return ErrBillingOperationConflict
			}
			return nil
		case "consumed", subscriptionPreConsumeStatusRefundPending:
			reservation, err := getSubscriptionBillingReservationTx(tx, requestID, 0)
			if err != nil {
				return err
			}
			if reservation.UserID != normalized.UserID || reservation.TokenID != normalized.TokenID ||
				reservation.SubscriptionID != normalized.SubscriptionID ||
				reservation.TokenUnlimited != normalized.TokenUnlimited {
				return ErrBillingOperationConflict
			}
			if normalized.SubscriptionDelta != -reservation.SubscriptionQuota ||
				normalized.TokenDelta != -reservation.TokenQuota {
				return ErrBillingReservationChanged
			}
			appliedNow, err = applyBillingOperationTx(tx, normalized)
			if err != nil {
				return err
			}
			marker.Status = "refunded"
			return tx.Save(&marker).Error
		default:
			return fmt.Errorf("invalid subscription pre-consume status %q", marker.Status)
		}
	})
	if err != nil {
		return err
	}
	syncBillingOperationCaches(normalized, appliedNow)
	return nil
}

// applyBillingOperationTx applies one normalized operation while tx is open
// and returns whether this transaction changed the ledgers.  Keeping the
// marker/ledger sequence in one helper lets task-scoped adjustments add their
// own quota CAS without duplicating the idempotency checks.
func applyBillingOperationTx(tx *gorm.DB, normalized BillingOperationSpec) (bool, error) {
	if tx == nil {
		return false, ErrBillingOperationInvalid
	}
	// Acquire the portable owner fence before the operation row. Every caller
	// uses this order, so lifecycle peer rows can be locked later without a
	// component-to-user deadlock.
	if err := lockBillingUserTx(tx, normalized); err != nil {
		return false, err
	}
	row, err := lockBillingOperationTx(tx, normalized)
	if err != nil {
		return false, err
	}
	if row.Status == BillingOperationApplied {
		return false, nil
	}
	if row.Status != BillingOperationPending {
		return false, fmt.Errorf("%w: unknown status %q", ErrBillingOperationConflict, row.Status)
	}

	// Lifecycle ordering is checked while the user fence is held.  A refund
	// intent therefore wins or loses atomically against a concurrent settle or
	// Reserve attempt; neither side can observe a half-applied request outcome.
	if err := ensureBillingLifecycleOrderTx(tx, normalized,
		classifyBillingOperationComponent(normalized.Component), row.Status, nil); err != nil {
		return false, err
	}
	if isBillingUsageComponent(normalized.Component) {
		if err := ensureBillingUsageCompanionTx(tx, normalized); err != nil {
			return false, err
		}
	}

	if normalized.WalletDelta != 0 {
		if err := applyBillingWalletDeltaTx(tx, normalized); err != nil {
			return false, err
		}
	}
	if normalized.SubscriptionDelta != 0 {
		if err := applyBillingSubscriptionDeltaTx(tx, normalized); err != nil {
			return false, err
		}
	}
	if normalized.TokenDelta != 0 {
		if err := applyBillingTokenDeltaTx(tx, normalized); err != nil {
			return false, err
		}
	}
	if normalized.UserUsedQuotaDelta != 0 || normalized.UserRequestCountDelta != 0 {
		if err := applyBillingUserUsageDeltaTx(tx, normalized); err != nil {
			return false, err
		}
	}
	if normalized.ChannelUsedQuotaDelta != 0 {
		if err := applyBillingChannelUsageDeltaTx(tx, normalized); err != nil {
			return false, err
		}
	}
	if common.RedisEnabled {
		mutationID := BillingOperationKey(normalized.RequestID, normalized.Component)[:32]
		if normalized.WalletDelta != 0 {
			if err := upsertQuotaCacheRepair(tx, QuotaCacheRepairEntityUser, normalized.UserID,
				getUserCacheKey(normalized.UserID), mutationID, ErrQuotaCacheMiss); err != nil {
				return false, err
			}
		}
		if normalized.TokenDelta != 0 {
			var token Token
			err := tx.Unscoped().Select("id", mainKeyColumn(tx), "key_ciphertext", "key_hash").Where("id = ?", normalized.TokenID).First(&token).Error
			if errors.Is(err, gorm.ErrRecordNotFound) && normalized.TokenDelta < 0 {
				// A hard-deleted token has no cache that can authorize future use.
			} else if err != nil {
				return false, err
			} else if strings.TrimSpace(token.Key) != "" {
				if err := upsertQuotaCacheRepair(tx, QuotaCacheRepairEntityToken, normalized.TokenID,
					getTokenCacheKey(token.Key), mutationID, ErrQuotaCacheMiss); err != nil {
					return false, err
				}
			}
		}
	}

	if err := tx.Model(&BillingOperation{}).
		Where("id = ?", row.Id).
		Updates(map[string]interface{}{"status": BillingOperationApplied, "updated_at": common.GetTimestamp()}).Error; err != nil {
		return false, err
	}
	return true, nil
}

// lockBillingOperationTx creates (if necessary) and locks one operation row.
// Keeping this small primitive separate lets callers that must discover a
// subscription after reserving the idempotency key establish the same global
// order without applying the operation prematurely.
func lockBillingOperationTx(tx *gorm.DB, normalized BillingOperationSpec) (BillingOperation, error) {
	if tx == nil {
		return BillingOperation{}, ErrBillingOperationInvalid
	}
	candidate := billingOperationFromSpec(normalized)
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
		return BillingOperation{}, err
	}
	// On MySQL's default REPEATABLE READ a plain read can retain a snapshot
	// taken before a concurrent insert; the locking/current read avoids treating
	// a valid duplicate as missing.
	var row BillingOperation
	if err := lockForUpdate(tx).
		Where("operation_key = ?", candidate.OperationKey).
		First(&row).Error; err != nil {
		return BillingOperation{}, err
	}
	if !billingOperationMatches(&row, normalized) {
		return BillingOperation{}, ErrBillingOperationConflict
	}
	if row.Status != BillingOperationPending && row.Status != BillingOperationApplied {
		return BillingOperation{}, fmt.Errorf("%w: unknown status %q", ErrBillingOperationConflict, row.Status)
	}
	return row, nil
}

// lockBillingUserTx is part of the durable billing lock protocol. In addition
// to rows that directly mutate a wallet/subscription/aggregate, every
// lifecycle-sensitive component takes the user fence even when it only mutates
// a token (for example refund_token or subscription_preconsume_token). This is
// the portable serialization point that makes the request-level fence race
// free across SQLite, MySQL, and PostgreSQL.
func lockBillingUserTx(tx *gorm.DB, spec BillingOperationSpec) error {
	if tx == nil {
		return ErrBillingOperationInvalid
	}
	if spec.UserID <= 0 {
		if billingOperationNeedsUserFence(spec.Component) {
			return ErrBillingOperationInvalid
		}
		return nil
	}
	if spec.WalletDelta == 0 && spec.SubscriptionDelta == 0 &&
		spec.UserUsedQuotaDelta == 0 && spec.UserRequestCountDelta == 0 &&
		!billingOperationNeedsUserFence(spec.Component) {
		return nil
	}
	var user User
	return lockForUpdate(tx).Select("id").Where("id = ?", spec.UserID).First(&user).Error
}

// lockBillingUserByIDTx is used by lifecycle fences whose operation has no
// wallet/subscription delta yet (for example a token-only refund intent). A
// user row is the portable serialization point shared by pre-consume, settle,
// reserve, and refund transactions.
func lockBillingUserByIDTx(tx *gorm.DB, userID int) error {
	if tx == nil || userID <= 0 {
		return ErrBillingOperationInvalid
	}
	var user User
	return lockForUpdate(tx).Select("id").Where("id = ?", userID).First(&user).Error
}

// classifyBillingOperationComponent is the single source of truth for
// request-lifecycle semantics. Do not infer a role from the sign of a delta:
// terminal task refunds deliberately reverse an already-settled request, while
// violation fees are an independent post-failure charge.
func classifyBillingOperationComponent(component string) billingOperationComponentClass {
	switch strings.TrimSpace(component) {
	case BillingOperationWalletRefundComponent,
		BillingOperationSubscriptionRefundComponent,
		BillingOperationTokenRefundComponent,
		BillingOperationExtraRefundComponent,
		BillingOperationRefundComponent:
		return billingOperationClassRefund
	case BillingOperationTaskRefundComponent,
		BillingOperationTerminalRefundComponent,
		BillingOperationLegacyTaskRefundComponent:
		return billingOperationClassTerminalRefund
	case BillingOperationWalletPreConsumeComponent,
		BillingOperationSubscriptionPreConsumeTokenComponent,
		BillingOperationPreConsumeComponent,
		BillingOperationPreConsumeTokenComponent:
		return billingOperationClassCharge
	case BillingOperationSettleComponent,
		BillingOperationLegacySettleComponent,
		BillingOperationWalletSettleComponent,
		BillingOperationSubscriptionSettleComponent,
		BillingOperationTerminalSettleComponent,
		BillingOperationLegacyTaskRecalculateComponent:
		return billingOperationClassSettlement
	case BillingOperationSettleUsageComponent,
		BillingOperationSyncSettleUsageComponent,
		BillingOperationLegacySettleUsageComponent,
		BillingOperationTerminalSettleUsageComponent:
		return billingOperationClassUsage
	case BillingOperationViolationFeeComponent:
		// ChargeViolationFeeIfNeeded intentionally runs after the normal failure
		// flow (including a successful refund), so this component is not part of
		// the request settlement/refund fence.
		return billingOperationClassPostAdjustment
	case "provider_reversal":
		// Provider refund/dispute events have their own effect state machine and
		// may legitimately coexist with local request operations.
		return billingOperationClassProviderReversal
	}
	component = strings.TrimSpace(component)
	if strings.HasPrefix(component, "reserve:") || strings.HasPrefix(component, "realtime_preconsume:") {
		return billingOperationClassCharge
	}
	return billingOperationClassUnknown
}

func isBillingSettlementComponent(component string) bool {
	return classifyBillingOperationComponent(component) == billingOperationClassSettlement
}

// isBillingRefundComponent includes both ordinary request refunds and terminal
// task refunds. Callers that need the ordering distinction must use the more
// specific helpers below.
func isBillingRefundComponent(component string) bool {
	class := classifyBillingOperationComponent(component)
	return class == billingOperationClassRefund || class == billingOperationClassTerminalRefund
}

func isBillingOrdinaryRefundComponent(component string) bool {
	return classifyBillingOperationComponent(component) == billingOperationClassRefund
}

func isBillingTerminalRefundComponent(component string) bool {
	return classifyBillingOperationComponent(component) == billingOperationClassTerminalRefund
}

func isBillingUsageComponent(component string) bool {
	return classifyBillingOperationComponent(component) == billingOperationClassUsage
}

func isBillingChargeComponent(component string) bool {
	return classifyBillingOperationComponent(component) == billingOperationClassCharge
}

func billingOperationNeedsUserFence(component string) bool {
	class := classifyBillingOperationComponent(component)
	return class == billingOperationClassCharge ||
		class == billingOperationClassSettlement ||
		class == billingOperationClassUsage ||
		class == billingOperationClassRefund ||
		class == billingOperationClassTerminalRefund ||
		class == billingOperationClassPostAdjustment
}

func billingOperationStatusActive(status BillingOperationStatus) bool {
	return status == BillingOperationPending || status == BillingOperationApplied
}

// billingLifecycleConflict reports whether an existing marker in otherStatus
// is incompatible with a new marker of currentClass. The relation is
// intentionally directional: terminal refunds may follow an applied
// settlement, but no settlement may be created after a terminal refund.
func billingLifecycleConflict(currentClass, otherClass billingOperationComponentClass, otherStatus BillingOperationStatus) bool {
	if currentClass == billingOperationClassUnknown ||
		currentClass == billingOperationClassProviderReversal ||
		currentClass == billingOperationClassPostAdjustment ||
		otherClass == billingOperationClassUnknown ||
		otherClass == billingOperationClassProviderReversal ||
		otherClass == billingOperationClassPostAdjustment {
		return false
	}
	if !billingOperationStatusActive(otherStatus) {
		// A malformed lifecycle marker must not be treated as an empty row. If it
		// has a recognized role, fail closed so an operator can repair it.
		return true
	}

	switch currentClass {
	case billingOperationClassRefund:
		// Synchronous refunds are only valid before settlement/usage and must not
		// race a terminal refund. An applied/pending pre-consume marker is the
		// balance being reversed and is therefore allowed; a pending one remains
		// ambiguous and is fenced below.
		if otherClass == billingOperationClassSettlement ||
			otherClass == billingOperationClassUsage ||
			otherClass == billingOperationClassTerminalRefund {
			return true
		}
		if otherClass == billingOperationClassCharge && otherStatus == BillingOperationPending {
			return true
		}
	case billingOperationClassTerminalRefund:
		// Terminal refunds reverse the final task charge. They may follow an
		// already-applied submit/terminal settlement and usage marker, but an
		// in-flight marker is ambiguous and must finish first.
		if otherClass == billingOperationClassRefund || otherClass == billingOperationClassTerminalRefund {
			return true
		}
		if (otherClass == billingOperationClassSettlement ||
			otherClass == billingOperationClassUsage ||
			otherClass == billingOperationClassCharge) &&
			otherStatus == BillingOperationPending {
			return true
		}
	case billingOperationClassCharge:
		// Once any refund intent exists, no new charge/reservation may be
		// appended. Applied pre-consume markers are not considered here because
		// this direction is the charge attempt itself.
		if otherClass == billingOperationClassRefund || otherClass == billingOperationClassTerminalRefund {
			return true
		}
	case billingOperationClassSettlement:
		// A settlement after either kind of refund would charge money that has
		// already been returned.
		if otherClass == billingOperationClassRefund || otherClass == billingOperationClassTerminalRefund {
			return true
		}
	case billingOperationClassUsage:
		// Ordinary refunds invalidate usage entirely. A terminal refund may
		// reverse an already-recorded usage delta, but not leave a pending usage
		// marker that could be applied after the refund.
		if otherClass == billingOperationClassRefund {
			return true
		}
		if otherClass == billingOperationClassTerminalRefund && otherStatus == BillingOperationPending {
			return true
		}
	}
	return false
}

// billingOperationAliasConflict closes a gap that class-level lifecycle
// checks intentionally leave open: compatibility component names in the same
// phase have different operation keys, but they still represent one logical
// mutation.  Allowing two such aliases for one request would apply the wallet,
// token, subscription, or usage delta twice.  The check is deliberately
// component-based (rather than sign-based) because a few old refund paths use
// multiple components for distinct ledgers.
//
// The following combinations remain valid by design:
//   - one submit-settlement alias with one submit-usage alias;
//   - one terminal-settlement alias with terminal-settlement usage;
//   - submit settlement followed by a terminal settlement or terminal refund;
//   - the legacy wallet fallback's wallet_refund + refund_token/refund_extra
//     pair (the wallet component only touches the wallet; the other two touch
//     independent ledgers).
//
// currentStatus/otherStatus are included so callers can use the same helper
// for marker creation and replay.  A duplicate is blocked whenever both rows
// are active (pending or applied); malformed/closed rows are left to the
// existing class-level validation instead of being treated as a live alias.
func billingOperationAliasConflict(currentComponent, otherComponent string, currentStatus, otherStatus BillingOperationStatus) bool {
	currentComponent = strings.TrimSpace(currentComponent)
	otherComponent = strings.TrimSpace(otherComponent)
	if currentComponent == "" || otherComponent == "" || currentComponent == otherComponent {
		return false
	}
	if !billingOperationStatusActive(currentStatus) || !billingOperationStatusActive(otherStatus) {
		return false
	}

	// Submit-time financial settlement aliases are mutually exclusive.  The
	// terminal adjustment aliases are a separate phase and may follow one
	// submit settlement, so they are intentionally not included here.
	if isBillingSubmitSettlementAlias(currentComponent) && isBillingSubmitSettlementAlias(otherComponent) {
		return true
	}
	if isBillingTerminalSettlementAlias(currentComponent) && isBillingTerminalSettlementAlias(otherComponent) {
		return true
	}

	// A terminal success adjustment and a terminal failure refund are two
	// alternative outcomes for the same task.  Class-level checks cannot tell a
	// submit settlement from a terminal settlement, so fence this pair here.
	if (isBillingTerminalSettlementAlias(currentComponent) && isBillingTerminalRefundAlias(otherComponent)) ||
		(isBillingTerminalRefundAlias(currentComponent) && isBillingTerminalSettlementAlias(otherComponent)) {
		return true
	}
	if isBillingTerminalRefundAlias(currentComponent) && isBillingTerminalRefundAlias(otherComponent) {
		return true
	}

	// Submit usage aliases are alternatives, while terminal usage is a distinct
	// delta that may coexist with the submit usage marker.  A terminal usage
	// marker and a terminal refund cannot both be applied: the refund path owns
	// the failed-task usage reversal and a later success marker would inflate
	// aggregates after the refund.
	if isBillingSubmitUsageAlias(currentComponent) && isBillingSubmitUsageAlias(otherComponent) {
		return true
	}
	if (isBillingTerminalUsageAlias(currentComponent) && isBillingTerminalRefundAlias(otherComponent)) ||
		(isBillingTerminalRefundAlias(currentComponent) && isBillingTerminalUsageAlias(otherComponent)) {
		return true
	}

	// A whole ordinary refund is mutually exclusive with every other whole
	// ordinary refund and with either standalone ledger component.  The one
	// supported split legacy shape is wallet_refund + refund_token/refund_extra;
	// wallet_refund only credits the wallet, so those components do not overlap.
	if isBillingWholeRefundAlias(currentComponent) && isBillingWholeRefundAlias(otherComponent) {
		return true
	}
	if isBillingWholeRefundAlias(currentComponent) && (isBillingRefundTokenAlias(otherComponent) || isBillingRefundExtraAlias(otherComponent)) {
		return currentComponent != BillingOperationWalletRefundComponent
	}
	if isBillingWholeRefundAlias(otherComponent) && (isBillingRefundTokenAlias(currentComponent) || isBillingRefundExtraAlias(currentComponent)) {
		return otherComponent != BillingOperationWalletRefundComponent
	}

	// Base pre-consume aliases all represent the initial reservation.  Dynamic
	// reserve:/realtime_preconsume:* components are intentionally excluded by
	// their predicates below because they are monotonic top-ups and may repeat
	// at different targets.
	if isBillingBasePreConsumeAlias(currentComponent) && isBillingBasePreConsumeAlias(otherComponent) {
		return true
	}
	return false
}

func isBillingSubmitSettlementAlias(component string) bool {
	switch strings.TrimSpace(component) {
	case BillingOperationSettleComponent,
		BillingOperationLegacySettleComponent,
		BillingOperationWalletSettleComponent,
		BillingOperationSubscriptionSettleComponent:
		return true
	default:
		return false
	}
}

func isBillingTerminalSettlementAlias(component string) bool {
	switch strings.TrimSpace(component) {
	case BillingOperationTerminalSettleComponent,
		BillingOperationLegacyTaskRecalculateComponent:
		return true
	default:
		return false
	}
}

func isBillingSubmitUsageAlias(component string) bool {
	switch strings.TrimSpace(component) {
	case BillingOperationSettleUsageComponent,
		BillingOperationSyncSettleUsageComponent,
		BillingOperationLegacySettleUsageComponent:
		return true
	default:
		return false
	}
}

func isBillingTerminalUsageAlias(component string) bool {
	return strings.TrimSpace(component) == BillingOperationTerminalSettleUsageComponent
}

func isBillingWholeRefundAlias(component string) bool {
	switch strings.TrimSpace(component) {
	case BillingOperationRefundComponent,
		BillingOperationWalletRefundComponent,
		BillingOperationSubscriptionRefundComponent:
		return true
	default:
		return false
	}
}

func isBillingRefundTokenAlias(component string) bool {
	return strings.TrimSpace(component) == BillingOperationTokenRefundComponent
}

func isBillingRefundExtraAlias(component string) bool {
	return strings.TrimSpace(component) == BillingOperationExtraRefundComponent
}

func isBillingTerminalRefundAlias(component string) bool {
	switch strings.TrimSpace(component) {
	case BillingOperationTaskRefundComponent,
		BillingOperationTerminalRefundComponent,
		BillingOperationLegacyTaskRefundComponent:
		return true
	default:
		return false
	}
}

func isBillingBasePreConsumeAlias(component string) bool {
	switch strings.TrimSpace(component) {
	case BillingOperationWalletPreConsumeComponent,
		BillingOperationSubscriptionPreConsumeTokenComponent,
		BillingOperationPreConsumeComponent,
		BillingOperationPreConsumeTokenComponent:
		return true
	default:
		return false
	}
}

// validateBillingOperationBatch checks marker pairs before any INSERT. This
// keeps EnsureBillingOperations(settle, usage) atomic in either argument order
// while rejecting incompatible pairs before a transaction can expose a
// half-created marker to a concurrent worker.
func validateBillingOperationBatch(specs []BillingOperationSpec) error {
	owners := make(map[string]int, len(specs))
	for _, spec := range specs {
		class := classifyBillingOperationComponent(spec.Component)
		if class == billingOperationClassProviderReversal || spec.UserID <= 0 {
			continue
		}
		requestID := strings.TrimSpace(spec.RequestID)
		if owner, exists := owners[requestID]; exists && owner != spec.UserID {
			return fmt.Errorf("%w: request %q has multiple user owners", ErrBillingOperationConflict, requestID)
		}
		owners[requestID] = spec.UserID
	}
	for i := 0; i < len(specs); i++ {
		current := classifyBillingOperationComponent(specs[i].Component)
		if current == billingOperationClassUnknown || current == billingOperationClassProviderReversal || current == billingOperationClassPostAdjustment {
			continue
		}
		if specs[i].UserID <= 0 {
			return ErrBillingOperationInvalid
		}
		for j := i + 1; j < len(specs); j++ {
			// A caller may install markers for several independent requests in
			// one transaction. Lifecycle/alias compatibility is a request-scoped
			// rule; comparing rows from different requests would reject valid
			// batches (and could make a bulk reconciliation job non-deterministic).
			if specs[i].RequestID != specs[j].RequestID {
				continue
			}
			other := classifyBillingOperationComponent(specs[j].Component)
			if billingOperationAliasConflict(
				specs[i].Component,
				specs[j].Component,
				BillingOperationPending,
				BillingOperationPending,
			) {
				return fmt.Errorf("%w: duplicate billing component aliases %q and %q", ErrBillingOperationConflict, specs[i].Component, specs[j].Component)
			}
			if billingLifecycleConflict(current, other, BillingOperationPending) ||
				billingLifecycleConflict(other, current, BillingOperationPending) {
				return fmt.Errorf("%w: incompatible billing components %q and %q", ErrBillingOperationConflict, specs[i].Component, specs[j].Component)
			}
		}
	}
	return nil
}

// ensureBillingLifecycleOrderTx enforces the request-level lifecycle fence
// while the owning user row is locked. batchOperationKeys contains markers
// created by the same EnsureBillingOperations transaction; those pairs were
// checked by validateBillingOperationBatch and are intentionally skipped here.
// The full request/component key is required because component names are
// reused across independent requests in one batch.
// An already-applied operation is an idempotent replay and is allowed to return
// even if a later terminal refund marker now exists.
func ensureBillingLifecycleOrderTx(tx *gorm.DB, spec BillingOperationSpec, currentClass billingOperationComponentClass, currentStatus BillingOperationStatus, batchOperationKeys map[string]struct{}) error {
	if tx == nil || strings.TrimSpace(spec.RequestID) == "" {
		return ErrBillingOperationInvalid
	}
	if currentClass == billingOperationClassUnknown ||
		currentClass == billingOperationClassProviderReversal ||
		currentClass == billingOperationClassPostAdjustment ||
		currentStatus == BillingOperationApplied {
		return nil
	}
	// The owning user row is locked before this helper is called. That gives all
	// lifecycle operations for one request a single serialization point; the
	// peer rows can consequently be locked in a deterministic order without the
	// operation -> user -> peer cycle that existed when callers locked their own
	// operation first.
	var rows []BillingOperation
	if err := lockForUpdate(tx).Where("request_id = ?", spec.RequestID).Order("id asc").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if row.Component == spec.Component {
			continue
		}
		if _, inBatch := batchOperationKeys[BillingOperationKey(row.RequestId, row.Component)]; inBatch {
			continue
		}
		if billingOperationStatusActive(row.Status) &&
			(row.UserId != spec.UserID || row.UserId <= 0 || spec.UserID <= 0) {
			return fmt.Errorf("%w: component %q has a different user owner", ErrBillingOperationConflict, row.Component)
		}
		if billingOperationAliasConflict(spec.Component, row.Component, currentStatus, row.Status) {
			return fmt.Errorf("%w: component %q duplicates alias %q", ErrBillingOperationConflict, spec.Component, row.Component)
		}
		otherClass := classifyBillingOperationComponent(row.Component)
		if billingLifecycleConflict(currentClass, otherClass, row.Status) {
			return fmt.Errorf("%w: component %q cannot follow %q", ErrBillingOperationConflict, spec.Component, row.Component)
		}
	}
	return nil
}

// ensureBillingUsageCompanionTx prevents an informational usage operation from
// being applied before the financial settlement it describes. Marker creation
// remains order-independent (EnsureBillingOperations may install the pair in
// one transaction), but the side-effecting apply must observe exactly one
// applied financial companion. The owning user fence held by the caller makes
// this check stable while concurrent settlement/replay workers run.
func ensureBillingUsageCompanionTx(tx *gorm.DB, spec BillingOperationSpec) error {
	if tx == nil {
		return ErrBillingOperationInvalid
	}
	var companions []string
	switch strings.TrimSpace(spec.Component) {
	case BillingOperationSettleUsageComponent,
		BillingOperationSyncSettleUsageComponent:
		companions = []string{
			BillingOperationSettleComponent,
			BillingOperationLegacySettleComponent,
			BillingOperationWalletSettleComponent,
			BillingOperationSubscriptionSettleComponent,
		}
	case BillingOperationTerminalSettleUsageComponent:
		companions = []string{
			BillingOperationTerminalSettleComponent,
			BillingOperationLegacyTaskRecalculateComponent,
		}
	default:
		// legacy_settle_usage is intentionally a marker for pre-journal task
		// rows and may have no financial companion. Preserve that compatibility
		// path while fencing all modern usage components above.
		return nil
	}

	var rows []BillingOperation
	if err := tx.Where("request_id = ? AND component IN ?", spec.RequestID, companions).Find(&rows).Error; err != nil {
		return err
	}
	applied := 0
	for _, row := range rows {
		if row.UserId != spec.UserID {
			return fmt.Errorf("%w: usage companion user mismatch", ErrBillingOperationConflict)
		}
		if row.Status == BillingOperationApplied {
			applied++
		}
	}
	if applied != 1 {
		return fmt.Errorf("%w: usage operation %q requires one applied financial companion", ErrBillingOperationConflict, spec.Component)
	}
	return nil
}

// ensureBillingRefundSettlementOrderTx is retained as a small compatibility
// wrapper for callers that only know whether an operation is a refund. New
// paths should call ensureBillingLifecycleOrderTx so terminal-refund semantics
// remain explicit.
func ensureBillingRefundSettlementOrderTx(tx *gorm.DB, spec BillingOperationSpec, refund bool) error {
	class := classifyBillingOperationComponent(spec.Component)
	if refund && class == billingOperationClassUnknown {
		class = billingOperationClassRefund
	}
	return ensureBillingLifecycleOrderTx(tx, spec, class, BillingOperationPending, nil)
}

// ApplyBillingOperationAndTaskQuota atomically applies a normalized billing
// operation and advances one persisted task's quota from expectedQuota to
// targetQuota. It is the durable seam for legacy terminal adjustments that do
// not yet carry BillingRequestId: stale pollers are fenced by both the
// operation key and the task-row quota, while a crash cannot commit one
// without the other.
//
// If the operation was already applied, the function repairs a task marker
// that is still at expectedQuota, but never overwrites a different quota. The
// returned bool is true only when this call applied ledger deltas; callers can
// therefore avoid duplicate informational logs on replay.
func ApplyBillingOperationAndTaskQuota(spec BillingOperationSpec, taskID int64, expectedStatus TaskStatus, expectedQuota, targetQuota int) (bool, error) {
	if taskID <= 0 {
		return false, ErrBillingTaskQuotaConflict
	}
	if expectedQuota < 0 || targetQuota < 0 || expectedQuota > common.MaxQuota || targetQuota > common.MaxQuota {
		return false, ErrBillingOperationInvalid
	}
	normalized, err := normalizeBillingOperationSpec(spec)
	if err != nil {
		return false, err
	}
	if DB == nil {
		return false, errors.New("billing operation database is not initialized")
	}

	var appliedNow bool
	err = DB.Transaction(func(tx *gorm.DB) error {
		// Lock the user before the operation and task. This is the same global
		// order as every other durable lifecycle path and prevents a multi-row
		// task adjustment from forming a task/user cycle with a concurrent
		// settlement.
		if err := lockBillingUserTx(tx, normalized); err != nil {
			return err
		}
		operation, err := lockBillingOperationTx(tx, normalized)
		if err != nil {
			return err
		}

		var task Task
		if err := lockForUpdate(tx).Where("id = ?", taskID).First(&task).Error; err != nil {
			return err
		}
		if expectedStatus != "" && task.Status != expectedStatus {
			return ErrBillingTaskQuotaConflict
		}

		switch operation.Status {
		case BillingOperationApplied:
			if task.Quota == targetQuota {
				return nil
			}
			if task.Quota != expectedQuota {
				return ErrBillingTaskQuotaConflict
			}
			// The operation committed before an earlier task-marker write could
			// complete. Repair only the exact expected baseline.
			result := tx.Model(&Task{}).Where("id = ? AND quota = ?", taskID, expectedQuota).Update("quota", targetQuota)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 && targetQuota != expectedQuota {
				return ErrBillingTaskQuotaConflict
			}
			return nil
		case BillingOperationPending:
			if task.Quota != expectedQuota {
				return ErrBillingTaskQuotaConflict
			}
			if _, err := applyBillingOperationTx(tx, normalized); err != nil {
				return err
			}
			result := tx.Model(&Task{}).Where("id = ? AND quota = ?", taskID, expectedQuota).Update("quota", targetQuota)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 && targetQuota != expectedQuota {
				return ErrBillingTaskQuotaConflict
			}
			appliedNow = true
			return nil
		default:
			return fmt.Errorf("%w: unknown status %q", ErrBillingOperationConflict, operation.Status)
		}
	})
	if err != nil {
		return false, err
	}
	syncBillingOperationCaches(normalized, appliedNow)
	return appliedNow, nil
}

func applyBillingWalletDeltaTx(tx *gorm.DB, spec BillingOperationSpec) error {
	query := tx.Model(&User{}).Where("id = ?", spec.UserID)
	if spec.RequireWalletBalance {
		query = query.Where("quota >= ?", -spec.WalletDelta)
	}
	if spec.WalletDelta > 0 {
		query = query.Where("quota <= ?", int64(common.MaxWalletQuota)-spec.WalletDelta)
	} else if spec.WalletDelta < 0 {
		query = query.Where("quota >= ?", -int64(common.MaxWalletQuota)-spec.WalletDelta)
	}
	result := query.Update("quota", gorm.Expr("quota + ?", spec.WalletDelta))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	if spec.RequireWalletBalance {
		var count int64
		if err := tx.Model(&User{}).Where("id = ?", spec.UserID).Count(&count).Error; err != nil {
			return err
		}
		if count == 1 {
			return fmt.Errorf("%w: %w", ErrBillingWalletInsufficient, ErrBillingOperationInsufficient)
		}
	}
	var count int64
	if err := tx.Model(&User{}).Where("id = ?", spec.UserID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		// WalletDelta is the authoritative funding ledger, unlike the usage
		// aggregates handled below. A soft-deleted/missing user therefore must
		// not make a refund or charge appear applied: returning nil here would
		// mark the operation applied while silently dropping the money movement.
		// Keep the operation pending so a reconciler can restore the row or route
		// the balance to a separately audited manual repair.
		return gorm.ErrRecordNotFound
	}
	if spec.WalletDelta > 0 {
		return fmt.Errorf("wallet quota limit exceeded")
	}
	return fmt.Errorf("%w: %w", ErrBillingWalletInsufficient, ErrBillingOperationInsufficient)
}

func applyBillingSubscriptionDeltaTx(tx *gorm.DB, spec BillingOperationSpec) error {
	if spec.SubscriptionID <= 0 {
		return ErrBillingOperationInvalid
	}
	var sub UserSubscription
	if err := lockForUpdate(tx).
		Where("id = ?", spec.SubscriptionID).
		First(&sub).Error; err != nil {
		return err
	}
	if spec.UserID > 0 && sub.UserId != spec.UserID {
		return ErrBillingOperationConflict
	}
	return applySubscriptionDeltaValues(&sub, spec.SubscriptionDelta, tx)
}

// applySubscriptionDeltaValues keeps subscription bounds identical to the
// existing post-consume helper while allowing the caller's transaction to
// include wallet/token rows and the operation marker.
func applySubscriptionDeltaValues(sub *UserSubscription, delta int64, tx *gorm.DB) error {
	if sub == nil || tx == nil {
		return ErrBillingOperationInvalid
	}
	newUsed := sub.AmountUsed + delta
	if (delta > 0 && newUsed < sub.AmountUsed) || (delta < 0 && newUsed > sub.AmountUsed) {
		return ErrBillingOperationInvalid
	}
	if newUsed < 0 {
		return fmt.Errorf("subscription usage underflow, used=%d delta=%d", sub.AmountUsed, delta)
	}
	if sub.AmountTotal > 0 && newUsed > sub.AmountTotal {
		return fmt.Errorf("subscription used exceeds total, used=%d total=%d", newUsed, sub.AmountTotal)
	}
	sub.AmountUsed = newUsed
	return tx.Save(sub).Error
}

func applyBillingTokenDeltaTx(tx *gorm.DB, spec BillingOperationSpec) error {
	query := tx.Model(&Token{}).Where("id = ?", spec.TokenID)
	if spec.TokenDelta < 0 {
		// A token can be soft-deleted after an asynchronous request was charged.
		// Refund its historical accounting row instead of blocking the user's
		// wallet/subscription refund on an object that can no longer authorize new
		// requests.
		query = tx.Unscoped().Model(&Token{}).Where("id = ?", spec.TokenID)
	}
	if spec.UserID > 0 {
		query = query.Where("user_id = ?", spec.UserID)
	}
	if spec.RequireTokenBalance && !spec.TokenUnlimited {
		query = query.Where("remain_quota >= ?", spec.TokenDelta)
	}
	if spec.TokenDelta < 0 {
		// Do not let an out-of-order refund make used_quota negative.  A
		// failed condition is retryable/manual rather than silently erasing
		// unrelated usage.
		query = query.Where("used_quota >= ?", -spec.TokenDelta)
	}
	result := query.Updates(map[string]interface{}{
		"remain_quota":  gorm.Expr("remain_quota - ?", spec.TokenDelta),
		"used_quota":    gorm.Expr("used_quota + ?", spec.TokenDelta),
		"accessed_time": common.GetTimestamp(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	if spec.RequireTokenBalance || spec.TokenDelta < 0 {
		var count int64
		if err := tx.Unscoped().Model(&Token{}).Where("id = ?", spec.TokenID).Count(&count).Error; err != nil {
			return err
		}
		if count == 1 {
			return fmt.Errorf("%w: %w", ErrBillingTokenInsufficient, ErrBillingOperationInsufficient)
		}
		if spec.TokenDelta < 0 {
			// A hard-deleted token has no spendable ledger left to restore. The
			// operation marker retains the intended delta for audit, while the other
			// financial ledgers can still be refunded atomically.
			return nil
		}
	}
	return gorm.ErrRecordNotFound
}

// applyBillingUserUsageDeltaTx applies informational user aggregates inside
// the operation transaction. A legacy task may have a financial charge but no
// historical usage row, so a refund must not fail and strand spendable credit.
// A negative delta is different: silently clamping it to zero would hide a
// missing/duplicated usage record while still marking the financial operation
// applied. Use a conditional UPDATE and return a typed underflow error when
// the stored counter is smaller than the requested reversal; the surrounding
// transaction then rolls back every ledger mutation and leaves the marker
// pending for reconciliation.
func applyBillingUserUsageDeltaTx(tx *gorm.DB, spec BillingOperationSpec) error {
	if tx == nil || spec.UserID <= 0 {
		return ErrBillingOperationInvalid
	}
	// Rows created during the legacy-task refund rollout may have a financial
	// charge but no historical usage aggregate at all.  In that one compatibility
	// case a zero counter means "there is nothing to reverse", not an accounting
	// instruction to block the wallet refund forever.  Read under the user lock
	// already held by applyBillingOperationTx and remove only the missing field;
	// a positive-but-insufficient counter remains a hard underflow below.
	if spec.Component == BillingOperationLegacyTaskRefundComponent &&
		(spec.UserUsedQuotaDelta < 0 || spec.UserRequestCountDelta < 0) {
		var current struct {
			UsedQuota    int64 `gorm:"column:used_quota"`
			RequestCount int64 `gorm:"column:request_count"`
		}
		if err := lockForUpdate(tx).Model(&User{}).
			Select("used_quota", "request_count").Where("id = ?", spec.UserID).First(&current).Error; err != nil {
			return err
		}
		if spec.UserUsedQuotaDelta < 0 && current.UsedQuota == 0 {
			spec.UserUsedQuotaDelta = 0
		}
		if spec.UserRequestCountDelta < 0 && current.RequestCount == 0 {
			spec.UserRequestCountDelta = 0
		}
	}
	updates := map[string]interface{}{}
	query := tx.Model(&User{}).Where("id = ?", spec.UserID)
	if spec.UserUsedQuotaDelta != 0 {
		updates["used_quota"] = gorm.Expr("used_quota + ?", spec.UserUsedQuotaDelta)
		if spec.UserUsedQuotaDelta < 0 {
			query = query.Where("used_quota >= ?", -spec.UserUsedQuotaDelta)
		} else {
			// User.used_quota is a signed bigint in all supported schemas. Keep
			// the arithmetic inside that domain instead of allowing a corrupt
			// near-MaxInt value to wrap in the database expression.
			query = query.Where("used_quota <= ?", math.MaxInt64-spec.UserUsedQuotaDelta)
		}
	}
	if spec.UserRequestCountDelta != 0 {
		updates["request_count"] = gorm.Expr("request_count + ?", spec.UserRequestCountDelta)
		if spec.UserRequestCountDelta < 0 {
			query = query.Where("request_count >= ?", -spec.UserRequestCountDelta)
		} else {
			// request_count is an int column (int32 on MySQL/Postgres). Use
			// MaxInt32 as the portable upper boundary.
			query = query.Where("request_count <= ?", int64(math.MaxInt32)-spec.UserRequestCountDelta)
		}
	}
	if len(updates) == 0 {
		return nil
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	// The conditional update matched no row. Re-read under the already-held
	// user lock to distinguish a deleted user from a counter-integrity
	// violation. Never mark the operation applied in either case.
	var current struct {
		Id           int `gorm:"column:id"`
		UsedQuota    int `gorm:"column:used_quota"`
		RequestCount int `gorm:"column:request_count"`
	}
	if err := lockForUpdate(tx).Model(&User{}).Select("id", "used_quota", "request_count").
		Where("id = ?", spec.UserID).First(&current).Error; err != nil {
		return err
	}
	if spec.UserUsedQuotaDelta < 0 && int64(current.UsedQuota) < -spec.UserUsedQuotaDelta {
		return fmt.Errorf("%w: user %d used_quota=%d delta=%d", ErrBillingUsageUnderflow, spec.UserID, current.UsedQuota, spec.UserUsedQuotaDelta)
	}
	if spec.UserRequestCountDelta < 0 && int64(current.RequestCount) < -spec.UserRequestCountDelta {
		return fmt.Errorf("%w: user %d request_count=%d delta=%d", ErrBillingUsageUnderflow, spec.UserID, current.RequestCount, spec.UserRequestCountDelta)
	}
	if spec.UserUsedQuotaDelta > 0 && int64(current.UsedQuota) > math.MaxInt64-spec.UserUsedQuotaDelta {
		return fmt.Errorf("%w: user %d used_quota=%d delta=%d", ErrBillingUsageOverflow, spec.UserID, current.UsedQuota, spec.UserUsedQuotaDelta)
	}
	if spec.UserRequestCountDelta > 0 && int64(current.RequestCount) > int64(math.MaxInt32)-spec.UserRequestCountDelta {
		return fmt.Errorf("%w: user %d request_count=%d delta=%d", ErrBillingUsageOverflow, spec.UserID, current.RequestCount, spec.UserRequestCountDelta)
	}
	// A non-zero delta should always change the row when all conditions above
	// hold. Treat an unexpected zero-row result as a retryable database
	// conflict instead of silently acknowledging a missing counter mutation.
	return fmt.Errorf("billing usage update affected no rows for user %d", spec.UserID)
}

func applyBillingChannelUsageDeltaTx(tx *gorm.DB, spec BillingOperationSpec) error {
	if tx == nil || spec.ChannelID <= 0 || spec.ChannelUsedQuotaDelta == 0 {
		return ErrBillingOperationInvalid
	}
	// A post-rollout legacy task can have a financial/token charge without a
	// channel usage row ever being recorded.  Treat an actually zero channel
	// counter as an absent historical aggregate for that compatibility component
	// and let the financial refund proceed.  Do not mask a partial/corrupt
	// counter: any positive value smaller than the requested reversal remains a
	// typed underflow after the conditional UPDATE below.
	if spec.Component == BillingOperationLegacyTaskRefundComponent && spec.ChannelUsedQuotaDelta < 0 {
		var current struct {
			UsedQuota int64 `gorm:"column:used_quota"`
		}
		if err := lockForUpdate(tx).Model(&Channel{}).
			Select("used_quota").Where("id = ?", spec.ChannelID).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Deleted informational channel rows have historically been a
				// no-op for refunds; retain that behavior.
				return nil
			}
			return err
		}
		if current.UsedQuota == 0 {
			return nil
		}
	}
	query := tx.Model(&Channel{}).Where("id = ?", spec.ChannelID)
	if spec.ChannelUsedQuotaDelta < 0 {
		query = query.Where("used_quota >= ?", -spec.ChannelUsedQuotaDelta)
	} else {
		query = query.Where("used_quota <= ?", math.MaxInt64-spec.ChannelUsedQuotaDelta)
	}
	result := query.Update("used_quota", gorm.Expr("used_quota + ?", spec.ChannelUsedQuotaDelta))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	// Channel rows are informational and may legitimately have been deleted;
	// preserve the historical no-op for that case. If the row exists but its
	// counter cannot absorb the requested delta, surface a typed integrity
	// error so the operation transaction rolls back rather than erasing the
	// discrepancy.
	var current struct {
		Id        int   `gorm:"column:id"`
		UsedQuota int64 `gorm:"column:used_quota"`
	}
	if err := lockForUpdate(tx).Model(&Channel{}).Select("id", "used_quota").
		Where("id = ?", spec.ChannelID).First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if spec.ChannelUsedQuotaDelta < 0 && current.UsedQuota < -spec.ChannelUsedQuotaDelta {
		return fmt.Errorf("%w: channel %d used_quota=%d delta=%d", ErrBillingUsageUnderflow, spec.ChannelID, current.UsedQuota, spec.ChannelUsedQuotaDelta)
	}
	if spec.ChannelUsedQuotaDelta > 0 && current.UsedQuota > math.MaxInt64-spec.ChannelUsedQuotaDelta {
		return fmt.Errorf("%w: channel %d used_quota=%d delta=%d", ErrBillingUsageOverflow, spec.ChannelID, current.UsedQuota, spec.ChannelUsedQuotaDelta)
	}
	return fmt.Errorf("billing usage update affected no rows for channel %d", spec.ChannelID)
}

// syncBillingOperationCaches updates only entries that are already hydrated.
// If an entry is absent or a Redis write fails, invalidating it is safer than
// serving a stale balance; the database transaction remains authoritative.
func syncBillingOperationCaches(spec BillingOperationSpec, appliedNow bool) {
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	mutationID := BillingOperationKey(spec.RequestID, spec.Component)[:32]
	if spec.WalletDelta != 0 && spec.UserID > 0 {
		cacheSafe := false
		if appliedNow {
			result, err := cacheApplyUserQuotaDelta(spec.UserID, spec.WalletDelta)
			if err == nil && result == cacheQuotaOK {
				cacheSafe = true
			} else if err == nil {
				err = ErrQuotaCacheMiss
			}
			if !cacheSafe {
				common.SysLog("failed to synchronize billing wallet cache: " + err.Error())
			}
		}
		if !cacheSafe {
			if err := repairUserQuotaCache(spec.UserID); err != nil {
				recordQuotaCacheRepair(QuotaCacheRepairEntityUser, spec.UserID, getUserCacheKey(spec.UserID), err)
			} else {
				cacheSafe = true
			}
		}
		if cacheSafe {
			if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityUser, spec.UserID, getUserCacheKey(spec.UserID), mutationID); err != nil {
				common.SysLog("failed to complete billing wallet cache repair: " + err.Error())
			}
		}
	}
	if spec.TokenDelta != 0 && spec.TokenID > 0 && strings.TrimSpace(spec.TokenKey) != "" {
		cacheSafe := false
		if appliedNow {
			result, err := cacheApplyTokenQuotaDelta(spec.TokenID, spec.TokenKey, -spec.TokenDelta)
			if err == nil && result == cacheQuotaOK {
				cacheSafe = true
			} else if err == nil {
				err = ErrQuotaCacheMiss
			}
			if !cacheSafe {
				common.SysLog("failed to synchronize billing token cache: " + err.Error())
			}
		}
		if !cacheSafe {
			if err := invalidateTokenCacheForMutation(spec.TokenKey); err != nil {
				recordQuotaCacheRepair(QuotaCacheRepairEntityToken, spec.TokenID, getTokenCacheKey(spec.TokenKey), err)
			} else {
				cacheSafe = true
			}
		}
		if cacheSafe {
			if err := completeStagedQuotaCacheRepair(QuotaCacheRepairEntityToken, spec.TokenID, getTokenCacheKey(spec.TokenKey), mutationID); err != nil {
				common.SysLog("failed to complete billing token cache repair: " + err.Error())
			}
		}
	}
}
