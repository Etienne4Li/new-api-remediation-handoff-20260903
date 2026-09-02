package common

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

// withTestRedis installs an isolated client because the production helpers use
// package globals.  Restore both globals even when a test fails so the rest of
// the package (and tests running in the same process) are unaffected.
func withTestRedis(t *testing.T, fn func(*miniredis.Miniredis)) {
	t.Helper()
	m := miniredis.RunT(t)
	previousClient, previousEnabled := RDB, RedisEnabled
	RDB = redis.NewClient(&redis.Options{Addr: m.Addr()})
	RedisEnabled = true
	t.Cleanup(func() {
		_ = RDB.Close()
		RDB, RedisEnabled = previousClient, previousEnabled
	})
	fn(m)
}

func TestRedisIncrWorksForPersistentAndExpiringKeys(t *testing.T) {
	withTestRedis(t, func(m *miniredis.Miniredis) {
		m.Set("persistent", "2")
		require.NoError(t, RedisIncr("persistent", 3))
		value, err := m.Get("persistent")
		require.NoError(t, err)
		require.Equal(t, "5", value)
		require.False(t, m.Exists("missing"))
		require.NoError(t, RedisIncr("missing", 4))
		value, err = m.Get("missing")
		require.NoError(t, err)
		require.Equal(t, "4", value)

		m.Set("expiring", "10")
		m.SetTTL("expiring", time.Minute)
		require.NoError(t, RedisIncr("expiring", -2))
		value, err = m.Get("expiring")
		require.NoError(t, err)
		require.Equal(t, "8", value)
		require.Greater(t, m.TTL("expiring"), time.Duration(0))
	})
}

func TestRedisHashMutationsWorkWithoutTTLAndPreserveTTL(t *testing.T) {
	withTestRedis(t, func(m *miniredis.Miniredis) {
		require.NoError(t, RedisHIncrBy("hash", "count", 1))
		require.Equal(t, "1", m.HGet("hash", "count"))
		require.NoError(t, RedisHSetField("hash", "name", "new-api"))
		require.Equal(t, "new-api", m.HGet("hash", "name"))

		m.SetTTL("hash", time.Minute)
		require.NoError(t, RedisHIncrBy("hash", "count", 2))
		require.Equal(t, "3", m.HGet("hash", "count"))
		require.Greater(t, m.TTL("hash"), time.Duration(0))
	})
}

func TestRedisHSetObjRejectsInvalidInputs(t *testing.T) {
	withTestRedis(t, func(_ *miniredis.Miniredis) {
		require.Error(t, RedisHSetObj("obj", nil, 0))
		require.Error(t, RedisHSetObj("obj", struct{ Name string }{}, 0))
		var obj *struct{ Name string }
		require.Error(t, RedisHSetObj("obj", obj, 0))
	})
}

func TestParseRedisOptionReadsConnectionStringFromFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "redis_conn_string")
	require.NoError(t, os.WriteFile(file, []byte("redis://cache-user:cache-pass@redis.internal:6380/2\n"), 0600))
	t.Setenv("REDIS_CONN_STRING", "")
	t.Setenv("REDIS_CONN_STRING_FILE", file)

	option := ParseRedisOption()
	require.Equal(t, "redis.internal:6380", option.Addr)
	require.Equal(t, "cache-user", option.Username)
	require.Equal(t, "cache-pass", option.Password)
	require.Equal(t, 2, option.DB)
}

func TestInitRedisClientPublishesMemoryCacheBeforeWorkersStart(t *testing.T) {
	m := miniredis.RunT(t)
	t.Setenv("REDIS_CONN_STRING", "redis://"+m.Addr())
	t.Setenv("REDIS_CONN_STRING_FILE", "")
	t.Setenv("SYNC_FREQUENCY", "17")
	t.Setenv("REDIS_POOL_SIZE", "2")

	previousClient, previousEnabled := RDB, RedisEnabled
	previousMemoryCache, previousSyncFrequency := MemoryCacheEnabled, SyncFrequency
	RDB = nil
	RedisEnabled = false
	MemoryCacheEnabled = false
	SyncFrequency = 1
	t.Cleanup(func() {
		if RDB != nil {
			_ = RDB.Close()
		}
		RDB, RedisEnabled = previousClient, previousEnabled
		MemoryCacheEnabled, SyncFrequency = previousMemoryCache, previousSyncFrequency
	})

	require.NoError(t, InitRedisClient())
	require.True(t, RedisEnabled)
	require.True(t, MemoryCacheEnabled)
	require.Equal(t, 1, SyncFrequency, "an explicitly configured sync frequency must not be changed by Redis initialization")
}

func TestInitRedisClientHonorsExplicitMemoryCacheDisable(t *testing.T) {
	m := miniredis.RunT(t)
	t.Setenv("REDIS_CONN_STRING", "redis://"+m.Addr())
	t.Setenv("REDIS_CONN_STRING_FILE", "")
	t.Setenv("MEMORY_CACHE_ENABLED", "false")
	t.Setenv("REDIS_POOL_SIZE", "2")

	previousClient, previousEnabled := RDB, RedisEnabled
	previousMemoryCache := MemoryCacheEnabled
	RDB = nil
	RedisEnabled = false
	MemoryCacheEnabled = true
	t.Cleanup(func() {
		if RDB != nil {
			_ = RDB.Close()
		}
		RDB, RedisEnabled = previousClient, previousEnabled
		MemoryCacheEnabled = previousMemoryCache
	})

	require.NoError(t, InitRedisClient())
	require.False(t, MemoryCacheEnabled)
}
