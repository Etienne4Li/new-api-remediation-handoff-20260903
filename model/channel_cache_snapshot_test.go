package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withIsolatedChannelCache keeps these tests independent from the process-wide
// cache used by routing tests and by package initialisation.
func withIsolatedChannelCache(t *testing.T) {
	t.Helper()
	previousMemoryCache := common.MemoryCacheEnabled
	channelSyncLock.Lock()
	previousChannels := channelsIDM
	previousGroups := group2model2channels
	previousAdvanced := channel2advancedCustomConfig
	channelsIDM = make(map[int]*Channel)
	group2model2channels = make(map[string]map[string][]int)
	channel2advancedCustomConfig = make(map[int]*dto.AdvancedCustomConfig)
	channelSyncLock.Unlock()
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		channelSyncLock.Lock()
		channelsIDM = previousChannels
		group2model2channels = previousGroups
		channel2advancedCustomConfig = previousAdvanced
		channelSyncLock.Unlock()
		common.MemoryCacheEnabled = previousMemoryCache
	})
}

func testCachedChannel(id int) *Channel {
	weight := uint(10)
	priority := int64(3)
	setting := `{"proxy":"http://proxy.example"}`
	return &Channel{
		Id:       id,
		Type:     constant.ChannelTypeOpenAI,
		Key:      "key-a\nkey-b",
		Status:   common.ChannelStatusEnabled,
		Name:     "snapshot-test",
		Weight:   &weight,
		Priority: &priority,
		Group:    "default",
		Models:   "snapshot-model",
		Setting:  &setting,
		ChannelInfo: ChannelInfo{
			IsMultiKey:           true,
			MultiKeySize:         2,
			MultiKeyMode:         constant.MultiKeyModePolling,
			MultiKeyStatusList:   map[int]int{1: common.ChannelStatusAutoDisabled},
			MultiKeyDisabledTime: map[int]int64{1: 123},
			MultiKeyDisabledReason: map[int]string{
				1: "provider failure",
			},
		},
	}
}

func TestCacheGetChannelReturnsDetachedSnapshot(t *testing.T) {
	withIsolatedChannelCache(t)
	input := testCachedChannel(9101)
	CacheUpdateChannel(input)

	got, err := CacheGetChannel(input.Id)
	require.NoError(t, err)
	require.NotNil(t, got)
	*got.Weight = 999
	got.Key = "mutated"
	got.Keys = []string{"mutated"}
	got.ChannelInfo.MultiKeyStatusList[1] = common.ChannelStatusEnabled
	got.ChannelInfo.MultiKeyDisabledReason[1] = "mutated"
	got.ChannelInfo.MultiKeyDisabledTime[1] = 999
	*got.Setting = `{"proxy":"http://attacker.example"}`

	again, err := CacheGetChannel(input.Id)
	require.NoError(t, err)
	assert.Equal(t, uint(10), *again.Weight)
	assert.Equal(t, "key-a\nkey-b", again.Key)
	assert.Equal(t, common.ChannelStatusAutoDisabled, again.ChannelInfo.MultiKeyStatusList[1])
	assert.Equal(t, "provider failure", again.ChannelInfo.MultiKeyDisabledReason[1])
	assert.Equal(t, int64(123), again.ChannelInfo.MultiKeyDisabledTime[1])
	assert.Equal(t, "http://proxy.example", again.GetSetting().Proxy)
}

func TestCacheGetChannelRejectsNilCachedEntry(t *testing.T) {
	withIsolatedChannelCache(t)
	const channelID = 9108

	channelSyncLock.Lock()
	channelsIDM[channelID] = nil
	channelSyncLock.Unlock()

	got, err := CacheGetChannel(channelID)
	require.Error(t, err)
	assert.Nil(t, got)
}

func TestCacheGetChannelInfoRejectsNilCachedEntry(t *testing.T) {
	withIsolatedChannelCache(t)
	const channelID = 9109

	channelSyncLock.Lock()
	channelsIDM[channelID] = nil
	channelSyncLock.Unlock()

	got, err := CacheGetChannelInfo(channelID)
	require.Error(t, err)
	assert.Nil(t, got)
}

func TestCacheUpdateChannelDoesNotRetainCallerPointer(t *testing.T) {
	withIsolatedChannelCache(t)
	input := testCachedChannel(9102)
	CacheUpdateChannel(input)

	input.Name = "changed-after-publish"
	input.ChannelInfo.MultiKeyStatusList[1] = common.ChannelStatusEnabled
	*input.Weight = 77

	got, err := CacheGetChannel(input.Id)
	require.NoError(t, err)
	assert.Equal(t, "snapshot-test", got.Name)
	assert.Equal(t, common.ChannelStatusAutoDisabled, got.ChannelInfo.MultiKeyStatusList[1])
	assert.Equal(t, uint(10), *got.Weight)
}

func TestCacheUpdateChannelStatusReenablesRoutingIndex(t *testing.T) {
	withIsolatedChannelCache(t)
	channel := testCachedChannel(9103)
	channel.ChannelInfo.MultiKeyStatusList = nil
	CacheUpdateChannel(channel)

	CacheUpdateChannelStatus(channel.Id, common.ChannelStatusManuallyDisabled)
	selected, err := GetRandomSatisfiedChannel("default", "snapshot-model", 0, "", nil)
	require.NoError(t, err)
	assert.Nil(t, selected)

	CacheUpdateChannelStatus(channel.Id, common.ChannelStatusEnabled)
	selected, err = GetRandomSatisfiedChannel("default", "snapshot-model", 0, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, channel.Id, selected.Id)
}

func TestCachedPollingCursorAdvancesAcrossDetachedSnapshots(t *testing.T) {
	withIsolatedChannelCache(t)
	channel := testCachedChannel(9104)
	channel.ChannelInfo.MultiKeyStatusList = nil
	CacheUpdateChannel(channel)

	first, err := CacheGetChannel(channel.Id)
	require.NoError(t, err)
	key, index, apiErr := first.GetNextEnabledKey()
	require.Nil(t, apiErr)
	assert.Equal(t, "key-a", key)
	assert.Equal(t, 0, index)

	second, err := CacheGetChannel(channel.Id)
	require.NoError(t, err)
	key, index, apiErr = second.GetNextEnabledKey()
	require.Nil(t, apiErr)
	assert.Equal(t, "key-b", key)
	assert.Equal(t, 1, index)

	info, err := CacheGetChannelInfo(channel.Id)
	require.NoError(t, err)
	assert.Equal(t, 0, info.MultiKeyPollingIndex)
}

func TestCachedPollingUsesCanonicalKeyStatus(t *testing.T) {
	withIsolatedChannelCache(t)
	channel := testCachedChannel(9105)
	// The caller holds an older snapshot that believes key 0 is enabled.
	channel.ChannelInfo.MultiKeyStatusList = nil
	CacheUpdateChannel(channel)

	// Update only the canonical cache state; the detached receiver below must
	// not continue using its stale status map.
	channelSyncLock.Lock()
	channelsIDM[channel.Id].ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusAutoDisabled}
	channelSyncLock.Unlock()

	stale := cloneChannel(channel)
	key, index, apiErr := stale.GetNextEnabledKey()
	require.Nil(t, apiErr)
	assert.Equal(t, "key-b", key)
	assert.Equal(t, 1, index)
}

func TestCachedSelectionClampsNegativeRetry(t *testing.T) {
	withIsolatedChannelCache(t)
	channel := testCachedChannel(9106)
	channel.ChannelInfo.MultiKeyStatusList = nil
	CacheUpdateChannel(channel)

	selected, err := GetRandomSatisfiedChannel("default", "snapshot-model", -1, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, channel.Id, selected.Id)
}

func TestAddChannelToGroupIndexInitializesMissingGroupMap(t *testing.T) {
	withIsolatedChannelCache(t)
	channel := testCachedChannel(9107)
	channel.Group = "group-without-ability"
	channel.Models = "model-without-ability"

	channelSyncLock.Lock()
	channelsIDM[channel.Id] = channel
	// Deliberately leave group2model2channels empty, mirroring a cache refresh
	// where the abilities table has not caught up with the channel row.
	addChannelToGroupIndexLocked(channel)
	channelSyncLock.Unlock()

	selected, err := GetRandomSatisfiedChannel(channel.Group, channel.Models, 0, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, channel.Id, selected.Id)
}
