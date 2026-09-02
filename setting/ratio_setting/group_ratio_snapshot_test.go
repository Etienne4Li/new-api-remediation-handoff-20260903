package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupRatioSnapshotDetachesNestedMaps(t *testing.T) {
	original := GroupGroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, UpdateGroupGroupRatioByJSONString(original))
	})
	require.NoError(t, UpdateGroupGroupRatioByJSONString(`{"vip":{"model-a":0.75}}`))

	snapshot := GetGroupRatioSettingSnapshot()
	require.NotNil(t, snapshot)
	nested, ok := snapshot.GroupGroupRatio.Get("vip")
	require.True(t, ok)
	nested["model-a"] = 99
	snapshot.GroupGroupRatio.Set("vip", nested)

	ratio, ok := GetGroupGroupRatio("vip", "model-a")
	require.True(t, ok)
	assert.Equal(t, 0.75, ratio)
}
