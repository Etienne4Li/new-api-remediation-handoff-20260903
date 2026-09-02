package model

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Ability struct {
	Group     string  `json:"group" gorm:"type:varchar(64);primaryKey;autoIncrement:false"`
	Model     string  `json:"model" gorm:"type:varchar(255);primaryKey;autoIncrement:false"`
	ChannelId int     `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool    `json:"enabled"`
	Priority  *int64  `json:"priority" gorm:"bigint;default:0;index"`
	Weight    uint    `json:"weight" gorm:"default:0;index"`
	Tag       *string `json:"tag" gorm:"index"`
}

type AbilityWithChannel struct {
	Ability
	ChannelType int `json:"channel_type"`
}

func GetAllEnableAbilityWithChannels() ([]AbilityWithChannel, error) {
	var abilities []AbilityWithChannel
	err := DB.Table("abilities").
		Select("abilities.*, channels.type as channel_type").
		Joins("left join channels on abilities.channel_id = channels.id").
		Where("abilities.enabled = ?", true).
		Scan(&abilities).Error
	return abilities, err
}

func GetGroupEnabledModels(group string) []string {
	models, _ := GetGroupEnabledModelsWithError(group)
	return models
}

func GetGroupEnabledModelsWithError(group string) ([]string, error) {
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	var models []string
	// Find distinct models
	err := DB.Table("abilities").Where(commonGroupCol+" = ? and enabled = ?", group, true).Distinct("model").Pluck("model", &models).Error
	return models, err
}

// GetEnabledModelsWithError returns the distinct enabled model names and
// preserves database errors for callers that need to fail closed.  The legacy
// GetEnabledModels helper remains below for read-only call sites that can only
// expose a best-effort list.
func GetEnabledModelsWithError() ([]string, error) {
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	var models []string
	// Find distinct models
	err := DB.Table("abilities").Where("enabled = ?", true).Distinct("model").Pluck("model", &models).Error
	return models, err
}

func GetEnabledModels() []string {
	models, _ := GetEnabledModelsWithError()
	return models
}

func GetAllEnableAbilities() []Ability {
	var abilities []Ability
	DB.Find(&abilities, "enabled = ?", true)
	return abilities
}

func getPriority(group string, model string, retry int) (int, error) {

	var priorities []int
	err := DB.Model(&Ability{}).
		Select("DISTINCT(priority)").
		Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, model, true).
		Order("priority DESC").              // 按优先级降序排序
		Pluck("priority", &priorities).Error // Pluck用于将查询的结果直接扫描到一个切片中

	if err != nil {
		// 处理错误
		return 0, err
	}

	if len(priorities) == 0 {
		// 如果没有查询到优先级，则返回错误
		return 0, errors.New("数据库一致性被破坏")
	}

	// 确定要使用的优先级
	var priorityToUse int
	if retry >= len(priorities) {
		// 如果重试次数大于优先级数，则使用最小的优先级
		priorityToUse = priorities[len(priorities)-1]
	} else {
		priorityToUse = priorities[retry]
	}
	return priorityToUse, nil
}

func getChannelQuery(group string, model string, retry int, excludedChannelIDs map[int]struct{}) (*gorm.DB, error) {
	baseCondition := commonGroupCol + " = ? and model = ? and enabled = ?"
	maxPrioritySubQuery := DB.Model(&Ability{}).
		Select("MAX(priority)").
		Where(baseCondition, group, model, true)
	channelQuery := DB.Where(baseCondition, group, model, true)

	if len(excludedChannelIDs) > 0 {
		excludedIDs := make([]int, 0, len(excludedChannelIDs))
		for channelID := range excludedChannelIDs {
			excludedIDs = append(excludedIDs, channelID)
		}
		sort.Ints(excludedIDs)
		maxPrioritySubQuery = maxPrioritySubQuery.Where("channel_id NOT IN ?", excludedIDs)
		channelQuery = channelQuery.Where("channel_id NOT IN ?", excludedIDs)
		// Once a failed channel is excluded, always take the highest priority
		// among the remaining candidates instead of advancing two dimensions.
		retry = 0
	}

	channelQuery = channelQuery.Where("priority = (?)", maxPrioritySubQuery)
	if retry != 0 {
		priority, err := getPriority(group, model, retry)
		if err != nil {
			return nil, err
		} else {
			channelQuery = DB.Where(commonGroupCol+" = ? and model = ? and enabled = ? and priority = ?", group, model, true, priority)
		}
	}

	return channelQuery, nil
}

func GetChannel(group string, model string, retry int, requestPath string, excludedChannelIDs map[int]struct{}) (*Channel, error) {
	if DB == nil {
		return nil, fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	var abilities []Ability

	// Load all priorities before filtering by endpoint capability. This is
	// important for both built-in and custom paths: an unsupported high-priority
	// channel must not hide a lower-priority fallback that has a matching route.
	baseCondition := commonGroupCol + " = ? and model = ? and enabled = ?"
	channelQuery := DB.Where(baseCondition, group, model, true)
	if len(excludedChannelIDs) > 0 {
		excludedIDs := make([]int, 0, len(excludedChannelIDs))
		for channelID := range excludedChannelIDs {
			excludedIDs = append(excludedIDs, channelID)
		}
		sort.Ints(excludedIDs)
		channelQuery = channelQuery.Where("channel_id NOT IN ?", excludedIDs)
	}
	if err := channelQuery.Order("priority DESC, weight DESC").Find(&abilities).Error; err != nil {
		return nil, err
	}
	abilities, err := filterAbilitiesByRequestPathAndModel(abilities, requestPath, model)
	if err != nil {
		return nil, err
	}
	if len(abilities) == 0 {
		return nil, nil
	}

	priorities := make([]int64, 0, len(abilities))
	seenPriorities := make(map[int64]struct{}, len(abilities))
	for _, ability := range abilities {
		priority := int64(0)
		if ability.Priority != nil {
			priority = *ability.Priority
		}
		if _, seen := seenPriorities[priority]; seen {
			continue
		}
		seenPriorities[priority] = struct{}{}
		priorities = append(priorities, priority)
	}
	if len(excludedChannelIDs) > 0 {
		// Excluding a failed channel already advances the candidate set; keep
		// the highest remaining priority instead of advancing twice.
		retry = 0
	}
	if retry < 0 {
		retry = 0
	}
	if retry >= len(priorities) {
		retry = len(priorities) - 1
	}
	targetPriority := priorities[retry]
	priorityAbilities := make([]Ability, 0, len(abilities))
	for _, ability := range abilities {
		priority := int64(0)
		if ability.Priority != nil {
			priority = *ability.Priority
		}
		if priority == targetPriority {
			priorityAbilities = append(priorityAbilities, ability)
		}
	}
	abilities = priorityAbilities
	channel := Channel{}
	if len(abilities) > 0 {
		// Randomly choose one. Clamp legacy out-of-range weights and keep the
		// accumulation signed-safe; malformed persisted values must not reach
		// rand.Intn with a zero/negative bound and panic the request path.
		weightSum := int64(0)
		for _, ability_ := range abilities {
			weight := ability_.Weight
			if weight > MaxChannelWeight {
				weight = MaxChannelWeight
			}
			effectiveWeight := int64(weight) + 10
			if weightSum > math.MaxInt64-effectiveWeight {
				weightSum = math.MaxInt64
				break
			}
			weightSum += effectiveWeight
		}
		if weightSum <= 0 {
			return nil, errors.New("invalid channel weight total")
		}
		weight := rand.Int63n(weightSum)
		for _, ability_ := range abilities {
			abilityWeight := ability_.Weight
			if abilityWeight > MaxChannelWeight {
				abilityWeight = MaxChannelWeight
			}
			weight -= int64(abilityWeight) + 10
			if weight < 0 {
				channel.Id = ability_.ChannelId
				break
			}
		}
	} else {
		return nil, nil
	}
	err = DB.First(&channel, "id = ?", channel.Id).Error
	return &channel, err
}

// filterAbilitiesByRequestPathAndModel restricts candidates by request path and
// model for the DB (non-memory-cache) selection path. Native channel
// capabilities are checked first, followed by the Advanced Custom route
// matcher. Unknown/custom paths remain eligible and are validated downstream.
func filterAbilitiesByRequestPathAndModel(abilities []Ability, requestPath string, model string) ([]Ability, error) {
	if requestPath == "" || len(abilities) == 0 {
		return abilities, nil
	}
	canonicalPath := common.CanonicalRelayRequestPath(requestPath)

	channelIds := make([]int, 0, len(abilities))
	seen := make(map[int]struct{}, len(abilities))
	for _, ability := range abilities {
		if _, ok := seen[ability.ChannelId]; ok {
			continue
		}
		seen[ability.ChannelId] = struct{}{}
		channelIds = append(channelIds, ability.ChannelId)
	}

	var channels []*Channel
	if err := DB.Where("id IN ?", channelIds).Find(&channels).Error; err != nil {
		return nil, err
	}

	advancedConfigs := make(map[int]*dto.AdvancedCustomConfig)
	for _, channel := range channels {
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			advancedConfigs[channel.Id] = channel.GetOtherSettings().AdvancedCustom
		}
	}

	filtered := make([]Ability, 0, len(abilities))
	for _, ability := range abilities {
		config, isAdvancedCustom := advancedConfigs[ability.ChannelId]
		channelType := constant.ChannelTypeUnknown
		for _, candidate := range channels {
			if candidate.Id == ability.ChannelId {
				channelType = candidate.Type
				break
			}
		}
		if !common.ChannelSupportsRequestPath(channelType, model, canonicalPath) {
			continue
		}
		if !isAdvancedCustom {
			filtered = append(filtered, ability)
			continue
		}
		if config != nil && config.SupportsPathForModel(canonicalPath, model) {
			filtered = append(filtered, ability)
		}
	}
	return filtered, nil
}

func (channel *Channel) AddAbilities(tx *gorm.DB) error {
	models_ := strings.Split(channel.Models, ",")
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "|" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  channel.Priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}
	if len(abilities) == 0 {
		return nil
	}
	// choose DB or provided tx
	useDB := DB
	if tx != nil {
		useDB = tx
	}
	for _, chunk := range lo.Chunk(abilities, 50) {
		err := useDB.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func (channel *Channel) DeleteAbilities() error {
	if channel == nil || channel.Id <= 0 {
		return errors.New("invalid channel")
	}
	if DB == nil {
		return fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	return DB.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
}

// UpdateAbilities updates abilities of this channel.
// Make sure the channel is completed before calling this function.
func (channel *Channel) UpdateAbilities(tx *gorm.DB) error {
	if channel == nil || channel.Id <= 0 {
		return errors.New("invalid channel")
	}
	if tx == nil {
		if DB == nil {
			return fmt.Errorf("%w: database is not initialized", ErrDatabase)
		}
		return DB.Transaction(func(inner *gorm.DB) error {
			return channel.UpdateAbilities(inner)
		})
	}

	// First delete all abilities of this channel
	err := tx.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
	if err != nil {
		return err
	}

	// Then add new abilities
	models_ := strings.Split(channel.Models, ",")
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "|" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  channel.Priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}

	if len(abilities) > 0 {
		for _, chunk := range lo.Chunk(abilities, 50) {
			err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func UpdateAbilityStatus(channelId int, status bool) error {
	return DB.Model(&Ability{}).Where("channel_id = ?", channelId).Select("enabled").Update("enabled", status).Error
}

func UpdateAbilityStatusByTag(tag string, status bool) error {
	return DB.Model(&Ability{}).Where("tag = ?", tag).Select("enabled").Update("enabled", status).Error
}

func UpdateAbilityByTag(tag string, newTag *string, priority *int64, weight *uint) error {
	if DB == nil {
		return fmt.Errorf("%w: database is not initialized", ErrDatabase)
	}
	return updateAbilityByTagTx(DB, tag, newTag, priority, weight)
}

func updateAbilityByTagTx(tx *gorm.DB, tag string, newTag *string, priority *int64, weight *uint) error {
	if tx == nil {
		return errors.New("transaction is nil")
	}
	if err := ValidateChannelWeight(weight); err != nil {
		return err
	}
	updates := make(map[string]interface{}, 3)
	if newTag != nil {
		updates["tag"] = *newTag
	}
	if priority != nil {
		updates["priority"] = *priority
	}
	if weight != nil {
		updates["weight"] = *weight
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.Model(&Ability{}).Where("tag = ?", tag).Updates(updates).Error
}

var fixLock = sync.Mutex{}

func FixAbility() (int, int, error) {
	lock := fixLock.TryLock()
	if !lock {
		return 0, 0, errors.New("已经有一个修复任务在运行中，请稍后再试")
	}
	defer fixLock.Unlock()

	// truncate abilities table
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		err := DB.Exec("DELETE FROM abilities").Error
		if err != nil {
			common.SysLog(fmt.Sprintf("Delete abilities failed: %s", err.Error()))
			return 0, 0, err
		}
	} else {
		err := DB.Exec("TRUNCATE TABLE abilities").Error
		if err != nil {
			common.SysLog(fmt.Sprintf("Truncate abilities failed: %s", err.Error()))
			return 0, 0, err
		}
	}
	var channels []*Channel
	// Find all channels
	err := DB.Model(&Channel{}).Find(&channels).Error
	if err != nil {
		return 0, 0, err
	}
	if len(channels) == 0 {
		return 0, 0, nil
	}
	successCount := 0
	failCount := 0
	for _, chunk := range lo.Chunk(channels, 50) {
		ids := lo.Map(chunk, func(c *Channel, _ int) int { return c.Id })
		// Delete all abilities of this channel
		err = DB.Where("channel_id IN ?", ids).Delete(&Ability{}).Error
		if err != nil {
			common.SysLog(fmt.Sprintf("Delete abilities failed: %s", err.Error()))
			failCount += len(chunk)
			continue
		}
		// Then add new abilities
		for _, channel := range chunk {
			err = channel.AddAbilities(nil)
			if err != nil {
				common.SysLog(fmt.Sprintf("Add abilities for channel %d failed: %s", channel.Id, err.Error()))
				failCount++
			} else {
				successCount++
			}
		}
	}
	InitChannelCache()
	return successCount, failCount, nil
}
