package operation_setting

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type StatusCodeRange struct {
	Start int
	End   int
}

var defaultAutomaticDisableStatusCodeRanges = []StatusCodeRange{{Start: 401, End: 401}}

// Default behavior matches legacy hardcoded retry rules in controller/relay.go shouldRetry:
// retry for 1xx, 3xx, 4xx(except 400/408), 5xx(except 504/524), and no retry for 2xx.
var defaultAutomaticRetryStatusCodeRanges = []StatusCodeRange{
	{Start: 100, End: 199},
	{Start: 300, End: 399},
	{Start: 401, End: 407},
	{Start: 409, End: 499},
	{Start: 500, End: 503},
	{Start: 505, End: 523},
	{Start: 525, End: 599},
}

// Deprecated: use GetOperationRuntimeConfig and its policy methods. These
// slices remain exported only so older integrations continue to compile; they
// are replaced (never mutated in place) whenever a runtime update publishes.
var AutomaticDisableStatusCodeRanges = append([]StatusCodeRange(nil), defaultAutomaticDisableStatusCodeRanges...)
var AutomaticRetryStatusCodeRanges = append([]StatusCodeRange(nil), defaultAutomaticRetryStatusCodeRanges...)

var alwaysSkipRetryStatusCodes = map[int]struct{}{
	504: {},
	524: {},
}

var alwaysSkipRetryCodes = map[types.ErrorCode]struct{}{
	types.ErrorCodeBadResponseBody:             {},
	types.ErrorCodeSessionBlockedByCyberPolicy: {},
}

func AutomaticDisableStatusCodesToString() string {
	return statusCodeRangesToString(GetOperationRuntimeConfig().AutomaticDisableStatusCodeRanges)
}

// AutomaticDisableStatusCodesToStringWithoutOptionLock is for publication
// code that already holds common.OptionMapRWMutex.
func AutomaticDisableStatusCodesToStringWithoutOptionLock() string {
	return statusCodeRangesToString(GetOperationRuntimeConfigWithoutOptionLock().AutomaticDisableStatusCodeRanges)
}

func AutomaticDisableStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	UpdateOperationRuntimeConfig(func(config *OperationRuntimeConfig) {
		config.AutomaticDisableStatusCodeRanges = ranges
	})
	return nil
}

func ShouldDisableByStatusCode(code int) bool {
	return GetOperationRuntimeConfig().ShouldDisableByStatusCode(code)
}

// ShouldDisableByStatusCodeWithoutOptionLock is the lock-free-internally
// variant for callers that already hold the option publication lock.
func ShouldDisableByStatusCodeWithoutOptionLock(code int) bool {
	return GetOperationRuntimeConfigWithoutOptionLock().ShouldDisableByStatusCode(code)
}

func AutomaticRetryStatusCodesToString() string {
	return statusCodeRangesToString(GetOperationRuntimeConfig().AutomaticRetryStatusCodeRanges)
}

// AutomaticRetryStatusCodesToStringWithoutOptionLock is for publication code
// that already holds common.OptionMapRWMutex.
func AutomaticRetryStatusCodesToStringWithoutOptionLock() string {
	return statusCodeRangesToString(GetOperationRuntimeConfigWithoutOptionLock().AutomaticRetryStatusCodeRanges)
}

func AutomaticRetryStatusCodesFromString(s string) error {
	ranges, err := ParseHTTPStatusCodeRanges(s)
	if err != nil {
		return err
	}
	UpdateOperationRuntimeConfig(func(config *OperationRuntimeConfig) {
		config.AutomaticRetryStatusCodeRanges = ranges
	})
	return nil
}

func IsAlwaysSkipRetryStatusCode(code int) bool {
	_, exists := alwaysSkipRetryStatusCodes[code]
	return exists
}

func IsAlwaysSkipRetryCode(errorCode types.ErrorCode) bool {
	_, exists := alwaysSkipRetryCodes[errorCode]
	return exists
}

func ShouldRetryByStatusCode(code int) bool {
	return GetOperationRuntimeConfig().ShouldRetryByStatusCode(code)
}

// ShouldRetryByStatusCodeWithoutOptionLock is the lock-free-internally
// variant for callers that already hold the option publication lock.
func ShouldRetryByStatusCodeWithoutOptionLock(code int) bool {
	return GetOperationRuntimeConfigWithoutOptionLock().ShouldRetryByStatusCode(code)
}

// ShouldDisableByStatusCode evaluates the policy contained in one already
// loaded snapshot. Keeping the range and keyword decisions on the same value
// prevents a request from mixing generations during a hot reload.
func (config OperationRuntimeConfig) ShouldDisableByStatusCode(code int) bool {
	return shouldMatchStatusCodeRanges(config.AutomaticDisableStatusCodeRanges, code)
}

// ShouldRetryByStatusCode evaluates retry policy from one already loaded
// snapshot. The caller may combine this with other fields from that snapshot.
func (config OperationRuntimeConfig) ShouldRetryByStatusCode(code int) bool {
	// Cloudflare 524 is opt-in for synchronous relays. Task relays continue to
	// use IsAlwaysSkipRetryStatusCode to avoid duplicate task creation.
	if code == 524 {
		return shouldMatchStatusCodeRanges(config.AutomaticRetryStatusCodeRanges, code)
	}
	if IsAlwaysSkipRetryStatusCode(code) {
		return false
	}
	return shouldMatchStatusCodeRanges(config.AutomaticRetryStatusCodeRanges, code)
}

func statusCodeRangesToString(ranges []StatusCodeRange) string {
	if len(ranges) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.Start == r.End {
			parts = append(parts, strconv.Itoa(r.Start))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r.Start, r.End))
	}
	return strings.Join(parts, ",")
}

func shouldMatchStatusCodeRanges(ranges []StatusCodeRange, code int) bool {
	if code < 100 || code > 599 {
		return false
	}
	for _, r := range ranges {
		if code < r.Start {
			return false
		}
		if code <= r.End {
			return true
		}
	}
	return false
}

func ParseHTTPStatusCodeRanges(input string) ([]StatusCodeRange, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	input = strings.NewReplacer("，", ",").Replace(input)
	segments := strings.Split(input, ",")

	var ranges []StatusCodeRange
	var invalid []string

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		r, err := parseHTTPStatusCodeToken(seg)
		if err != nil {
			invalid = append(invalid, seg)
			continue
		}
		ranges = append(ranges, r)
	}

	if len(invalid) > 0 {
		return nil, fmt.Errorf("invalid http status code rules: %s", strings.Join(invalid, ", "))
	}
	if len(ranges) == 0 {
		return nil, nil
	}

	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})

	merged := []StatusCodeRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.Start <= last.End+1 {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}

	return merged, nil
}

func parseHTTPStatusCodeToken(token string) (StatusCodeRange, error) {
	token = strings.TrimSpace(token)
	token = strings.ReplaceAll(token, " ", "")
	if token == "" {
		return StatusCodeRange{}, fmt.Errorf("empty token")
	}

	if strings.Contains(token, "-") {
		parts := strings.Split(token, "-")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return StatusCodeRange{}, fmt.Errorf("invalid range token: %s", token)
		}
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return StatusCodeRange{}, fmt.Errorf("invalid range start: %s", token)
		}
		end, err := strconv.Atoi(parts[1])
		if err != nil {
			return StatusCodeRange{}, fmt.Errorf("invalid range end: %s", token)
		}
		if start > end {
			return StatusCodeRange{}, fmt.Errorf("range start > end: %s", token)
		}
		if start < 100 || end > 599 {
			return StatusCodeRange{}, fmt.Errorf("range out of bounds: %s", token)
		}
		return StatusCodeRange{Start: start, End: end}, nil
	}

	code, err := strconv.Atoi(token)
	if err != nil {
		return StatusCodeRange{}, fmt.Errorf("invalid status code: %s", token)
	}
	if code < 100 || code > 599 {
		return StatusCodeRange{}, fmt.Errorf("status code out of bounds: %s", token)
	}
	return StatusCodeRange{Start: code, End: code}, nil
}
