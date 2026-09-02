package ionet

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeDataWithFlexibleTimesNormalizesTimezoneLessTimestamps(t *testing.T) {
	var decoded struct {
		CreatedAt time.Time  `json:"created_at"`
		StartedAt *time.Time `json:"started_at"`
	}

	err := decodeDataWithFlexibleTimes([]byte(`{
		"data": {
			"created_at": "2026-08-31T12:34:56.123456",
			"started_at": "2026-08-31T12:34:57"
		}
	}`), &decoded)
	require.NoError(t, err)
	assert.Equal(t, "2026-08-31T12:34:56.123456Z", decoded.CreatedAt.Format(time.RFC3339Nano))
	require.NotNil(t, decoded.StartedAt)
	assert.Equal(t, "2026-08-31T12:34:57Z", decoded.StartedAt.Format(time.RFC3339Nano))
}

func TestDecodeDataWithFlexibleTimesPreservesNonTimestampStrings(t *testing.T) {
	var decoded struct {
		Name string `json:"name"`
	}

	err := decodeDataWithFlexibleTimes([]byte(`{"data":{"name":"2026-08-31"}}`), &decoded)
	require.NoError(t, err)
	assert.Equal(t, "2026-08-31", decoded.Name)
}
