package common

// SaturatingAddNonNegativeInt adds token/count values while treating
// negative input as malformed zero and saturating at the largest int. It is
// shared by relay adapters and service accounting, where upstream counters
// must never wrap into a negative billable quantity.
func SaturatingAddNonNegativeInt(values ...int) int {
	maxInt := int(^uint(0) >> 1)
	total := 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if total > maxInt-value {
			return maxInt
		}
		total += value
	}
	return total
}

// SaturatingMulNonNegativeInt multiplies non-negative count/token values and
// saturates at the largest int instead of wrapping. A non-positive operand is
// treated as malformed zero.
func SaturatingMulNonNegativeInt(value, multiplier int) int {
	if value <= 0 || multiplier <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if value > maxInt/multiplier {
		return maxInt
	}
	return value * multiplier
}
