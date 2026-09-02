package setting

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// Keep the persisted/request-facing limits finite. The count cap matches the
// current admin UI and remains comfortably below the int64 capacity product
// for a 24-hour window.
const (
	maxRateLimitDurationSeconds         = 24 * 60 * 60
	MaxModelRequestRateLimitDurationMin = maxRateLimitDurationSeconds / 60
	MaxModelRequestRateLimitCount       = 100_000_000
	MaxModelRequestRateLimitGroupSize   = 1_000
)

var ModelRequestRateLimitEnabled = false
var ModelRequestRateLimitDurationMinutes = 1
var ModelRequestRateLimitCount = 0
var ModelRequestRateLimitSuccessCount = 1000
var ModelRequestRateLimitGroup = map[string][2]int{}
var ModelRequestRateLimitMutex sync.RWMutex

const (
	defaultModelRequestRateLimitDurationMinutes = 1
	defaultModelRequestRateLimitCount           = 0
	defaultModelRequestRateLimitSuccessCount    = 1000
)

// ModelRequestRateLimitConfig is one coherent request-limit snapshot. The
// legacy exported variables remain for source compatibility, but production
// readers should obtain this value once per request so a hot update cannot
// mix duration/count values from different generations.
type ModelRequestRateLimitConfig struct {
	Enabled         bool
	DurationMinutes int
	Count           int
	SuccessCount    int
	Group           map[string][2]int
}

func cloneRateLimitGroup(group map[string][2]int) map[string][2]int {
	if group == nil {
		return nil
	}
	clone := make(map[string][2]int, len(group))
	for key, value := range group {
		clone[key] = value
	}
	return clone
}

// GetModelRequestRateLimitConfig returns a detached, internally consistent
// snapshot without taking OptionMapRWMutex (it is also called while that
// writer lock is held during bootstrap/option publication).
func GetModelRequestRateLimitConfig() ModelRequestRateLimitConfig {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()
	cfg := ModelRequestRateLimitConfig{
		Enabled:         ModelRequestRateLimitEnabled,
		DurationMinutes: ModelRequestRateLimitDurationMinutes,
		Count:           ModelRequestRateLimitCount,
		SuccessCount:    ModelRequestRateLimitSuccessCount,
		Group:           cloneRateLimitGroup(ModelRequestRateLimitGroup),
	}
	normalizeModelRequestRateLimitConfig(&cfg)
	return cfg
}

// UpdateModelRequestRateLimitConfig publishes all related scalar/map fields
// together. The callback receives a private copy and may replace Group.
func UpdateModelRequestRateLimitConfig(update func(*ModelRequestRateLimitConfig)) {
	if update == nil {
		return
	}
	ModelRequestRateLimitMutex.Lock()
	defer ModelRequestRateLimitMutex.Unlock()
	cfg := ModelRequestRateLimitConfig{
		Enabled:         ModelRequestRateLimitEnabled,
		DurationMinutes: ModelRequestRateLimitDurationMinutes,
		Count:           ModelRequestRateLimitCount,
		SuccessCount:    ModelRequestRateLimitSuccessCount,
		Group:           cloneRateLimitGroup(ModelRequestRateLimitGroup),
	}
	update(&cfg)
	normalizeModelRequestRateLimitConfig(&cfg)
	ModelRequestRateLimitEnabled = cfg.Enabled
	ModelRequestRateLimitDurationMinutes = cfg.DurationMinutes
	ModelRequestRateLimitCount = cfg.Count
	ModelRequestRateLimitSuccessCount = cfg.SuccessCount
	ModelRequestRateLimitGroup = cloneRateLimitGroup(cfg.Group)
}

func normalizeModelRequestRateLimitConfig(cfg *ModelRequestRateLimitConfig) {
	if cfg == nil {
		return
	}
	if cfg.DurationMinutes < 1 {
		cfg.DurationMinutes = defaultModelRequestRateLimitDurationMinutes
	} else if cfg.DurationMinutes > MaxModelRequestRateLimitDurationMin {
		cfg.DurationMinutes = MaxModelRequestRateLimitDurationMin
	}
	if cfg.Count < 0 {
		cfg.Count = defaultModelRequestRateLimitCount
	} else if cfg.Count > MaxModelRequestRateLimitCount {
		cfg.Count = MaxModelRequestRateLimitCount
	}
	if cfg.SuccessCount < 1 {
		cfg.SuccessCount = defaultModelRequestRateLimitSuccessCount
	} else if cfg.SuccessCount > MaxModelRequestRateLimitCount {
		cfg.SuccessCount = MaxModelRequestRateLimitCount
	}
	if len(cfg.Group) > MaxModelRequestRateLimitGroupSize {
		trimmed := make(map[string][2]int, MaxModelRequestRateLimitGroupSize)
		for key, limits := range cfg.Group {
			if len(trimmed) >= MaxModelRequestRateLimitGroupSize {
				break
			}
			trimmed[key] = normalizeGroupRateLimit(limits)
		}
		cfg.Group = trimmed
		return
	}
	for key, limits := range cfg.Group {
		cfg.Group[key] = normalizeGroupRateLimit(limits)
	}
}

func normalizeGroupRateLimit(limits [2]int) [2]int {
	if limits[0] < 0 {
		limits[0] = 0
	} else if limits[0] > MaxModelRequestRateLimitCount {
		limits[0] = MaxModelRequestRateLimitCount
	}
	if limits[1] < 1 {
		limits[1] = 1
	} else if limits[1] > MaxModelRequestRateLimitCount {
		limits[1] = MaxModelRequestRateLimitCount
	}
	return limits
}

// ValidateModelRequestRateLimitConfig is used by persistence/API preflight
// paths. It rejects invalid values instead of silently changing an operator's
// requested policy; direct setters still normalize defensively above.
func ValidateModelRequestRateLimitConfig(cfg ModelRequestRateLimitConfig) error {
	if cfg.DurationMinutes < 1 || cfg.DurationMinutes > MaxModelRequestRateLimitDurationMin {
		return fmt.Errorf("rate limit duration must be between 1 and %d minutes", MaxModelRequestRateLimitDurationMin)
	}
	if cfg.Count < 0 || cfg.Count > MaxModelRequestRateLimitCount {
		return fmt.Errorf("rate limit count must be between 0 and %d", MaxModelRequestRateLimitCount)
	}
	if cfg.SuccessCount < 1 || cfg.SuccessCount > MaxModelRequestRateLimitCount {
		return fmt.Errorf("rate limit success count must be between 1 and %d", MaxModelRequestRateLimitCount)
	}
	if len(cfg.Group) > MaxModelRequestRateLimitGroupSize {
		return fmt.Errorf("rate limit group count must not exceed %d", MaxModelRequestRateLimitGroupSize)
	}
	for group, limits := range cfg.Group {
		if len(group) > 256 {
			return fmt.Errorf("rate limit group name is too long")
		}
		if limits[0] < 0 || limits[0] > MaxModelRequestRateLimitCount || limits[1] < 1 || limits[1] > MaxModelRequestRateLimitCount {
			return fmt.Errorf("group %s rate limits must be [0..%d, 1..%d]", group, MaxModelRequestRateLimitCount, MaxModelRequestRateLimitCount)
		}
	}
	return nil
}

// ValidateModelRequestRateLimitValue validates one legacy option before it is
// persisted. These options are stored as independent rows, so validating the
// scalar directly avoids accidentally accepting an overflowed value that the
// setter would later normalize.
func ValidateModelRequestRateLimitValue(key string, value int) error {
	switch key {
	case "ModelRequestRateLimitDurationMinutes":
		if value < 1 || value > MaxModelRequestRateLimitDurationMin {
			return fmt.Errorf("rate limit duration must be between 1 and %d minutes", MaxModelRequestRateLimitDurationMin)
		}
	case "ModelRequestRateLimitCount":
		if value < 0 || value > MaxModelRequestRateLimitCount {
			return fmt.Errorf("rate limit count must be between 0 and %d", MaxModelRequestRateLimitCount)
		}
	case "ModelRequestRateLimitSuccessCount":
		if value < 1 || value > MaxModelRequestRateLimitCount {
			return fmt.Errorf("rate limit success count must be between 1 and %d", MaxModelRequestRateLimitCount)
		}
	default:
		return fmt.Errorf("unknown model request rate limit option %q", key)
	}
	return nil
}

func SetModelRequestRateLimitEnabled(enabled bool) {
	UpdateModelRequestRateLimitConfig(func(cfg *ModelRequestRateLimitConfig) { cfg.Enabled = enabled })
}

func SetModelRequestRateLimitDurationMinutes(minutes int) {
	UpdateModelRequestRateLimitConfig(func(cfg *ModelRequestRateLimitConfig) { cfg.DurationMinutes = minutes })
}

func SetModelRequestRateLimitCount(count int) {
	UpdateModelRequestRateLimitConfig(func(cfg *ModelRequestRateLimitConfig) { cfg.Count = count })
}

func SetModelRequestRateLimitSuccessCount(count int) {
	UpdateModelRequestRateLimitConfig(func(cfg *ModelRequestRateLimitConfig) { cfg.SuccessCount = count })
}

func ModelRequestRateLimitGroup2JSONString() string {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	jsonBytes, err := common.Marshal(ModelRequestRateLimitGroup)
	if err != nil {
		common.SysLog("error marshalling model ratio: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateModelRequestRateLimitGroupByJSONString(jsonStr string) error {
	if err := CheckModelRequestRateLimitGroup(jsonStr); err != nil {
		return err
	}
	decoded := make(map[string][2]int)
	if err := common.Unmarshal([]byte(jsonStr), &decoded); err != nil {
		return err
	}
	ModelRequestRateLimitMutex.Lock()
	ModelRequestRateLimitGroup = cloneRateLimitGroup(decoded)
	ModelRequestRateLimitMutex.Unlock()
	return nil
}

func GetGroupRateLimit(group string) (totalCount, successCount int, found bool) {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	if ModelRequestRateLimitGroup == nil {
		return 0, 0, false
	}

	limits, found := ModelRequestRateLimitGroup[group]
	if !found {
		return 0, 0, false
	}
	return limits[0], limits[1], true
}

func CheckModelRequestRateLimitGroup(jsonStr string) error {
	checkModelRequestRateLimitGroup := make(map[string][2]int)
	err := common.Unmarshal([]byte(jsonStr), &checkModelRequestRateLimitGroup)
	if err != nil {
		return err
	}
	return ValidateModelRequestRateLimitConfig(ModelRequestRateLimitConfig{
		DurationMinutes: defaultModelRequestRateLimitDurationMinutes,
		Count:           defaultModelRequestRateLimitCount,
		SuccessCount:    defaultModelRequestRateLimitSuccessCount,
		Group:           checkModelRequestRateLimitGroup,
	})
}
