package limiter

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowReloadsRateLimitScriptAfterRedisScriptFlush(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())

	rateLimiter := New(ctx, client)
	allowed, err := rateLimiter.Allow(
		ctx,
		"rate-limit-restart",
		WithCapacity(1),
		WithRate(1),
		WithRequested(1),
	)
	require.NoError(t, err)
	assert.True(t, allowed)

	// Lua scripts are not persisted in AOF/RDB. Flushing the script cache
	// models a Redis restart while the API process and its limiter remain live.
	_, err = client.Do(ctx, "SCRIPT", "FLUSH", "SYNC").Result()
	require.NoError(t, err)

	allowed, err = rateLimiter.Allow(
		ctx,
		"rate-limit-restart",
		WithCapacity(1),
		WithRate(1),
		WithRequested(1),
	)
	require.NoError(t, err)
	assert.False(t, allowed, "the existing bucket must survive script reload")
}
