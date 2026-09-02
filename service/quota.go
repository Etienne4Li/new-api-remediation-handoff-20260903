package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type TokenDetails struct {
	TextTokens   int
	AudioTokens  int
	CachedTokens int
	ImageTokens  int
}

type QuotaInfo struct {
	InputDetails  TokenDetails
	OutputDetails TokenDetails
	ModelName     string
	UsePrice      bool
	ModelPrice    float64
	ModelRatio    float64
	GroupRatio    float64
	CacheRatio    float64
	ImageRatio    float64
	// Optional request-scoped pricing fields. A positive value is treated as a
	// frozen snapshot; zero keeps the historical fallback for legacy callers
	// that construct QuotaInfo directly. ModelRatio has its own explicit marker
	// on RelayInfo because zero is a valid free-model rate.
	CompletionRatio      float64
	AudioRatio           float64
	AudioCompletionRatio float64
	// PriceSnapshotReady distinguishes a request-scoped ratio snapshot from
	// legacy callers that only populate the fields they know about.  ModelRatio
	// is supplied separately by the caller because zero is a valid free-model
	// rate.  The auxiliary ratios retain their historical non-positive fallback
	// so partially populated compatibility structs remain usable.
	PriceSnapshotReady bool
}

// normalizeRealtimeQuotaUsage canonicalises provider usage before either
// reservation or settlement. Realtime providers may omit detail objects and
// can return malformed negative counters; treating aggregate-only usage as
// zero would undercharge, while allowing negatives would make the result
// non-deterministic across pricing paths.
func normalizeRealtimeQuotaUsage(src *dto.RealtimeUsage) dto.RealtimeUsage {
	if src == nil {
		return dto.RealtimeUsage{}
	}
	value := *src
	nonNegative := func(v int) int {
		if v < 0 {
			return 0
		}
		return v
	}
	value.TotalTokens = nonNegative(value.TotalTokens)
	value.InputTokens = nonNegative(value.InputTokens)
	value.OutputTokens = nonNegative(value.OutputTokens)
	value.InputTokenDetails.CachedTokens = nonNegative(value.InputTokenDetails.CachedTokens)
	value.InputTokenDetails.TextTokens = nonNegative(value.InputTokenDetails.TextTokens)
	value.InputTokenDetails.AudioTokens = nonNegative(value.InputTokenDetails.AudioTokens)
	value.InputTokenDetails.ImageTokens = nonNegative(value.InputTokenDetails.ImageTokens)
	value.InputTokenDetails.CachedCreationTokens = nonNegative(value.InputTokenDetails.CachedCreationTokens)
	value.InputTokenDetails.CacheWriteTokens = nonNegative(value.InputTokenDetails.CacheWriteTokens)
	value.OutputTokenDetails.TextTokens = nonNegative(value.OutputTokenDetails.TextTokens)
	value.OutputTokenDetails.AudioTokens = nonNegative(value.OutputTokenDetails.AudioTokens)
	value.OutputTokenDetails.ImageTokens = nonNegative(value.OutputTokenDetails.ImageTokens)
	value.OutputTokenDetails.ReasoningTokens = nonNegative(value.OutputTokenDetails.ReasoningTokens)

	inputDetails := common.SaturatingAddNonNegativeInt(
		value.InputTokenDetails.TextTokens,
		value.InputTokenDetails.AudioTokens,
		value.InputTokenDetails.ImageTokens,
	)
	if inputDetails == 0 && value.InputTokens > 0 {
		value.InputTokenDetails.TextTokens = value.InputTokens
		inputDetails = value.InputTokens
	}
	if value.InputTokens < inputDetails {
		value.InputTokens = inputDetails
	} else if value.InputTokens > inputDetails {
		// Preserve aggregate-only residual tokens (for example cached/context
		// dimensions omitted by an older provider) in the billable text bucket.
		value.InputTokenDetails.TextTokens = common.SaturatingAddNonNegativeInt(
			value.InputTokenDetails.TextTokens,
			value.InputTokens-inputDetails,
		)
	}

	outputDetails := common.SaturatingAddNonNegativeInt(
		value.OutputTokenDetails.TextTokens,
		value.OutputTokenDetails.AudioTokens,
		value.OutputTokenDetails.ImageTokens,
	)
	if outputDetails == 0 && value.OutputTokens > 0 {
		value.OutputTokenDetails.TextTokens = value.OutputTokens
		outputDetails = value.OutputTokens
	}
	if value.OutputTokens < outputDetails {
		value.OutputTokens = outputDetails
	} else if value.OutputTokens > outputDetails {
		value.OutputTokenDetails.TextTokens = common.SaturatingAddNonNegativeInt(
			value.OutputTokenDetails.TextTokens,
			value.OutputTokens-outputDetails,
		)
	}

	minimumTotal := common.SaturatingAddNonNegativeInt(value.InputTokens, value.OutputTokens)
	if value.TotalTokens < minimumTotal {
		value.TotalTokens = minimumTotal
	}
	return value
}

func nonNegativeRealtimeQuotaToken(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

// realtimeQuotaRatios returns the ratios used by audio/realtime settlement.
// Pricing helpers populate PriceData once per request; when that explicit
// snapshot is ready every value (including zero) is authoritative.  A number
// of internal and compatibility callers still construct RelayInfo directly,
// so those callers retain the historical live ratio lookup.
func realtimeQuotaRatios(relayInfo *relaycommon.RelayInfo, modelName string) (completion, audio, audioCompletion, cache, image float64, snapshotReady bool) {
	if relayInfo != nil && relayInfo.PriceDataSnapshotReady {
		priceData := relayInfo.PriceData
		return priceData.CompletionRatio, priceData.AudioRatio, priceData.AudioCompletionRatio,
			priceData.CacheRatio, priceData.ImageRatio, true
	}
	if modelName == "" && relayInfo != nil {
		modelName = relayInfo.OriginModelName
	}
	audioModelName := modelName
	if relayInfo != nil && relayInfo.OriginModelName != "" {
		audioModelName = relayInfo.OriginModelName
	}
	// Legacy RelayInfo values may still carry explicitly configured cache/image
	// ratios even though no marker was set. Preserve those values and only use
	// the live map when the field was left at its zero-value.
	if relayInfo != nil {
		cache = relayInfo.PriceData.CacheRatio
		image = relayInfo.PriceData.ImageRatio
	}
	if cache <= 0 {
		cache, _ = ratio_setting.GetCacheRatio(modelName)
	}
	if image <= 0 {
		image, _ = ratio_setting.GetImageRatio(modelName)
	}
	return ratio_setting.GetCompletionRatio(modelName),
		ratio_setting.GetAudioRatio(audioModelName),
		ratio_setting.GetAudioCompletionRatio(audioModelName),
		cache, image, false
}

func hasCustomModelRatio(modelName string, currentRatio float64) bool {
	defaultRatio, exists := ratio_setting.GetDefaultModelRatioMap()[modelName]
	if !exists {
		return true
	}
	return currentRatio != defaultRatio
}

func calculateAudioQuota(info QuotaInfo) (int, *common.QuotaClamp) {
	quotaConfig := common.GetQuotaConfig()
	if info.UsePrice {
		modelPrice := decimal.NewFromFloat(info.ModelPrice)
		quotaPerUnit := decimal.NewFromFloat(quotaConfig.QuotaPerUnit)
		groupRatio := decimal.NewFromFloat(info.GroupRatio)

		quota := modelPrice.Mul(quotaPerUnit).Mul(groupRatio)
		return common.QuotaFromDecimalChecked(quota)
	}

	completionRatioValue := info.CompletionRatio
	if completionRatioValue <= 0 {
		completionRatioValue = ratio_setting.GetCompletionRatio(info.ModelName)
	}
	audioRatioValue := info.AudioRatio
	if audioRatioValue <= 0 {
		audioRatioValue = ratio_setting.GetAudioRatio(info.ModelName)
	}
	audioCompletionRatioValue := info.AudioCompletionRatio
	if audioCompletionRatioValue <= 0 {
		audioCompletionRatioValue = ratio_setting.GetAudioCompletionRatio(info.ModelName)
	}
	completionRatio := decimal.NewFromFloat(completionRatioValue)
	audioRatio := decimal.NewFromFloat(audioRatioValue)
	audioCompletionRatio := decimal.NewFromFloat(audioCompletionRatioValue)

	groupRatio := decimal.NewFromFloat(info.GroupRatio)
	modelRatio := decimal.NewFromFloat(info.ModelRatio)
	ratio := groupRatio.Mul(modelRatio)

	inputTextTokens := decimal.NewFromInt(int64(info.InputDetails.TextTokens))
	outputTextTokens := decimal.NewFromInt(int64(info.OutputDetails.TextTokens))
	inputAudioTokens := decimal.NewFromInt(int64(info.InputDetails.AudioTokens))
	outputAudioTokens := decimal.NewFromInt(int64(info.OutputDetails.AudioTokens))
	inputImageTokens := decimal.NewFromInt(int64(nonNegativeRealtimeQuotaToken(info.InputDetails.ImageTokens)))
	cachedInputTokens := decimal.NewFromInt(int64(nonNegativeRealtimeQuotaToken(info.InputDetails.CachedTokens)))
	cacheRatio := info.CacheRatio
	if cacheRatio <= 0 {
		cacheRatio, _ = ratio_setting.GetCacheRatio(info.ModelName)
	}
	imageRatio := info.ImageRatio
	if imageRatio <= 0 {
		imageRatio, _ = ratio_setting.GetImageRatio(info.ModelName)
	}
	// Realtime input text counters include cached tokens. Price the uncached
	// remainder normally and the cached subset at the configured cache ratio.
	if !cachedInputTokens.IsZero() {
		uncachedText := inputTextTokens.Sub(cachedInputTokens)
		if uncachedText.IsNegative() {
			uncachedText = decimal.Zero
		}
		inputTextTokens = uncachedText.Add(cachedInputTokens.Mul(decimal.NewFromFloat(cacheRatio)))
	}

	quota := decimal.Zero
	quota = quota.Add(inputTextTokens)
	quota = quota.Add(outputTextTokens.Mul(completionRatio))
	quota = quota.Add(inputAudioTokens.Mul(audioRatio))
	quota = quota.Add(outputAudioTokens.Mul(audioRatio).Mul(audioCompletionRatio))
	if !inputImageTokens.IsZero() {
		quota = quota.Add(inputImageTokens.Mul(decimal.NewFromFloat(imageRatio)))
	}

	quota = quota.Mul(ratio)

	// If ratio is not zero and quota is less than or equal to zero, set quota to 1
	if !ratio.IsZero() && quota.LessThanOrEqual(decimal.Zero) {
		quota = decimal.NewFromInt(1)
	}

	return common.QuotaFromDecimalChecked(quota)
}

// conservativeIncompleteResponsesAudioQuota returns a minimum settlement for
// an abnormal OpenAI Responses stream when the provider did not return a
// trustworthy usage object.  Audio Responses are billed from token detail
// fields, so the text-stream normalizer cannot be reused directly: an empty
// detail object would otherwise make calculateAudioQuota return only its
// one-unit floor (or be reset to zero by the legacy missing-usage branch).
//
// The minimum is deliberately derived from the request-side input estimate
// and the operator's completion baseline, using ordinary text dimensions. Any
// observed audio/text dimensions have already contributed to quota; taking the
// maximum preserves those dimensions without guessing an audio-token duration
// or charging the client's max_output_tokens ceiling.
func conservativeIncompleteResponsesAudioQuota(relayInfo *relaycommon.RelayInfo, usage *dto.Usage, quota int) (int, *common.QuotaClamp) {
	if relayInfo == nil {
		return quota, nil
	}
	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	if relayInfo.PriceData.FreeModel || groupRatio <= 0 ||
		(relayInfo.PriceData.UsePrice && relayInfo.PriceData.ModelPrice <= 0) ||
		(!relayInfo.PriceData.UsePrice && relayInfo.PriceData.ModelRatio <= 0) {
		// Free models/groups must retain their zero-charge semantics, including
		// when the stream terminates before usage is available.
		return quota, nil
	}

	promptTokens := relayInfo.GetEstimatePromptTokens()
	if promptTokens < 0 {
		promptTokens = 0
	}
	completionTokens := common.GetPreConsumedQuota()
	if completionTokens <= 0 {
		completionTokens = 500
	}
	if usage != nil {
		if usage.PromptTokens > promptTokens {
			promptTokens = usage.PromptTokens
		}
		if usage.CompletionTokens > completionTokens {
			completionTokens = usage.CompletionTokens
		}
	}

	completionRatio, audioRatio, audioCompletionRatio, cacheRatio, imageRatio, snapshotReady := realtimeQuotaRatios(relayInfo, relayInfo.OriginModelName)
	baseline, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails: TokenDetails{TextTokens: promptTokens},
		OutputDetails: TokenDetails{
			TextTokens: completionTokens,
		},
		ModelName:            relayInfo.OriginModelName,
		UsePrice:             relayInfo.PriceData.UsePrice,
		ModelPrice:           relayInfo.PriceData.ModelPrice,
		ModelRatio:           relayInfo.PriceData.ModelRatio,
		GroupRatio:           groupRatio,
		CompletionRatio:      completionRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		PriceSnapshotReady:   snapshotReady,
	})
	if baseline > quota {
		return baseline, clamp
	}
	return quota, nil
}

func PreWssConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.RealtimeUsage) error {
	if relayInfo == nil || usage == nil {
		return errors.New("invalid realtime quota input")
	}
	normalizedUsage := normalizeRealtimeQuotaUsage(usage)
	// PriceData is request-scoped and frozen by the pricing helper.  Prefer its
	// billing mode when the explicit snapshot marker is set; RelayInfo.UsePrice
	// is retained for legacy callers that construct RelayInfo by hand.
	usePrice := relayInfo.UsePrice
	if relayInfo.PriceDataSnapshotReady {
		usePrice = relayInfo.PriceData.UsePrice
	}
	if usePrice {
		return nil
	}

	modelName := relayInfo.OriginModelName
	completionRatio, audioRatio, audioCompletionRatio, cacheRatio, imageRatio, snapshotReady := realtimeQuotaRatios(relayInfo, modelName)
	textInputTokens := normalizedUsage.InputTokenDetails.TextTokens
	textOutTokens := normalizedUsage.OutputTokenDetails.TextTokens
	audioInputTokens := normalizedUsage.InputTokenDetails.AudioTokens
	audioOutTokens := normalizedUsage.OutputTokenDetails.AudioTokens
	groupRatio := ratio_setting.GetGroupRatio(relayInfo.UsingGroup)
	modelRatio := relayInfo.PriceData.ModelRatio
	if !relayInfo.PriceDataSnapshotReady {
		// Legacy/internal callers may invoke this helper before the normal
		// pricing phase and therefore have no snapshot marker. Preserve the
		// historical global lookup for that compatibility path.
		modelRatio, _, _ = ratio_setting.GetModelRatio(modelName)
	} else {
		// Keep the group rate captured by the same pricing snapshot. An
		// auto-group retry may update this field before the next reservation.
		groupRatio = relayInfo.PriceData.GroupRatioInfo.GroupRatio
	}

	var autoGroup any
	var exists bool
	autoGroupApplied := false
	if ctx != nil {
		autoGroup, exists = common.GetContextKey(ctx, constant.ContextKeyAutoGroup)
	}
	if exists {
		if group, ok := autoGroup.(string); ok && strings.TrimSpace(group) != "" {
			autoGroupApplied = true
			groupRatio = ratio_setting.GetGroupRatio(group)
			if ctx != nil {
				logger.LogDebug(ctx, "final group ratio: %f", groupRatio)
			}
			relayInfo.UsingGroup = group
		}
	}

	actualGroupRatio := groupRatio
	if snapshotReady && !autoGroupApplied {
		// The helper already captured a user-group override in the snapshot.
		// Do not re-read mutable settings during a live stream.
		if relayInfo.PriceData.GroupRatioInfo.HasSpecialRatio {
			actualGroupRatio = relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio
		}
	} else {
		userGroupRatio, ok := ratio_setting.GetGroupGroupRatio(relayInfo.UserGroup, relayInfo.UsingGroup)
		if ok {
			actualGroupRatio = userGroupRatio
		}
	}

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:   textInputTokens,
			AudioTokens:  audioInputTokens,
			CachedTokens: normalizedUsage.InputTokenDetails.CachedTokens,
			ImageTokens:  normalizedUsage.InputTokenDetails.ImageTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName:            modelName,
		UsePrice:             usePrice,
		ModelRatio:           modelRatio,
		GroupRatio:           actualGroupRatio,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		CompletionRatio:      completionRatio,
		AudioRatio:           audioRatio,
		AudioCompletionRatio: audioCompletionRatio,
		PriceSnapshotReady:   snapshotReady,
	}

	quota, clamp := calculateAudioQuota(quotaInfo)
	noteQuotaClamp(relayInfo, clamp)
	if quota <= 0 {
		return nil
	}

	// A realtime request already owns a BillingSession from the normal relay
	// pre-consume path. Raise that reservation to the cumulative observed usage;
	// never charge each response.done frame as an independent operation.
	if relayInfo.Billing != nil {
		if quota <= relayInfo.Billing.GetPreConsumedQuota() {
			return nil
		}
		if err := relayInfo.Billing.Reserve(quota); err != nil {
			return err
		}
		if ctx != nil {
			logger.LogInfo(ctx, "realtime streaming reserve quota success: "+fmt.Sprintf("%d", quota))
		}
		return nil
	}

	// Compatibility path for callers that intentionally skipped the regular
	// billing session (for example a free model switching to a paid group).
	// Charge only the monotonic delta and give every target its own durable
	// operation key, so a retry cannot apply the same cumulative amount twice.
	previousQuota := relayInfo.FinalPreConsumedQuota
	if previousQuota < 0 {
		previousQuota = 0
	}
	if quota <= previousQuota {
		return nil
	}
	delta := quota - previousQuota
	userQuota, err := model.GetUserQuota(relayInfo.UserId, false)
	if err != nil {
		return err
	}
	token, err := model.GetTokenByKey(strings.TrimPrefix(relayInfo.TokenKey, "sk-"), false)
	if err != nil {
		return err
	}

	if userQuota < delta {
		return fmt.Errorf("user quota is not enough, user quota: %s, need quota: %s", logger.FormatQuota(userQuota), logger.FormatQuota(delta))
	}

	if !token.UnlimitedQuota && token.RemainQuota < delta {
		return fmt.Errorf("token quota is not enough, token remain quota: %s, need quota: %s", logger.FormatQuota(token.RemainQuota), logger.FormatQuota(delta))
	}

	err = postConsumeQuotaWithComponent(relayInfo, delta, 0, false, fmt.Sprintf("realtime_preconsume:%d", quota))
	if err != nil {
		return err
	}
	relayInfo.FinalPreConsumedQuota = quota
	if ctx != nil {
		logger.LogInfo(ctx, "realtime streaming consume quota success, quota: "+fmt.Sprintf("%d", delta))
	}
	return nil
}

func PostWssConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, modelName string,
	usage *dto.RealtimeUsage, extraContent string) {
	if relayInfo == nil {
		return
	}
	normalizedUsage := normalizeRealtimeQuotaUsage(usage)
	usage = &normalizedUsage

	var tieredResult *billingexpr.TieredResult
	tieredOk, tieredQuota, tieredRes := TryTieredSettle(relayInfo, billingexpr.TokenParams{
		P:   float64(usage.InputTokens),
		C:   float64(usage.OutputTokens),
		Len: float64(usage.InputTokens),
	})
	if tieredOk {
		tieredResult = tieredRes
	}

	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
	textInputTokens := usage.InputTokenDetails.TextTokens
	textOutTokens := usage.OutputTokenDetails.TextTokens

	audioInputTokens := usage.InputTokenDetails.AudioTokens
	audioOutTokens := usage.OutputTokenDetails.AudioTokens

	tokenName := ""
	if ctx != nil {
		tokenName = ctx.GetString("token_name")
	}
	completionRatioValue, audioRatioValue, audioCompletionRatioValue, cacheRatio, imageRatio, snapshotReady := realtimeQuotaRatios(relayInfo, modelName)
	completionRatio := decimal.NewFromFloat(completionRatioValue)
	audioRatio := decimal.NewFromFloat(audioRatioValue)
	audioCompletionRatio := decimal.NewFromFloat(audioCompletionRatioValue)

	modelRatio := relayInfo.PriceData.ModelRatio
	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	modelPrice := relayInfo.PriceData.ModelPrice
	usePrice := relayInfo.PriceData.UsePrice

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:   textInputTokens,
			AudioTokens:  audioInputTokens,
			CachedTokens: usage.InputTokenDetails.CachedTokens,
			ImageTokens:  usage.InputTokenDetails.ImageTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName:            modelName,
		UsePrice:             usePrice,
		ModelRatio:           modelRatio,
		GroupRatio:           groupRatio,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		CompletionRatio:      completionRatioValue,
		AudioRatio:           audioRatioValue,
		AudioCompletionRatio: audioCompletionRatioValue,
		PriceSnapshotReady:   snapshotReady,
	}

	quota, clamp := calculateAudioQuota(quotaInfo)
	noteQuotaClamp(relayInfo, clamp)
	if tieredOk {
		quota = tieredQuota
	}

	totalTokens := usage.TotalTokens
	var logContent string
	if !usePrice {
		logContent = fmt.Sprintf("模型倍率 %.2f，补全倍率 %.2f，音频倍率 %.2f，音频补全倍率 %.2f，分组倍率 %.2f",
			modelRatio, completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), groupRatio)
	} else {
		logContent = fmt.Sprintf("模型价格 %.2f，分组倍率 %.2f", modelPrice, groupRatio)
	}

	// record all the consume log even if quota is 0
	if totalTokens == 0 {
		// in this case, must be some error happened
		// we cannot just return, because we may have to return the pre-consumed quota
		quota = 0
		logContent += "（可能是上游超时）"
		logger.LogError(ctx, fmt.Sprintf("total tokens is 0, cannot consume quota, userId %d, channelId %d, "+
			"tokenId %d, model %s， pre-consumed quota %d", relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, modelName, relayInfo.FinalPreConsumedQuota))
	}

	settlementErr := SettleBillingAndRecordUsage(ctx, relayInfo, quota, totalTokens != 0)
	if settlementErr != nil {
		logger.LogError(ctx, "error settling billing: "+settlementErr.Error())
	}

	logModel := modelName
	if extraContent != "" {
		logContent += ", " + extraContent
	}
	other := GenerateWssOtherInfo(ctx, relayInfo, usage, modelRatio, groupRatio,
		completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), modelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		InjectTieredBillingInfo(other, relayInfo, tieredResult)
	}
	if settlementErr != nil {
		adminInfo, ok := other["admin_info"].(map[string]interface{})
		if !ok || adminInfo == nil {
			adminInfo = map[string]interface{}{}
			other["admin_info"] = adminInfo
		}
		adminInfo["billing_settlement_failed"] = true
		adminInfo["billing_settlement_error"] = common.SensitiveLogMeta(settlementErr.Error())
	}
	attachQuotaSaturation(ctx, relayInfo, other)
	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:        relayInfo.ChannelId,
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		ModelName:        logModel,
		TokenName:        tokenName,
		Quota:            quota,
		Content:          logContent,
		TokenId:          relayInfo.TokenId,
		UseTimeSeconds:   int(useTimeSeconds),
		IsStream:         relayInfo.IsStream,
		Group:            relayInfo.UsingGroup,
		Other:            other,
	})
}

func CalcOpenRouterCacheCreateTokens(usage dto.Usage, priceData types.PriceData) int {
	if priceData.CacheCreationRatio == 1 {
		return 0
	}
	cost, ok := parseOpenRouterUsageCost(usage.Cost)
	if !ok || cost <= 0 {
		// The provider cost is optional, but an invalid value must never be
		// treated as zero: doing so can manufacture a cache-write token count
		// and under/over-charge the request.  The caller treats -1 as
		// "not derivable" and keeps the explicit token dimensions instead.
		return -1
	}
	quotaPrice := priceData.ModelRatio / common.GetQuotaPerUnit()
	promptCacheCreatePrice := quotaPrice * priceData.CacheCreationRatio
	promptCacheReadPrice := quotaPrice * priceData.CacheRatio
	completionPrice := quotaPrice * priceData.CompletionRatio

	totalPromptTokens := float64(usage.PromptTokens)
	completionTokens := float64(usage.CompletionTokens)
	promptCacheReadTokens := float64(usage.PromptTokensDetails.CachedTokens)
	denominator := promptCacheCreatePrice - quotaPrice
	if !isFiniteOpenRouterNumber(denominator) || denominator == 0 {
		return -1
	}

	value := (cost -
		totalPromptTokens*quotaPrice +
		promptCacheReadTokens*(quotaPrice-promptCacheReadPrice) -
		completionTokens*completionPrice) /
		denominator
	if !isFiniteNonNegativeOpenRouterCost(value) {
		return -1
	}
	quota, clamp := common.QuotaRoundChecked(value)
	if clamp != nil {
		return -1
	}
	return quota
}

// parseOpenRouterUsageCost accepts the numeric representations that can
// reach Usage.Cost (JSON decoding normally produces float64, while tests and
// internal converters may use json.Number or an integer).  Rejecting every
// other shape is important: comparing an any value directly with zero can
// panic for an object/slice, and silently treating a failed assertion as zero
// can invent cache-creation tokens.
func parseOpenRouterUsageCost(value any) (float64, bool) {
	var cost float64
	switch v := value.(type) {
	case float64:
		cost = v
	case float32:
		cost = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		cost = parsed
	case int:
		cost = float64(v)
	case int8:
		cost = float64(v)
	case int16:
		cost = float64(v)
	case int32:
		cost = float64(v)
	case int64:
		cost = float64(v)
	case uint:
		cost = float64(v)
	case uint8:
		cost = float64(v)
	case uint16:
		cost = float64(v)
	case uint32:
		cost = float64(v)
	case uint64:
		cost = float64(v)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		cost = parsed
	default:
		return 0, false
	}
	return cost, isFiniteNonNegativeOpenRouterCost(cost)
}

func isFiniteNonNegativeOpenRouterCost(value float64) bool {
	return isFiniteOpenRouterNumber(value) && value >= 0
}

func isFiniteOpenRouterNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func PostAudioConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent string) {
	if usage == nil {
		usage = &dto.Usage{}
	}
	incompleteStreamUsage := needsIncompleteResponsesStreamBilling(ctx, relayInfo, usage)

	var tieredUsedVars map[string]bool
	if snap := relayInfo.TieredBillingSnapshot; snap != nil {
		tieredUsedVars = billingexpr.UsedVars(snap.ExprString)
	}
	var tieredResult *billingexpr.TieredResult
	tieredUsage := usage
	if incompleteStreamUsage {
		// Feed tiered billing the same conservative dimensions used by the
		// ordinary Responses text path.  In particular, an empty audio usage
		// object must not make an expression-error fallback return the entire
		// max-output reservation.
		tieredUsage, _ = normalizeIncompleteResponsesStreamUsage(ctx, relayInfo, usage)
	}
	tieredParams := BuildTieredTokenParams(tieredUsage, false, tieredUsedVars)
	var tieredOk bool
	var tieredQuota int
	var tieredRes *billingexpr.TieredResult
	if incompleteStreamUsage {
		tieredOk, tieredQuota, tieredRes = TryTieredSettleConservative(relayInfo, tieredParams)
	} else {
		tieredOk, tieredQuota, tieredRes = TryTieredSettle(relayInfo, tieredParams)
	}
	if tieredOk {
		tieredResult = tieredRes
	}

	useTimeSeconds := time.Now().Unix() - relayInfo.StartTime.Unix()
	textInputTokens := usage.PromptTokensDetails.TextTokens
	textOutTokens := usage.CompletionTokenDetails.TextTokens

	audioInputTokens := usage.PromptTokensDetails.AudioTokens
	audioOutTokens := usage.CompletionTokenDetails.AudioTokens

	tokenName := ""
	if ctx != nil {
		tokenName = ctx.GetString("token_name")
	}
	completionRatioValue, audioRatioValue, audioCompletionRatioValue, cacheRatio, imageRatio, snapshotReady := realtimeQuotaRatios(relayInfo, relayInfo.OriginModelName)
	completionRatio := decimal.NewFromFloat(completionRatioValue)
	audioRatio := decimal.NewFromFloat(audioRatioValue)
	audioCompletionRatio := decimal.NewFromFloat(audioCompletionRatioValue)

	modelRatio := relayInfo.PriceData.ModelRatio
	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	modelPrice := relayInfo.PriceData.ModelPrice
	usePrice := relayInfo.PriceData.UsePrice

	quotaInfo := QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:  textInputTokens,
			AudioTokens: audioInputTokens,
		},
		OutputDetails: TokenDetails{
			TextTokens:  textOutTokens,
			AudioTokens: audioOutTokens,
		},
		ModelName:            relayInfo.OriginModelName,
		UsePrice:             usePrice,
		ModelRatio:           modelRatio,
		GroupRatio:           groupRatio,
		CacheRatio:           cacheRatio,
		ImageRatio:           imageRatio,
		CompletionRatio:      completionRatioValue,
		AudioRatio:           audioRatioValue,
		AudioCompletionRatio: audioCompletionRatioValue,
		PriceSnapshotReady:   snapshotReady,
	}

	quota, clamp := calculateAudioQuota(quotaInfo)
	noteQuotaClamp(relayInfo, clamp)
	if tieredOk {
		quota = tieredQuota
	}

	// A Responses adapter can expose an incomplete stream with no aggregate
	// usage even though work was already sent downstream.  Keep the normal
	// audio calculation (including any observed audio dimensions), then apply a
	// bounded input/completion baseline for paid models before deciding whether
	// this is a zero-usage refund.
	if incompleteStreamUsage {
		var baselineClamp *common.QuotaClamp
		quota, baselineClamp = conservativeIncompleteResponsesAudioQuota(relayInfo, usage, quota)
		noteQuotaClamp(relayInfo, baselineClamp)
	}

	totalTokens := usage.TotalTokens
	var logContent string
	if !usePrice {
		logContent = fmt.Sprintf("模型倍率 %.2f，补全倍率 %.2f，音频倍率 %.2f，音频补全倍率 %.2f，分组倍率 %.2f",
			modelRatio, completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), groupRatio)
	} else {
		logContent = fmt.Sprintf("模型价格 %.2f，分组倍率 %.2f", modelPrice, groupRatio)
	}

	// Record all the consume log even if quota is 0.  For an incomplete paid
	// Responses stream, retain the conservative quota above and mark the usage
	// as billable so SettleBilling does not refund the whole reservation.
	recordUsage := totalTokens != 0 || (incompleteStreamUsage && quota > 0)
	if totalTokens == 0 {
		if incompleteStreamUsage && quota > 0 {
			logContent += "（Responses 流异常结束，按保守估算结算）"
			logger.LogWarn(ctx, fmt.Sprintf("incomplete Responses audio stream billed conservatively: quota %d, userId %d, channelId %d, "+
				"tokenId %d, model %s， pre-consumed quota %d", quota, relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, relayInfo.OriginModelName, relayInfo.FinalPreConsumedQuota))
		} else {
			// In the ordinary completed/non-Responses path, missing usage retains
			// the historical full-refund behaviour.
			quota = 0
			logContent += "（可能是上游超时）"
			logger.LogError(ctx, fmt.Sprintf("total tokens is 0, cannot consume quota, userId %d, channelId %d, "+
				"tokenId %d, model %s， pre-consumed quota %d", relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, relayInfo.OriginModelName, relayInfo.FinalPreConsumedQuota))
		}
	}

	settlementErr := SettleBillingAndRecordUsage(ctx, relayInfo, quota, recordUsage)
	if settlementErr != nil {
		logger.LogError(ctx, "error settling billing: "+settlementErr.Error())
	}

	logModel := relayInfo.OriginModelName
	if extraContent != "" {
		logContent += ", " + extraContent
	}
	other := GenerateAudioOtherInfo(ctx, relayInfo, usage, modelRatio, groupRatio,
		completionRatio.InexactFloat64(), audioRatio.InexactFloat64(), audioCompletionRatio.InexactFloat64(), modelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
	if tieredResult != nil {
		InjectTieredBillingInfo(other, relayInfo, tieredResult)
	}
	if settlementErr != nil {
		adminInfo, ok := other["admin_info"].(map[string]interface{})
		if !ok || adminInfo == nil {
			adminInfo = map[string]interface{}{}
			other["admin_info"] = adminInfo
		}
		adminInfo["billing_settlement_failed"] = true
		adminInfo["billing_settlement_error"] = common.SensitiveLogMeta(settlementErr.Error())
	}
	attachQuotaSaturation(ctx, relayInfo, other)
	model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{
		ChannelId:        relayInfo.ChannelId,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ModelName:        logModel,
		TokenName:        tokenName,
		Quota:            quota,
		Content:          logContent,
		TokenId:          relayInfo.TokenId,
		UseTimeSeconds:   int(useTimeSeconds),
		IsStream:         relayInfo.IsStream,
		Group:            relayInfo.UsingGroup,
		Other:            other,
	})
	cacheUsage := perfCacheUsageFromUpstream(ctx, usage, usage)
	gopool.Go(func() {
		perfmetrics.RecordRelaySampleWithTokens(
			relayInfo,
			true,
			cacheUsage.InputTokens,
			cacheUsage.CacheReadTokens,
			cacheUsage.CacheWriteTokens,
			int64(usage.CompletionTokens),
			cacheUsage.Observed,
		)
	})
}

func PreConsumeTokenQuota(relayInfo *relaycommon.RelayInfo, quota int) error {
	if quota < 0 {
		return errors.New("quota 不能为负数！")
	}
	if relayInfo.IsPlayground {
		return nil
	}
	// 原子预扣：检查与扣减在同一操作中完成，并发请求不可能同时通过检查后超扣。
	reserved, err := model.TryReserveTokenQuota(relayInfo.TokenId, relayInfo.TokenKey, quota, relayInfo.TokenUnlimited)
	if err != nil {
		return err
	}
	if !reserved {
		remainQuota := 0
		if token, tokenErr := model.GetTokenByKey(relayInfo.TokenKey, false); tokenErr == nil && token != nil {
			remainQuota = token.RemainQuota
		}
		return fmt.Errorf("token quota is not enough, token remain quota: %s, need quota: %s", logger.FormatQuota(remainQuota), logger.FormatQuota(quota))
	}
	return nil
}

type postConsumeQuotaResult struct {
	// FundingApplied and TokenApplied describe the net state after this call,
	// rather than merely whether an individual write was attempted.  In
	// particular, when the token write fails and the funding write is
	// successfully compensated, FundingApplied is reset to false so callers
	// can safely clear a durable billing marker and retry the whole operation.
	FundingApplied bool
	TokenApplied   bool

	// FundingCompensated is true when a successful funding write was undone
	// after a token failure.  It is useful for diagnostics and lets callers
	// distinguish a clean rollback from a partial, reconciliation-required
	// failure (FundingApplied remains true in the latter case).
	FundingCompensated bool

	// Durable is true when the financial mutation (and any supplied usage
	// deltas) was committed through BillingOperation. Callers that still need
	// the historical best-effort usage updates can use this to avoid applying
	// those counters a second time on an idempotent retry.
	Durable bool
}

// postConsumeQuotaUsage carries informational counters that must commit in the
// same BillingOperation transaction as a component-specific financial charge.
// It is intentionally private: only legacy paths with a well-defined request
// component (currently the violation-fee path) should opt into this seam.
type postConsumeQuotaUsage struct {
	UserUsedQuotaDelta    int64
	UserRequestCountDelta int64
	ChannelID             int
	ChannelUsedQuotaDelta int64
}

// ErrPostConsumeFundingCompensation identifies a partial legacy settlement:
// the funding write succeeded, the token write failed, and the inverse
// funding write could not be committed.  Callers must keep a durable billing
// marker in this case and reconcile the funding side before replaying a full
// charge.
var ErrPostConsumeFundingCompensation = errors.New("post-consume funding compensation failed")

func PostConsumeQuota(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int, sendEmail bool) error {
	_, err := postConsumeQuotaWithResult(relayInfo, quota, preConsumedQuota, sendEmail)
	return err
}

func postConsumeQuotaWithResult(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int, sendEmail bool) (result postConsumeQuotaResult, err error) {
	return postConsumeQuotaWithComponentResult(relayInfo, quota, preConsumedQuota, sendEmail, "")
}

// postConsumeQuotaWithComponent is the durable variant used by request paths
// which do not yet have a BillingSession (realtime pre-charge, legacy
// settlement, and violation fees).  The historical PostConsumeQuota API is
// intentionally kept as a compatibility wrapper for callers/tests that lack a
// stable request identity; new call sites must provide a component so two
// legitimate phases of one request cannot share a marker accidentally.
func postConsumeQuotaWithComponent(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int, sendEmail bool, component string) error {
	_, err := postConsumeQuotaWithComponentResult(relayInfo, quota, preConsumedQuota, sendEmail, component)
	return err
}

func postConsumeQuotaWithComponentResult(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int, sendEmail bool, component string) (result postConsumeQuotaResult, err error) {
	return postConsumeQuotaWithComponentUsageResult(relayInfo, quota, preConsumedQuota, sendEmail, component, nil)
}

// postConsumeQuotaWithComponentUsageResult is the durable variant for a
// component whose informational usage must be replay-safe with its financial
// delta. A nil usage argument preserves the historical behavior for callers
// that have not opted into the combined operation.
func postConsumeQuotaWithComponentUsageResult(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int, sendEmail bool, component string, usage *postConsumeQuotaUsage) (result postConsumeQuotaResult, err error) {
	if relayInfo == nil {
		return result, errors.New("relay info is nil")
	}
	inverseQuota, err := invertQuotaDelta(quota)
	if err != nil {
		return result, err
	}
	if quota < -common.MaxQuota || quota > common.MaxQuota {
		return result, fmt.Errorf("post-consume quota is out of range: %d", quota)
	}
	if preConsumedQuota < 0 || preConsumedQuota > common.MaxQuota {
		return result, fmt.Errorf("pre-consumed quota is out of range: %d", preConsumedQuota)
	}

	// Once a request has a durable identity, apply both financial ledgers in
	// one transaction.  This closes the old funding-then-token crash window and
	// makes a retry after an ambiguous DB response a no-op.  Keep the exported
	// compatibility wrapper on its historical path when no component/identity
	// is available; those callers have no safe cross-process replay key.
	if strings.TrimSpace(component) != "" && strings.TrimSpace(relayInfo.RequestId) != "" && model.DB != nil {
		delta := int64(quota)
		spec := model.BillingOperationSpec{
			RequestID:            strings.TrimSpace(relayInfo.RequestId),
			Component:            component,
			UserID:               relayInfo.UserId,
			TokenID:              relayInfo.TokenId,
			TokenKey:             relayInfo.TokenKey,
			TokenDelta:           delta,
			TokenUnlimited:       relayInfo.TokenUnlimited,
			RequireWalletBalance: delta > 0,
			RequireTokenBalance:  delta > 0 && !relayInfo.TokenUnlimited,
		}
		if relayInfo.IsPlayground {
			spec.TokenDelta = 0
			spec.RequireTokenBalance = false
		}
		if usage != nil {
			spec.UserUsedQuotaDelta = usage.UserUsedQuotaDelta
			spec.UserRequestCountDelta = usage.UserRequestCountDelta
			spec.ChannelID = usage.ChannelID
			spec.ChannelUsedQuotaDelta = usage.ChannelUsedQuotaDelta
			if spec.ChannelID <= 0 {
				spec.ChannelID = 0
				spec.ChannelUsedQuotaDelta = 0
			}
		}
		switch strings.TrimSpace(relayInfo.BillingSource) {
		case "", BillingSourceWallet:
			spec.WalletDelta = -delta
		case BillingSourceSubscription:
			if relayInfo.SubscriptionId <= 0 {
				return result, errors.New("subscription id is missing")
			}
			spec.SubscriptionID = relayInfo.SubscriptionId
			spec.SubscriptionDelta = delta
		default:
			return result, fmt.Errorf("unsupported billing source: %s", relayInfo.BillingSource)
		}
		if spec.WalletDelta != 0 && relayInfo.UserId <= 0 {
			return result, errors.New("user id is missing")
		}
		if spec.TokenDelta != 0 && relayInfo.TokenId <= 0 {
			return result, errors.New("token id is missing")
		}
		if err := model.ApplyBillingOperation(spec); err != nil {
			return result, err
		}
		if relayInfo.BillingSource == BillingSourceSubscription && quota != 0 {
			relayInfo.SubscriptionPostDelta += int64(quota)
		}
		result.FundingApplied = true
		result.TokenApplied = relayInfo.IsPlayground || relayInfo.TokenId > 0
		result.Durable = true
		if sendEmail && quota+preConsumedQuota != 0 {
			checkAndSendQuotaNotify(relayInfo, quota, preConsumedQuota)
		}
		return result, nil
	}

	// 1) Consume from wallet quota OR subscription item.
	if err = applyPostConsumeFunding(relayInfo, quota); err != nil {
		return result, err
	}
	if relayInfo.BillingSource == BillingSourceSubscription && quota != 0 {
		relayInfo.SubscriptionPostDelta += int64(quota)
	}
	result.FundingApplied = true

	if !relayInfo.IsPlayground {
		if quota > 0 {
			err = model.DecreaseTokenQuota(relayInfo.TokenId, relayInfo.TokenKey, quota)
		} else if quota < 0 {
			err = model.IncreaseTokenQuota(relayInfo.TokenId, relayInfo.TokenKey, inverseQuota)
		}
		if err != nil {
			// The legacy path updates the two ledgers independently.  Never leave
			// a successful funding write behind when the token write has failed:
			// compensate the exact signed delta before returning.  A failed
			// compensation is deliberately fail-closed; FundingApplied stays true
			// so the caller can retain its durable marker for reconciliation instead
			// of retrying the whole charge and double-spending the wallet.
			compensationErr := applyPostConsumeFunding(relayInfo, inverseQuota)
			if compensationErr == nil {
				result.FundingApplied = false
				result.FundingCompensated = true
				if relayInfo.BillingSource == BillingSourceSubscription {
					relayInfo.SubscriptionPostDelta -= int64(quota)
				}
				return result, fmt.Errorf("token quota adjustment failed (funding compensated): %w", err)
			}
			common.SysError(fmt.Sprintf("post-consume partial settlement: userId=%d tokenId=%d quota=%d token_error=%v funding_compensation_error=%v",
				relayInfo.UserId, relayInfo.TokenId, quota, err, compensationErr))
			return result, errors.Join(err, ErrPostConsumeFundingCompensation, compensationErr)
		}
		result.TokenApplied = true
	}

	if sendEmail {
		if (quota + preConsumedQuota) != 0 {
			checkAndSendQuotaNotify(relayInfo, quota, preConsumedQuota)
		}
	}

	return result, nil
}

// invertQuotaDelta returns the exact signed inverse used for compensation. A
// minimum machine integer cannot be negated without overflow; reject it before
// either ledger is touched rather than allowing an asymmetric update.
func invertQuotaDelta(delta int) (int, error) {
	if delta == 0 {
		return 0, nil
	}
	inverse := -delta
	if inverse == delta {
		return 0, errors.New("quota delta cannot be inverted")
	}
	return inverse, nil
}

// applyPostConsumeFunding applies a signed delta to the funding source used by
// the legacy post-consume path.  Keeping this operation in one helper is
// important because compensation must be the exact inverse operation for both
// wallet and subscription billing.
func applyPostConsumeFunding(relayInfo *relaycommon.RelayInfo, delta int) error {
	if relayInfo == nil {
		return errors.New("relay info is nil")
	}
	if delta == 0 {
		return nil
	}
	if relayInfo.BillingSource == BillingSourceSubscription {
		if relayInfo.SubscriptionId == 0 {
			return errors.New("subscription id is missing")
		}
		return model.PostConsumeUserSubscriptionDelta(relayInfo.SubscriptionId, int64(delta))
	}
	if delta > 0 {
		return model.DecreaseUserQuota(relayInfo.UserId, delta, false)
	}
	return model.IncreaseUserQuota(relayInfo.UserId, -delta, false)
}

func checkAndSendQuotaNotify(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int) {
	runtimeConfig := common.GetGeneralRuntimeConfig()
	gopool.Go(func() {
		userSetting := relayInfo.UserSetting
		threshold := runtimeConfig.QuotaRemindThreshold
		if userSetting.QuotaWarningThreshold != 0 {
			threshold = int(userSetting.QuotaWarningThreshold)
		}

		//noMoreQuota := userCache.Quota-(quota+preConsumedQuota) <= 0
		quotaTooLow := false
		consumeQuota := quota + preConsumedQuota
		if relayInfo.UserQuota-consumeQuota < threshold {
			quotaTooLow = true
		}
		if quotaTooLow {
			prompt := "您的额度即将用尽"
			topUpLink := PaymentReturnURL("/wallet")

			// 根据通知方式生成不同的内容格式
			var content string
			var values []interface{}

			notifyType := userSetting.NotifyType
			if notifyType == "" {
				notifyType = dto.NotifyTypeEmail
			}

			if notifyType == dto.NotifyTypeBark {
				// Bark推送使用简短文本，不支持HTML
				content = "{{value}}，剩余额度：{{value}}，请及时充值"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota)}
			} else if notifyType == dto.NotifyTypeGotify {
				content = "{{value}}，当前剩余额度为 {{value}}，请及时充值。"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota)}
			} else {
				// 默认内容格式，适用于Email和Webhook（支持HTML）
				content = "{{value}}，当前剩余额度为 {{value}}，为了不影响您的使用，请及时充值。<br/>充值链接：<a href='{{value}}'>{{value}}</a>"
				values = []interface{}{prompt, logger.FormatQuota(relayInfo.UserQuota), topUpLink, topUpLink}
			}

			err := NotifyUser(relayInfo.UserId, relayInfo.UserEmail, relayInfo.UserSetting, dto.NewNotify(dto.NotifyTypeQuotaExceed, prompt, content, values))
			if err != nil {
				common.SysError(fmt.Sprintf("failed to send quota notify to user %d: %s", relayInfo.UserId, err.Error()))
			}
		}
	})
}

func checkAndSendSubscriptionQuotaNotify(relayInfo *relaycommon.RelayInfo) {
	runtimeConfig := common.GetGeneralRuntimeConfig()
	gopool.Go(func() {
		if relayInfo == nil {
			return
		}
		if relayInfo.SubscriptionId == 0 || relayInfo.SubscriptionAmountTotal <= 0 {
			return
		}

		userSetting := relayInfo.UserSetting
		threshold := runtimeConfig.QuotaRemindThreshold
		if userSetting.QuotaWarningThreshold != 0 {
			threshold = int(userSetting.QuotaWarningThreshold)
		}

		usedAfter := relayInfo.SubscriptionAmountUsedAfterPreConsume + relayInfo.SubscriptionPostDelta
		remaining := relayInfo.SubscriptionAmountTotal - usedAfter
		if remaining >= int64(threshold) {
			return
		}

		prompt := "您的订阅额度即将用尽"
		topUpLink := PaymentReturnURL("/wallet")

		var content string
		var values []interface{}
		notifyType := userSetting.NotifyType
		if notifyType == "" {
			notifyType = dto.NotifyTypeEmail
		}

		if notifyType == dto.NotifyTypeBark {
			content = "{{value}}，剩余额度：{{value}}，请及时充值"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining))}
		} else if notifyType == dto.NotifyTypeGotify {
			content = "{{value}}，当前剩余额度为 {{value}}，请及时充值。"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining))}
		} else {
			content = "{{value}}，当前剩余额度为 {{value}}，为了不影响您的使用，请及时充值。<br/>充值链接：<a href='{{value}}'>{{value}}</a>"
			values = []interface{}{prompt, logger.FormatQuota(int(remaining)), topUpLink, topUpLink}
		}

		if err := NotifyUser(relayInfo.UserId, relayInfo.UserEmail, relayInfo.UserSetting, dto.NewNotify(dto.NotifyTypeQuotaExceed, prompt, content, values)); err != nil {
			common.SysError(fmt.Sprintf("failed to send subscription quota notify to user %d: %s", relayInfo.UserId, err.Error()))
		}
	})
}
