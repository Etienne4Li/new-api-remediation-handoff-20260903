package common

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSaturatingAddNonNegativeInt(t *testing.T) {
	assert.Equal(t, 10, SaturatingAddNonNegativeInt(4, 6))
	assert.Equal(t, 4, SaturatingAddNonNegativeInt(-2, 4))
	assert.Equal(t, math.MaxInt, SaturatingAddNonNegativeInt(math.MaxInt, 1))
}

func TestSaturatingMulNonNegativeInt(t *testing.T) {
	assert.Equal(t, 42, SaturatingMulNonNegativeInt(6, 7))
	assert.Equal(t, 0, SaturatingMulNonNegativeInt(-1, 7))
	assert.Equal(t, math.MaxInt, SaturatingMulNonNegativeInt(math.MaxInt, 2))
}
