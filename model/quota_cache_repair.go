package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// QuotaCacheRepair is a durable hint that a database-authoritative quota
// mutation could not be reflected in Redis.  Redis is only a performance
// cache; the repair row makes a transient outage or a lost async goroutine
// converge back to the database snapshot instead of serving a stale balance
// indefinitely.
type QuotaCacheRepair struct {
	ID            int64  `json:"id" gorm:"primaryKey"`
	EntityType    string `json:"entity_type" gorm:"type:varchar(16);not null;uniqueIndex:idx_quota_cache_repair_entity,priority:1"`
	EntityID      int    `json:"entity_id" gorm:"not null;uniqueIndex:idx_quota_cache_repair_entity,priority:2"`
	CacheKey      string `json:"cache_key" gorm:"type:varchar(255);not null"`
	MutationID    string `json:"mutation_id" gorm:"type:varchar(32);index"`
	Status        string `json:"status" gorm:"type:varchar(16);not null;index:idx_quota_cache_repair_due,priority:1"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt int64  `json:"next_attempt_at" gorm:"bigint;index:idx_quota_cache_repair_due,priority:2"`
	LockedBy      string `json:"locked_by" gorm:"type:varchar(128);index"`
	LockedUntil   int64  `json:"locked_until" gorm:"bigint;index"`
	LastError     string `json:"last_error" gorm:"type:text"`
	CreatedAt     int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt     int64  `json:"updated_at" gorm:"bigint"`
}

const (
	QuotaCacheRepairEntityUser  = "user"
	QuotaCacheRepairEntityToken = "token"
	quotaCacheRepairPending     = "pending"
	quotaCacheRepairProcessing  = "processing"
	quotaCacheRepairRetryable   = "retryable"
	quotaCacheRepairLease       = 60 * time.Second
	quotaCacheRepairMaxAttempts = 20
)

var (
	ErrQuotaCacheRepairInvalid   = errors.New("invalid quota cache repair")
	ErrQuotaCacheRepairLeaseLost = errors.New("quota cache repair lease lost")
)

func (QuotaCacheRepair) TableName() string { return "quota_cache_repairs" }

func upsertQuotaCacheRepair(tx *gorm.DB, entityType string, entityID int, cacheKey, mutationID string, cause error) error {
	entityType = strings.TrimSpace(entityType)
	cacheKey = strings.TrimSpace(cacheKey)
	mutationID = strings.TrimSpace(mutationID)
	if tx == nil || entityID <= 0 || cacheKey == "" || mutationID == "" ||
		(entityType != QuotaCacheRepairEntityUser && entityType != QuotaCacheRepairEntityToken) {
		return ErrQuotaCacheRepairInvalid
	}
	now := common.GetTimestamp()
	lastError := ""
	if cause != nil {
		lastError = strings.TrimSpace(cause.Error())
		if len(lastError) > 1024 {
			lastError = lastError[:1024]
		}
	}
	row := &QuotaCacheRepair{
		EntityType:    entityType,
		EntityID:      entityID,
		CacheKey:      cacheKey,
		MutationID:    mutationID,
		Status:        quotaCacheRepairPending,
		NextAttemptAt: now,
		LastError:     lastError,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "entity_type"}, {Name: "entity_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"cache_key":       cacheKey,
			"mutation_id":     mutationID,
			"status":          quotaCacheRepairPending,
			"attempts":        0,
			"next_attempt_at": now,
			"locked_by":       "",
			"locked_until":    0,
			"last_error":      lastError,
			"updated_at":      now,
		}),
	}).Create(row).Error
}

// stageQuotaCacheRepairTx records the post-commit cache work in the same
// transaction as its authoritative quota mutation. If the process exits after
// commit but before Redis is synchronized, another worker can still converge
// the cache. The unique mutation id prevents an older completion from deleting
// a newer concurrent repair intent for the same entity.
func stageQuotaCacheRepairTx(tx *gorm.DB, entityType string, entityID int, cacheKey string) (string, error) {
	if !common.RedisEnabled {
		return "", nil
	}
	mutationID := common.GetUUID()
	if err := upsertQuotaCacheRepair(tx, entityType, entityID, cacheKey, mutationID, ErrQuotaCacheMiss); err != nil {
		return "", err
	}
	return mutationID, nil
}

func completeStagedQuotaCacheRepair(entityType string, entityID int, cacheKey, mutationID string) error {
	if mutationID == "" || strings.TrimSpace(cacheKey) == "" || DB == nil {
		return nil
	}
	return DB.Where("entity_type = ? AND entity_id = ? AND cache_key = ? AND mutation_id = ? AND status IN ?",
		entityType, entityID, cacheKey, mutationID, []string{quotaCacheRepairPending, quotaCacheRepairRetryable}).
		Delete(&QuotaCacheRepair{}).Error
}

// enqueueQuotaCacheRepair is intentionally best effort at the call site: a
// database outage cannot be made worse by a missing cache hint, and all quota
// mutations remain database-authoritative. Once the database is available,
// the upsert coalesces repeated failures for the same entity.
func enqueueQuotaCacheRepair(entityType string, entityID int, cacheKey string, cause error) error {
	if DB == nil {
		return ErrQuotaCacheRepairInvalid
	}
	return upsertQuotaCacheRepair(DB, entityType, entityID, cacheKey, common.GetUUID(), cause)
}

// recordQuotaCacheRepair fences the cache immediately and persists a retry
// marker.  It is safe to call from asynchronous mutation paths.
func recordQuotaCacheRepair(entityType string, entityID int, cacheKey string, cause error) {
	if !common.RedisEnabled || entityID <= 0 || strings.TrimSpace(cacheKey) == "" {
		return
	}
	if err := common.RedisDelKey(cacheKey); err != nil {
		common.SysLog(fmt.Sprintf("failed to invalidate quota cache key=%s: %v", cacheKey, err))
	}
	if err := enqueueQuotaCacheRepair(entityType, entityID, cacheKey, cause); err != nil {
		common.SysLog(fmt.Sprintf("failed to enqueue quota cache repair type=%s id=%d: %v", entityType, entityID, err))
	}
}

func quotaCacheRepairBackoff(attempt int) int64 {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 10 {
		attempt = 10
	}
	seconds := int64(1) << (attempt - 1)
	if seconds > 3600 {
		seconds = 3600
	}
	return seconds
}

func claimQuotaCacheRepairs(workerID string, now, leaseUntil int64, limit int) ([]QuotaCacheRepair, error) {
	workerID = strings.TrimSpace(workerID)
	if DB == nil || workerID == "" || leaseUntil <= now {
		return nil, ErrQuotaCacheRepairInvalid
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var candidates []QuotaCacheRepair
	query := DB.Where("next_attempt_at <= ?", now).
		Where("((status IN ? AND (locked_until = 0 OR locked_until IS NULL OR locked_until <= ?)) OR (status = ? AND locked_until <= ?))",
			[]string{quotaCacheRepairPending, quotaCacheRepairRetryable}, now, quotaCacheRepairProcessing, now).
		Order("id ASC").Limit(limit * 4)
	if err := query.Find(&candidates).Error; err != nil {
		return nil, err
	}
	claimed := make([]QuotaCacheRepair, 0, limit)
	for _, candidate := range candidates {
		result := DB.Model(&QuotaCacheRepair{}).
			Where("id = ? AND next_attempt_at <= ?", candidate.ID, now).
			Where("((status IN ? AND (locked_until = 0 OR locked_until IS NULL OR locked_until <= ?)) OR (status = ? AND locked_until <= ?))",
				[]string{quotaCacheRepairPending, quotaCacheRepairRetryable}, now, quotaCacheRepairProcessing, now).
			Updates(map[string]interface{}{
				"status":       quotaCacheRepairProcessing,
				"locked_by":    workerID,
				"locked_until": leaseUntil,
				"updated_at":   now,
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		candidate.Status = quotaCacheRepairProcessing
		candidate.LockedBy = workerID
		candidate.LockedUntil = leaseUntil
		claimed = append(claimed, candidate)
		if len(claimed) == limit {
			break
		}
	}
	return claimed, nil
}

func completeQuotaCacheRepair(row QuotaCacheRepair, workerID string, now int64) error {
	result := DB.Where("id = ? AND status = ? AND locked_by = ?", row.ID, quotaCacheRepairProcessing, workerID).
		Delete(&QuotaCacheRepair{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrQuotaCacheRepairLeaseLost
	}
	return nil
}

func failQuotaCacheRepair(row QuotaCacheRepair, workerID string, now int64, cause error) error {
	attempts := row.Attempts + 1
	if attempts > quotaCacheRepairMaxAttempts {
		attempts = quotaCacheRepairMaxAttempts
	}
	next := now + quotaCacheRepairBackoff(attempts)
	lastError := ""
	if cause != nil {
		lastError = strings.TrimSpace(cause.Error())
		if len(lastError) > 1024 {
			lastError = lastError[:1024]
		}
	}
	result := DB.Model(&QuotaCacheRepair{}).
		Where("id = ? AND status = ? AND locked_by = ?", row.ID, quotaCacheRepairProcessing, workerID).
		Updates(map[string]interface{}{
			"status":          quotaCacheRepairRetryable,
			"attempts":        attempts,
			"next_attempt_at": next,
			"locked_by":       "",
			"locked_until":    0,
			"last_error":      lastError,
			"updated_at":      now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrQuotaCacheRepairLeaseLost
	}
	return nil
}

func countPendingQuotaCacheRepairs() (int, error) {
	if DB == nil {
		return 0, ErrQuotaCacheRepairInvalid
	}
	var count int64
	result := DB.Model(&QuotaCacheRepair{}).
		Where("status IN ?", []string{quotaCacheRepairPending, quotaCacheRepairRetryable, quotaCacheRepairProcessing}).
		Count(&count)
	if result.Error != nil {
		return 0, result.Error
	}
	return int(count), nil
}

func repairQuotaCacheRow(row QuotaCacheRepair) error {
	if !common.RedisEnabled {
		return nil
	}
	switch row.EntityType {
	case QuotaCacheRepairEntityUser:
		if err := common.RedisDelKey(row.CacheKey); err != nil {
			return err
		}
		// Republish the committed snapshot after fencing.  This closes the race
		// where a stale reader observes the deletion and initializes the hash just
		// before the repair worker finishes.  Auth-version fencing in writeUserCache
		// rejects the snapshot when a restrictive user update is still pending.
		user, err := GetUserById(row.EntityID, false)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// A hard-deleted user has no current snapshot to republish. The old
			// cache key was already fenced above, so treating this as a successful
			// repair lets the durable marker converge instead of retrying forever.
			return nil
		}
		if err != nil {
			return err
		}
		return populateUserCache(*user)
	case QuotaCacheRepairEntityToken:
		var token Token
		err := DB.Unscoped().Select("id", "key", "key_ciphertext", "key_hash").Where("id = ?", row.EntityID).First(&token).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The token was deleted; removing the old cache key is the only safe
			// outcome and the durable row can be discarded.
			return common.RedisDelKey(row.CacheKey)
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(token.Key) == "" {
			return fmt.Errorf("token %d has empty key", row.EntityID)
		}
		if err := invalidateTokenCacheForMutation(token.Key); err != nil {
			return err
		}
		if current := getTokenCacheKey(token.Key); current != row.CacheKey {
			if err := common.RedisDelKey(row.CacheKey); err != nil {
				return err
			}
		}
		return nil
	default:
		return ErrQuotaCacheRepairInvalid
	}
}

// ProcessQuotaCacheRepairs runs one bounded repair pass.  It is exported so
// operators and tests can trigger convergence immediately; the production
// worker invokes it periodically.
func ProcessQuotaCacheRepairs(ctx context.Context, limit int) (processed, pending int) {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil || !common.RedisEnabled || common.RDB == nil {
		return 0, 0
	}
	now := common.GetTimestamp()
	workerID := fmt.Sprintf("quota-cache-%d", time.Now().UnixNano())
	rows, err := claimQuotaCacheRepairs(workerID, now, now+int64(quotaCacheRepairLease/time.Second), limit)
	if err != nil {
		common.SysLog("failed to claim quota cache repairs: " + err.Error())
		return 0, 0
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		processed++
		if err := repairQuotaCacheRow(row); err != nil {
			if updateErr := failQuotaCacheRepair(row, workerID, common.GetTimestamp(), err); updateErr != nil {
				common.SysLog(fmt.Sprintf("failed to reschedule quota cache repair id=%d: %v", row.ID, updateErr))
			}
			continue
		}
		if err := completeQuotaCacheRepair(row, workerID, common.GetTimestamp()); err != nil {
			common.SysLog(fmt.Sprintf("failed to complete quota cache repair id=%d: %v", row.ID, err))
		}
	}
	pending, err = countPendingQuotaCacheRepairs()
	if err != nil {
		// The worker API predates error returns and intentionally remains a
		// two-counter compatibility surface. Do not silently report a healthy
		// zero backlog when the database cannot answer the count query.
		common.SysLog("failed to count quota cache repairs: " + err.Error())
		return processed, 0
	}
	return processed, pending
}

// StartQuotaCacheRepairWorker starts the bounded convergence loop.  It is
// intentionally independent of BATCH_UPDATE_ENABLED because normal (non-batch)
// quota mutations also update Redis asynchronously.
func StartQuotaCacheRepairWorker(frequency int) {
	if frequency <= 0 {
		frequency = 60
	}
	go func() {
		ticker := time.NewTicker(time.Duration(frequency) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ProcessQuotaCacheRepairs(context.Background(), 100)
		}
	}()
}
