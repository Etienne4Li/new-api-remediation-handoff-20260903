package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// NormalizeMidjourneyStatus maps the provider's common spelling variants to
// the shared task status vocabulary used by billing fences.  Midjourney
// proxies in the wild have returned both lifecycle words (for example
// PROCESSING/COMPLETED) and the New-API spellings (IN_PROGRESS/SUCCESS).  An
// empty status without a failure reason is intentionally left empty: callers
// may still refresh metadata while retaining the previously persisted state.
func NormalizeMidjourneyStatus(status, failReason string) TaskStatus {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "NOT_START", "NOT START", "NOTSTART":
		return TaskStatusNotStart
	case "SUBMITTED", "SUBMIT":
		return TaskStatusSubmitted
	case "QUEUED", "QUEUE", "PENDING", "WAITING":
		return TaskStatusQueued
	case "IN_PROGRESS", "IN PROGRESS", "PROCESSING", "RUNNING":
		return TaskStatusInProgress
	case "SUCCESS", "SUCCEEDED", "COMPLETED", "COMPLETE", "DONE":
		return TaskStatusSuccess
	case "FAILURE", "FAILED", "ERROR", "CANCELED", "CANCELLED", "REJECTED":
		return TaskStatusFailure
	case "":
		if strings.TrimSpace(failReason) != "" {
			return TaskStatusFailure
		}
		return ""
	default:
		return TaskStatusUnknown
	}
}

// IsMidjourneyStatusStale applies the shared monotonic task-state fence to a
// provider observation.  A terminal row is authoritative: an explicit
// opposite terminal state (or an unknown/empty state) can never overwrite it.
// Unknown current states remain recoverable for rows written by older
// deployments.  Unknown non-terminal incoming states are rejected when the
// current state is known, preventing a provider spelling we do not understand
// from silently regressing an in-flight task.
func IsMidjourneyStatusStale(current, incoming string) bool {
	currentRaw := strings.TrimSpace(current)
	incomingRaw := strings.TrimSpace(incoming)
	currentStatus := NormalizeMidjourneyStatus(currentRaw, "")
	incomingStatus := NormalizeMidjourneyStatus(incomingRaw, "")

	if currentStatus == TaskStatusSuccess || currentStatus == TaskStatusFailure {
		return incomingStatus != currentStatus
	}
	if incomingRaw == "" {
		// No status is not a transition; the caller can preserve current status
		// while applying metadata/failure-reason changes.
		return false
	}
	if incomingStatus == TaskStatusUnknown {
		// Preserve forward compatibility for legacy rows whose current state is
		// itself unknown, but do not let an unrecognised provider value replace a
		// known lifecycle state.
		return currentStatus != TaskStatusUnknown && currentRaw != incomingRaw
	}
	if currentStatus == TaskStatusUnknown || currentStatus == "" {
		return false
	}
	return IsTaskStatusStale(currentStatus, incomingStatus)
}

type Midjourney struct {
	Id          int    `json:"id"`
	Code        int    `json:"code"`
	UserId      int    `json:"user_id" gorm:"index"`
	Action      string `json:"action" gorm:"type:varchar(40);index"`
	MjId        string `json:"mj_id" gorm:"index"`
	Prompt      string `json:"prompt"`
	PromptEn    string `json:"prompt_en"`
	Description string `json:"description" gorm:"serializer:midjourney_storage"`
	State       string `json:"state"`
	SubmitTime  int64  `json:"submit_time" gorm:"index"`
	StartTime   int64  `json:"start_time" gorm:"index"`
	FinishTime  int64  `json:"finish_time" gorm:"index"`
	ImageUrl    string `json:"image_url" gorm:"serializer:midjourney_storage"`
	VideoUrl    string `json:"video_url" gorm:"serializer:midjourney_storage"`
	VideoUrls   string `json:"video_urls" gorm:"serializer:midjourney_storage"`
	Status      string `json:"status" gorm:"type:varchar(20);index"`
	Progress    string `json:"progress" gorm:"type:varchar(30);index"`
	FailReason  string `json:"fail_reason" gorm:"serializer:midjourney_storage"`
	ChannelId   int    `json:"channel_id"`
	Quota       int    `json:"quota"`
	Buttons     string `json:"buttons" gorm:"serializer:midjourney_storage"`
	Properties  string `json:"properties" gorm:"serializer:midjourney_storage"`

	TokenId          int `json:"-" gorm:"default:0"`
	BillingChannelId int `json:"-" gorm:"default:0"`
	// RefundRetryAt is a scheduling cursor for bounded terminal-refund scans.
	// It never replaces Quota as the durable refund marker; failed rows are
	// moved behind untouched work while remaining fully recoverable.
	RefundRetryAt int64 `json:"-" gorm:"bigint;not null;default:0;index"`
	// BillingRequestId is populated for requests whose accepted-provider
	// response has a durable BillingOperation settlement marker.  Midjourney
	// predates the generic Task table, so this request-scoped identity is the
	// only way a later worker can distinguish a retry from a second provider
	// submission.  It is intentionally hidden from API DTOs.
	BillingRequestId string `json:"-" gorm:"type:varchar(128);index"`
}

const (
	maxMidjourneyStorageURLBytes  = 4096
	maxMidjourneyStorageTextBytes = 16 << 10
	maxMidjourneyStorageJSONBytes = 1 << 20
)

type midjourneyStorageSerializer struct{}

func init() {
	schema.RegisterSerializer("midjourney_storage", midjourneyStorageSerializer{})
}

func (midjourneyStorageSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	bytesValue, ok := databaseJSONBytes(dbValue)
	if !ok {
		return fmt.Errorf("midjourney %s has unsupported database value %T", field.DBName, dbValue)
	}
	value := field.ReflectValueOf(ctx, dst)
	if !value.IsValid() || !value.CanSet() || value.Kind() != reflect.String {
		return fmt.Errorf("midjourney %s is not a writable string", field.DBName)
	}
	value.SetString(string(bytesValue))
	return nil
}

func (midjourneyStorageSerializer) Value(_ context.Context, field *schema.Field, _ reflect.Value, fieldValue interface{}) (interface{}, error) {
	value, ok := fieldValue.(string)
	if !ok {
		return nil, fmt.Errorf("midjourney %s must be a string", field.DBName)
	}
	return prepareMidjourneyStorageValue(field.DBName, value)
}

func prepareMidjourneyStorageValue(column, value string) (string, error) {
	switch column {
	case "image_url":
		return sealProviderRuntimeValue("midjourney image URL", value, maxMidjourneyStorageURLBytes)
	case "video_url":
		return sealProviderRuntimeValue("midjourney video URL", value, maxMidjourneyStorageURLBytes)
	case "video_urls":
		return sealProviderRuntimeValue("midjourney video URLs", value, maxMidjourneyStorageJSONBytes)
	case "description", "fail_reason":
		return common.SanitizeProviderDiagnosticForStorage(value, maxMidjourneyStorageTextBytes), nil
	case "properties", "buttons":
		return common.SanitizeProviderJSONForStorage(value, maxMidjourneyStorageJSONBytes), nil
	default:
		return "", fmt.Errorf("unsupported midjourney storage column %s", column)
	}
}

func (task *Midjourney) openRuntimeMedia() error {
	if task == nil {
		return nil
	}
	var err error
	if task.ImageUrl, err = openProviderRuntimeValue("midjourney image URL", task.ImageUrl, maxMidjourneyStorageURLBytes); err != nil {
		return err
	}
	if task.VideoUrl, err = openProviderRuntimeValue("midjourney video URL", task.VideoUrl, maxMidjourneyStorageURLBytes); err != nil {
		return err
	}
	if task.VideoUrls, err = openProviderRuntimeValue("midjourney video URLs", task.VideoUrls, maxMidjourneyStorageJSONBytes); err != nil {
		return err
	}
	return nil
}

func normalizeMidjourneyStorageUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	type fieldRule struct {
		canonical string
		aliases   []string
	}
	rules := []fieldRule{
		{canonical: "image_url", aliases: []string{"image_url", "ImageUrl"}},
		{canonical: "video_url", aliases: []string{"video_url", "VideoUrl"}},
		{canonical: "video_urls", aliases: []string{"video_urls", "VideoUrls"}},
		{canonical: "description", aliases: []string{"description", "Description"}},
		{canonical: "fail_reason", aliases: []string{"fail_reason", "FailReason"}},
		{canonical: "properties", aliases: []string{"properties", "Properties"}},
		{canonical: "buttons", aliases: []string{"buttons", "Buttons"}},
	}
	for _, rule := range rules {
		var raw any
		present := false
		for _, alias := range rule.aliases {
			if value, ok := values[alias]; ok {
				if present {
					return fmt.Errorf("midjourney %s update is ambiguous", rule.canonical)
				}
				raw = value
				present = true
			}
			delete(values, alias)
		}
		if !present {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("midjourney %s must be a string", rule.canonical)
		}
		prepared, err := prepareMidjourneyStorageValue(rule.canonical, value)
		if err != nil {
			return err
		}
		values[rule.canonical] = prepared
	}
	return nil
}

// BeforeSave protects map writes. Struct writes use the field serializer so a
// failed SQL statement cannot leave the caller's runtime object encrypted.
func (midjourney *Midjourney) BeforeSave(tx *gorm.DB) error {
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			return normalizeMidjourneyStorageUpdateMap(values)
		}
		return rejectUnsupportedProtectedStructDestination[Midjourney](tx,
			"image_url", "video_url", "video_urls", "description", "fail_reason", "properties", "buttons")
	}
	return nil
}

// AfterFind exposes complete media only inside the runtime model. Public DTO
// boundaries continue to redact signed query parameters.
func (midjourney *Midjourney) AfterFind(_ *gorm.DB) error {
	return midjourney.openRuntimeMedia()
}

// ErrMidjourneyBillingFenceMissing means a caller attempted to persist an
// accepted/billable Midjourney task without first journaling its settlement
// intent.  Charging such a row through the legacy independent writes would
// recreate the very crash window the durable path is meant to close.
var ErrMidjourneyBillingFenceMissing = errors.New("midjourney billing fence is missing")

// ErrMidjourneyInsertFenceConflict means a request-scoped identity was reused
// for a different provider task or billing snapshot.  Retrying the upstream
// request in that situation could create a second provider job and/or charge
// the wrong account, so callers must fail closed for manual reconciliation.
var ErrMidjourneyInsertFenceConflict = errors.New("midjourney insert fence conflicts with existing task")

// TaskQueryParams 用于包含所有搜索条件的结构体，可以根据需求添加更多字段
type TaskQueryParams struct {
	ChannelID      string
	MjID           string
	StartTimestamp string
	EndTimestamp   string
}

func GetAllUserTask(userId int, startIdx int, num int, queryParams TaskQueryParams) []*Midjourney {
	var tasks []*Midjourney
	var err error

	// 初始化查询构建器
	query := DB.Where("user_id = ?", userId)

	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		// 假设您已将前端传来的时间戳转换为数据库所需的时间格式，并处理了时间戳的验证和解析
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetAllTasks(startIdx int, num int, queryParams TaskQueryParams) []*Midjourney {
	var tasks []*Midjourney
	var err error

	// 初始化查询构建器
	query := DB

	// 添加过滤条件
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetAllUnFinishTasks() []*Midjourney {
	tasks, _ := GetAllUnFinishTasksWithError()
	return tasks
}

// GetAllUnFinishTasksWithError preserves the database error so schedulers do
// not mistake an outage for an empty Midjourney queue.  The historical
// GetAllUnFinishTasks wrapper remains for callers that only need a best-effort
// slice.
func GetAllUnFinishTasksWithError() ([]*Midjourney, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var tasks []*Midjourney
	// get all tasks progress is not 100%
	err := DB.Where("progress != ?", "100%").Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetMidjourneyTasksWithPendingRefundWithError returns terminally failed rows
// whose non-zero Quota marker still needs to be reversed. A process can exit
// after the status CAS commits but before RefundMidjourneyQuota installs its
// BillingOperation; keeping this query separate from provider polling makes
// that crash window recoverable without asking the upstream for a terminal
// task again.
func GetMidjourneyTasksWithPendingRefundWithError(limit int) ([]*Midjourney, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	failedStatuses := []string{"FAILURE", "FAILED", "ERROR", "CANCELED", "CANCELLED", "REJECTED"}
	successStatuses := []string{"SUCCESS", "SUCCEEDED", "COMPLETED", "COMPLETE", "DONE"}
	var tasks []*Midjourney
	err := DB.Where("quota > 0").
		Where("UPPER(TRIM(status)) IN ? OR (progress = ? AND TRIM(fail_reason) <> '' AND UPPER(TRIM(status)) NOT IN ?)", failedStatuses, "100%", successStatuses).
		Order("refund_retry_at asc, id asc").Limit(limit).Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// DeferMidjourneyRefundReconciliation moves a failed terminal-refund row
// behind untouched work without consuming its durable Quota marker. The
// observed retry timestamp is a CAS fence so a concurrent deferral or refund
// completion is never overwritten by a stale worker.
func DeferMidjourneyRefundReconciliation(id int, observedRetryAt int64) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	if id <= 0 {
		return errors.New("midjourney id is required for refund deferral")
	}
	next := common.GetTimestamp() + 1
	if next <= observedRetryAt {
		if observedRetryAt == math.MaxInt64 {
			next = math.MaxInt64
		} else {
			next = observedRetryAt + 1
		}
	}
	return DB.Model(&Midjourney{}).
		Where("id = ? AND quota > 0 AND refund_retry_at = ?", id, observedRetryAt).
		UpdateColumn("refund_retry_at", next).Error
}

// HasMidjourneyTasksWithPendingRefundWithError is the cheap scheduler probe
// corresponding to GetMidjourneyTasksWithPendingRefundWithError.
func HasMidjourneyTasksWithPendingRefundWithError() (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	failedStatuses := []string{"FAILURE", "FAILED", "ERROR", "CANCELED", "CANCELLED", "REJECTED"}
	successStatuses := []string{"SUCCESS", "SUCCEEDED", "COMPLETED", "COMPLETE", "DONE"}
	var id int
	err := DB.Model(&Midjourney{}).Where("quota > 0").
		Where("UPPER(TRIM(status)) IN ? OR (progress = ? AND TRIM(fail_reason) <> '' AND UPPER(TRIM(status)) NOT IN ?)", failedStatuses, "100%", successStatuses).
		Limit(1).Pluck("id", &id).Error
	if err != nil {
		return false, err
	}
	return id != 0, nil
}

// HasUnfinishedMidjourneyTasksWithError reports whether at least one
// Midjourney task is still in progress. It is a cheap existence check
// (LIMIT 1) used to decide whether the midjourney_poll system task needs to
// run. Returning the database error lets schedulers fail closed instead of
// treating an outage as an idle queue.
func HasUnfinishedMidjourneyTasksWithError() (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	var id int
	err := DB.Model(&Midjourney{}).
		Where("progress != ?", "100%").
		Limit(1).
		Pluck("id", &id).Error
	if err != nil {
		return false, err
	}
	return id != 0, nil
}

// HasUnfinishedMidjourneyTasks is retained for legacy callers.
func HasUnfinishedMidjourneyTasks() bool {
	pending, _ := HasUnfinishedMidjourneyTasksWithError()
	return pending
}

func GetByOnlyMJId(mjId string) *Midjourney {
	var matches []Midjourney
	err := DB.Where("mj_id = ?", mjId).Limit(2).Find(&matches).Error
	if err != nil || len(matches) != 1 {
		// Callers without a channel/user namespace cannot safely attribute a
		// provider id that appears on multiple rows.
		return nil
	}
	return &matches[0]
}

func GetByMJId(userId int, mjId string) *Midjourney {
	var matches []Midjourney
	err := DB.Where("user_id = ? and mj_id = ?", userId, mjId).Limit(2).Find(&matches).Error
	if err != nil || len(matches) != 1 {
		// Provider task ids are scoped by channel, but legacy public routes contain
		// only mj_id. Fail closed when two channels issued the same id to one user;
		// selecting either row would fetch media or dispatch an action arbitrarily.
		return nil
	}
	return &matches[0]
}

func GetByMJIds(userId int, mjIds []string) []*Midjourney {
	var mj []*Midjourney
	var err error
	err = DB.Where("user_id = ? and mj_id in (?)", userId, mjIds).Find(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func GetMjByuId(id int) *Midjourney {
	var mj *Midjourney
	var err error
	err = DB.Where("id = ?", id).First(&mj).Error
	if err != nil {
		return nil
	}
	return mj
}

func UpdateProgress(id int, progress string) error {
	return DB.Model(&Midjourney{}).Where("id = ?", id).Update("progress", progress).Error
}

func (midjourney *Midjourney) Insert() error {
	var err error
	err = DB.Create(midjourney).Error
	return err
}

// InsertWithBillingFence persists an accepted Midjourney task without losing
// the request-scoped settlement intent when the database returns an ambiguous
// error. PrepareMidjourneyTaskBilling creates the legacy_settle marker first;
// this method locks that marker, then either loads the row committed by an
// earlier attempt or inserts exactly one row. The marker and task are kept in
// separate transactions deliberately: a task INSERT failure must leave the
// marker pending so the billing reconciler can still discover the provider
// acceptance.
//
// Callers handling an unbilled/legacy task should continue using Insert().
func (midjourney *Midjourney) InsertWithBillingFence() error {
	if midjourney == nil {
		return errors.New("midjourney task is nil")
	}
	if midjourney.Id != 0 {
		return errors.New("midjourney task id must be zero before insert")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	requestID := strings.TrimSpace(midjourney.BillingRequestId)
	if requestID == "" {
		return ErrMidjourneyBillingFenceMissing
	}
	midjourney.BillingRequestId = requestID

	return DB.Transaction(func(tx *gorm.DB) error {
		// The settlement marker is the durable intent boundary. A missing marker
		// is not safe to repair here: doing so would let a caller accidentally
		// persist/charge a row for which the upstream acceptance was never
		// journaled.
		var marker BillingOperation
		if err := lockForUpdate(tx).
			Where("request_id = ? AND component = ?", requestID, BillingOperationLegacySettleComponent).
			First(&marker).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMidjourneyBillingFenceMissing
			}
			return err
		}
		if marker.UserId != midjourney.UserId || !midjourneySettlementMarkerMatchesTask(&marker, midjourney) {
			return ErrMidjourneyInsertFenceConflict
		}
		// A previous ambiguous INSERT may already have committed. Load it under
		// the same marker lock rather than creating a duplicate provider mapping.
		var existing []Midjourney
		if err := tx.Where("billing_request_id = ?", requestID).
			Order("id asc").Limit(2).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) > 1 {
			return ErrMidjourneyInsertFenceConflict
		}
		if len(existing) == 1 {
			if !midjourneyInsertMatches(&existing[0], midjourney) {
				return ErrMidjourneyInsertFenceConflict
			}
			*midjourney = existing[0]
			return nil
		}
		if err := tx.Create(midjourney).Error; err != nil {
			return err
		}
		return nil
	})
}

// midjourneySettlementMarkerMatchesTask verifies the immutable billing fields
// before an accepted task is associated with a marker. TokenKey is not stored
// in BillingOperation; TokenId and the signed deltas are the authoritative
// identity. A zero-quota operation is valid and intentionally matches zero
// token/wallet deltas.
func midjourneySettlementMarkerMatchesTask(marker *BillingOperation, task *Midjourney) bool {
	if marker == nil || task == nil {
		return false
	}
	quota := int64(task.Quota)
	if quota < 0 {
		return false
	}
	if marker.WalletDelta != -quota || marker.SubscriptionDelta != 0 {
		return false
	}
	if marker.TokenId != task.TokenId {
		// Playground/wallet-only markers deliberately zero TokenId even when the
		// request carried an execution token for routing. Since TokenDelta is also
		// zero, that token must not become part of a later refund identity.
		if marker.TokenId == 0 && marker.TokenDelta == 0 {
			// Keep the task's routing token for response/log compatibility; callers
			// constructing a refund use the marker's zero TokenId instead.
		} else {
			// Older callers may not have copied TokenId onto the task before the
			// marker was introduced. Let the insert path hydrate the missing value
			// from the immutable marker, but reject two non-zero identities diverging.
			if marker.TokenId == 0 || task.TokenId != 0 {
				return false
			}
			task.TokenId = marker.TokenId
		}
	}
	// Playground/explicit wallet-only requests intentionally carry no token
	// ledger delta even when the wallet charge is positive. A non-zero token
	// marker must still match the task's immutable token identity exactly.
	if marker.TokenDelta != quota &&
		!(quota == 0 && marker.TokenDelta == 0) &&
		!(marker.TokenDelta == 0 && marker.TokenId == 0 && !marker.RequireTokenBalance) {
		return false
	}
	return true
}

// midjourneyInsertMatches compares the provider and billing identity fields
// that must remain stable across an INSERT retry. Mutable result metadata is
// deliberately excluded: a retry may have received a newer provider payload,
// but it must never bind the request fence to a different provider id or
// amount.
func midjourneyInsertMatches(existing, candidate *Midjourney) bool {
	if existing == nil || candidate == nil {
		return false
	}
	return existing.UserId == candidate.UserId &&
		existing.MjId == candidate.MjId &&
		existing.Action == candidate.Action &&
		existing.ChannelId == candidate.ChannelId &&
		existing.Quota == candidate.Quota &&
		existing.TokenId == candidate.TokenId &&
		existing.BillingChannelId == candidate.BillingChannelId &&
		strings.TrimSpace(existing.BillingRequestId) == strings.TrimSpace(candidate.BillingRequestId)
}

// FindByBillingRequestID returns the single Midjourney row attached to a
// durable request identity. Multiple rows are reported as a conflict rather
// than selecting an arbitrary task during reconciliation.
func FindByBillingRequestID(requestID string) (*Midjourney, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, errors.New("midjourney billing request id is missing")
	}
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var rows []Midjourney
	if err := DB.Where("billing_request_id = ?", requestID).Order("id asc").Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(rows) > 1 {
		return nil, ErrMidjourneyInsertFenceConflict
	}
	return &rows[0], nil
}

// EnsureMidjourneyTaskFromSubmitIntent restores the smallest useful local task
// row for an accepted provider response. The HTTP process can die after the
// provider identity is persisted but before the normal Midjourney INSERT; in
// that case charging the accepted intent without a row would make the task
// impossible to poll or refund through the normal lifecycle. This helper is
// intentionally limited to accepted/accepted_pending intents—plain pending
// rows never establish that the provider accepted anything and must not create
// a billable task.
//
// Existing rows are checked against every immutable identity field. A quota of
// zero is allowed only as the post-refund state of an otherwise identical row;
// all other drift (user, provider id, action, execution/billing channel,
// token, or request fence) is a hard conflict. InsertWithBillingFence then
// performs the final request-fence transaction, so an ambiguous INSERT retry
// cannot create a second row.
func EnsureMidjourneyTaskFromSubmitIntent(intent *MidjourneySubmitIntent) (*Midjourney, error) {
	if intent == nil {
		return nil, errors.New("midjourney submit intent is nil")
	}
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if intent.Status != MidjourneySubmitIntentAccepted && intent.Status != MidjourneySubmitIntentAcceptedPending {
		return nil, ErrMidjourneyInsertFenceConflict
	}
	requestID := strings.TrimSpace(intent.RequestId)
	providerTaskID := strings.TrimSpace(intent.ProviderTaskId)
	if requestID == "" || providerTaskID == "" || len(requestID) > 128 || len(providerTaskID) > 255 ||
		intent.UserId <= 0 || intent.ChannelId < 0 || intent.BillingChannelId < 0 ||
		intent.TokenId < 0 || intent.Quota < 0 || intent.Quota > common.MaxQuota || strings.TrimSpace(intent.Action) == "" {
		return nil, ErrMidjourneySubmitIntentInvalid
	}

	if existing, err := FindByBillingRequestID(requestID); err == nil {
		if !midjourneyTaskMatchesSubmitIntent(existing, intent) {
			return nil, ErrMidjourneyInsertFenceConflict
		}
		return existing, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Keep timestamps in the millisecond unit used by Midjourney rows. Intent
	// timestamps are Unix seconds; accepting a pre-populated millisecond value
	// makes this safe for operator-created repair rows as well.
	submitTime := intent.CreatedAt
	if submitTime <= 0 {
		submitTime = common.GetTimestamp()
	}
	if submitTime < 1_000_000_000_000 {
		submitTime *= 1000
	}
	task := &Midjourney{
		Code:             1,
		UserId:           intent.UserId,
		Action:           strings.TrimSpace(intent.Action),
		MjId:             providerTaskID,
		SubmitTime:       submitTime,
		Progress:         "0%",
		ChannelId:        intent.ChannelId,
		Quota:            intent.Quota,
		TokenId:          intent.TokenId,
		BillingChannelId: intent.BillingChannelId,
		BillingRequestId: requestID,
	}
	if err := task.InsertWithBillingFence(); err != nil {
		return nil, err
	}
	return task, nil
}

func midjourneyTaskMatchesSubmitIntent(task *Midjourney, intent *MidjourneySubmitIntent) bool {
	if task == nil || intent == nil {
		return false
	}
	if task.UserId != intent.UserId ||
		strings.TrimSpace(task.MjId) != strings.TrimSpace(intent.ProviderTaskId) ||
		strings.TrimSpace(task.Action) != strings.TrimSpace(intent.Action) ||
		task.ChannelId != intent.ChannelId ||
		task.BillingChannelId != intent.BillingChannelId ||
		task.TokenId != intent.TokenId ||
		strings.TrimSpace(task.BillingRequestId) != strings.TrimSpace(intent.RequestId) {
		return false
	}
	// Refund clears only the task quota marker. Treat that one lifecycle change
	// as compatible; a different non-zero amount indicates request identity
	// reuse or corruption and must not be silently adopted.
	return task.Quota == intent.Quota || task.Quota == 0
}

// ValidateMidjourneyBillingMarker exposes a small read-only consistency check
// for the service/reconciler without leaking the model's private comparison
// helpers. It returns a typed conflict for a marker/task mismatch.
func ValidateMidjourneyBillingMarker(marker *BillingOperation, task *Midjourney) error {
	if marker == nil || task == nil || !midjourneySettlementMarkerMatchesTask(marker, task) {
		return fmt.Errorf("%w: settlement marker does not match task", ErrMidjourneyInsertFenceConflict)
	}
	return nil
}

func (midjourney *Midjourney) Update() error {
	var err error
	err = DB.Save(midjourney).Error
	return err
}

func (midjourney *Midjourney) UpdateBillingState() error {
	if midjourney == nil || midjourney.Id <= 0 {
		return errors.New("midjourney id is required for billing update")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	result := DB.Model(&Midjourney{}).
		Where("id = ?", midjourney.Id).
		Updates(map[string]interface{}{
			"quota":              midjourney.Quota,
			"token_id":           midjourney.TokenId,
			"billing_channel_id": midjourney.BillingChannelId,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ClearBillingQuota atomically consumes the task's refund marker. The ledger
// operation is already idempotent; this CAS is the matching at-most-once fence
// for the human refund log when two pollers loaded the same failed row before
// either cleared it. A false result with nil error means another worker has
// already cleared the exact marker.
func (midjourney *Midjourney) ClearBillingQuota(expectedQuota int) (bool, error) {
	if midjourney == nil || midjourney.Id <= 0 {
		return false, errors.New("midjourney id is required for billing update")
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	if expectedQuota <= 0 {
		return false, errors.New("midjourney refund quota must be positive")
	}
	result := DB.Model(&Midjourney{}).
		Where("id = ? AND quota = ?", midjourney.Id, expectedQuota).
		Update("quota", 0)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		midjourney.Quota = 0
		return true, nil
	}
	var current Midjourney
	if err := DB.Select("id", "quota").Where("id = ?", midjourney.Id).First(&current).Error; err != nil {
		return false, err
	}
	if current.Quota == 0 {
		midjourney.Quota = 0
		return false, nil
	}
	return false, fmt.Errorf("midjourney quota marker changed: expected=%d current=%d", expectedQuota, current.Quota)
}

func (midjourney *Midjourney) GetBillingChannelId() int {
	if midjourney.BillingChannelId > 0 {
		return midjourney.BillingChannelId
	}
	return midjourney.ChannelId
}

// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Returns (true, nil) if this caller won the update, (false, nil) if
// another process already moved the task out of fromStatus.
// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Uses Model().Select("*").Updates() to avoid GORM Save()'s INSERT fallback.
func (midjourney *Midjourney) UpdateWithStatus(fromStatus string) (bool, error) {
	if midjourney == nil || midjourney.Id <= 0 {
		return false, errors.New("midjourney id is required for status update")
	}
	// Provider polls can arrive out of order.  Reject a regressive/unknown
	// observation before touching the database; the caller must not perform a
	// refund after a delayed FAILURE loses this fence.
	if IsMidjourneyStatusStale(fromStatus, midjourney.Status) {
		return false, nil
	}
	// Build an explicit assignment map instead of handing the live struct to
	// GORM.  The update callback may mutate struct fields while resolving
	// values, which races with concurrent pollers/readers and can also include
	// stale immutable identity fields in a terminal-state write.
	updates := map[string]interface{}{
		"code":               midjourney.Code,
		"action":             midjourney.Action,
		"prompt":             midjourney.Prompt,
		"prompt_en":          midjourney.PromptEn,
		"description":        midjourney.Description,
		"state":              midjourney.State,
		"submit_time":        midjourney.SubmitTime,
		"start_time":         midjourney.StartTime,
		"finish_time":        midjourney.FinishTime,
		"image_url":          midjourney.ImageUrl,
		"video_url":          midjourney.VideoUrl,
		"video_urls":         midjourney.VideoUrls,
		"status":             midjourney.Status,
		"progress":           midjourney.Progress,
		"fail_reason":        midjourney.FailReason,
		"quota":              midjourney.Quota,
		"buttons":            midjourney.Buttons,
		"properties":         midjourney.Properties,
		"token_id":           midjourney.TokenId,
		"billing_channel_id": midjourney.BillingChannelId,
	}
	result := DB.Model(&Midjourney{}).
		Where("id = ? AND status = ?", midjourney.Id, fromStatus).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func MjBulkUpdate(mjIds []string, params map[string]any) error {
	return DB.Model(&Midjourney{}).
		Where("mj_id in (?)", mjIds).
		Updates(params).Error
}

func MjBulkUpdateByTaskIds(taskIDs []int, params map[string]any) error {
	return DB.Model(&Midjourney{}).
		Where("id in (?)", taskIDs).
		Updates(params).Error
}

// CountAllTasks returns total midjourney tasks for admin query.
func CountAllTasks(queryParams TaskQueryParams) (int64, error) {
	var total int64
	query := DB.Model(&Midjourney{})
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	if err := query.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// CountAllUserTask returns total midjourney tasks for user.
func CountAllUserTask(userId int, queryParams TaskQueryParams) (int64, error) {
	var total int64
	query := DB.Model(&Midjourney{}).Where("user_id = ?", userId)
	if queryParams.MjID != "" {
		query = query.Where("mj_id = ?", queryParams.MjID)
	}
	if queryParams.StartTimestamp != "" {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != "" {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	if err := query.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}
