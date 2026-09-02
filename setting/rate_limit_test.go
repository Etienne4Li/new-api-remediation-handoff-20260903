package setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelRequestRateLimitConfigNormalizesDirectUpdates(t *testing.T) {
	original := GetModelRequestRateLimitConfig()
	t.Cleanup(func() {
		UpdateModelRequestRateLimitConfig(func(cfg *ModelRequestRateLimitConfig) { *cfg = original })
	})

	UpdateModelRequestRateLimitConfig(func(cfg *ModelRequestRateLimitConfig) {
		cfg.DurationMinutes = 0
		cfg.Count = -1
		cfg.SuccessCount = 0
		cfg.Group = map[string][2]int{"bad": {-1, 0}, "huge": {MaxModelRequestRateLimitCount + 1, MaxModelRequestRateLimitCount + 1}}
	})

	got := GetModelRequestRateLimitConfig()
	assert.Equal(t, defaultModelRequestRateLimitDurationMinutes, got.DurationMinutes)
	assert.Equal(t, defaultModelRequestRateLimitCount, got.Count)
	assert.Equal(t, defaultModelRequestRateLimitSuccessCount, got.SuccessCount)
	assert.Equal(t, [2]int{0, 1}, got.Group["bad"])
	assert.Equal(t, [2]int{MaxModelRequestRateLimitCount, MaxModelRequestRateLimitCount}, got.Group["huge"])
}

func TestCheckModelRequestRateLimitGroupRejectsInvalidBounds(t *testing.T) {
	assert.Error(t, CheckModelRequestRateLimitGroup(`{"g":[-1,1]}`))
	assert.Error(t, CheckModelRequestRateLimitGroup(`{"g":[0,0]}`))
	assert.Error(t, CheckModelRequestRateLimitGroup(`{"g":[100000001,1]}`))
	assert.NoError(t, CheckModelRequestRateLimitGroup(`{"g":[0,1]}`))
}

func TestUpdateModelRequestRateLimitGroupRejectsInvalidAndPreservesState(t *testing.T) {
	original := GetModelRequestRateLimitConfig()
	t.Cleanup(func() {
		UpdateModelRequestRateLimitConfig(func(cfg *ModelRequestRateLimitConfig) { *cfg = original })
	})

	require.NoError(t, UpdateModelRequestRateLimitGroupByJSONString(`{"staff":[4,2]}`))
	assert.Equal(t, [2]int{4, 2}, GetModelRequestRateLimitConfig().Group["staff"])
	require.Error(t, UpdateModelRequestRateLimitGroupByJSONString(`{"staff":[-1,2]}`))
	assert.Equal(t, [2]int{4, 2}, GetModelRequestRateLimitConfig().Group["staff"])
}
