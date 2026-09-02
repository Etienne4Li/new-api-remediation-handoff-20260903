package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	commonRelay "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"gorm.io/gorm"
)

type TaskStatus string

func (t TaskStatus) ToVideoStatus() string {
	var status string
	switch t {
	case TaskStatusQueued, TaskStatusSubmitted:
		status = dto.VideoStatusQueued
	case TaskStatusInProgress:
		status = dto.VideoStatusInProgress
	case TaskStatusSuccess:
		status = dto.VideoStatusCompleted
	case TaskStatusFailure:
		status = dto.VideoStatusFailed
	default:
		status = dto.VideoStatusUnknown // Default fallback
	}
	return status
}

const (
	TaskStatusNotStart   TaskStatus = "NOT_START"
	TaskStatusSubmitted             = "SUBMITTED"
	TaskStatusQueued                = "QUEUED"
	TaskStatusInProgress            = "IN_PROGRESS"
	TaskStatusFailure               = "FAILURE"
	TaskStatusSuccess               = "SUCCESS"
	TaskStatusUnknown               = "UNKNOWN"
)

// IsTerminalTaskStatus reports whether no later provider poll is allowed to
// move a task out of this state.  A stale response must never turn a charged
// SUCCESS into FAILURE (or revive a refunded FAILURE).
func IsTerminalTaskStatus(status TaskStatus) bool {
	return status == TaskStatusSuccess || status == TaskStatusFailure
}

// IsTaskStatusStale reports whether incoming is an unusable or regressive
// observation relative to current. Async provider responses are not
// necessarily ordered: a delayed QUEUED response can arrive after the task
// reached IN_PROGRESS, and a delayed terminal response can race another
// worker. Keeping this rule in model makes every status CAS use the same
// billing-safe fence.
//
// Empty/UNKNOWN incoming values are never authoritative. An unknown current
// value is recoverable by an explicit, known provider state so rows written by
// older versions (or a partially migrated deployment) can make progress.
func IsTaskStatusStale(current, incoming TaskStatus) bool {
	incomingRank, incomingKnown := taskStatusRank(incoming)
	if !incomingKnown {
		return true
	}
	currentRank, currentKnown := taskStatusRank(current)
	if !currentKnown {
		return false
	}

	// SUCCESS and FAILURE share the terminal rank but are mutually exclusive;
	// once either is persisted, only an observation of the same state may
	// update metadata.
	if IsTerminalTaskStatus(current) {
		return incoming != current
	}
	if IsTerminalTaskStatus(incoming) {
		return false
	}
	return incomingRank < currentRank
}

func taskStatusRank(status TaskStatus) (int, bool) {
	switch status {
	case TaskStatusNotStart:
		return 0, true
	case TaskStatusSubmitted:
		return 1, true
	case TaskStatusQueued:
		return 2, true
	case TaskStatusInProgress:
		return 3, true
	case TaskStatusSuccess, TaskStatusFailure:
		return 4, true
	default:
		return 0, false
	}
}

// TaskRefundLegacyCutoff separates tasks created before timeout refunds were
// introduced. Those legacy tasks are failed without an automatic refund.
const TaskRefundLegacyCutoff int64 = 1771718400 // 2026-02-22 00:00:00 UTC

// TaskBillingReconcileBatchLimit bounds the amount of terminal billing work a
// single polling pass can claim. Legacy/ambiguous reconciliations are fenced
// for manual review; modern journaled operations are safe to replay.
const TaskBillingReconcileBatchLimit = 100

// TaskBillingReconcileState is the durable state machine for terminal-task
// refunds. Empty is kept as a backwards-compatible alias for pending: rows
// written before this field was introduced can still be claimed once. Legacy
// rows left in processing are deliberately not reclaimed automatically after
// their lease expires because the old worker may have crossed a
// non-idempotent ledger side effect before crashing; modern rows with a stable
// BillingRequestId use the durable BillingOperation fence and may be reclaimed.
type TaskBillingReconcileState string

const (
	TaskBillingReconcilePending TaskBillingReconcileState = "pending"
	// TaskBillingReconcileRetryable means the previous attempt is known to
	// have left no net ledger side effect (for example, a token refund was
	// compensated after funding rejected). It is safe for the worker to claim
	// this row again after the short lease has elapsed.
	TaskBillingReconcileRetryable  TaskBillingReconcileState = "retryable"
	TaskBillingReconcileProcessing TaskBillingReconcileState = "processing"
	TaskBillingReconcileComplete   TaskBillingReconcileState = "complete"
	TaskBillingReconcileManual     TaskBillingReconcileState = "manual"
)

// TaskBillingSettlementState is the durable state machine for the small
// window between accepting an asynchronous task and committing its final
// billing delta.  A task is deliberately kept out of the provider polling
// queue while it is pending: polling a task whose charge is not committed can
// otherwise turn a transient database failure into a free task (or a double
// charge on retry).
//
// Unlike terminal-task refunds, settlement operations are backed by the
// BillingOperation journal and are therefore safe to reclaim after a lease
// expires.  A processing row left by a crashed worker can be retried by the
// next worker; manual is reserved for legacy rows that lack the immutable
// inputs needed to reconstruct a durable operation.
type TaskBillingSettlementState string

const (
	TaskBillingSettlementPending    TaskBillingSettlementState = "pending"
	TaskBillingSettlementProcessing TaskBillingSettlementState = "processing"
	TaskBillingSettlementComplete   TaskBillingSettlementState = "complete"
	TaskBillingSettlementManual     TaskBillingSettlementState = "manual"
)

// TaskBillingAdjustmentState is the durable state machine for the second
// (terminal) billing adjustment of an asynchronous task.  The submit path
// records BillingSettlementQuota, but some providers only reveal the actual
// billable amount when the task completes (for example final token usage or a
// server-selected duration).  Keeping that adjustment separate from the
// submit settlement lets both operations have their own idempotency key.
type TaskBillingAdjustmentState string

const (
	TaskBillingAdjustmentPending    TaskBillingAdjustmentState = "pending"
	TaskBillingAdjustmentProcessing TaskBillingAdjustmentState = "processing"
	TaskBillingAdjustmentComplete   TaskBillingAdjustmentState = "complete"
	TaskBillingAdjustmentManual     TaskBillingAdjustmentState = "manual"
)

type Task struct {
	ID        int64 `json:"id" gorm:"primary_key;AUTO_INCREMENT"`
	CreatedAt int64 `json:"created_at" gorm:"index"`
	UpdatedAt int64 `json:"updated_at"`
	// LastPolledAt is an internal fairness marker for the bounded async polling
	// queue. Nanoseconds keep consecutive passes ordered even when they run within
	// the same second. Keeping the column non-null lets all supported databases
	// use the ordering index directly; zero means that a task was never polled.
	LastPolledAt int64                 `json:"-" gorm:"not null;default:0;index"`
	TaskID       string                `json:"task_id" gorm:"type:varchar(191);index"`                                                    // 第三方id，不一定有/ song id\ Task id
	Platform     constant.TaskPlatform `json:"platform" gorm:"type:varchar(30);index;uniqueIndex:idx_tasks_upstream_identity,priority:2"` // 平台
	UserId       int                   `json:"user_id" gorm:"index"`
	Group        string                `json:"group" gorm:"type:varchar(50)"` // 修正计费用
	ChannelId    int                   `json:"channel_id" gorm:"index;uniqueIndex:idx_tasks_upstream_identity,priority:1"`
	// UpstreamTaskIdentity is a nullable, fixed-width digest of the provider
	// task identity. It is denormalized from PrivateData so the database can
	// enforce uniqueness across processes. NULL is retained for rows that
	// predate the field or have no provider identity; SQL unique indexes permit
	// multiple NULLs, so legacy rows do not collide during the additive migration.
	UpstreamTaskIdentity *string    `json:"-" gorm:"type:char(64);uniqueIndex:idx_tasks_upstream_identity,priority:3"`
	Quota                int        `json:"quota"`
	Action               string     `json:"action" gorm:"type:varchar(40);index"` // 任务类型, song, lyrics, description-mode
	Status               TaskStatus `json:"status" gorm:"type:varchar(20);index"` // 任务状态
	FailReason           string     `json:"fail_reason"`
	SubmitTime           int64      `json:"submit_time" gorm:"index"`
	StartTime            int64      `json:"start_time" gorm:"index"`
	FinishTime           int64      `json:"finish_time" gorm:"index"`
	Progress             string     `json:"progress" gorm:"type:varchar(20);index"`
	// BillingReconcileUntil is a short-lived DB lease used when retrying a
	// failed-task refund. It is deliberately separate from Quota: a non-zero
	// quota is the durable refund marker and must never be cleared before both
	// funding and token compensation succeed.
	BillingReconcileUntil int64 `json:"-" gorm:"index"`
	// BillingReconcileState is persisted independently from the lease. A lease
	// is only a concurrency hint; the state is the crash-safety fence.
	BillingReconcileState TaskBillingReconcileState `json:"-" gorm:"type:varchar(16);index"`
	// BillingRequestId and BillingPreConsumedQuota are immutable snapshots of
	// the request-level billing operation.  They let a later worker reconstruct
	// the same idempotency key after the HTTP process has exited.
	BillingRequestId        string `json:"-" gorm:"type:varchar(128);index"`
	BillingPreConsumedQuota int    `json:"-"`
	BillingSettlementQuota  int    `json:"-"`
	BillingTokenUnlimited   bool   `json:"-"`
	BillingPlayground       bool   `json:"-"`
	// BillingSettlementState/Until form a DB lease plus crash-recovery fence.
	// BillingSettlementError is operator-facing metadata only; never expose it
	// in task DTOs because provider errors may contain sensitive details.
	BillingSettlementState TaskBillingSettlementState `json:"-" gorm:"type:varchar(16);index"`
	BillingSettlementUntil int64                      `json:"-" gorm:"index"`
	BillingSettlementError string                     `json:"-" gorm:"type:text"`
	// BillingFinalQuota is the immutable terminal amount selected by the first
	// observer of a successful task.  It is deliberately separate from Quota:
	// Quota remains the submit-time amount until the durable terminal operation
	// and its task marker are committed.
	BillingFinalQuota              int                        `json:"-"`
	BillingAdjustmentReason        string                     `json:"-" gorm:"type:varchar(255)"`
	BillingAdjustmentState         TaskBillingAdjustmentState `json:"-" gorm:"type:varchar(16);index"`
	BillingAdjustmentUntil         int64                      `json:"-" gorm:"index"`
	BillingAdjustmentError         string                     `json:"-" gorm:"type:text"`
	BillingAdjustmentUsageRecorded bool                       `json:"-"`
	// BillingUsageRecorded is an at-most-once fence for the informational
	// consume log/aggregate counters.  The financial ledger is committed by
	// BillingOperation; this marker prevents normal retries from double-counting
	// usage after a settlement worker restart.
	BillingUsageRecorded bool       `json:"-"`
	Properties           Properties `json:"properties" gorm:"type:json"`
	Username             string     `json:"username,omitempty" gorm:"-"`
	// 禁止返回给用户，内部可能包含key等隐私信息
	PrivateData TaskPrivateData `json:"-" gorm:"column:private_data;type:json"`
	Data        json.RawMessage `json:"data" gorm:"type:json"`
}

func (t *Task) SetData(data any) {
	b, _ := common.Marshal(data)
	t.Data = json.RawMessage(b)
}

func (t *Task) GetData(v any) error {
	if v == nil {
		return errors.New("task data target is nil")
	}
	return common.Unmarshal(t.Data, v)
}

type Properties struct {
	Input             string `json:"input"`
	UpstreamModelName string `json:"upstream_model_name,omitempty"`
	OriginModelName   string `json:"origin_model_name,omitempty"`
}

func (m *Properties) Scan(val interface{}) error {
	bytesValue, ok := databaseJSONBytes(val)
	if !ok || len(bytesValue) == 0 {
		*m = Properties{}
		return nil
	}
	return common.Unmarshal(bytesValue, m)
}

func (m Properties) Value() (driver.Value, error) {
	if m == (Properties{}) {
		return nil, nil
	}
	return common.Marshal(m)
}

type TaskPrivateData struct {
	Key            string `json:"key,omitempty"`
	UpstreamTaskID string `json:"upstream_task_id,omitempty"` // 上游真实 task ID
	ResultURL      string `json:"result_url,omitempty"`       // 任务成功后的结果 URL（视频地址等）
	// 计费上下文：用于异步退款/差额结算（轮询阶段读取）
	BillingSource  string              `json:"billing_source,omitempty"`  // "wallet" 或 "subscription"
	SubscriptionId int                 `json:"subscription_id,omitempty"` // 订阅 ID，用于订阅退款
	TokenId        int                 `json:"token_id,omitempty"`        // 令牌 ID，用于令牌额度退款
	NodeName       string              `json:"node_name,omitempty"`       // 发起任务的节点名，轮询结算阶段据此归属日志而非最后查询节点
	BillingContext *TaskBillingContext `json:"billing_context,omitempty"` // 计费参数快照（用于轮询阶段重新计算）
}

// TaskBillingContext 记录任务提交时的计费参数，以便轮询阶段可以重新计算额度。
type TaskBillingContext struct {
	ModelPrice      float64            `json:"model_price,omitempty"`       // 模型单价
	GroupRatio      float64            `json:"group_ratio,omitempty"`       // 分组倍率
	ModelRatio      float64            `json:"model_ratio,omitempty"`       // 模型倍率
	OtherRatios     map[string]float64 `json:"other_ratios,omitempty"`      // 附加倍率（时长、分辨率等）
	OriginModelName string             `json:"origin_model_name,omitempty"` // 模型名称，必须为OriginModelName
	PerCallBilling  bool               `json:"per_call_billing,omitempty"`  // 按次计费：跳过轮询阶段的差额结算
}

// GetUpstreamTaskID 获取上游真实 task ID（用于与 provider 通信）
// 旧数据没有 UpstreamTaskID 时，TaskID 本身就是上游 ID
func (t *Task) GetUpstreamTaskID() string {
	if t == nil {
		return ""
	}
	if upstreamID := strings.TrimSpace(t.PrivateData.UpstreamTaskID); upstreamID != "" {
		return upstreamID
	}
	return strings.TrimSpace(t.TaskID)
}

// GetResultURL 获取任务结果 URL（视频地址等）
// 新数据存在 PrivateData.ResultURL 中；旧数据回退到 FailReason（历史兼容）
func (t *Task) GetResultURL() string {
	if t == nil {
		return ""
	}
	if t.PrivateData.ResultURL != "" {
		return t.PrivateData.ResultURL
	}
	return t.FailReason
}

// GenerateTaskID 生成对外暴露的 task_xxxx 格式 ID
func GenerateTaskID() string {
	key, _ := common.GenerateRandomCharsKey(32)
	return "task_" + key
}

func (p *TaskPrivateData) Scan(val interface{}) error {
	bytesValue, ok := databaseJSONBytes(val)
	if !ok || len(bytesValue) == 0 {
		*p = TaskPrivateData{}
		return nil
	}
	if err := common.Unmarshal(bytesValue, p); err != nil {
		return err
	}
	// Task.PrivateData is an internal JSON blob, but its Key can be a
	// provider credential that outlives the request which created the task.
	// New rows store the field as the versioned credential envelope. Keep old
	// plaintext rows readable during the one-way startup migration; the expand
	// migration (and the next task write) converts them in place.
	if strings.TrimSpace(p.Key) != "" && common.IsCredentialCiphertext(p.Key) {
		plaintext, err := common.DecryptCredential(strings.TrimSpace(p.Key))
		if err != nil {
			return fmt.Errorf("%w: decrypt task private key: %v", ErrCredentialStorageCorrupt, err)
		}
		p.Key = plaintext
	}
	resultURL, err := openProviderRuntimeValue("task private result URL", p.ResultURL, common.MaxHTTPURLLength)
	if err != nil {
		return err
	}
	p.ResultURL = resultURL
	return nil
}

// databaseJSONBytes normalizes the values returned by the three supported
// SQL drivers for TEXT/JSON columns.  SQLite and MySQL usually return []byte,
// while PostgreSQL (and some test drivers) may return string; treating the
// latter as empty silently loses UpstreamTaskID during identity backfill.
func databaseJSONBytes(value any) ([]byte, bool) {
	switch typed := value.(type) {
	case []byte:
		return typed, true
	case string:
		return []byte(typed), true
	case nil:
		return nil, true
	default:
		return nil, false
	}
}

func (p TaskPrivateData) Value() (driver.Value, error) {
	if (p == TaskPrivateData{}) {
		return nil, nil
	}
	// Always seal the runtime value at the DB boundary. Do not special-case a
	// string beginning with "enc:v1:": a provider key may legitimately have
	// that prefix, and accepting it as pre-sealed would make the boundary
	// ambiguous.
	if p.Key != "" {
		sealed, err := common.EncryptCredential(p.Key)
		if err != nil {
			return nil, fmt.Errorf("encrypt task private key: %w", err)
		}
		p.Key = sealed
	}
	sealedResultURL, err := sealProviderRuntimeValue("task private result URL", p.ResultURL, common.MaxHTTPURLLength)
	if err != nil {
		return nil, err
	}
	p.ResultURL = sealedResultURL
	return common.Marshal(p)
}

func normalizeTaskPrivateDataUpdateMap(values map[string]interface{}) error {
	if values == nil {
		return nil
	}
	var (
		rawValue interface{}
		present  bool
	)
	for _, alias := range []string{"private_data", "PrivateData"} {
		if value, ok := values[alias]; ok {
			if present {
				return errors.New("task private_data update is ambiguous")
			}
			rawValue = value
			present = true
		}
		delete(values, alias)
	}
	if !present {
		return nil
	}
	switch value := rawValue.(type) {
	case nil:
		values["private_data"] = nil
	case TaskPrivateData:
		persisted, err := value.Value()
		if err != nil {
			return err
		}
		values["private_data"] = persisted
	case *TaskPrivateData:
		if value == nil {
			values["private_data"] = nil
			return nil
		}
		persisted, err := value.Value()
		if err != nil {
			return err
		}
		values["private_data"] = persisted
	case string:
		persisted, _, err := sealTaskPrivateDataJSON(value)
		if err != nil {
			return err
		}
		values["private_data"] = persisted
	case []byte:
		persisted, _, err := sealTaskPrivateDataJSON(string(value))
		if err != nil {
			return err
		}
		values["private_data"] = persisted
	default:
		return errors.New("task private_data must be TaskPrivateData or JSON")
	}
	return nil
}

// SyncTaskQueryParams 用于包含所有搜索条件的结构体，可以根据需求添加更多字段
type SyncTaskQueryParams struct {
	Platform       constant.TaskPlatform
	ChannelID      string
	TaskID         string
	UserID         string
	Action         string
	Status         string
	StartTimestamp int64
	EndTimestamp   int64
	UserIDs        []int
}

func InitTask(platform constant.TaskPlatform, relayInfo *commonRelay.RelayInfo) *Task {
	properties := Properties{}
	privateData := TaskPrivateData{}
	if relayInfo != nil && relayInfo.ChannelMeta != nil {
		if relayInfo.ChannelMeta.ChannelType == constant.ChannelTypeGemini ||
			relayInfo.ChannelMeta.ChannelType == constant.ChannelTypeVertexAi {
			privateData.Key = relayInfo.ChannelMeta.ApiKey
		}
		if relayInfo.UpstreamModelName != "" {
			properties.UpstreamModelName = relayInfo.UpstreamModelName
		}
		if relayInfo.OriginModelName != "" {
			properties.OriginModelName = relayInfo.OriginModelName
		}
	}

	// 使用预生成的公开 ID（如果有），否则新生成
	taskID := ""
	if relayInfo.TaskRelayInfo != nil && relayInfo.TaskRelayInfo.PublicTaskID != "" {
		taskID = relayInfo.TaskRelayInfo.PublicTaskID
	} else {
		taskID = GenerateTaskID()
	}

	t := &Task{
		TaskID:      taskID,
		UserId:      relayInfo.UserId,
		Group:       relayInfo.UsingGroup,
		SubmitTime:  time.Now().Unix(),
		Status:      TaskStatusNotStart,
		Progress:    "0%",
		ChannelId:   relayInfo.ChannelId,
		Platform:    platform,
		Properties:  properties,
		PrivateData: privateData,
	}
	return t
}

func TaskGetAllUserTask(userId int, startIdx int, num int, queryParams SyncTaskQueryParams) []*Task {
	var tasks []*Task
	var err error

	// 初始化查询构建器
	query := DB.Where("user_id = ?", userId)

	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.StartTimestamp != 0 {
		// 假设您已将前端传来的时间戳转换为数据库所需的时间格式，并处理了时间戳的验证和解析
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Omit("channel_id").Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func TaskGetAllTasks(startIdx int, num int, queryParams SyncTaskQueryParams) []*Task {
	var tasks []*Task
	var err error

	// 初始化查询构建器
	query := DB

	// 添加过滤条件
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.UserID != "" {
		query = query.Where("user_id = ?", queryParams.UserID)
	}
	if len(queryParams.UserIDs) != 0 {
		query = query.Where("user_id in (?)", queryParams.UserIDs)
	}
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}

	// 获取数据
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&tasks).Error
	if err != nil {
		return nil
	}

	return tasks
}

func GetTimedOutUnfinishedTasksWithError(cutoffUnix int64, limit int) ([]*Task, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var tasks []*Task
	err := DB.Where("(status IS NULL OR status = '' OR status NOT IN ?)", []string{TaskStatusFailure, TaskStatusSuccess}).
		Where("submit_time < ?", cutoffUnix).
		Order("submit_time").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetTimedOutUnfinishedTasks is retained for legacy callers. New worker code
// should use the error-returning variant so a database outage is observable.
func GetTimedOutUnfinishedTasks(cutoffUnix int64, limit int) []*Task {
	tasks, _ := GetTimedOutUnfinishedTasksWithError(cutoffUnix, limit)
	return tasks
}

// GetTerminalTasksWithPendingQuota returns modern failed tasks whose durable
// quota marker still needs a refund. The submit-time cutoff is the rollout
// fence; finish_time is intentionally not required because older versions
// could persist FAILURE rows without it. Successful tasks intentionally do not
// appear here: their non-zero quota is the normal record of the final charge,
// and there is no safe way to infer a failed settlement from that value alone.
// The query only returns pending/retryable/legacy-empty states and honors the per-row
// lease. Legacy processing/manual/complete rows are never automatically
// replayed; modern rows with a stable BillingRequestId may reclaim an expired
// processing lease because BillingOperation fences the actual refund.
func GetTerminalTasksWithPendingQuotaWithError(cutoffUnix, nowUnix int64, limit int) ([]*Task, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if limit <= 0 {
		limit = TaskBillingReconcileBatchLimit
	}
	if limit > TaskBillingReconcileBatchLimit {
		limit = TaskBillingReconcileBatchLimit
	}
	var tasks []*Task
	err := DB.Where("status = ?", TaskStatusFailure).
		Where("quota > ?", 0).
		// A zero submit_time is malformed modern data, not proof that the task
		// predates the refund rollout. Terminal transition code already treats it
		// as modern; the reconciliation query must use the same boundary or such a
		// task remains charged forever after an initial refund failure.
		Where("(submit_time <= 0 OR submit_time >= ?)", cutoffUnix).
		Where("((billing_reconcile_state = '' OR billing_reconcile_state IS NULL OR billing_reconcile_state IN ?) OR (billing_reconcile_state = ? AND TRIM(billing_request_id) <> '' AND (billing_reconcile_until = 0 OR billing_reconcile_until IS NULL OR billing_reconcile_until <= ?)))", []TaskBillingReconcileState{TaskBillingReconcilePending, TaskBillingReconcileRetryable}, TaskBillingReconcileProcessing, nowUnix).
		Where("(billing_reconcile_until = 0 OR billing_reconcile_until IS NULL OR billing_reconcile_until <= ?)", nowUnix).
		Order("id").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

func GetTerminalTasksWithPendingQuota(cutoffUnix, nowUnix int64, limit int) []*Task {
	tasks, _ := GetTerminalTasksWithPendingQuotaWithError(cutoffUnix, nowUnix, limit)
	return tasks
}

// HasTerminalTasksWithPendingQuota is a cheap existence check used by the
// scheduler. It mirrors GetTerminalTasksWithPendingQuota's safety filters.
func HasTerminalTasksWithPendingQuotaWithError(cutoffUnix, nowUnix int64) (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	var id int64
	err := DB.Model(&Task{}).
		Where("status = ?", TaskStatusFailure).
		Where("quota > ?", 0).
		Where("(submit_time <= 0 OR submit_time >= ?)", cutoffUnix).
		Where("((billing_reconcile_state = '' OR billing_reconcile_state IS NULL OR billing_reconcile_state IN ?) OR (billing_reconcile_state = ? AND TRIM(billing_request_id) <> '' AND (billing_reconcile_until = 0 OR billing_reconcile_until IS NULL OR billing_reconcile_until <= ?)))", []TaskBillingReconcileState{TaskBillingReconcilePending, TaskBillingReconcileRetryable}, TaskBillingReconcileProcessing, nowUnix).
		Where("(billing_reconcile_until = 0 OR billing_reconcile_until IS NULL OR billing_reconcile_until <= ?)", nowUnix).
		Limit(1).
		Pluck("id", &id).Error
	if err != nil {
		return false, err
	}
	return id != 0, nil
}

// HasTerminalTasksWithPendingQuota is the compatibility wrapper. Scheduler
// code should use HasTerminalTasksWithPendingQuotaWithError.
func HasTerminalTasksWithPendingQuota(cutoffUnix, nowUnix int64) bool {
	pending, _ := HasTerminalTasksWithPendingQuotaWithError(cutoffUnix, nowUnix)
	return pending
}

// ClaimBillingReconciliation atomically acquires a short-lived lease for a
// failed task. The task ID, status, quota and current lease are all part of
// the compare-and-swap predicate; a stale poller therefore cannot claim a
// task after another worker has refunded or changed it.
func (t *Task) ClaimBillingReconciliation(nowUnix, leaseUntilUnix int64) (bool, error) {
	if t == nil || t.ID <= 0 {
		return false, errors.New("task id is required for billing reconciliation")
	}
	if t.Quota <= 0 {
		return false, nil
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	if leaseUntilUnix <= nowUnix {
		return false, errors.New("billing reconciliation lease must be in the future")
	}
	result := DB.Model(&Task{}).
		Where("id = ? AND status = ? AND quota = ?", t.ID, TaskStatusFailure, t.Quota).
		Where("((billing_reconcile_state = '' OR billing_reconcile_state IS NULL OR billing_reconcile_state IN ?) OR (billing_reconcile_state = ? AND TRIM(billing_request_id) <> '' AND (billing_reconcile_until = 0 OR billing_reconcile_until IS NULL OR billing_reconcile_until <= ?)))", []TaskBillingReconcileState{TaskBillingReconcilePending, TaskBillingReconcileRetryable}, TaskBillingReconcileProcessing, nowUnix).
		Where("(billing_reconcile_until = 0 OR billing_reconcile_until IS NULL OR billing_reconcile_until <= ?)", nowUnix).
		Updates(map[string]interface{}{
			"billing_reconcile_until": leaseUntilUnix,
			"billing_reconcile_state": TaskBillingReconcileProcessing,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	// Keep the in-memory copy coherent for callers that inspect it or pass it
	// through another guarded update in the same reconciliation pass.
	t.BillingReconcileUntil = leaseUntilUnix
	t.BillingReconcileState = TaskBillingReconcileProcessing
	return true, nil
}

// MarkBillingReconcileManual fences a task after a strict reconciliation
// attempt encountered an uncertain side effect or could not persist the final
// quota marker. The state transition itself is conditional so a later manual
// repair cannot overwrite a task that was already completed by another actor.
func (t *Task) MarkBillingReconcileManual() error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for billing reconciliation")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	result := DB.Model(&Task{}).
		Where("id = ? AND billing_reconcile_state = ?", t.ID, TaskBillingReconcileProcessing).
		Updates(map[string]interface{}{
			"billing_reconcile_state": TaskBillingReconcileManual,
			"billing_reconcile_until": 0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	t.BillingReconcileState = TaskBillingReconcileManual
	t.BillingReconcileUntil = 0
	return nil
}

// MarkBillingReconcileRetryable releases a claimed reconciliation after the
// caller has proved that all ledger writes were compensated. This is a
// deliberately narrow transition: callers must never use it for an unknown
// database/network outcome, because replaying a non-idempotent refund could
// credit a wallet twice.
func (t *Task) MarkBillingReconcileRetryable() error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for billing reconciliation")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	result := DB.Model(&Task{}).
		Where("id = ? AND billing_reconcile_state = ?", t.ID, TaskBillingReconcileProcessing).
		Updates(map[string]interface{}{
			"billing_reconcile_state": TaskBillingReconcileRetryable,
			"billing_reconcile_until": 0,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	t.BillingReconcileState = TaskBillingReconcileRetryable
	t.BillingReconcileUntil = 0
	return nil
}

func GetAllUnFinishSyncTasksWithError(limit int) ([]*Task, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var tasks []*Task
	// Status is the authoritative lifecycle marker. Providers can report 100%
	// progress before they report a terminal status, so progress must not hide a
	// task from polling or timeout recovery.
	// Tasks inserted after an upstream submit remain hidden until their final
	// billing delta is durably committed.  Otherwise the provider poller could
	// advance a task (and even mark it successful) while the original request's
	// settlement is still pending.
	err := DB.Where("(status IS NULL OR status = '' OR status NOT IN ?)", []string{TaskStatusFailure, TaskStatusSuccess}).
		Where("(billing_settlement_state = '' OR billing_settlement_state IS NULL OR billing_settlement_state = ?)", TaskBillingSettlementComplete).
		Limit(limit).Order("last_polled_at, id").Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return tasks, nil
	}
	ids := make([]int64, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	pollStamp := time.Now().UnixNano()
	if err := DB.Model(&Task{}).Where("id IN ?", ids).UpdateColumn("last_polled_at", pollStamp).Error; err != nil {
		return nil, err
	}
	for _, task := range tasks {
		task.LastPolledAt = pollStamp
	}
	return tasks, nil
}

// GetAllUnFinishSyncTasks is retained for legacy callers. New polling code
// should use GetAllUnFinishSyncTasksWithError.
func GetAllUnFinishSyncTasks(limit int) []*Task {
	tasks, _ := GetAllUnFinishSyncTasksWithError(limit)
	return tasks
}

// HasUnfinishedSyncTasks reports whether at least one async (Suno/video) task is
// still in progress. It is a cheap existence check (LIMIT 1) used to decide
// whether the async_task_poll system task needs to run; when no task is pending
// the scheduler skips creating a row entirely.
func HasUnfinishedSyncTasksWithError() (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	var id int64
	err := DB.Model(&Task{}).
		Where("(status IS NULL OR status = '' OR status NOT IN ?)", []string{TaskStatusFailure, TaskStatusSuccess}).
		Where("(billing_settlement_state = '' OR billing_settlement_state IS NULL OR billing_settlement_state = ?)", TaskBillingSettlementComplete).
		Limit(1).
		Pluck("id", &id).Error
	if err != nil {
		return false, err
	}
	return id != 0, nil
}

// HasUnfinishedSyncTasks is the compatibility wrapper.
func HasUnfinishedSyncTasks() bool {
	pending, _ := HasUnfinishedSyncTasksWithError()
	return pending
}

func GetByTaskId(userId int, taskId string) (*Task, bool, error) {
	if taskId == "" {
		return nil, false, nil
	}
	var task *Task
	var err error
	err = DB.Where("user_id = ? and task_id = ?", userId, taskId).
		First(&task).Error
	exist, err := RecordExist(err)
	if err != nil {
		return nil, false, err
	}
	return task, exist, err
}

func GetByTaskIds(userId int, taskIds []any) ([]*Task, error) {
	if len(taskIds) == 0 {
		return nil, nil
	}
	var task []*Task
	var err error
	err = DB.Where("user_id = ? and task_id in (?)", userId, taskIds).
		Find(&task).Error
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (Task *Task) Insert() error {
	var err error
	err = DB.Create(Task).Error
	return err
}

type taskSnapshot struct {
	Status     TaskStatus
	Progress   string
	StartTime  int64
	FinishTime int64
	FailReason string
	ResultURL  string
	Data       json.RawMessage
}

func (s taskSnapshot) Equal(other taskSnapshot) bool {
	return s.Status == other.Status &&
		s.Progress == other.Progress &&
		s.StartTime == other.StartTime &&
		s.FinishTime == other.FinishTime &&
		s.FailReason == other.FailReason &&
		s.ResultURL == other.ResultURL &&
		bytes.Equal(s.Data, other.Data)
}

func (t *Task) Snapshot() taskSnapshot {
	return taskSnapshot{
		Status:     t.Status,
		Progress:   t.Progress,
		StartTime:  t.StartTime,
		FinishTime: t.FinishTime,
		FailReason: t.FailReason,
		ResultURL:  t.PrivateData.ResultURL,
		Data:       t.Data,
	}
}

func (Task *Task) Update() error {
	var err error
	err = DB.Save(Task).Error
	return err
}

func (t *Task) UpdateQuota() error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for quota update")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	// Keep the write narrowly scoped to the task identity. Passing a struct
	// with a zero primary key to GORM's Model().Update can otherwise result in
	// an unqualified UPDATE (or a global-update guard error).
	updates := map[string]interface{}{"quota": t.Quota}
	if t.Quota == 0 {
		// A successful refund no longer needs a retry lease. Clearing it in the
		// same UPDATE as the durable marker avoids leaving stale lease metadata.
		updates["billing_reconcile_until"] = 0
		updates["billing_reconcile_state"] = TaskBillingReconcileComplete
	}
	result := DB.Model(&Task{}).Where("id = ?", t.ID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateWithStatus performs a conditional UPDATE guarded by fromStatus (CAS).
// Returns (true, nil) if this caller won the update, (false, nil) if
// another process already moved the task out of fromStatus. MySQL commonly
// reports changed rows rather than matched rows, so a same-value no-op update
// can also return false even when the status predicate still matched.
//
// Uses Model().Select("*").Updates() instead of Save() because GORM's Save
// falls back to INSERT ON CONFLICT when the WHERE-guarded UPDATE matches
// zero rows, which silently bypasses the CAS guard.
//
// expectedQuota is optional for backwards compatibility.  By default the
// quota currently held by t is both the value written and the value expected
// in the database.  Callers that intentionally change t.Quota before this
// CAS (for example, the legacy timeout path which clears quota) must pass the
// previously observed database value so the CAS still fences a concurrent
// settlement/refund worker.
func (t *Task) UpdateWithStatus(fromStatus TaskStatus, expectedQuota ...int) (bool, error) {
	if t == nil || t.ID <= 0 {
		return false, errors.New("task id is required for status update")
	}
	if len(expectedQuota) > 1 {
		return false, errors.New("expected quota accepts at most one value")
	}
	// Treat a delayed/regressive provider observation as a lost CAS before
	// constructing the UPDATE. This protects every caller (including legacy
	// reconciliation paths) even if it forgets to perform the explicit fence.
	if IsTaskStatusStale(fromStatus, t.Status) {
		return false, nil
	}
	identityCAS, identityOK, identityErr := prepareTaskIdentityCAS(t)
	if identityErr != nil {
		// A task disappearing between the poll query and this CAS is an
		// ordinary lost update. Preserve the historical (false, nil) contract
		// instead of turning it into a retryable billing error.
		if errors.Is(identityErr, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, identityErr
	}
	if !identityOK {
		// The provider identity in memory no longer agrees with the durable
		// identity projection. Treat this exactly like any other CAS loss: do
		// not write status/metadata and never let the caller perform billing.
		return false, nil
	}
	observedQuota := t.Quota
	if len(expectedQuota) == 1 {
		observedQuota = expectedQuota[0]
	}
	// Do not pass the live struct to GORM's Updates callback.  GORM may mutate
	// struct fields while building assignments (notably timestamps and custom
	// valuers), which races with the polling goroutine that owns this snapshot.
	// An explicit map also prevents stale immutable fields (user/channel/task
	// identity) from being overwritten by a concurrent poller.
	updates := map[string]interface{}{
		"start_time":  t.StartTime,
		"finish_time": t.FinishTime,
		"status":      t.Status,
		"progress":    t.Progress,
		"fail_reason": t.FailReason,
		"quota":       t.Quota,
		// Use the identity-checked/merged value. Besides rejecting provider-ID
		// replacement, this keeps partial task projections from erasing the
		// provider key, billing context, or result URL.
		"private_data": identityCAS.privateData,
		"data":         t.Data,
	}
	if identityCAS.setDigest != "" {
		// Opportunistically backfill a legacy NULL/empty identity while the row
		// is already fenced. A uniqueness conflict is returned by the UPDATE and
		// must be reconciled rather than silently selecting a task.
		updates["upstream_task_identity"] = identityCAS.setDigest
	}
	// Billing markers are part of the status CAS, not ordinary payload fields.
	// A stale poller must not overwrite a refund/settlement state (or its quota)
	// after a concurrent worker has committed a financial operation. Include the
	// marker values in the assignment only when this caller explicitly carries
	// them; the predicates below still fence empty/NULL legacy states.
	if t.BillingReconcileState != "" {
		updates["billing_reconcile_state"] = t.BillingReconcileState
	}
	// A terminal success may carry an immutable billing-adjustment target. Only
	// include these fields when the caller has actually entered that state;
	// stale legacy objects must not erase a pending/processing marker written by
	// another worker.
	if t.BillingAdjustmentState != "" || t.BillingFinalQuota != 0 || t.BillingAdjustmentUsageRecorded {
		updates["billing_final_quota"] = t.BillingFinalQuota
		updates["billing_adjustment_reason"] = t.BillingAdjustmentReason
		updates["billing_adjustment_state"] = t.BillingAdjustmentState
		updates["billing_adjustment_until"] = t.BillingAdjustmentUntil
		updates["billing_adjustment_error"] = t.BillingAdjustmentError
		updates["billing_adjustment_usage_recorded"] = t.BillingAdjustmentUsageRecorded
	}
	query := DB.Model(&Task{}).
		Where("id = ? AND status = ?", t.ID, fromStatus).
		// Quota is the durable financial marker for terminal refunds and the
		// current submit amount for settlement. Requiring the value observed by
		// this caller prevents a delayed poll response from writing an old quota
		// over a concurrent settlement/refund. A caller that deliberately changes
		// the value before this CAS supplies the old value through expectedQuota.
		Where("quota = ?", observedQuota)
	if identityCAS.expectedNull {
		query = query.Where("(upstream_task_identity IS NULL OR TRIM(upstream_task_identity) = '')")
	} else {
		query = query.Where("TRIM(upstream_task_identity) = ?", identityCAS.expectedDigest)
	}
	// Fence every billing state, including the empty/NULL legacy state. Without
	// the explicit empty predicate a stale object loaded before a worker wrote a
	// pending marker could still move the task status and erase the recovery
	// signal. A transition from an empty legacy state to the initial pending
	// state is allowed; processing/complete/manual states always require an
	// exact match so a stale poller cannot move a lease backwards.
	if t.BillingReconcileState == "" {
		query = query.Where("(billing_reconcile_state = '' OR billing_reconcile_state IS NULL)")
	} else if t.BillingReconcileState == TaskBillingReconcilePending {
		query = query.Where("(billing_reconcile_state = '' OR billing_reconcile_state IS NULL OR billing_reconcile_state = ?)", TaskBillingReconcilePending)
	} else {
		query = query.Where("billing_reconcile_state = ?", t.BillingReconcileState)
	}
	if t.BillingSettlementState == "" {
		query = query.Where("(billing_settlement_state = '' OR billing_settlement_state IS NULL)")
	} else if t.BillingSettlementState == TaskBillingSettlementPending {
		query = query.Where("(billing_settlement_state = '' OR billing_settlement_state IS NULL OR billing_settlement_state = ?)", TaskBillingSettlementPending)
	} else {
		query = query.Where("billing_settlement_state = ?", t.BillingSettlementState)
	}
	if t.BillingAdjustmentState == "" {
		query = query.Where("(billing_adjustment_state = '' OR billing_adjustment_state IS NULL)")
	} else if t.BillingAdjustmentState == TaskBillingAdjustmentPending || t.BillingAdjustmentState == TaskBillingAdjustmentManual {
		// Pending is the normal initial terminal target; manual is the fail-closed
		// initial target when the provider succeeded but its billing amount was
		// invalid. Both may be installed only over an empty legacy state (or their
		// own current state), never over another worker's processing/completed target.
		query = query.Where("(billing_adjustment_state = '' OR billing_adjustment_state IS NULL OR billing_adjustment_state = ?)", t.BillingAdjustmentState)
	} else {
		query = query.Where("billing_adjustment_state = ?", t.BillingAdjustmentState)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// TaskBulkUpdateByID performs an unconditional bulk UPDATE by primary key IDs.
// WARNING: This function has NO CAS (Compare-And-Swap) guard — it will overwrite
// any concurrent status changes. DO NOT use in billing/quota lifecycle flows
// (e.g., timeout, success, failure transitions that trigger refunds or settlements).
// For status transitions that involve billing, use Task.UpdateWithStatus() instead.
func TaskBulkUpdateByID(ids []int64, params map[string]any) error {
	if len(ids) == 0 {
		return nil
	}
	return DB.Model(&Task{}).
		Where("id in (?)", ids).
		Updates(params).Error
}

type TaskQuotaUsage struct {
	Mode  string  `json:"mode"`
	Count float64 `json:"count"`
}

// TaskCountAllTasks returns total tasks that match the given query params (admin usage).
// Database errors are returned to the caller instead of being silently treated as zero.
func TaskCountAllTasks(queryParams SyncTaskQueryParams) (int64, error) {
	var total int64
	query := DB.Model(&Task{})
	if queryParams.ChannelID != "" {
		query = query.Where("channel_id = ?", queryParams.ChannelID)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.UserID != "" {
		query = query.Where("user_id = ?", queryParams.UserID)
	}
	if len(queryParams.UserIDs) != 0 {
		query = query.Where("user_id in (?)", queryParams.UserIDs)
	}
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	if err := query.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// TaskCountAllUserTask returns total tasks for given user.
// Database errors are returned to the caller instead of being silently treated as zero.
func TaskCountAllUserTask(userId int, queryParams SyncTaskQueryParams) (int64, error) {
	var total int64
	query := DB.Model(&Task{}).Where("user_id = ?", userId)
	if queryParams.TaskID != "" {
		query = query.Where("task_id = ?", queryParams.TaskID)
	}
	if queryParams.Action != "" {
		query = query.Where("action = ?", queryParams.Action)
	}
	if queryParams.Status != "" {
		query = query.Where("status = ?", queryParams.Status)
	}
	if queryParams.Platform != "" {
		query = query.Where("platform = ?", queryParams.Platform)
	}
	if queryParams.StartTimestamp != 0 {
		query = query.Where("submit_time >= ?", queryParams.StartTimestamp)
	}
	if queryParams.EndTimestamp != 0 {
		query = query.Where("submit_time <= ?", queryParams.EndTimestamp)
	}
	if err := query.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}
func (t *Task) ToOpenAIVideo() *dto.OpenAIVideo {
	openAIVideo := dto.NewOpenAIVideo()
	openAIVideo.ID = t.TaskID
	openAIVideo.Status = t.Status.ToVideoStatus()
	openAIVideo.Model = t.Properties.OriginModelName
	openAIVideo.SetProgressStr(t.Progress)
	openAIVideo.CreatedAt = t.CreatedAt
	openAIVideo.CompletedAt = t.UpdatedAt
	openAIVideo.SetMetadata("url", t.GetResultURL())
	return openAIVideo
}
