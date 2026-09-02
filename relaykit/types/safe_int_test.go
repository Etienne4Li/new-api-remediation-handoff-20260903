package types

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSaturatingUintToInt(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	assert.Equal(t, 0, SaturatingUintToInt(0))
	assert.Equal(t, 42, SaturatingUintToInt(42))
	assert.Equal(t, maxInt, SaturatingUintToInt(uint(maxInt)))
	assert.Equal(t, maxInt, SaturatingUintToInt(uint(maxInt)+1))
	assert.Equal(t, maxInt, SaturatingUintToInt(^uint(0)))

	// Keep the test meaningful on both 32-bit and 64-bit builders. On a
	// 64-bit host this is the largest uint value that differs from maxInt;
	// on a 32-bit host the conversion above already exercises the boundary.
	if uint64(maxInt) < math.MaxUint64 {
		assert.Equal(t, maxInt, SaturatingUintToInt(uint(maxInt)+1))
	}
}

func TestSaturatingAddNonNegativeInt(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	assert.Equal(t, 10, SaturatingAddNonNegativeInt(4, 6))
	assert.Equal(t, 4, SaturatingAddNonNegativeInt(-7, 4))
	assert.Equal(t, maxInt, SaturatingAddNonNegativeInt(maxInt, 1))
}
