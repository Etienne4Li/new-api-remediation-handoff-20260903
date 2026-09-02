package service

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// ToolSurchargeItem is one billable tool-call line for consume logs.
type ToolSurchargeItem struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Price float64 `json:"price"`
}

func appendToolSurchargeLogInfo(other map[string]interface{}, items []ToolSurchargeItem) {
	if len(items) == 0 {
		return
	}
	other["tool_surcharges"] = items
}

type textQuotaSummary struct {
	PromptTokens           int
	CompletionTokens       int
	TotalTokens            int
	CacheTokens            int
	CacheCreationTokens    int
	CacheCreationTokens5m  int
	CacheCreationTokens1h  int
	ImageTokens            int
	AudioTokens            int
	ModelName              string
	TokenName              string
	UseTimeSeconds         int64
	CompletionRatio        float64
	CacheRatio             float64
	ImageRatio             float64
	ModelRatio             float64
	GroupRatio             float64
	ModelPrice             float64
	CacheCreationRatio     float64
	CacheCreationRatio5m   float64
	CacheCreationRatio1h   float64
	Quota                  int
	IsClaudeUsageSemantic  bool
	UsageSemantic          string
	AudioInputPrice        float64
	ToolSurchargeItems     []ToolSurchargeItem
	ToolCallSurchargeQuota decimal.Decimal
}

// hasBillableUsage reports whether this request should incur any charge.
// A request can carry zero tokens yet still be billable via a tool-call
// surcharge (e.g. /v1/alpha/search returns no usage but bills one web_search
// call), so token count alone is not sufficient to decide.
func (s *textQuotaSummary) hasBillableUsage() bool {
	return s.TotalTokens > 0 || !s.ToolCallSurchargeQuota.IsZero()
}

// needsIncompleteResponsesStreamBilling reports whether a Responses stream
// ended abnormally without an authoritative upstream usage object. The
// explicit authority bit is set by the Responses adapter and intentionally
// takes precedence over the generic local-count flag: a provider may return a
// valid partial usage object while another part of the response was counted
// locally, and that provider usage must not be replaced by a reservation
// estimate.
func needsIncompleteResponsesStreamBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage) bool {
	if ctx == nil || relayInfo == nil || !relayInfo.IsStream || relayInfo.RelayMode != relayconstant.RelayModeResponses {
		return false
	}
	status := relayInfo.StreamStatus
	if status == nil {
		return false
	}
	streamMarkedIncomplete := common.GetContextKeyBool(ctx, constant.ContextKeyResponsesStreamIncomplete)
	terminalSeen := common.GetContextKeyBool(ctx, constant.ContextKeyResponsesStreamTerminalSeen)
	_, terminalSeenSet := common.GetContextKey(ctx, constant.ContextKeyResponsesStreamTerminalSeen)
	// For Responses, only an explicit [DONE] marker is a normal terminal
	// signal. StreamScannerHandler also uses EOF/HandlerStop as generic normal
	// endings for legacy adapters, but an EOF without [DONE] can mean the
	// upstream/client connection was cut before terminal usage was delivered.
	// The adapter's terminal-seen bit makes an EOF after a valid terminal event
	// safe, while an explicit incomplete bit covers response.incomplete/
	// response.cancelled followed by [DONE].
	if !streamMarkedIncomplete && !status.HasErrors() {
		switch status.EndReason {
		case relaycommon.StreamEndReasonDone:
			// A direct package-level caller from an older relay may not set the
			// adapter marker; retain the historical Done semantics in that case.
			if !terminalSeenSet || terminalSeen {
				return false
			}
		case relaycommon.StreamEndReasonEOF, relaycommon.StreamEndReasonHandlerStop:
			// EOF/HandlerStop is only safe after the Responses protocol emitted a
			// terminal event. Missing marker means the stream was truncated.
			if terminalSeen {
				return false
			}
		}
	}
	if common.GetContextKeyBool(ctx, constant.ContextKeyResponsesUsageAuthoritative) {
		return false
	}
	if (status.EndReason == relaycommon.StreamEndReasonEOF ||
		status.EndReason == relaycommon.StreamEndReasonHandlerStop) && !terminalSeen {
		return true
	}
	return streamMarkedIncomplete || common.GetContextKeyBool(ctx, constant.ContextKeyLocalCountTokens) || !ValidUsage(usage)
}

// needsIncompleteResponsesStreamBillingFloor is kept as a compatibility
// alias for package-local callers from older revisions. It now describes the
// usage-normalisation decision; no quota floor is retained.
func needsIncompleteResponsesStreamBillingFloor(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage) bool {
	return needsIncompleteResponsesStreamBilling(ctx, relayInfo, usage)
}

// cloneUsageForIncompleteResponsesBilling returns an independent usage value
// so the original upstream payload remains available for logging and
// diagnostics. BillingUsage is cleared on the clone: otherwise a second
// effectiveBillingUsage pass could unwrap the stale nested usage and undo the
// normalisation.
func cloneUsageForIncompleteResponsesBilling(usage *dto.Usage) *dto.Usage {
	if usage == nil {
		return &dto.Usage{}
	}
	clone := *usage
	clone.BillingUsage = nil
	if usage.InputTokensDetails != nil {
		details := *usage.InputTokensDetails
		clone.InputTokensDetails = &details
	}
	return &clone
}

// safeTokenTotal adds non-negative token counts without allowing malformed
// upstream values to wrap an int into a negative number.
func safeTokenTotal(promptTokens, completionTokens int) int {
	return common.SaturatingAddNonNegativeInt(promptTokens, completionTokens)
}

func nonNegativeTokenCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func subtractTokenCount(value, deduction int) int {
	value = nonNegativeTokenCount(value)
	deduction = nonNegativeTokenCount(deduction)
	if deduction >= value {
		return 0
	}
	return value - deduction
}

// normalizeIncompleteResponsesStreamUsage applies the conservative billing
// policy for an abnormal Responses stream:
//   - input is at least the request-side estimate;
//   - completion is at least common.PreConsumedQuota (500 by default), while
//     any larger observed reasoning/tool/text output is retained.
//
// This deliberately works at the usage layer rather than by raising summary
// quota to PriceData.QuotaToPreConsume. The latter may include a client-
// supplied max_output_tokens value many orders of magnitude larger than work
// actually observed, and would turn a protective baseline into a permanent
// overcharge. Free groups/models keep their zero-charge semantics.
func normalizeIncompleteResponsesStreamUsage(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage) (*dto.Usage, bool) {
	if !needsIncompleteResponsesStreamBilling(ctx, relayInfo, usage) {
		return usage, false
	}

	normalized := cloneUsageForIncompleteResponsesBilling(usage)
	if relayInfo.PriceData.FreeModel || relayInfo.PriceData.GroupRatioInfo.GroupRatio == 0 {
		// Preserve any observed dimensions for the log, but do not invent a
		// billable baseline for a free group/model.
		return normalized, true
	}

	promptTokens := relayInfo.GetEstimatePromptTokens()
	if promptTokens < 0 {
		promptTokens = 0
	}
	if normalized.PromptTokens > promptTokens {
		promptTokens = normalized.PromptTokens
	}

	completionBaseline := common.GetPreConsumedQuota()
	if completionBaseline <= 0 {
		completionBaseline = 500
	}
	completionTokens := normalized.CompletionTokens
	if completionTokens < completionBaseline {
		completionTokens = completionBaseline
	}
	if completionTokens < 0 { // defensive; the baseline above normally wins
		completionTokens = completionBaseline
	}

	normalized.PromptTokens = promptTokens
	normalized.InputTokens = promptTokens
	normalized.CompletionTokens = completionTokens
	normalized.OutputTokens = completionTokens
	normalized.TotalTokens = safeTokenTotal(promptTokens, completionTokens)
	return normalized, true
}

func cacheWriteTokensTotal(summary textQuotaSummary) int {
	if summary.CacheCreationTokens5m > 0 || summary.CacheCreationTokens1h > 0 {
		splitCacheWriteTokens := safeTokenTotal(summary.CacheCreationTokens5m, summary.CacheCreationTokens1h)
		if summary.CacheCreationTokens > splitCacheWriteTokens {
			return summary.CacheCreationTokens
		}
		return splitCacheWriteTokens
	}
	return summary.CacheCreationTokens
}

func isLegacyClaudeDerivedOpenAIUsage(relayInfo *relaycommon.RelayInfo, usage *dto.Usage) bool {
	if relayInfo == nil || usage == nil {
		return false
	}
	if relayInfo.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
		return false
	}
	if usage.UsageSource != "" || usage.UsageSemantic != "" {
		return false
	}
	return usage.ClaudeCacheCreation5mTokens > 0 || usage.ClaudeCacheCreation1hTokens > 0
}

func collectToolSurchargeItem(items []ToolSurchargeItem, name string, count int, modelName string) []ToolSurchargeItem {
	if count <= 0 {
		return items
	}
	price := operation_setting.GetToolPriceForModel(name, modelName)
	if price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return items
	}
	return append(items, ToolSurchargeItem{
		Name:  name,
		Count: count,
		Price: price,
	})
}

func mergeToolSurchargeItems(items []ToolSurchargeItem) []ToolSurchargeItem {
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].Price < items[j].Price
		}
		return items[i].Name < items[j].Name
	})

	merged := items[:0]
	for _, item := range items {
		lastIndex := len(merged) - 1
		if lastIndex >= 0 &&
			merged[lastIndex].Name == item.Name &&
			merged[lastIndex].Price == item.Price {
			if item.Count > math.MaxInt-merged[lastIndex].Count {
				common.SysError("tool surcharge call count overflow for " + item.Name)
				merged[lastIndex].Count = math.MaxInt
			} else {
				merged[lastIndex].Count += item.Count
			}
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

func calculateTextToolCallSurcharge(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, summary *textQuotaSummary) decimal.Decimal {
	dGroupRatio := decimal.NewFromFloat(summary.GroupRatio)
	dQuotaPerUnit := decimal.NewFromFloat(common.GetQuotaPerUnit())

	var items []ToolSurchargeItem

	if relayInfo.ResponsesUsageInfo != nil {
		for name, tool := range relayInfo.ResponsesUsageInfo.BuiltInTools {
			if tool == nil {
				continue
			}
			items = collectToolSurchargeItem(items, name, tool.CallCount, summary.ModelName)
		}
	}
	if relayInfo.RelayMode != relayconstant.RelayModeResponses &&
		strings.HasSuffix(summary.ModelName, "search-preview") {
		items = collectToolSurchargeItem(items, dto.BuildInToolWebSearchPreview, 1, summary.ModelName)
	}

	items = collectToolSurchargeItem(
		items,
		dto.BuildInToolWebSearch,
		ctx.GetInt("claude_web_search_requests"),
		summary.ModelName,
	)

	if ctx.GetBool("gemini_google_search_call") {
		items = collectToolSurchargeItem(items, dto.BuildInToolGoogleSearch, 1, summary.ModelName)
	}

	summary.ToolSurchargeItems = mergeToolSurchargeItems(items)
	var surcharge decimal.Decimal
	for _, item := range summary.ToolSurchargeItems {
		surcharge = surcharge.Add(decimal.NewFromFloat(item.Price).
			Mul(decimal.NewFromInt(int64(item.Count))).
			Div(decimal.NewFromInt(1000)).
			Mul(dGroupRatio).
			Mul(dQuotaPerUnit))
	}

	return surcharge
}

// noteQuotaClamp records the first quota saturation event onto relayInfo so it
// can later be attached to the consume/task log for admin auditing. First
// non-nil clamp wins (a single request may hit multiple conversions).
func noteQuotaClamp(relayInfo *relaycommon.RelayInfo, clamp *common.QuotaClamp) {
	if clamp == nil || relayInfo == nil {
		return
	}
	if relayInfo.QuotaClamp == nil {
		relayInfo.QuotaClamp = clamp
	}
}

func composeTieredTextQuota(relayInfo *relaycommon.RelayInfo, summary textQuotaSummary, tieredQuota int, tieredResult *billingexpr.TieredResult) int {
	if summary.ToolCallSurchargeQuota.IsZero() {
		return tieredQuota
	}

	if tieredResult != nil {
		if snap := relayInfo.TieredBillingSnapshot; snap != nil {
			quota, clamp := common.QuotaFromDecimalChecked(decimal.NewFromFloat(tieredResult.ActualQuotaBeforeGroup).
				Mul(decimal.NewFromFloat(snap.GroupRatio)).
				Add(summary.ToolCallSurchargeQuota))
			noteQuotaClamp(relayInfo, clamp)
			return quota
		}
	}

	// Saturate the final sum, not just the surcharge: tieredQuota can be near
	// MaxQuota and adding the surcharge could push the total past the
	// single-request quota policy bound.
	total, clamp := common.QuotaFromDecimalChecked(
		decimal.NewFromInt(int64(tieredQuota)).Add(summary.ToolCallSurchargeQuota),
	)
	noteQuotaClamp(relayInfo, clamp)
	return total
}

// shouldUseTieredTextFallback reports whether a tiered settlement result is
// safe to use for the text path. TryTieredSettle deliberately returns the
// frozen pre-consume quota when expression evaluation fails, so callers can
// preserve legacy behaviour for ordinary requests. For an abnormal Responses
// stream, the conservative variant returns a bounded quota with a nil result;
// that value must still be applied. A zero bounded result means there is no
// reservation to settle and the usage-normalised summary remains the fallback.
func shouldUseTieredTextFallback(incompleteResponses bool, tieredQuota int, tieredResult *billingexpr.TieredResult) bool {
	if !incompleteResponses {
		return true
	}
	return tieredResult != nil || tieredQuota > 0
}

// calculateTextQuotaSummary expects a usage already remapped by
// effectiveBillingUsage; PostTextConsumeQuota performs that remap once and shares
// the result with tiered billing, affinity observation and logging.
func calculateTextQuotaSummary(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage) textQuotaSummary {
	summary := textQuotaSummary{
		ModelName:            relayInfo.OriginModelName,
		TokenName:            ctx.GetString("token_name"),
		UseTimeSeconds:       time.Now().Unix() - relayInfo.StartTime.Unix(),
		CompletionRatio:      relayInfo.PriceData.CompletionRatio,
		CacheRatio:           relayInfo.PriceData.CacheRatio,
		ImageRatio:           relayInfo.PriceData.ImageRatio,
		ModelRatio:           relayInfo.PriceData.ModelRatio,
		GroupRatio:           relayInfo.PriceData.GroupRatioInfo.GroupRatio,
		ModelPrice:           relayInfo.PriceData.ModelPrice,
		CacheCreationRatio:   relayInfo.PriceData.CacheCreationRatio,
		CacheCreationRatio5m: relayInfo.PriceData.CacheCreation5mRatio,
		CacheCreationRatio1h: relayInfo.PriceData.CacheCreation1hRatio,
		UsageSemantic:        usageSemanticFromUsage(relayInfo, usage),
	}
	summary.IsClaudeUsageSemantic = summary.UsageSemantic == "anthropic"

	if usage == nil {
		usage = &dto.Usage{
			PromptTokens:     relayInfo.GetEstimatePromptTokens(),
			CompletionTokens: 0,
			TotalTokens:      relayInfo.GetEstimatePromptTokens(),
		}
	}

	summary.PromptTokens = nonNegativeTokenCount(usage.PromptTokens)
	summary.CompletionTokens = nonNegativeTokenCount(usage.CompletionTokens)
	summary.TotalTokens = safeTokenTotal(usage.PromptTokens, usage.CompletionTokens)
	summary.CacheTokens = nonNegativeTokenCount(usage.PromptTokensDetails.CachedTokens)
	summary.CacheCreationTokens = nonNegativeTokenCount(usage.PromptTokensDetails.CacheCreationTokensTotal())
	summary.CacheCreationTokens5m = nonNegativeTokenCount(usage.ClaudeCacheCreation5mTokens)
	summary.CacheCreationTokens1h = nonNegativeTokenCount(usage.ClaudeCacheCreation1hTokens)
	summary.ImageTokens = nonNegativeTokenCount(usage.PromptTokensDetails.ImageTokens)
	summary.AudioTokens = nonNegativeTokenCount(usage.PromptTokensDetails.AudioTokens)
	legacyClaudeDerived := isLegacyClaudeDerivedOpenAIUsage(relayInfo, usage)
	isOpenRouterClaudeBilling := relayInfo.ChannelMeta != nil &&
		relayInfo.ChannelType == constant.ChannelTypeOpenRouter &&
		summary.IsClaudeUsageSemantic

	if isOpenRouterClaudeBilling {
		summary.PromptTokens = subtractTokenCount(summary.PromptTokens, summary.CacheTokens)
		isUsingCustomSettings := relayInfo.PriceData.UsePrice || hasCustomModelRatio(summary.ModelName, relayInfo.PriceData.ModelRatio)
		if summary.CacheCreationTokens == 0 && relayInfo.PriceData.CacheCreationRatio != 1 && !isUsingCustomSettings {
			// Cost is an `any` field populated from an upstream JSON payload.
			// Validate its shape before attempting the cache-write inference;
			// direct interface comparison can panic when a provider sends an
			// object or array instead of a number.
			if cost, ok := parseOpenRouterUsageCost(usage.Cost); ok && cost > 0 {
				maybeCacheCreationTokens := CalcOpenRouterCacheCreateTokens(*usage, relayInfo.PriceData)
				if maybeCacheCreationTokens >= 0 && summary.PromptTokens >= maybeCacheCreationTokens {
					summary.CacheCreationTokens = maybeCacheCreationTokens
				}
			}
		}
		summary.PromptTokens = subtractTokenCount(summary.PromptTokens, summary.CacheCreationTokens)
	}

	dPromptTokens := decimal.NewFromInt(int64(summary.PromptTokens))
	dCacheTokens := decimal.NewFromInt(int64(summary.CacheTokens))
	dImageTokens := decimal.NewFromInt(int64(summary.ImageTokens))
	dAudioTokens := decimal.NewFromInt(int64(summary.AudioTokens))
	dCompletionTokens := decimal.NewFromInt(int64(summary.CompletionTokens))
	dCachedCreationTokens := decimal.NewFromInt(int64(summary.CacheCreationTokens))
	dCompletionRatio := decimal.NewFromFloat(summary.CompletionRatio)
	dCacheRatio := decimal.NewFromFloat(summary.CacheRatio)
	dImageRatio := decimal.NewFromFloat(summary.ImageRatio)
	dModelRatio := decimal.NewFromFloat(summary.ModelRatio)
	dGroupRatio := decimal.NewFromFloat(summary.GroupRatio)
	dModelPrice := decimal.NewFromFloat(summary.ModelPrice)
	dCacheCreationRatio := decimal.NewFromFloat(summary.CacheCreationRatio)
	dCacheCreationRatio5m := decimal.NewFromFloat(summary.CacheCreationRatio5m)
	dCacheCreationRatio1h := decimal.NewFromFloat(summary.CacheCreationRatio1h)
	dQuotaPerUnit := decimal.NewFromFloat(common.GetQuotaPerUnit())

	ratio := dModelRatio.Mul(dGroupRatio)
	summary.ToolCallSurchargeQuota = calculateTextToolCallSurcharge(ctx, relayInfo, &summary)

	var audioInputQuota decimal.Decimal
	if !relayInfo.PriceData.UsePrice {
		baseTokens := dPromptTokens

		var cachedTokensWithRatio decimal.Decimal
		if !dCacheTokens.IsZero() {
			if !summary.IsClaudeUsageSemantic && !legacyClaudeDerived {
				baseTokens = baseTokens.Sub(dCacheTokens)
			}
			cachedTokensWithRatio = dCacheTokens.Mul(dCacheRatio)
		}

		var cachedCreationTokensWithRatio decimal.Decimal
		hasSplitCacheCreationTokens := summary.CacheCreationTokens5m > 0 || summary.CacheCreationTokens1h > 0
		if !dCachedCreationTokens.IsZero() || hasSplitCacheCreationTokens {
			if !summary.IsClaudeUsageSemantic && !legacyClaudeDerived {
				baseTokens = baseTokens.Sub(dCachedCreationTokens)
				cachedCreationTokensWithRatio = dCachedCreationTokens.Mul(dCacheCreationRatio)
			} else {
				splitCacheWriteTokens := safeTokenTotal(summary.CacheCreationTokens5m, summary.CacheCreationTokens1h)
				remaining := subtractTokenCount(summary.CacheCreationTokens, splitCacheWriteTokens)
				cachedCreationTokensWithRatio = decimal.NewFromInt(int64(remaining)).Mul(dCacheCreationRatio)
				cachedCreationTokensWithRatio = cachedCreationTokensWithRatio.Add(decimal.NewFromInt(int64(summary.CacheCreationTokens5m)).Mul(dCacheCreationRatio5m))
				cachedCreationTokensWithRatio = cachedCreationTokensWithRatio.Add(decimal.NewFromInt(int64(summary.CacheCreationTokens1h)).Mul(dCacheCreationRatio1h))
			}
		}

		var imageTokensWithRatio decimal.Decimal
		if !dImageTokens.IsZero() {
			baseTokens = baseTokens.Sub(dImageTokens)
			imageTokensWithRatio = dImageTokens.Mul(dImageRatio)
		}

		if !dAudioTokens.IsZero() {
			summary.AudioInputPrice = operation_setting.GetGeminiInputAudioPricePerMillionTokens(summary.ModelName)
			if summary.AudioInputPrice > 0 {
				baseTokens = baseTokens.Sub(dAudioTokens)
				audioInputQuota = decimal.NewFromFloat(summary.AudioInputPrice).
					Div(decimal.NewFromInt(1000000)).Mul(dAudioTokens).Mul(dGroupRatio).Mul(dQuotaPerUnit)
			}
		}

		// OpenAI cache-write usage reports unadjusted prefix counts, so
		// cached_tokens + cache_write_tokens can exceed prompt_tokens and the
		// remainder can go negative. Clamp at zero so overlap never turns into
		// a negative base charge.
		if baseTokens.IsNegative() {
			baseTokens = decimal.Zero
		}

		promptQuota := baseTokens.Add(cachedTokensWithRatio).Add(imageTokensWithRatio).Add(cachedCreationTokensWithRatio)
		completionQuota := dCompletionTokens.Mul(dCompletionRatio)
		quotaCalculateDecimal := promptQuota.Add(completionQuota).Mul(ratio)
		quotaCalculateDecimal = quotaCalculateDecimal.Add(audioInputQuota)
		quotaCalculateDecimal = relayInfo.PriceData.ApplyOtherRatiosToDecimal(quotaCalculateDecimal)
		quotaCalculateDecimal = quotaCalculateDecimal.Add(summary.ToolCallSurchargeQuota)

		if !ratio.IsZero() && quotaCalculateDecimal.LessThanOrEqual(decimal.Zero) {
			quotaCalculateDecimal = decimal.NewFromInt(1)
		}
		quota, clamp := common.QuotaFromDecimalChecked(quotaCalculateDecimal)
		summary.Quota = quota
		noteQuotaClamp(relayInfo, clamp)
	} else {
		quotaCalculateDecimal := dModelPrice.Mul(dQuotaPerUnit).Mul(dGroupRatio)
		quotaCalculateDecimal = quotaCalculateDecimal.Add(audioInputQuota)
		quotaCalculateDecimal = relayInfo.PriceData.ApplyOtherRatiosToDecimal(quotaCalculateDecimal)
		quotaCalculateDecimal = quotaCalculateDecimal.Add(summary.ToolCallSurchargeQuota)
		quota, clamp := common.QuotaFromDecimalChecked(quotaCalculateDecimal)
		summary.Quota = quota
		noteQuotaClamp(relayInfo, clamp)
	}

	if !summary.hasBillableUsage() {
		summary.Quota = 0
	} else if !ratio.IsZero() && summary.Quota == 0 {
		summary.Quota = 1
	}

	return summary
}

func usageSemanticFromUsage(relayInfo *relaycommon.RelayInfo, usage *dto.Usage) string {
	if usage != nil && usage.UsageSemantic != "" {
		return usage.UsageSemantic
	}
	if relayInfo != nil && relayInfo.GetFinalRequestRelayFormat() == types.RelayFormatClaude {
		return "anthropic"
	}
	return "openai"
}

func PostTextConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent []string) {
	originUsage := usage
	billingUsage := effectiveBillingUsage(usage)
	incompleteStreamUsage := needsIncompleteResponsesStreamBilling(ctx, relayInfo, billingUsage)
	billingUsage, normalizedIncompleteUsage := normalizeIncompleteResponsesStreamUsage(ctx, relayInfo, billingUsage)
	incompleteStreamUsage = incompleteStreamUsage || normalizedIncompleteUsage
	if usage == nil {
		extraContent = append(extraContent, "上游无计费信息")
	}
	if originUsage != nil {
		// Affinity statistics must reflect the upstream observation rather than
		// the synthetic completion baseline used only for settlement.
		ObserveChannelAffinityUsageCacheByRelayFormat(ctx, effectiveBillingUsage(originUsage), relayInfo.GetFinalRequestRelayFormat())
	}

	adminRejectReason := common.GetContextKeyString(ctx, constant.ContextKeyAdminRejectReason)
	summary := calculateTextQuotaSummary(ctx, relayInfo, billingUsage)

	var tieredResult *billingexpr.TieredResult
	tieredBillingApplied := false
	if billingUsage != nil && (originUsage != nil || incompleteStreamUsage) {
		var tieredUsedVars map[string]bool
		if snap := relayInfo.TieredBillingSnapshot; snap != nil {
			tieredUsedVars = billingexpr.UsedVars(snap.ExprString)
		}
		var tieredOk bool
		var tieredQuota int
		var tieredRes *billingexpr.TieredResult
		params := BuildTieredTokenParams(billingUsage, summary.IsClaudeUsageSemantic, tieredUsedVars)
		if incompleteStreamUsage {
			tieredOk, tieredQuota, tieredRes = TryTieredSettleConservative(relayInfo, params)
		} else {
			tieredOk, tieredQuota, tieredRes = TryTieredSettle(relayInfo, params)
		}
		if tieredOk && shouldUseTieredTextFallback(incompleteStreamUsage, tieredQuota, tieredRes) {
			tieredBillingApplied = true
			tieredResult = tieredRes
			summary.Quota = composeTieredTextQuota(relayInfo, summary, tieredQuota, tieredRes)
		}
	}

	if incompleteStreamUsage {
		extraContent = append(extraContent, "Responses 流异常结束且缺少完整 usage，按估算输入与已观察输出（最低 500 token）结算")
		if summary.Quota == 0 {
			logger.LogError(ctx, fmt.Sprintf(
				"incomplete Responses stream still resolved to zero quota: userId=%d channelId=%d tokenId=%d model=%s end_reason=%s",
				relayInfo.UserId,
				relayInfo.ChannelId,
				relayInfo.TokenId,
				summary.ModelName,
				relayInfo.StreamStatus.EndReason,
			))
		} else {
			logger.LogWarn(ctx, fmt.Sprintf(
				"incomplete Responses stream billed conservatively: quota=%d baseline_applied=%t userId=%d channelId=%d tokenId=%d model=%s end_reason=%s",
				summary.Quota,
				normalizedIncompleteUsage,
				relayInfo.UserId,
				relayInfo.ChannelId,
				relayInfo.TokenId,
				summary.ModelName,
				relayInfo.StreamStatus.EndReason,
			))
		}
	}

	for _, item := range summary.ToolSurchargeItems {
		q := decimal.NewFromFloat(item.Price).
			Mul(decimal.NewFromInt(int64(item.Count))).
			Div(decimal.NewFromInt(1000)).
			Mul(decimal.NewFromFloat(summary.GroupRatio)).
			Mul(decimal.NewFromFloat(common.GetQuotaPerUnit()))
		extraContent = append(extraContent, fmt.Sprintf(
			"%s 调用 %d 次，调用花费 %s",
			item.Name,
			item.Count,
			logger.LogQuota(common.QuotaFromDecimal(q)),
		))
	}
	if summary.AudioInputPrice > 0 && summary.AudioTokens > 0 {
		q := decimal.NewFromFloat(summary.AudioInputPrice).Div(decimal.NewFromInt(1000000)).Mul(decimal.NewFromInt(int64(summary.AudioTokens))).Mul(decimal.NewFromFloat(summary.GroupRatio)).Mul(decimal.NewFromFloat(common.GetQuotaPerUnit()))
		extraContent = append(extraContent, fmt.Sprintf("Audio Input 花费 %s", logger.LogQuota(common.QuotaFromDecimal(q))))
	}

	hasBillableUsage := summary.hasBillableUsage() || (incompleteStreamUsage && summary.Quota > 0)
	if !hasBillableUsage {
		extraContent = append(extraContent, "上游没有返回计费信息，无法扣费（可能是上游超时）")
		logger.LogError(ctx, fmt.Sprintf("total tokens is 0, cannot consume quota, userId %d, channelId %d, tokenId %d, model %s， pre-consumed quota %d", relayInfo.UserId, relayInfo.ChannelId, relayInfo.TokenId, summary.ModelName, relayInfo.FinalPreConsumedQuota))
	}

	settlementErr := SettleBillingAndRecordUsage(ctx, relayInfo, summary.Quota, hasBillableUsage)
	if settlementErr != nil {
		logger.LogError(ctx, "error settling billing: "+settlementErr.Error())
		extraContent = append(extraContent, "计费结算失败，已进入对账队列")
	}

	logModel := summary.ModelName
	if strings.HasPrefix(logModel, "gpt-4-gizmo") {
		logModel = "gpt-4-gizmo-*"
		extraContent = append(extraContent, fmt.Sprintf("模型 %s", summary.ModelName))
	}
	if strings.HasPrefix(logModel, "gpt-4o-gizmo") {
		logModel = "gpt-4o-gizmo-*"
		extraContent = append(extraContent, fmt.Sprintf("模型 %s", summary.ModelName))
	}

	logContent := strings.Join(extraContent, ", ")
	var other map[string]interface{}
	if summary.IsClaudeUsageSemantic {
		other = GenerateClaudeOtherInfo(ctx, relayInfo,
			summary.ModelRatio, summary.GroupRatio, summary.CompletionRatio,
			summary.CacheTokens, summary.CacheRatio,
			summary.CacheCreationTokens, summary.CacheCreationRatio,
			summary.CacheCreationTokens5m, summary.CacheCreationRatio5m,
			summary.CacheCreationTokens1h, summary.CacheCreationRatio1h,
			summary.ModelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
		other["usage_semantic"] = "anthropic"
	} else {
		other = GenerateTextOtherInfo(ctx, relayInfo, summary.ModelRatio, summary.GroupRatio, summary.CompletionRatio, summary.CacheTokens, summary.CacheRatio, summary.ModelPrice, relayInfo.PriceData.GroupRatioInfo.GroupSpecialRatio)
	}
	appendUsageBillingPathForLog(other, common.GetContextKeyBool(ctx, constant.ContextKeyLocalCountTokens), originUsage)
	if adminRejectReason != "" {
		other["reject_reason"] = adminRejectReason
	}
	if summary.ImageTokens != 0 {
		other["image"] = true
		other["image_ratio"] = summary.ImageRatio
		other["image_output"] = summary.ImageTokens
	}
	appendToolSurchargeLogInfo(other, summary.ToolSurchargeItems)
	if summary.AudioInputPrice > 0 && summary.AudioTokens > 0 {
		other["audio_input_seperate_price"] = true
		other["audio_input_token_count"] = summary.AudioTokens
		other["audio_input_price"] = summary.AudioInputPrice
	}
	if summary.CacheCreationTokens > 0 {
		other["cache_creation_tokens"] = summary.CacheCreationTokens
		other["cache_creation_ratio"] = summary.CacheCreationRatio
	}
	if summary.CacheCreationTokens5m > 0 {
		other["cache_creation_tokens_5m"] = summary.CacheCreationTokens5m
		other["cache_creation_ratio_5m"] = summary.CacheCreationRatio5m
	}
	if summary.CacheCreationTokens1h > 0 {
		other["cache_creation_tokens_1h"] = summary.CacheCreationTokens1h
		other["cache_creation_ratio_1h"] = summary.CacheCreationRatio1h
	}
	cacheWriteTokens := cacheWriteTokensTotal(summary)
	if cacheWriteTokens > 0 {
		// cache_write_tokens: normalized cache creation total for UI display.
		// If split 5m/1h values are present, this is their sum; otherwise it falls back
		// to cache_creation_tokens.
		other["cache_write_tokens"] = cacheWriteTokens
	}
	if relayInfo.GetFinalRequestRelayFormat() != types.RelayFormatClaude && billingUsage != nil && billingUsage.UsageSource != "" && billingUsage.InputTokens > 0 {
		// input_tokens_total: explicit normalized total input used by the usage log UI.
		// Only write this field when upstream/current conversion has already provided a
		// reliable total input value and tagged the usage source. Do not infer it from
		// prompt/cache fields here, otherwise old upstream payloads may be double-counted.
		other["input_tokens_total"] = billingUsage.InputTokens
	}
	if tieredBillingApplied {
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
		PromptTokens:     summary.PromptTokens,
		CompletionTokens: summary.CompletionTokens,
		ModelName:        logModel,
		TokenName:        summary.TokenName,
		Quota:            summary.Quota,
		Content:          logContent,
		TokenId:          relayInfo.TokenId,
		UseTimeSeconds:   int(summary.UseTimeSeconds),
		IsStream:         relayInfo.IsStream,
		Group:            relayInfo.UsingGroup,
		Other:            other,
	})
	cacheUsage := perfCacheUsageFromUpstream(ctx, originUsage, billingUsage)
	gopool.Go(func() {
		perfmetrics.RecordRelaySampleWithTokens(
			relayInfo,
			true,
			cacheUsage.InputTokens,
			cacheUsage.CacheReadTokens,
			cacheUsage.CacheWriteTokens,
			int64(summary.CompletionTokens),
			cacheUsage.Observed,
		)
	})
}
