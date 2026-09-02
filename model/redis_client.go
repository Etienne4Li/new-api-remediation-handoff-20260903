package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

var errRedisClientUnavailable = errors.New("redis client is unavailable")

// modelRedisClient returns a stable local pointer for direct Redis operations.
// RedisEnabled describes configuration, while RDB can still be nil during a
// partial bootstrap or an embedded/test lifecycle.
func modelRedisClient() (*redis.Client, error) {
	if !common.RedisEnabled {
		return nil, errRedisClientUnavailable
	}
	client := common.RDB
	if client == nil {
		return nil, errRedisClientUnavailable
	}
	return client, nil
}
