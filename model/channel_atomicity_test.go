package model

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func atomicityTestChannel(id int, name, modelName string) *Channel {
	weight := uint(100)
	priority := int64(1)
	return &Channel{
		Id: id, Name: name, Type: 1, Key: "test-key", Status: common.ChannelStatusEnabled,
		Models: modelName, Group: "default", Weight: &weight, Priority: &priority,
	}
}

func TestChannelInsertRollsBackWhenAbilityInsertFails(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec(`
CREATE TRIGGER fail_channel_insert_ability
BEFORE INSERT ON abilities
BEGIN
  SELECT RAISE(ABORT, 'forced ability insert failure');
END`).Error)
	t.Cleanup(func() { _ = DB.Exec("DROP TRIGGER IF EXISTS fail_channel_insert_ability").Error })

	channel := atomicityTestChannel(993001, "atomic-channel-insert", "gpt-atomic-insert")
	err := channel.Insert()
	require.ErrorContains(t, err, "forced ability insert failure")
	var channelCount int64
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Count(&channelCount).Error)
	assert.Zero(t, channelCount)
	var abilityCount int64
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", channel.Id).Count(&abilityCount).Error)
	assert.Zero(t, abilityCount)
}

func TestChannelUpdateRollsBackConfigAndAbilitiesTogether(t *testing.T) {
	truncateTables(t)
	channel := atomicityTestChannel(993002, "atomic-channel-update", "gpt-before-update")
	require.NoError(t, channel.Insert())
	require.NoError(t, DB.Exec(`
CREATE TRIGGER fail_channel_update_ability
BEFORE INSERT ON abilities
WHEN NEW.model = 'gpt-after-update'
BEGIN
  SELECT RAISE(ABORT, 'forced ability rebuild failure');
END`).Error)
	t.Cleanup(func() { _ = DB.Exec("DROP TRIGGER IF EXISTS fail_channel_update_ability").Error })

	channel.Models = "gpt-after-update"
	err := channel.Update()
	require.ErrorContains(t, err, "forced ability rebuild failure")
	var persisted Channel
	require.NoError(t, DB.First(&persisted, channel.Id).Error)
	assert.Equal(t, "gpt-before-update", persisted.Models)
	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "gpt-before-update", abilities[0].Model)
}

func TestChannelDeleteRollsBackWhenAbilityDeleteFails(t *testing.T) {
	truncateTables(t)
	channel := atomicityTestChannel(993003, "atomic-channel-delete", "gpt-atomic-delete")
	require.NoError(t, channel.Insert())
	require.NoError(t, DB.Exec(`
CREATE TRIGGER fail_channel_delete_ability
BEFORE DELETE ON abilities
WHEN OLD.channel_id = 993003
BEGIN
  SELECT RAISE(ABORT, 'forced ability delete failure');
END`).Error)
	t.Cleanup(func() { _ = DB.Exec("DROP TRIGGER IF EXISTS fail_channel_delete_ability").Error })

	err := channel.Delete()
	require.ErrorContains(t, err, "forced ability delete failure")
	var channelCount int64
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Count(&channelCount).Error)
	assert.EqualValues(t, 1, channelCount)
	var abilityCount int64
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", channel.Id).Count(&abilityCount).Error)
	assert.EqualValues(t, 1, abilityCount)
}

func TestChannelWeightBoundsAndLegacyRouting(t *testing.T) {
	tooLarge := MaxChannelWeight + 1
	require.Error(t, ValidateChannelWeight(&tooLarge))
	channel := atomicityTestChannel(993004, "oversized-channel-weight", "gpt-oversized-weight")
	channel.Weight = &tooLarge
	require.Error(t, channel.Insert())

	truncateTables(t)
	legacyWeight := uint(math.MaxInt64)
	channel = atomicityTestChannel(993005, "legacy-channel-weight", "gpt-legacy-weight")
	channel.Weight = &legacyWeight
	// Bypass the guarded model entry points to represent a legacy/corrupted row.
	require.NoError(t, DB.Create(channel).Error)
	priority := int64(1)
	require.NoError(t, DB.Create(&Ability{
		Group: "default", Model: channel.Models, ChannelId: channel.Id,
		Enabled: true, Priority: &priority, Weight: legacyWeight,
	}).Error)

	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	var selected *Channel
	require.NotPanics(t, func() {
		var err error
		selected, err = GetChannel("default", channel.Models, 0, "/v1/responses", nil)
		require.NoError(t, err)
	})
	require.NotNil(t, selected)
	assert.Equal(t, channel.Id, selected.Id)
}

func TestEditChannelByTagCanSetAbilityWeightToZero(t *testing.T) {
	truncateTables(t)
	channel := atomicityTestChannel(993006, "zero-channel-weight", "gpt-zero-weight")
	tag := "zero-weight-tag"
	channel.Tag = &tag
	require.NoError(t, channel.Insert())

	zero := uint(0)
	require.NoError(t, EditChannelByTag(tag, nil, nil, nil, nil, nil, &zero, nil, nil))
	var persisted Channel
	require.NoError(t, DB.First(&persisted, channel.Id).Error)
	require.NotNil(t, persisted.Weight)
	assert.Zero(t, *persisted.Weight)
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).First(&ability).Error)
	assert.Zero(t, ability.Weight)
}
