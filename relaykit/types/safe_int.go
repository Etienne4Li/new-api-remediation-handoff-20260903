package types

// SaturatingUintToInt converts a machine-sized unsigned value to int without
// allowing a large JSON number to wrap into a negative value on platforms
// where uint is wider than int.  Request metadata uses int fields for
// historical compatibility, so callers that cannot return an error should
// saturate at the largest representable int.
func SaturatingUintToInt(value uint) int {
	maxInt := int(^uint(0) >> 1)
	if value > uint(maxInt) {
		return maxInt
	}
	return int(value)
}

// SaturatingAddNonNegativeInt adds token/count values while treating
// negative input as malformed zero and saturating at the largest int. It is
// intended for usage metadata assembled from independent upstream fields.
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
