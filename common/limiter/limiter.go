package limiter

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/go-redis/redis/v8"
)

//go:embed lua/rate_limit.lua
var rateLimitScriptSource string

// rateLimitScript is intentionally executed through Script.Run rather than a
// permanently cached EVALSHA. Redis keeps Lua scripts in memory only; a Redis
// restart (or SCRIPT FLUSH during maintenance) evicts the SHA even when the
// data itself is restored from AOF/RDB. Script.Run retries the script with
// EVAL after NOSCRIPT, allowing a live API process to recover automatically.
var rateLimitScript = redis.NewScript(rateLimitScriptSource)

type RedisLimiter struct {
	client *redis.Client
}

func New(ctx context.Context, r *redis.Client) *RedisLimiter {
	// Keep ctx in the signature for source compatibility with existing call
	// sites. Script loading is lazy in Allow, so startup does not fail merely
	// because Redis is temporarily unavailable.
	_ = ctx
	return &RedisLimiter{client: r}
}

func (rl *RedisLimiter) Allow(ctx context.Context, key string, opts ...Option) (bool, error) {
	if rl == nil || rl.client == nil {
		return false, errors.New("Redis client is not initialized")
	}
	// 默认配置
	config := &Config{
		Capacity:  10,
		Rate:      1,
		Requested: 1,
	}

	// 应用选项模式
	for _, opt := range opts {
		opt(config)
	}

	// 执行限流
	result, err := rateLimitScript.Run(
		ctx,
		rl.client,
		[]string{key},
		config.Requested,
		config.Rate,
		config.Capacity,
	).Int()

	if err != nil {
		return false, fmt.Errorf("rate limit failed: %w", err)
	}
	return result == 1, nil
}

// Config 配置选项模式
type Config struct {
	Capacity  int64
	Rate      int64
	Requested int64
}

type Option func(*Config)

func WithCapacity(c int64) Option {
	return func(cfg *Config) { cfg.Capacity = c }
}

func WithRate(r int64) Option {
	return func(cfg *Config) { cfg.Rate = r }
}

func WithRequested(n int64) Option {
	return func(cfg *Config) { cfg.Requested = n }
}
