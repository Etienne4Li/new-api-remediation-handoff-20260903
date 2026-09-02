package model

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// ErrQuotaCacheMiss is returned when a guarded Redis quota mutation could not
// find a complete, current cache hash. Callers must invalidate/enqueue a
// durable repair rather than treating the operation as successfully reflected
// in Redis.
var ErrQuotaCacheMiss = errors.New("quota cache entry is missing or stale")

// ErrQuotaCacheUnexpectedResult means a Redis quota script returned a value
// outside its documented {-1,0,1} contract. Treat it like an ambiguous script
// execution: the cache must be invalidated successfully before any database
// fallback is allowed.
var ErrQuotaCacheUnexpectedResult = errors.New("quota cache script returned an unexpected result")

type cacheQuotaResult int

const (
	cacheQuotaInsufficient cacheQuotaResult = iota
	cacheQuotaOK
	cacheQuotaMiss
)

const userQuotaReserveScript = `
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or tonumber(redis.call('HGET', KEYS[1], 'CacheSchema') or '0') ~= tonumber(ARGV[3])
  or redis.call('HEXISTS', KEYS[1], 'Quota') == 0 then
  return -1
end
local quota = tonumber(redis.call('HGET', KEYS[1], 'Quota'))
if quota == nil or quota < tonumber(ARGV[1]) then
  return 0
end
redis.call('HINCRBY', KEYS[1], 'Quota', -tonumber(ARGV[1]))
return 1`

const userQuotaDeltaScript = `
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or tonumber(redis.call('HGET', KEYS[1], 'CacheSchema') or '0') ~= tonumber(ARGV[3])
  or redis.call('HEXISTS', KEYS[1], 'Quota') == 0 then
  return -1
end
redis.call('HINCRBY', KEYS[1], 'Quota', tonumber(ARGV[1]))
return 1`

const tokenQuotaReserveScript = `
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or redis.call('HEXISTS', KEYS[1], 'RemainQuota') == 0
  or redis.call('HEXISTS', KEYS[1], 'UsedQuota') == 0 then
  return -1
end
local remain = tonumber(redis.call('HGET', KEYS[1], 'RemainQuota'))
if remain == nil or remain < tonumber(ARGV[1]) then
  return 0
end
redis.call('HINCRBY', KEYS[1], 'RemainQuota', -tonumber(ARGV[1]))
redis.call('HINCRBY', KEYS[1], 'UsedQuota', tonumber(ARGV[1]))
redis.call('HSET', KEYS[1], 'AccessedTime', ARGV[3])
return 1`

const tokenQuotaDeltaScript = `
if tonumber(redis.call('HGET', KEYS[1], 'Id') or '0') ~= tonumber(ARGV[2])
  or redis.call('HEXISTS', KEYS[1], 'RemainQuota') == 0
  or redis.call('HEXISTS', KEYS[1], 'UsedQuota') == 0 then
  return -1
end
redis.call('HINCRBY', KEYS[1], 'RemainQuota', tonumber(ARGV[1]))
redis.call('HINCRBY', KEYS[1], 'UsedQuota', -tonumber(ARGV[1]))
redis.call('HSET', KEYS[1], 'AccessedTime', ARGV[3])
return 1`

func quotaResultFromLua(result int, err error) (cacheQuotaResult, error) {
	if err != nil {
		return cacheQuotaMiss, err
	}
	switch result {
	case 1:
		return cacheQuotaOK, nil
	case 0:
		return cacheQuotaInsufficient, nil
	case -1:
		// -1 is the documented, non-ambiguous cache-miss result.  Keep the
		// error nil so reserve callers can hydrate the hash from the database
		// and retry the Lua operation; mutation callers will translate the
		// miss into ErrQuotaCacheMiss without falling back to a second ledger
		// mutation.
		return cacheQuotaMiss, nil
	default:
		return cacheQuotaMiss, fmt.Errorf("%w: %d", ErrQuotaCacheUnexpectedResult, result)
	}
}

func cacheTryReserveUserQuota(userID int, amount int64) (cacheQuotaResult, error) {
	client, err := modelRedisClient()
	if err != nil {
		return cacheQuotaMiss, err
	}
	result, err := client.Eval(context.Background(), userQuotaReserveScript,
		[]string{getUserCacheKey(userID)}, amount, userID, userCacheSchemaVersion).Int()
	return quotaResultFromLua(result, err)
}

func cacheApplyUserQuotaDelta(userID int, delta int64) (cacheQuotaResult, error) {
	client, err := modelRedisClient()
	if err != nil {
		return cacheQuotaMiss, err
	}
	result, err := client.Eval(context.Background(), userQuotaDeltaScript,
		[]string{getUserCacheKey(userID)}, delta, userID, userCacheSchemaVersion).Int()
	return quotaResultFromLua(result, err)
}

func cacheTryReserveTokenQuota(id int, key string, amount int64) (cacheQuotaResult, error) {
	client, err := modelRedisClient()
	if err != nil {
		return cacheQuotaMiss, err
	}
	result, err := client.Eval(context.Background(), tokenQuotaReserveScript,
		[]string{getTokenCacheKey(key)}, amount, id, common.GetTimestamp()).Int()
	return quotaResultFromLua(result, err)
}

func cacheApplyTokenQuotaDelta(id int, key string, delta int64) (cacheQuotaResult, error) {
	client, err := modelRedisClient()
	if err != nil {
		return cacheQuotaMiss, err
	}
	result, err := client.Eval(context.Background(), tokenQuotaDeltaScript,
		[]string{getTokenCacheKey(key)}, delta, id, common.GetTimestamp()).Int()
	return quotaResultFromLua(result, err)
}

func invalidateUserQuotaCacheForReserveFallback(userID int) error {
	if !common.RedisEnabled {
		return nil
	}
	return common.RedisDelKey(getUserCacheKey(userID))
}

func invalidateTokenCacheForReserveFallback(tokenID int, key string) error {
	if tokenID <= 0 || key == "" || !common.RedisEnabled {
		return nil
	}
	return invalidateTokenCacheForMutation(key)
}

// persistUserQuotaDelta 把已在缓存侧预扣成功的增量落库。
//
// 额度是资金账本，不能放进 BatchUpdateEnabled 使用的进程内聚合队列：
// Redis 预扣成功后如果进程在批量刷库前退出，队列中的扣减会丢失，下一次
// 缓存水合便会把旧余额重新发布并允许超额消费。批量模式只适用于统计字段
// （used_quota/request_count/channel usage）；这里始终直接写数据库，并在
// 失败时由调用方把 Redis 预扣补偿回来。
func persistUserQuotaDelta(id int, delta int) (bool, error) {
	// Redis is a fast-path reservation ledger, but it can be stale after a
	// direct DB mutation or an interrupted cache repair.  Keep a durable
	// non-negative predicate for negative deltas so a high stale cache can
	// never turn the wallet row negative.
	query := DB.Model(&User{}).Where("id = ?", id)
	if delta < 0 {
		query = query.Where("quota >= ?", -delta)
	}
	result := query.Update("quota", gorm.Expr("quota + ?", delta))
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		var count int64
		if err := DB.Model(&User{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return false, err
		}
		if count == 0 {
			return false, gorm.ErrRecordNotFound
		}
		return false, nil
	}
	return true, nil
}

func persistTokenQuotaDelta(id int, delta int) (bool, error) {
	query := DB.Model(&Token{}).Where("id = ?", id)
	if delta < 0 {
		query = query.Where("remain_quota >= ?", -delta)
	}
	result := query.Updates(
		map[string]interface{}{
			"remain_quota":  gorm.Expr("remain_quota + ?", delta),
			"used_quota":    gorm.Expr("used_quota - ?", delta),
			"accessed_time": common.GetTimestamp(),
		},
	)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		var count int64
		if err := DB.Model(&Token{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return false, err
		}
		if count == 0 {
			return false, gorm.ErrRecordNotFound
		}
		return false, nil
	}
	return true, nil
}

func reserveUserQuotaDB(id int, quota int) (bool, error) {
	reserved := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&User{}).
			Where("id = ? AND quota >= ?", id, quota).
			Update("quota", gorm.Expr("quota - ?", quota))
		if result.Error != nil {
			return result.Error
		}
		reserved = result.RowsAffected == 1
		if !reserved {
			return nil
		}
		_, err := stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityUser, id, getUserCacheKey(id))
		return err
	})
	return reserved, err
}

func reserveTokenQuotaDB(id int, quota int) (bool, error) {
	reserved := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Token{}).
			Where("id = ? AND remain_quota >= ?", id, quota).
			Updates(map[string]interface{}{
				"remain_quota":  gorm.Expr("remain_quota - ?", quota),
				"used_quota":    gorm.Expr("used_quota + ?", quota),
				"accessed_time": common.GetTimestamp(),
			})
		if result.Error != nil {
			return result.Error
		}
		reserved = result.RowsAffected == 1
		if !reserved || !common.RedisEnabled {
			return nil
		}
		var token Token
		if err := tx.Select("id", mainKeyColumn(tx), "key_ciphertext", "key_hash").Where("id = ?", id).First(&token).Error; err != nil {
			return err
		}
		_, err := stageQuotaCacheRepairTx(tx, QuotaCacheRepairEntityToken, id, getTokenCacheKey(token.Key))
		return err
	})
	return reserved, err
}

// reserveUserQuotaInBatchMode deliberately uses the database as the sole
// reservation authority.  The normal Redis-first path has an unavoidable
// crash point between the successful Lua decrement and persistence of the
// delta; putting the delta in the process-local batch map makes that point
// permanent after a restart.  Batch mode therefore trades the cache fast path
// for a conditional DB update.  Redis is fenced after the commit so a stale
// hash cannot remain authoritative; cache reads also refresh quota from DB
// while batch mode is enabled (see GetUserCache/GetTokenByKey).
func reserveUserQuotaInBatchMode(id int, quota int) (bool, error) {
	reserved, err := reserveUserQuotaDB(id, quota)
	if !common.RedisEnabled {
		return reserved, err
	}
	if cacheErr := invalidateUserQuotaCacheForReserveFallback(id); cacheErr != nil {
		// The database result is already authoritative.  Do not return an error
		// after a successful reservation: callers could retry and charge twice.
		// Persist a repair hint instead; the worker will fence/hydrate the cache
		// once Redis is reachable again.
		recordQuotaCacheRepair(QuotaCacheRepairEntityUser, id, getUserCacheKey(id), cacheErr)
	}
	return reserved, err
}

// reserveTokenQuotaInBatchMode is the token counterpart of
// reserveUserQuotaInBatchMode.  Token cache mutations already have a short
// fence, so invalidation after the conditional DB update prevents a stale
// snapshot from being republished when the process is healthy; a repair row
// handles Redis outages.
func reserveTokenQuotaInBatchMode(id int, key string, quota int) (bool, error) {
	reserved, err := reserveTokenQuotaDB(id, quota)
	if !common.RedisEnabled || strings.TrimSpace(key) == "" {
		return reserved, err
	}
	if cacheErr := invalidateTokenCacheForReserveFallback(id, key); cacheErr != nil {
		recordQuotaCacheRepair(QuotaCacheRepairEntityToken, id, getTokenCacheKey(key), cacheErr)
	}
	return reserved, err
}

// TryReserveUserQuota atomically checks and deducts a user's wallet quota.
// 缓存命中时以缓存余额为准（避免批量模式下过期的数据库余额放大并发超扣）；
// Redis 异常或水合失败时降级为数据库条件更新，保证服务可用。
func TryReserveUserQuota(id int, quota int) (bool, error) {
	if quota < 0 {
		return false, errors.New("quota 不能为负数！")
	}
	if quota == 0 {
		return true, nil
	}
	if common.BatchUpdateEnabled {
		return reserveUserQuotaInBatchMode(id, quota)
	}
	if !common.RedisEnabled || common.RDB == nil {
		return reserveUserQuotaDB(id, quota)
	}

	result, err := cacheTryReserveUserQuota(id, int64(quota))
	if err == nil && result == cacheQuotaMiss {
		if _, hydrateErr := GetUserCache(id); hydrateErr == nil {
			result, err = cacheTryReserveUserQuota(id, int64(quota))
		}
	}
	if err != nil || result == cacheQuotaMiss {
		if err != nil {
			common.SysLog("user quota cache reserve unavailable, falling back to database: " + err.Error())
		}
		// EVAL errors and unexpected results are ambiguous: Redis may have
		// committed the reservation before the response was lost. A database
		// fallback is safe only after the cache key has been invalidated. A
		// failed DEL is itself ambiguous; Ping cannot prove that the script did
		// not execute, so fail closed instead of risking a double charge.
		if invalidateErr := invalidateUserQuotaCacheForReserveFallback(id); invalidateErr != nil {
			return false, fmt.Errorf("invalidate user quota cache before database fallback: %w (original: %v)", invalidateErr, err)
		}
		reserved, dbErr := reserveUserQuotaDB(id, quota)
		if dbErr != nil || !reserved {
			return reserved, dbErr
		}
		if repairErr := repairUserQuotaCache(id); repairErr != nil {
			recordQuotaCacheRepair(QuotaCacheRepairEntityUser, id, getUserCacheKey(id), repairErr)
		}
		return true, nil
	}
	if result == cacheQuotaInsufficient {
		return false, nil
	}
	persisted, persistErr := persistUserQuotaDelta(id, -quota)
	if persistErr != nil || !persisted {
		compensated, compensateErr := cacheApplyUserQuotaDelta(id, int64(quota))
		if compensateErr != nil || compensated != cacheQuotaOK {
			common.SysError(fmt.Sprintf("failed to compensate reserved user quota: result=%d error=%v", compensated, compensateErr))
		}
		if persistErr != nil {
			return false, persistErr
		}
		// The durable wallet no longer had enough balance even though the
		// cache did.  After restoring the cache delta, report an ordinary
		// insufficiency so callers can choose another funding source.
		recordQuotaCacheRepair(QuotaCacheRepairEntityUser, id, getUserCacheKey(id), ErrQuotaCacheMiss)
		return false, nil
	}
	return true, nil
}

// TryReserveTokenQuota atomically checks and deducts a token quota. Unlimited
// tokens skip the balance check but still update remain/used accounting.
func TryReserveTokenQuota(id int, key string, quota int, unlimited bool) (bool, error) {
	if quota < 0 {
		return false, errors.New("quota 不能为负数！")
	}
	if quota == 0 {
		return true, nil
	}
	if unlimited {
		return true, DecreaseTokenQuota(id, key, quota)
	}
	if common.BatchUpdateEnabled {
		return reserveTokenQuotaInBatchMode(id, key, quota)
	}
	if !common.RedisEnabled || common.RDB == nil {
		return reserveTokenQuotaDB(id, quota)
	}

	result, err := cacheTryReserveTokenQuota(id, key, int64(quota))
	if err == nil && result == cacheQuotaMiss {
		if _, hydrateErr := GetTokenByKey(key, true); hydrateErr == nil {
			result, err = cacheTryReserveTokenQuota(id, key, int64(quota))
		}
	}
	if err != nil || result == cacheQuotaMiss {
		if err != nil {
			common.SysLog("token quota cache reserve unavailable, falling back to database: " + err.Error())
		}
		// As with user quota, never fall back while an ambiguous cache
		// reservation may still exist. Successful invalidation fences the stale
		// cache; failure is surfaced to the caller for retry/reconciliation.
		if invalidateErr := invalidateTokenCacheForReserveFallback(id, key); invalidateErr != nil {
			return false, fmt.Errorf("invalidate token quota cache before database fallback: %w (original: %v)", invalidateErr, err)
		}
		reserved, dbErr := reserveTokenQuotaDB(id, quota)
		if dbErr != nil || !reserved {
			return reserved, dbErr
		}
		if repairErr := invalidateTokenCacheForMutation(key); repairErr != nil {
			recordQuotaCacheRepair(QuotaCacheRepairEntityToken, id, getTokenCacheKey(key), repairErr)
		}
		return true, nil
	}
	if result == cacheQuotaInsufficient {
		return false, nil
	}
	persisted, persistErr := persistTokenQuotaDelta(id, -quota)
	if persistErr != nil || !persisted {
		compensated, compensateErr := cacheApplyTokenQuotaDelta(id, key, int64(quota))
		if compensateErr != nil || compensated != cacheQuotaOK {
			common.SysError(fmt.Sprintf("failed to compensate reserved token quota: result=%d error=%v", compensated, compensateErr))
		}
		if persistErr != nil {
			return false, persistErr
		}
		recordQuotaCacheRepair(QuotaCacheRepairEntityToken, id, getTokenCacheKey(key), ErrQuotaCacheMiss)
		return false, nil
	}
	return true, nil
}
