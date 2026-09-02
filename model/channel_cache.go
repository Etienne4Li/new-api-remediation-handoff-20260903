package model

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

var group2model2channels map[string]map[string][]int // enabled channel
var channelsIDM map[int]*Channel                     // all channels include disabled
// channel2advancedCustomConfig caches parsed Advanced Custom (type 58) configs so
// path-aware selection avoids re-parsing JSON per request. Refreshed on full sync.
var channel2advancedCustomConfig map[int]*dto.AdvancedCustomConfig
var channelSyncLock sync.RWMutex

// cloneChannelInfo returns a detached copy of the mutable cache metadata.
// ChannelInfo contains maps, so a plain struct copy would still let callers
// mutate the cache while another request is reading it.
func cloneChannelInfo(info ChannelInfo) ChannelInfo {
	clone := info
	if info.MultiKeyStatusList != nil {
		clone.MultiKeyStatusList = make(map[int]int, len(info.MultiKeyStatusList))
		for index, status := range info.MultiKeyStatusList {
			clone.MultiKeyStatusList[index] = status
		}
	}
	if info.MultiKeyDisabledReason != nil {
		clone.MultiKeyDisabledReason = make(map[int]string, len(info.MultiKeyDisabledReason))
		for index, reason := range info.MultiKeyDisabledReason {
			clone.MultiKeyDisabledReason[index] = reason
		}
	}
	if info.MultiKeyDisabledTime != nil {
		clone.MultiKeyDisabledTime = make(map[int]int64, len(info.MultiKeyDisabledTime))
		for index, disabledAt := range info.MultiKeyDisabledTime {
			clone.MultiKeyDisabledTime[index] = disabledAt
		}
	}
	return clone
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneUintPointer(value *uint) *uint {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// cloneChannel detaches every pointer/map/slice in a Channel. Cache readers
// intentionally receive this snapshot rather than the object held in
// channelsIDM; otherwise a request that edits a returned pointer can race
// with channel selection and hot-cache refreshes.
func cloneChannel(channel *Channel) *Channel {
	if channel == nil {
		return nil
	}
	clone := *channel
	clone.OpenAIOrganization = cloneStringPointer(channel.OpenAIOrganization)
	clone.TestModel = cloneStringPointer(channel.TestModel)
	clone.Weight = cloneUintPointer(channel.Weight)
	clone.BaseURL = cloneStringPointer(channel.BaseURL)
	clone.ModelMapping = cloneStringPointer(channel.ModelMapping)
	clone.StatusCodeMapping = cloneStringPointer(channel.StatusCodeMapping)
	clone.Priority = cloneInt64Pointer(channel.Priority)
	clone.AutoBan = cloneIntPointer(channel.AutoBan)
	clone.Tag = cloneStringPointer(channel.Tag)
	clone.Setting = cloneStringPointer(channel.Setting)
	clone.ParamOverride = cloneStringPointer(channel.ParamOverride)
	clone.HeaderOverride = cloneStringPointer(channel.HeaderOverride)
	clone.Remark = cloneStringPointer(channel.Remark)
	clone.ChannelInfo = cloneChannelInfo(channel.ChannelInfo)
	clone.Keys = append([]string(nil), channel.Keys...)
	return &clone
}

func InitChannelCache() {
	if !common.MemoryCacheEnabled {
		InvalidatePricingCache()
		return
	}
	newChannelId2channel := make(map[int]*Channel)
	newChannel2advancedCustomConfig := make(map[int]*dto.AdvancedCustomConfig)
	var channels []*Channel
	if DB == nil {
		common.SysLog("channel cache refresh skipped: database is not initialized")
		return
	}
	if err := DB.Find(&channels).Error; err != nil {
		// Keep the last known-good snapshot. Publishing an empty cache after a
		// database outage would make async polling interpret every channel as
		// deleted and refund provider tasks that are still running.
		common.SysLog("channel cache refresh failed: " + err.Error())
		return
	}
	for _, channel := range channels {
		newChannelId2channel[channel.Id] = channel
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
				newChannel2advancedCustomConfig[channel.Id] = config
			}
		}
	}
	var abilities []*Ability
	if err := DB.Find(&abilities).Error; err != nil {
		common.SysLog("channel ability cache refresh failed: " + err.Error())
		return
	}
	groups := make(map[string]bool)
	for _, ability := range abilities {
		groups[ability.Group] = true
	}
	newGroup2model2channels := make(map[string]map[string][]int)
	for group := range groups {
		newGroup2model2channels[group] = make(map[string][]int)
	}
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue // skip disabled channels
		}
		groups := strings.Split(channel.Group, ",")
		for _, group := range groups {
			// An enabled channel can temporarily have no corresponding Ability
			// row (for example while an admin edit transaction is rebuilding
			// abilities). Ensure the nested map exists before publishing the
			// cache, otherwise the assignment below panics on a nil map.
			if newGroup2model2channels[group] == nil {
				newGroup2model2channels[group] = make(map[string][]int)
			}
			models := strings.Split(channel.Models, ",")
			for _, model := range models {
				if _, ok := newGroup2model2channels[group][model]; !ok {
					newGroup2model2channels[group][model] = make([]int, 0)
				}
				newGroup2model2channels[group][model] = append(newGroup2model2channels[group][model], channel.Id)
			}
		}
	}

	// sort by priority
	for group, model2channels := range newGroup2model2channels {
		for model, channels := range model2channels {
			sort.Slice(channels, func(i, j int) bool {
				return newChannelId2channel[channels[i]].GetPriority() > newChannelId2channel[channels[j]].GetPriority()
			})
			newGroup2model2channels[group][model] = channels
		}
	}

	channelSyncLock.Lock()
	group2model2channels = newGroup2model2channels
	//channelsIDM = newChannelId2channel
	for i, channel := range newChannelId2channel {
		if channel.ChannelInfo.IsMultiKey {
			channel.Keys = channel.GetKeys()
			if channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
				if oldChannel, ok := channelsIDM[i]; ok {
					// 存在旧的渠道，如果是多key且轮询，保留轮询索引信息
					if oldChannel.ChannelInfo.IsMultiKey && oldChannel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
						channel.ChannelInfo.MultiKeyPollingIndex = oldChannel.ChannelInfo.MultiKeyPollingIndex
					}
				}
			}
		}
	}
	channelsIDM = newChannelId2channel
	channel2advancedCustomConfig = newChannel2advancedCustomConfig
	channelSyncLock.Unlock()
	// Lock ordering: InvalidatePricingCache acquires updatePricingLock, and
	// GetPricing (holding updatePricingLock) nests channelSyncLock.RLock via
	// loadPricingAdvancedCustomConfigs. channelSyncLock MUST be released before
	// invalidating the pricing cache, otherwise the reversed order deadlocks.
	InvalidatePricingCache()
	common.SysLog("channels synced from database")
}

func SyncChannelCache(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		common.SysLog("syncing channels from database")
		InitChannelCache()
	}
}

func GetRandomSatisfiedChannel(group string, model string, retry int, requestPath string, excludedChannelIDs map[int]struct{}) (*Channel, error) {
	// if memory cache is disabled, get channel directly from database
	if !common.MemoryCacheEnabled {
		return GetChannel(group, model, retry, requestPath, excludedChannelIDs)
	}

	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	// First, try to find channels with the exact model name.
	channels := filterChannelsByRequestPathAndModel(group2model2channels[group][model], requestPath, model)

	// If no channels found, try to find channels with the normalized model name.
	if len(channels) == 0 {
		normalizedModel := ratio_setting.FormatMatchingModelName(model)
		channels = filterChannelsByRequestPathAndModel(group2model2channels[group][normalizedModel], requestPath, model)
	}
	if len(excludedChannelIDs) > 0 {
		candidates := make([]int, 0, len(channels))
		for _, channelID := range channels {
			if _, excluded := excludedChannelIDs[channelID]; !excluded {
				candidates = append(candidates, channelID)
			}
		}
		channels = candidates
		// Exclusion already advances to a different channel. Start priority
		// selection from the highest priority still represented by candidates.
		retry = 0
	}

	if len(channels) == 0 {
		return nil, nil
	}
	if retry < 0 {
		retry = 0
	}

	if len(channels) == 1 {
		if channel, ok := channelsIDM[channels[0]]; ok {
			return cloneChannel(channel), nil
		}
		return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channels[0])
	}

	uniquePriorities := make(map[int]bool)
	for _, channelId := range channels {
		if channel, ok := channelsIDM[channelId]; ok {
			uniquePriorities[int(channel.GetPriority())] = true
		} else {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channelId)
		}
	}
	var sortedUniquePriorities []int
	for priority := range uniquePriorities {
		sortedUniquePriorities = append(sortedUniquePriorities, priority)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sortedUniquePriorities)))

	if retry >= len(uniquePriorities) {
		retry = len(uniquePriorities) - 1
	}
	targetPriority := int64(sortedUniquePriorities[retry])

	// get the priority for the given retry number
	var sumWeight = 0
	var targetChannels []*Channel
	for _, channelId := range channels {
		if channel, ok := channelsIDM[channelId]; ok {
			if channel.GetPriority() == targetPriority {
				sumWeight += channel.GetWeight()
				targetChannels = append(targetChannels, channel)
			}
		} else {
			return nil, fmt.Errorf("数据库一致性错误，渠道# %d 不存在，请联系管理员修复", channelId)
		}
	}

	if len(targetChannels) == 0 {
		return nil, errors.New(fmt.Sprintf("no channel found, group: %s, model: %s, priority: %d", group, model, targetPriority))
	}

	// smoothing factor and adjustment
	smoothingFactor := 1
	smoothingAdjustment := 0

	if sumWeight == 0 {
		// when all channels have weight 0, set sumWeight to the number of channels and set smoothing adjustment to 100
		// each channel's effective weight = 100
		sumWeight = len(targetChannels) * 100
		smoothingAdjustment = 100
	} else if sumWeight/len(targetChannels) < 10 {
		// when the average weight is less than 10, set smoothing factor to 100
		smoothingFactor = 100
	}

	// Calculate the total weight of all channels up to endIdx
	totalWeight := sumWeight * smoothingFactor

	// Generate a random value in the range [0, totalWeight)
	randomWeight := rand.Intn(totalWeight)

	// Find a channel based on its weight
	for _, channel := range targetChannels {
		randomWeight -= channel.GetWeight()*smoothingFactor + smoothingAdjustment
		if randomWeight < 0 {
			return cloneChannel(channel), nil
		}
	}
	// return null if no channel is not found
	return nil, errors.New("channel not found")
}

// filterChannelsByRequestPathAndModel restricts candidates by request path and
// model. Native channel capabilities are checked before the optional Advanced
// Custom route matcher so an unsupported adaptor is never selected for a known
// endpoint. Unknown/custom paths remain eligible and are validated downstream.
// Caller must hold channelSyncLock (read lock). The cached slice is never mutated.
func filterChannelsByRequestPathAndModel(channels []int, requestPath string, model string) []int {
	if requestPath == "" || len(channels) == 0 {
		return channels
	}
	canonicalPath := common.CanonicalRelayRequestPath(requestPath)
	filtered := make([]int, 0, len(channels))
	for _, channelId := range channels {
		channel, ok := channelsIDM[channelId]
		if !ok {
			// keep it so the downstream consistency error is raised as before
			filtered = append(filtered, channelId)
			continue
		}
		if !common.ChannelSupportsRequestPath(channel.Type, model, canonicalPath) {
			continue
		}
		if channel.Type != constant.ChannelTypeAdvancedCustom {
			filtered = append(filtered, channelId)
			continue
		}
		if config := channel2advancedCustomConfig[channelId]; config != nil && config.SupportsPathForModel(canonicalPath, model) {
			filtered = append(filtered, channelId)
		}
	}
	return filtered
}

func CacheGetChannel(id int) (*Channel, error) {
	if !common.MemoryCacheEnabled {
		channel, err := GetChannelById(id, true)
		if err != nil {
			return nil, err
		}
		return cloneChannel(channel), nil
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	c, ok := channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("%w: 渠道# %d，已不存在", ErrChannelNotFound, id)
	}
	// A cache hit must never return a nil channel with a nil error.  That
	// violates the lookup contract and turns every caller that dereferences the
	// returned snapshot into a panic.  Treat a corrupted/partially published
	// entry exactly like a missing channel so callers can fail closed.
	if c == nil {
		return nil, fmt.Errorf("渠道# %d，缓存数据为空", id)
	}
	return cloneChannel(c), nil
}

func CacheGetChannelInfo(id int) (*ChannelInfo, error) {
	if !common.MemoryCacheEnabled {
		channel, err := GetChannelById(id, true)
		if err != nil {
			return nil, err
		}
		info := cloneChannelInfo(channel.ChannelInfo)
		return &info, nil
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	c, ok := channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("渠道# %d，已不存在", id)
	}
	// Keep the same non-nil-on-success contract as CacheGetChannel. A
	// partially published/corrupted cache entry must fail closed rather than
	// dereferencing c below.
	if c == nil {
		return nil, fmt.Errorf("渠道# %d，缓存数据为空", id)
	}
	info := cloneChannelInfo(c.ChannelInfo)
	return &info, nil
}

func CacheUpdateChannelStatus(id int, status int) {
	if !common.MemoryCacheEnabled {
		return
	}
	channelSyncLock.Lock()
	defer channelSyncLock.Unlock()
	channel, ok := channelsIDM[id]
	if !ok || channel == nil {
		return
	}
	channel.Status = status
	removeChannelFromGroupIndexLocked(id)
	if status == common.ChannelStatusEnabled {
		addChannelToGroupIndexLocked(channel)
	}
}

func CacheUpdateChannel(channel *Channel) {
	if !common.MemoryCacheEnabled {
		return
	}
	if channel == nil {
		return
	}
	// Never retain a caller-owned pointer. Controllers and background workers
	// often continue mutating their DB snapshot after publishing it here.
	snapshot := cloneChannel(channel)
	// Keep the critical section in a closure so the unlock is deferred on every
	// panic path, while pricing invalidation still happens after releasing the
	// channel lock (the pricing code acquires the locks in the opposite order).
	func() {
		channelSyncLock.Lock()
		defer channelSyncLock.Unlock()

		if channelsIDM == nil {
			channelsIDM = make(map[int]*Channel)
		}
		if oldChannel, ok := channelsIDM[snapshot.Id]; ok {
			logger.LogDebug(nil, "CacheUpdateChannel before: id=%d, name=%s, status=%d, polling_index=%d", snapshot.Id, snapshot.Name, snapshot.Status, oldChannel.ChannelInfo.MultiKeyPollingIndex)
			// Polling cursors are process-local when memory cache is enabled. A DB
			// refresh must not reset a cursor advanced by an in-flight request.
			if oldChannel.ChannelInfo.IsMultiKey && oldChannel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling &&
				snapshot.ChannelInfo.IsMultiKey && snapshot.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
				snapshot.ChannelInfo.MultiKeyPollingIndex = oldChannel.ChannelInfo.MultiKeyPollingIndex
			}
			removeChannelFromGroupIndexLocked(snapshot.Id)
		}
		channelsIDM[snapshot.Id] = snapshot
		if channel2advancedCustomConfig == nil {
			channel2advancedCustomConfig = make(map[int]*dto.AdvancedCustomConfig)
		}
		delete(channel2advancedCustomConfig, snapshot.Id)
		if snapshot.Type == constant.ChannelTypeAdvancedCustom {
			if config := snapshot.GetOtherSettings().AdvancedCustom; config != nil {
				channel2advancedCustomConfig[snapshot.Id] = config
			}
		}
		if snapshot.Status == common.ChannelStatusEnabled {
			addChannelToGroupIndexLocked(snapshot)
		}
		logger.LogDebug(nil, "CacheUpdateChannel after: id=%d, name=%s, status=%d, polling_index=%d", snapshot.Id, snapshot.Name, snapshot.Status, snapshot.ChannelInfo.MultiKeyPollingIndex)
	}()
	// Lock ordering: do NOT hold channelSyncLock while calling
	// InvalidatePricingCache. GetPricing acquires updatePricingLock first and then
	// channelSyncLock.RLock (via loadPricingAdvancedCustomConfigs); acquiring
	// updatePricingLock while holding channelSyncLock would be an AB-BA deadlock.
	InvalidatePricingCache()
}

// removeChannelFromGroupIndexLocked removes all occurrences of id from the
// enabled-channel index. The caller must hold channelSyncLock.
func removeChannelFromGroupIndexLocked(id int) {
	for group, model2channels := range group2model2channels {
		for model, channels := range model2channels {
			filtered := channels[:0]
			for _, channelID := range channels {
				if channelID != id {
					filtered = append(filtered, channelID)
				}
			}
			if len(filtered) == 0 {
				delete(model2channels, model)
			} else {
				model2channels[model] = filtered
			}
		}
		if len(model2channels) == 0 {
			delete(group2model2channels, group)
		}
	}
}

// addChannelToGroupIndexLocked inserts an enabled channel into every
// configured group/model bucket, preserving priority order and avoiding
// duplicates. The caller must hold channelSyncLock.
func addChannelToGroupIndexLocked(channel *Channel) {
	if channel == nil || channel.Status != common.ChannelStatusEnabled {
		return
	}
	if group2model2channels == nil {
		group2model2channels = make(map[string]map[string][]int)
	}
	for _, group := range channel.GetGroups() {
		if group == "" {
			continue
		}
		if group2model2channels[group] == nil {
			group2model2channels[group] = make(map[string][]int)
		}
		for _, modelName := range channel.GetModels() {
			if modelName == "" {
				continue
			}
			bucket := group2model2channels[group][modelName]
			alreadyPresent := false
			for _, channelID := range bucket {
				if channelID == channel.Id {
					alreadyPresent = true
					break
				}
			}
			if alreadyPresent {
				continue
			}
			bucket = append(bucket, channel.Id)
			sort.SliceStable(bucket, func(i, j int) bool {
				left, leftOK := channelsIDM[bucket[i]]
				right, rightOK := channelsIDM[bucket[j]]
				if !leftOK || !rightOK {
					return leftOK
				}
				if !rightOK {
					return false
				}
				return left.GetPriority() > right.GetPriority()
			})
			group2model2channels[group][modelName] = bucket
		}
	}
}

// updateCachedChannelPollingIndex publishes the process-local polling cursor
// while retaining the cache's copy-on-write boundary. The caller must hold
// the per-channel polling lock; channelSyncLock serializes cache readers.
func updateCachedChannelPollingIndex(id, index int) {
	if !common.MemoryCacheEnabled {
		return
	}
	channelSyncLock.Lock()
	if channel, ok := channelsIDM[id]; ok && channel != nil {
		channel.ChannelInfo.MultiKeyPollingIndex = index
	}
	channelSyncLock.Unlock()
}
