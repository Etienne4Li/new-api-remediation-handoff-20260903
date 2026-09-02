package common

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

var RDB *redis.Client
var RedisEnabled = true

var errRedisUnavailable = errors.New("redis client is unavailable")

// redisReady centralises the nil check for the optional Redis dependency.  A
// failed/partially completed startup must degrade to an ordinary error instead
// of panicking in a request goroutine.  Callers can then use their existing DB
// fallback or enqueue a repair operation.
func redisReady() error {
	if !RedisEnabled || RDB == nil {
		return errRedisUnavailable
	}
	return nil
}

func redisLogKey(key string) string {
	return SensitiveLogMeta(key)
}

func redisLogValue(value interface{}) string {
	if value == nil {
		return "type=<nil> empty"
	}
	return fmt.Sprintf("type=%T %s", value, SensitiveLogMeta(fmt.Sprint(value)))
}

func RedisKeyCacheSeconds() int {
	return SyncFrequency
}

// InitRedisClient This function is called after init()
func InitRedisClient() (err error) {
	connString, readErr := GetEnvOrFile("REDIS_CONN_STRING", "REDIS_CONN_STRING_FILE")
	if readErr != nil {
		FatalLog("failed to load Redis connection string: " + readErr.Error())
	}
	if connString == "" {
		RedisEnabled = false
		SysLog("REDIS_CONN_STRING not set, Redis is not enabled")
		return nil
	}
	if os.Getenv("SYNC_FREQUENCY") == "" {
		SysLog("SYNC_FREQUENCY not set, use default value 60")
		SyncFrequency = 60
	}
	SysLog("Redis is enabled")
	opt, err := redis.ParseURL(connString)
	if err != nil {
		FatalLog("failed to parse Redis connection string: " + err.Error())
	}
	// A non-positive pool size makes go-redis choose its own default, while an
	// unbounded positive value can allocate far more connections than the
	// database or Redis server can accept. Keep startup configuration finite and
	// deterministic.
	opt.PoolSize = GetEnvOrDefaultBounded("REDIS_POOL_SIZE", 10, 1, maxConnectionPoolEntries)
	RDB = redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = RDB.Ping(ctx).Result()
	if err != nil {
		FatalLog("Redis ping test failed: " + err.Error())
	}
	// InitRedisClient is also used by embedded/test bootstraps that may have
	// previously disabled Redis. Re-publish the successful connection state
	// instead of relying on the package default (true).
	RedisEnabled = true
	// Redis-backed deployments historically implied the in-process channel/user
	// cache. Preserve that default when the setting is absent, but do not
	// override an explicit false: operators may deliberately disable the local
	// cache to reduce memory usage or force reads through Redis.
	if raw, ok := os.LookupEnv("MEMORY_CACHE_ENABLED"); !ok || strings.TrimSpace(raw) == "" {
		MemoryCacheEnabled = true
	} else {
		MemoryCacheEnabled = GetEnvOrDefaultBool("MEMORY_CACHE_ENABLED", true)
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis connected to %s", opt.Addr))
		SysLog(fmt.Sprintf("Redis database: %d", opt.DB))
	}
	return err
}

func ParseRedisOption() *redis.Options {
	connString, readErr := GetEnvOrFile("REDIS_CONN_STRING", "REDIS_CONN_STRING_FILE")
	if readErr != nil {
		FatalLog("failed to load Redis connection string: " + readErr.Error())
	}
	opt, err := redis.ParseURL(connString)
	if err != nil {
		FatalLog("failed to parse Redis connection string: " + err.Error())
	}
	return opt
}

func RedisSet(key string, value string, expiration time.Duration) error {
	if err := redisReady(); err != nil {
		return err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis SET: key_meta=%s, value_meta=%s, expiration=%v", redisLogKey(key), redisLogValue(value), expiration))
	}
	ctx := context.Background()
	return RDB.Set(ctx, key, value, expiration).Err()
}

func RedisGet(key string) (string, error) {
	if err := redisReady(); err != nil {
		return "", err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis GET: key_meta=%s", redisLogKey(key)))
	}
	ctx := context.Background()
	val, err := RDB.Get(ctx, key).Result()
	return val, err
}

//func RedisExpire(key string, expiration time.Duration) error {
//	ctx := context.Background()
//	return RDB.Expire(ctx, key, expiration).Err()
//}
//
//func RedisGetEx(key string, expiration time.Duration) (string, error) {
//	ctx := context.Background()
//	return RDB.GetSet(ctx, key, expiration).Result()
//}

func RedisDel(key string) error {
	if err := redisReady(); err != nil {
		return err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis DEL: key_meta=%s", redisLogKey(key)))
	}
	ctx := context.Background()
	return RDB.Del(ctx, key).Err()
}

func RedisDelKey(key string) error {
	if err := redisReady(); err != nil {
		return err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis DEL Key: key_meta=%s", redisLogKey(key)))
	}
	ctx := context.Background()
	return RDB.Del(ctx, key).Err()
}

func RedisHSetObj(key string, obj interface{}, expiration time.Duration) error {
	if err := redisReady(); err != nil {
		return err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis HSET: key_meta=%s, obj_meta=%s, expiration=%v", redisLogKey(key), redisLogValue(obj), expiration))
	}
	ctx := context.Background()

	data := make(map[string]interface{})

	// 使用反射遍历结构体字段。这个 helper 被多个缓存路径调用，输入来自
	// 请求/数据库对象，不能让 nil 或非指针值把服务进程直接 panic。
	if obj == nil {
		return errors.New("obj must be a non-nil pointer to a struct")
	}
	rv := reflect.ValueOf(obj)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("obj must be a non-nil pointer to a struct, got %T", obj)
	}
	v := rv.Elem()
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("obj must be a pointer to a struct, got pointer to %s", v.Kind())
	}
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		value := v.Field(i)

		// Skip DeletedAt field
		if field.Type.String() == "gorm.DeletedAt" {
			continue
		}

		// 处理指针类型
		if value.Kind() == reflect.Ptr {
			if value.IsNil() {
				data[field.Name] = ""
				continue
			}
			value = value.Elem()
		}

		// 处理布尔类型
		if value.Kind() == reflect.Bool {
			data[field.Name] = strconv.FormatBool(value.Bool())
			continue
		}

		// 其他类型直接转换为字符串
		data[field.Name] = fmt.Sprintf("%v", value.Interface())
	}

	txn := RDB.TxPipeline()
	txn.HSet(ctx, key, data)

	// 只有在 expiration 大于 0 时才设置过期时间
	if expiration > 0 {
		txn.Expire(ctx, key, expiration)
	}

	_, err := txn.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to execute transaction: %w", err)
	}
	return nil
}

func RedisHGetObj(key string, obj interface{}) error {
	if err := redisReady(); err != nil {
		return err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis HGETALL: key_meta=%s", redisLogKey(key)))
	}
	ctx := context.Background()

	result, err := RDB.HGetAll(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to load hash from Redis: %w", err)
	}

	if len(result) == 0 {
		return fmt.Errorf("key %s not found in Redis", key)
	}

	// Handle both pointer and non-pointer values
	val := reflect.ValueOf(obj)
	if val.Kind() != reflect.Ptr {
		return fmt.Errorf("obj must be a pointer to a struct, got %T", obj)
	}

	v := val.Elem()
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("obj must be a pointer to a struct, got pointer to %T", v.Interface())
	}

	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldName := field.Name
		if value, ok := result[fieldName]; ok {
			fieldValue := v.Field(i)

			// Handle pointer types
			if fieldValue.Kind() == reflect.Ptr {
				if value == "" {
					continue
				}
				if fieldValue.IsNil() {
					fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
				}
				fieldValue = fieldValue.Elem()
			}

			// Enhanced type handling for Token struct
			switch fieldValue.Kind() {
			case reflect.String:
				fieldValue.SetString(value)
			case reflect.Int, reflect.Int64:
				intValue, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return fmt.Errorf("failed to parse int field %s: %w", fieldName, err)
				}
				fieldValue.SetInt(intValue)
			case reflect.Bool:
				boolValue, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("failed to parse bool field %s: %w", fieldName, err)
				}
				fieldValue.SetBool(boolValue)
			case reflect.Struct:
				// Special handling for gorm.DeletedAt
				if fieldValue.Type().String() == "gorm.DeletedAt" {
					if value != "" {
						timeValue, err := time.Parse(time.RFC3339, value)
						if err != nil {
							return fmt.Errorf("failed to parse DeletedAt field %s: %w", fieldName, err)
						}
						fieldValue.Set(reflect.ValueOf(gorm.DeletedAt{Time: timeValue, Valid: true}))
					}
				}
			default:
				return fmt.Errorf("unsupported field type: %s for field %s", fieldValue.Kind(), fieldName)
			}
		}
	}

	return nil
}

// RedisIncr Add this function to handle atomic increments
func RedisIncr(key string, delta int64) error {
	if err := redisReady(); err != nil {
		return err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis INCR: key_meta=%s, delta=%d", redisLogKey(key), delta))
	}
	// Increment must happen even for a persistent/non-existent key.  The old
	// implementation silently returned nil in that case, causing quota and
	// rate-limit counters to be lost.  Keep a positive TTL when one exists and
	// do both operations atomically to avoid the TTL changing between commands.
	const script = `local ttl = redis.call('PTTL', KEYS[1]); local value = redis.call('INCRBY', KEYS[1], ARGV[1]); if ttl > 0 then redis.call('PEXPIRE', KEYS[1], ttl); end; return value`
	_, err := RDB.Eval(context.Background(), script, []string{key}, delta).Result()
	if err != nil {
		return fmt.Errorf("failed to increment Redis key: %w", err)
	}
	return nil
}

func RedisHIncrBy(key, field string, delta int64) error {
	if err := redisReady(); err != nil {
		return err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis HINCRBY: key_meta=%s, field=%s, delta=%d", redisLogKey(key), field, delta))
	}
	const script = `local ttl = redis.call('PTTL', KEYS[1]); local value = redis.call('HINCRBY', KEYS[1], ARGV[1], ARGV[2]); if ttl > 0 then redis.call('PEXPIRE', KEYS[1], ttl); end; return value`
	_, err := RDB.Eval(context.Background(), script, []string{key}, field, delta).Result()
	if err != nil {
		return fmt.Errorf("failed to increment Redis hash field: %w", err)
	}
	return nil
}

func RedisHSetField(key, field string, value interface{}) error {
	if err := redisReady(); err != nil {
		return err
	}
	if DebugEnabled {
		SysLog(fmt.Sprintf("Redis HSET field: key_meta=%s, field=%s, value_meta=%s", redisLogKey(key), field, redisLogValue(value)))
	}
	// HSET is also meaningful for persistent/non-existent hashes.  Preserve a
	// positive existing TTL atomically, but never turn a persistent key into an
	// accidentally expiring one.
	const script = `local ttl = redis.call('PTTL', KEYS[1]); local result = redis.call('HSET', KEYS[1], ARGV[1], ARGV[2]); if ttl > 0 then redis.call('PEXPIRE', KEYS[1], ttl); end; return result`
	_, err := RDB.Eval(context.Background(), script, []string{key}, field, value).Result()
	if err != nil {
		return fmt.Errorf("failed to set Redis hash field: %w", err)
	}
	return nil
}
