package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MidjourneySubmitIntentStatus is the request-level state recorded before an
// upstream submission. Pending means that no authoritative provider result
// has been observed yet. AcceptedPending means an authoritative provider task
// identity has been durably recorded, but the settlement markers still need to
// be installed. Accepted means the provider identity and marker pair are
// installed; rejected is a confirmed non-billable response. A plain pending
// intent is deliberately never auto-charged: a process may have died before
// reading a response, and guessing accepted would turn an ordinary network
// timeout into an account debit.
type MidjourneySubmitIntentStatus string

const (
	MidjourneySubmitIntentPending         MidjourneySubmitIntentStatus = "pending"
	MidjourneySubmitIntentAcceptedPending MidjourneySubmitIntentStatus = "accepted_pending"
	MidjourneySubmitIntentAccepted        MidjourneySubmitIntentStatus = "accepted"
	MidjourneySubmitIntentRejected        MidjourneySubmitIntentStatus = "rejected"
)

var (
	ErrMidjourneySubmitIntentInvalid  = errors.New("invalid Midjourney submit intent")
	ErrMidjourneySubmitIntentConflict = errors.New("Midjourney submit intent conflicts with existing request")
	ErrMidjourneySubmitIntentInFlight = errors.New("Midjourney submit request is already in flight")
)

// MidjourneySubmitIntent is a lightweight outbox row. It contains no ledger
// delta and therefore is safe to create before contacting the provider. The
// immutable billing snapshot lets a later accepted-intent reconciler recreate
// the exact settlement markers without a live HTTP request or RelayInfo.
type MidjourneySubmitIntent struct {
	Id int64 `json:"id" gorm:"primaryKey"`

	RequestId string                       `json:"request_id" gorm:"type:varchar(128);not null;uniqueIndex;index"`
	Status    MidjourneySubmitIntentStatus `json:"status" gorm:"type:varchar(16);not null;index"`

	UserId           int    `json:"user_id" gorm:"index"`
	TokenId          int    `json:"token_id" gorm:"index"`
	ChannelId        int    `json:"channel_id" gorm:"index"`
	BillingChannelId int    `json:"billing_channel_id" gorm:"index"`
	Quota            int    `json:"quota"`
	TokenUnlimited   bool   `json:"token_unlimited"`
	Playground       bool   `json:"playground"`
	Action           string `json:"action" gorm:"type:varchar(40)"`

	ProviderTaskId string `json:"provider_task_id" gorm:"type:varchar(255);index"`
	LastError      string `json:"last_error" gorm:"type:text"`

	CreatedAt    int64 `json:"created_at" gorm:"bigint;index"`
	UpdatedAt    int64 `json:"updated_at" gorm:"bigint;index"`
	AcceptedAt   int64 `json:"accepted_at" gorm:"bigint;index"`
	ReconciledAt int64 `json:"reconciled_at" gorm:"bigint;index"`
}

func (MidjourneySubmitIntent) TableName() string {
	return "midjourney_submit_intents"
}

func normalizeMidjourneySubmitIntent(intent *MidjourneySubmitIntent) error {
	if intent == nil {
		return ErrMidjourneySubmitIntentInvalid
	}
	intent.RequestId = strings.TrimSpace(intent.RequestId)
	intent.Action = strings.TrimSpace(intent.Action)
	intent.ProviderTaskId = strings.TrimSpace(intent.ProviderTaskId)
	if intent.RequestId == "" || len(intent.RequestId) > 128 || intent.UserId <= 0 ||
		intent.TokenId < 0 || intent.ChannelId < 0 || intent.BillingChannelId < 0 ||
		intent.Quota < 0 || intent.Quota > common.MaxQuota || intent.Action == "" || len(intent.Action) > 40 {
		return ErrMidjourneySubmitIntentInvalid
	}
	if intent.Status == "" {
		intent.Status = MidjourneySubmitIntentPending
	}
	if intent.Status != MidjourneySubmitIntentPending &&
		intent.Status != MidjourneySubmitIntentAcceptedPending &&
		intent.Status != MidjourneySubmitIntentAccepted &&
		intent.Status != MidjourneySubmitIntentRejected {
		return ErrMidjourneySubmitIntentInvalid
	}
	if len(intent.ProviderTaskId) > 255 || len(intent.LastError) > 4000 {
		return ErrMidjourneySubmitIntentInvalid
	}
	return nil
}

func midjourneySubmitIntentImmutableEqual(a, b *MidjourneySubmitIntent) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.TrimSpace(a.RequestId) == strings.TrimSpace(b.RequestId) &&
		a.UserId == b.UserId &&
		a.TokenId == b.TokenId &&
		a.ChannelId == b.ChannelId &&
		a.BillingChannelId == b.BillingChannelId &&
		a.Quota == b.Quota &&
		a.TokenUnlimited == b.TokenUnlimited &&
		a.Playground == b.Playground &&
		strings.TrimSpace(a.Action) == strings.TrimSpace(b.Action)
}

// CreateMidjourneySubmitIntent durably records the pre-provider boundary.
// The bool is true only when this call inserted the row. An exact existing
// pending row is returned with (false, nil), allowing the HTTP layer to fail
// closed without issuing a second provider submission; accepted/rejected rows
// are explicit idempotency conflicts.
func CreateMidjourneySubmitIntent(intent *MidjourneySubmitIntent) (bool, error) {
	if err := normalizeMidjourneySubmitIntent(intent); err != nil {
		return false, err
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	candidate := *intent
	candidate.Id = 0
	candidate.Status = MidjourneySubmitIntentPending
	candidate.ProviderTaskId = ""
	candidate.LastError = ""
	now := common.GetTimestamp()
	if candidate.CreatedAt == 0 {
		candidate.CreatedAt = now
	}
	candidate.UpdatedAt = now
	created := true
	err := DB.Transaction(func(tx *gorm.DB) error {
		createResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if createResult.Error != nil {
			return createResult.Error
		}
		created = createResult.RowsAffected == 1
		var existing MidjourneySubmitIntent
		if err := lockForUpdate(tx).Where("request_id = ?", candidate.RequestId).First(&existing).Error; err != nil {
			return err
		}
		if !midjourneySubmitIntentImmutableEqual(&existing, &candidate) {
			return ErrMidjourneySubmitIntentConflict
		}
		*intent = existing
		if existing.Status != MidjourneySubmitIntentPending {
			return fmt.Errorf("%w: status=%s", ErrMidjourneySubmitIntentConflict, existing.Status)
		}
		return nil
	})
	return created, err
}

// EnsureMidjourneySubmitIntent is the convenience form for callers that do
// not need to distinguish a newly-created row from an exact pending retry.
func EnsureMidjourneySubmitIntent(intent *MidjourneySubmitIntent) error {
	_, err := CreateMidjourneySubmitIntent(intent)
	return err
}

// validateMidjourneySubmitIntentBillingSpecs checks that the accepted result
// carries exactly the immutable snapshot recorded before the provider call.
// It intentionally accepts only the legacy_settle + settle_usage pair used by
// Midjourney; another component would make the outbox ambiguous to replay.
func validateMidjourneySubmitIntentBillingSpecs(intent *MidjourneySubmitIntent, financial, usage BillingOperationSpec) (BillingOperationSpec, BillingOperationSpec, error) {
	if intent == nil {
		return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentInvalid
	}
	financial, err := normalizeBillingOperationSpec(financial)
	if err != nil {
		return BillingOperationSpec{}, BillingOperationSpec{}, err
	}
	usage, err = normalizeBillingOperationSpec(usage)
	if err != nil {
		return BillingOperationSpec{}, BillingOperationSpec{}, err
	}
	if financial.RequestID != intent.RequestId || usage.RequestID != intent.RequestId ||
		financial.Component != BillingOperationLegacySettleComponent || usage.Component != BillingOperationSettleUsageComponent {
		return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
	}
	quota := int64(intent.Quota)
	if financial.UserID != intent.UserId || financial.WalletDelta != -quota || financial.SubscriptionDelta != 0 ||
		financial.UserUsedQuotaDelta != 0 || financial.UserRequestCountDelta != 0 || financial.ChannelID != 0 || financial.ChannelUsedQuotaDelta != 0 {
		return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
	}
	if financial.TokenUnlimited != intent.TokenUnlimited || financial.RequireWalletBalance != (quota > 0) {
		return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
	}
	if intent.Playground {
		if financial.TokenID != 0 || financial.TokenDelta != 0 || financial.RequireTokenBalance {
			return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
		}
	} else if financial.TokenID != intent.TokenId || financial.TokenDelta != quota ||
		financial.RequireTokenBalance != (quota > 0 && !intent.TokenUnlimited) {
		return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
	}
	expectedChannel := intent.BillingChannelId
	if expectedChannel < 0 {
		return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
	}
	if usage.UserID != intent.UserId || usage.WalletDelta != 0 || usage.TokenDelta != 0 || usage.SubscriptionDelta != 0 ||
		usage.UserUsedQuotaDelta != quota || usage.UserRequestCountDelta != 1 || usage.ChannelID != expectedChannel {
		return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
	}
	if expectedChannel == 0 {
		if usage.ChannelUsedQuotaDelta != 0 {
			return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
		}
	} else if usage.ChannelUsedQuotaDelta != quota {
		return BillingOperationSpec{}, BillingOperationSpec{}, ErrMidjourneySubmitIntentConflict
	}
	return financial, usage, nil
}

// ensureBillingOperationsInTx is the transaction-scoped counterpart of
// EnsureBillingOperations. It lives here so accepting an intent can commit
// the status transition and both markers atomically without opening a nested
// transaction on the same connection. The implementation deliberately uses
// the same normalization, alias, and lifecycle fences as the public helper.
func ensureBillingOperationsInTx(tx *gorm.DB, specs ...BillingOperationSpec) error {
	if tx == nil || len(specs) == 0 {
		return ErrBillingOperationInvalid
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
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].UserID != normalized[j].UserID {
			return normalized[i].UserID < normalized[j].UserID
		}
		return BillingOperationKey(normalized[i].RequestID, normalized[i].Component) <
			BillingOperationKey(normalized[j].RequestID, normalized[j].Component)
	})
	batchKeys := make(map[string]struct{}, len(normalized))
	for _, spec := range normalized {
		batchKeys[BillingOperationKey(spec.RequestID, spec.Component)] = struct{}{}
	}
	for _, spec := range normalized {
		kind := classifyBillingOperationComponent(spec.Component)
		if kind != billingOperationClassUnknown && kind != billingOperationClassProviderReversal {
			if err := lockBillingUserByIDTx(tx, spec.UserID); err != nil {
				return err
			}
		}
		candidate := billingOperationFromSpec(spec)
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			return err
		}
		var row BillingOperation
		if err := lockForUpdate(tx).Where("operation_key = ?", candidate.OperationKey).First(&row).Error; err != nil {
			return err
		}
		if !billingOperationMatches(&row, spec) {
			return ErrBillingOperationConflict
		}
		if row.Status != BillingOperationPending && row.Status != BillingOperationApplied {
			return fmt.Errorf("%w: unknown status %q", ErrBillingOperationConflict, row.Status)
		}
		if kind != billingOperationClassUnknown && kind != billingOperationClassProviderReversal {
			if err := ensureBillingLifecycleOrderTx(tx, spec, kind, row.Status, batchKeys); err != nil {
				return err
			}
		}
	}
	return nil
}

// MarkMidjourneySubmitIntentAcceptedPending durably records the provider task
// identity before any billing marker is written. This is the first half of the
// accepted-response saga. If the process/database dies after this commit, a
// fresh reconciler can still identify the exact provider task and finish the
// immutable billing pair; a plain pending intent remains intentionally
// fail-closed because its provider outcome is unknown.
func MarkMidjourneySubmitIntentAcceptedPending(requestID, providerTaskID string) error {
	requestID = strings.TrimSpace(requestID)
	providerTaskID = strings.TrimSpace(providerTaskID)
	if requestID == "" || providerTaskID == "" || len(providerTaskID) > 255 {
		return ErrMidjourneySubmitIntentInvalid
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var intent MidjourneySubmitIntent
		if err := lockForUpdate(tx).Where("request_id = ?", requestID).First(&intent).Error; err != nil {
			return err
		}
		if intent.RequestId != requestID {
			return ErrMidjourneySubmitIntentConflict
		}
		switch intent.Status {
		case MidjourneySubmitIntentRejected:
			return ErrMidjourneySubmitIntentConflict
		case MidjourneySubmitIntentAccepted, MidjourneySubmitIntentAcceptedPending:
			if strings.TrimSpace(intent.ProviderTaskId) != providerTaskID {
				return ErrMidjourneySubmitIntentConflict
			}
			return nil
		case MidjourneySubmitIntentPending:
			// Continue below and commit the provider identity without charging.
		default:
			return ErrMidjourneySubmitIntentConflict
		}
		now := common.GetTimestamp()
		result := tx.Model(&MidjourneySubmitIntent{}).
			Where("id = ? AND status = ?", intent.Id, MidjourneySubmitIntentPending).
			Updates(map[string]interface{}{
				"status":           MidjourneySubmitIntentAcceptedPending,
				"provider_task_id": providerTaskID,
				"accepted_at":      now,
				"updated_at":       now,
				"last_error":       "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrMidjourneySubmitIntentConflict
		}
		return nil
	})
}

// FinalizeMidjourneySubmitIntent installs the immutable settlement/usage
// markers and advances accepted_pending -> accepted in one transaction. It is
// safe to repeat for an already-accepted intent: the marker pair is checked
// against the original snapshot and no ledger is touched here. Keeping this
// second phase separate from MarkMidjourneySubmitIntentAcceptedPending closes
// the crash window where the provider response was known but marker creation
// had not yet committed.
func FinalizeMidjourneySubmitIntent(requestID, providerTaskID string, financial, usage BillingOperationSpec) error {
	requestID = strings.TrimSpace(requestID)
	providerTaskID = strings.TrimSpace(providerTaskID)
	if requestID == "" || providerTaskID == "" || len(providerTaskID) > 255 {
		return ErrMidjourneySubmitIntentInvalid
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var intent MidjourneySubmitIntent
		if err := lockForUpdate(tx).Where("request_id = ?", requestID).First(&intent).Error; err != nil {
			return err
		}
		if intent.RequestId != requestID {
			return ErrMidjourneySubmitIntentConflict
		}
		if intent.Status == MidjourneySubmitIntentRejected || intent.Status == MidjourneySubmitIntentPending {
			return ErrMidjourneySubmitIntentConflict
		}
		if strings.TrimSpace(intent.ProviderTaskId) != providerTaskID {
			return ErrMidjourneySubmitIntentConflict
		}
		financial, usage, err := validateMidjourneySubmitIntentBillingSpecs(&intent, financial, usage)
		if err != nil {
			return err
		}
		if err := ensureBillingOperationsInTx(tx, financial, usage); err != nil {
			return err
		}
		if intent.Status != MidjourneySubmitIntentAcceptedPending {
			// The accepted state is already finalized. The marker pair above is
			// still revalidated, repairing a marker-only crash if necessary.
			return nil
		}
		now := common.GetTimestamp()
		result := tx.Model(&MidjourneySubmitIntent{}).
			Where("id = ? AND status = ?", intent.Id, MidjourneySubmitIntentAcceptedPending).
			Updates(map[string]interface{}{
				"status":     MidjourneySubmitIntentAccepted,
				"updated_at": now,
				"last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrMidjourneySubmitIntentConflict
		}
		return nil
	})
}

// AcceptMidjourneySubmitIntent is the compatibility wrapper used by older
// callers. It now executes the two durable phases explicitly: provider ID
// first, then billing markers. If phase two fails, the intent remains
// accepted_pending and is recoverable by the reconciler instead of silently
// reverting to an outcome-unknown pending row.
func AcceptMidjourneySubmitIntent(requestID, providerTaskID string, financial, usage BillingOperationSpec) error {
	if err := MarkMidjourneySubmitIntentAcceptedPending(requestID, providerTaskID); err != nil {
		return err
	}
	return FinalizeMidjourneySubmitIntent(requestID, providerTaskID, financial, usage)
}

// RejectMidjourneySubmitIntent closes an intent only after an explicit
// non-accepted provider response. Transport/parse failures must leave the row
// pending, because they do not prove whether the provider accepted the job.
func RejectMidjourneySubmitIntent(requestID, reason string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || len(reason) > 4000 {
		return ErrMidjourneySubmitIntentInvalid
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var intent MidjourneySubmitIntent
		if err := lockForUpdate(tx).Where("request_id = ?", requestID).First(&intent).Error; err != nil {
			return err
		}
		switch intent.Status {
		case MidjourneySubmitIntentRejected:
			return nil
		case MidjourneySubmitIntentAccepted:
			return ErrMidjourneySubmitIntentConflict
		case MidjourneySubmitIntentPending:
			return tx.Model(&MidjourneySubmitIntent{}).Where("id = ? AND status = ?", intent.Id, MidjourneySubmitIntentPending).
				Updates(map[string]interface{}{
					"status":     MidjourneySubmitIntentRejected,
					"last_error": strings.TrimSpace(reason),
					"updated_at": common.GetTimestamp(),
				}).Error
		default:
			return ErrMidjourneySubmitIntentConflict
		}
	})
}

// GetMidjourneySubmitIntentsNeedingReconcile returns accepted or
// accepted_pending intents whose marker pair has not yet been confirmed
// applied. accepted_pending is the durable second-phase recovery state: the
// provider identity is known, so charging can be retried safely. It is bounded
// for scheduler fairness; a process crash after the marker transaction simply
// leaves the timestamp at zero and the next pass safely retries.
func GetMidjourneySubmitIntentsNeedingReconcile(limit int) ([]MidjourneySubmitIntent, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var rows []MidjourneySubmitIntent
	err := DB.Where("status IN ? AND (reconciled_at = 0 OR reconciled_at IS NULL)", []string{
		string(MidjourneySubmitIntentAccepted),
		string(MidjourneySubmitIntentAcceptedPending),
	}).
		Order("updated_at asc, id asc").Limit(limit).Find(&rows).Error
	return rows, err
}

// DeferMidjourneySubmitIntentReconciliation moves one failed accepted intent
// behind untouched work while preserving its recoverable lifecycle state. The
// observed timestamp is a CAS fence so a concurrent finalization/reconciliation
// is never overwritten by a stale worker.
func DeferMidjourneySubmitIntentReconciliation(id, observedUpdatedAt int64, cause error) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	if id <= 0 {
		return ErrMidjourneySubmitIntentInvalid
	}
	next := common.GetTimestamp() + 1
	if next <= observedUpdatedAt {
		if observedUpdatedAt == int64(^uint64(0)>>1) {
			next = observedUpdatedAt
		} else {
			next = observedUpdatedAt + 1
		}
	}
	errorMeta := "reconciliation failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		errorMeta = common.SensitiveLogMeta(cause.Error())
	}
	return DB.Model(&MidjourneySubmitIntent{}).
		Where("id = ? AND updated_at = ?", id, observedUpdatedAt).
		Where("status IN ? AND (reconciled_at = 0 OR reconciled_at IS NULL)", []string{
			string(MidjourneySubmitIntentAccepted),
			string(MidjourneySubmitIntentAcceptedPending),
		}).
		Updates(map[string]interface{}{
			"updated_at": next,
			"last_error": errorMeta,
		}).Error
}

func HasMidjourneySubmitIntentsNeedingReconcile() (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	var id int64
	err := DB.Model(&MidjourneySubmitIntent{}).
		Where("status IN ? AND (reconciled_at = 0 OR reconciled_at IS NULL)", []string{
			string(MidjourneySubmitIntentAccepted),
			string(MidjourneySubmitIntentAcceptedPending),
		}).
		Limit(1).Pluck("id", &id).Error
	return id != 0, err
}

// MarkMidjourneySubmitIntentReconciled records that both accepted-intent
// markers were observed applied. It is a CAS so concurrent reconcilers emit
// no conflicting state; repeating after an already-applied timestamp is safe.
func MarkMidjourneySubmitIntentReconciled(requestID string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || DB == nil {
		if DB == nil {
			return errors.New("database is not initialized")
		}
		return ErrMidjourneySubmitIntentInvalid
	}
	now := common.GetTimestamp()
	result := DB.Model(&MidjourneySubmitIntent{}).
		Where("request_id = ? AND status = ? AND (reconciled_at = 0 OR reconciled_at IS NULL)", requestID, MidjourneySubmitIntentAccepted).
		Updates(map[string]interface{}{"reconciled_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var current MidjourneySubmitIntent
	lookup := DB.Where("request_id = ?", requestID).Limit(1).Find(&current)
	if lookup.Error != nil {
		return lookup.Error
	}
	if lookup.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	if current.Status == MidjourneySubmitIntentAccepted && current.ReconciledAt > 0 {
		return nil
	}
	return ErrMidjourneySubmitIntentConflict
}
