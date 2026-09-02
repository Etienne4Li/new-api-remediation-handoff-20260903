package operation_setting

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

// OperationRuntimeConfig groups the legacy operational switches changed by
// option hot reloads. Returned snapshots are detached and request-safe.
type OperationRuntimeConfig struct {
	DemoSiteEnabled                  bool
	SelfUseModeEnabled               bool
	AutomaticDisableKeywords         []string
	AutomaticDisableStatusCodeRanges []StatusCodeRange
	AutomaticRetryStatusCodeRanges   []StatusCodeRange
}

var defaultAutomaticDisableKeywords = []string{
	"Your credit balance is too low",
	"This organization has been disabled.",
	"You exceeded your current quota",
	"Permission denied",
	"The security token included in the request is invalid",
	"Operation not allowed",
	"Your account is not authorized",
}

// Deprecated: use GetOperationRuntimeConfig (or the typed accessors below).
// These exports remain for source compatibility with older integrations; new
// request code must not read or write them directly.
var DemoSiteEnabled bool
var SelfUseModeEnabled bool
var AutomaticDisableKeywords = append([]string(nil), defaultAutomaticDisableKeywords...)

var operationRuntimeState atomic.Pointer[OperationRuntimeConfig]
var operationRuntimeUpdateMu sync.Mutex

func init() {
	initial := OperationRuntimeConfig{
		DemoSiteEnabled:                  DemoSiteEnabled,
		SelfUseModeEnabled:               SelfUseModeEnabled,
		AutomaticDisableKeywords:         append([]string(nil), AutomaticDisableKeywords...),
		AutomaticDisableStatusCodeRanges: append([]StatusCodeRange(nil), AutomaticDisableStatusCodeRanges...),
		AutomaticRetryStatusCodeRanges:   append([]StatusCodeRange(nil), AutomaticRetryStatusCodeRanges...),
	}
	operationRuntimeState.Store(&initial)
}

func cloneOperationRuntimeConfig(source OperationRuntimeConfig) OperationRuntimeConfig {
	clone := source
	clone.AutomaticDisableKeywords = append([]string(nil), source.AutomaticDisableKeywords...)
	clone.AutomaticDisableStatusCodeRanges = append([]StatusCodeRange(nil), source.AutomaticDisableStatusCodeRanges...)
	clone.AutomaticRetryStatusCodeRanges = append([]StatusCodeRange(nil), source.AutomaticRetryStatusCodeRanges...)
	return clone
}

func currentOperationRuntimeConfig() *OperationRuntimeConfig {
	if current := operationRuntimeState.Load(); current != nil {
		return current
	}
	initial := OperationRuntimeConfig{
		AutomaticDisableKeywords:         append([]string(nil), defaultAutomaticDisableKeywords...),
		AutomaticDisableStatusCodeRanges: append([]StatusCodeRange(nil), AutomaticDisableStatusCodeRanges...),
		AutomaticRetryStatusCodeRanges:   append([]StatusCodeRange(nil), AutomaticRetryStatusCodeRanges...),
	}
	if operationRuntimeState.CompareAndSwap(nil, &initial) {
		return &initial
	}
	return operationRuntimeState.Load()
}

// GetOperationRuntimeConfig returns a detached operation snapshot and fences
// it against an in-flight OptionMap publication. Request handlers should use
// this accessor so a bulk option update cannot expose a partially applied
// operation policy.
func GetOperationRuntimeConfig() OperationRuntimeConfig {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return GetOperationRuntimeConfigWithoutOptionLock()
}

// GetOperationRuntimeConfigWithoutOptionLock returns the same detached
// snapshot without taking common.OptionMapRWMutex. It is reserved for
// bootstrap/publication code that already holds that lock; using the locking
// accessor from such code would recursively acquire an RWMutex and can block
// forever when a writer is waiting.
func GetOperationRuntimeConfigWithoutOptionLock() OperationRuntimeConfig {
	return cloneOperationRuntimeConfig(*currentOperationRuntimeConfig())
}

// UpdateOperationRuntimeConfig publishes every callback change as one
// copy-on-write value. The callback never receives the live snapshot.
func UpdateOperationRuntimeConfig(update func(*OperationRuntimeConfig)) {
	if update == nil {
		return
	}
	operationRuntimeUpdateMu.Lock()
	defer operationRuntimeUpdateMu.Unlock()
	next := cloneOperationRuntimeConfig(*currentOperationRuntimeConfig())
	update(&next)
	published := cloneOperationRuntimeConfig(next)
	operationRuntimeState.Store(&published)
	// Keep the legacy exports synchronized for callers that still inspect them.
	// They are intentionally not used by production request paths.
	DemoSiteEnabled = published.DemoSiteEnabled
	SelfUseModeEnabled = published.SelfUseModeEnabled
	AutomaticDisableKeywords = append([]string(nil), published.AutomaticDisableKeywords...)
	AutomaticDisableStatusCodeRanges = append([]StatusCodeRange(nil), published.AutomaticDisableStatusCodeRanges...)
	AutomaticRetryStatusCodeRanges = append([]StatusCodeRange(nil), published.AutomaticRetryStatusCodeRanges...)
}

func IsDemoSiteEnabled() bool {
	return GetOperationRuntimeConfig().DemoSiteEnabled
}

func SetDemoSiteEnabled(enabled bool) {
	UpdateOperationRuntimeConfig(func(config *OperationRuntimeConfig) {
		config.DemoSiteEnabled = enabled
	})
}

func IsSelfUseModeEnabled() bool {
	return GetOperationRuntimeConfig().SelfUseModeEnabled
}

func SetSelfUseModeEnabled(enabled bool) {
	UpdateOperationRuntimeConfig(func(config *OperationRuntimeConfig) {
		config.SelfUseModeEnabled = enabled
	})
}

func AutomaticDisableKeywordsToString() string {
	return strings.Join(GetOperationRuntimeConfig().AutomaticDisableKeywords, "\n")
}

// AutomaticDisableKeywordsToStringWithoutOptionLock is for callers already
// holding common.OptionMapRWMutex (notably option-map bootstrap).
func AutomaticDisableKeywordsToStringWithoutOptionLock() string {
	return strings.Join(GetOperationRuntimeConfigWithoutOptionLock().AutomaticDisableKeywords, "\n")
}

func AutomaticDisableKeywordsFromString(value string) {
	keywords := make([]string, 0)
	for _, keyword := range strings.Split(value, "\n") {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" {
			keywords = append(keywords, keyword)
		}
	}
	UpdateOperationRuntimeConfig(func(config *OperationRuntimeConfig) {
		config.AutomaticDisableKeywords = keywords
	})
}
