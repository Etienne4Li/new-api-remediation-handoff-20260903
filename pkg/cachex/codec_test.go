package cachex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONCodecRoundTrip(t *testing.T) {
	type value struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	codec := JSONCodec[value]{}
	encoded, err := codec.Encode(value{Name: "worker", Count: 2})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"worker","count":2}`, encoded)

	decoded, err := codec.Decode(encoded)
	require.NoError(t, err)
	assert.Equal(t, value{Name: "worker", Count: 2}, decoded)
}

func TestJSONCodecRejectsEmptyAndMalformedValues(t *testing.T) {
	codec := JSONCodec[map[string]string]{}

	_, err := codec.Decode("  ")
	require.EqualError(t, err, "empty json value")

	_, err = codec.Decode(`{"name":`)
	assert.Error(t, err)
}
