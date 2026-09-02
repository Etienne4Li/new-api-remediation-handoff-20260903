package service

import (
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// TieredResultWrapper wraps billingexpr.TieredResult for use at the service layer.
type TieredResultWrapper = billingexpr.TieredResult

// BuildTieredTokenParams constructs billingexpr.TokenParams from a dto.Usage,
// normalizing P and C so they mean "tokens not separately priced by the
// expression". Sub-categories (cache, image, audio) are only subtracted
// when the expression references them via their own variable.
//
// GPT-format APIs report prompt_tokens / completion_tokens as totals that
// include all sub-categories (cache, image, audio). Claude-format APIs
// report them as text-only. This function normalizes to text-only when
// sub-categories are separately priced.
func BuildTieredTokenParams(usage *dto.Usage, isClaudeUsageSemantic bool, usedVars map[string]bool) billingexpr.TokenParams {
	if usage == nil {
		return billingexpr.TokenParams{}
	}
	// Usage counters are upstream-controlled. Normalize negative values before
	// converting to float so malformed metadata cannot create a credit in an
	// expression or reduce the tiered pre-consume amount.
	p := float64(nonNegativeTokenCount(usage.PromptTokens))
	c := float64(nonNegativeTokenCount(usage.CompletionTokens))
	cr := float64(nonNegativeTokenCount(usage.PromptTokensDetails.CachedTokens))
	cc5m := float64(nonNegativeTokenCount(usage.PromptTokensDetails.CacheCreationTokensTotal()))
	cc1h := float64(0)

	if usage.UsageSemantic == "anthropic" {
		cc1h = float64(nonNegativeTokenCount(usage.ClaudeCacheCreation1hTokens))
		cc5m = float64(nonNegativeTokenCount(usage.ClaudeCacheCreation5mTokens))
	}

	img := float64(nonNegativeTokenCount(usage.PromptTokensDetails.ImageTokens))
	ai := float64(nonNegativeTokenCount(usage.PromptTokensDetails.AudioTokens))
	imgO := float64(nonNegativeTokenCount(usage.CompletionTokenDetails.ImageTokens))
	ao := float64(nonNegativeTokenCount(usage.CompletionTokenDetails.AudioTokens))

	// len = total input context length for tier condition evaluation.
	// Non-Claude: prompt_tokens already includes everything.
	// Claude: input_tokens is text-only, so add cache read + cache creation.
	inputLen := p
	if isClaudeUsageSemantic {
		inputLen = p + cr + cc5m + cc1h
	}

	if !isClaudeUsageSemantic {
		if usedVars["cr"] {
			p -= cr
		}
		if usedVars["cc"] {
			p -= cc5m
		}
		if usedVars["cc1h"] {
			p -= cc1h
		}
		if usedVars["img"] {
			p -= img
		}
		if usedVars["ai"] {
			p -= ai
		}
		if usedVars["img_o"] {
			c -= imgO
		}
		if usedVars["ao"] {
			c -= ao
		}
	}

	// OpenAI cache-write usage reports unadjusted prefix counts, so cr + cc can
	// exceed the prompt and drive the remainder negative. Clamp at zero.
	if p < 0 {
		p = 0
	}
	if c < 0 {
		c = 0
	}

	return billingexpr.TokenParams{
		P:    p,
		C:    c,
		Len:  inputLen,
		CR:   cr,
		CC:   cc5m,
		CC1h: cc1h,
		Img:  img,
		ImgO: imgO,
		AI:   ai,
		AO:   ao,
	}
}

func refreshTieredBillingGroup(relayInfo *relaycommon.RelayInfo) (*billingexpr.BillingSnapshot, error) {
	if relayInfo == nil {
		return nil, nil
	}
	snap := relayInfo.TieredBillingSnapshot
	if snap == nil || snap.BillingMode != "tiered_expr" {
		return nil, nil
	}

	groupRatio := relayInfo.PriceData.GroupRatioInfo.GroupRatio
	if snap.GroupRatio == groupRatio {
		return snap, nil
	}

	estimatedQuotaAfterGroup := snap.EstimatedQuotaBeforeGroup * groupRatio
	estimatedQuota, err := billingexpr.QuotaRoundStrict(estimatedQuotaAfterGroup)
	if err != nil {
		return nil, err
	}
	snap.GroupRatio = groupRatio
	snap.EstimatedQuotaAfterGroup = estimatedQuota
	return snap, nil
}

// PrepareTieredBillingForSelectedGroup refreshes routing-dependent billing
// state before an upstream attempt. An existing session reserves any higher
// estimate before sending. If the initial group was free and skipped
// pre-consume, switching to a paid group creates the session at that point.
func PrepareTieredBillingForSelectedGroup(c *gin.Context, relayInfo *relaycommon.RelayInfo) *types.NewAPIError {
	snap, err := refreshTieredBillingGroup(relayInfo)
	if err != nil {
		return types.NewErrorWithStatusCode(
			err,
			types.ErrorCodeModelPriceError,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if snap == nil {
		return nil
	}
	if snap.GroupRatio == 0 {
		// Paid-to-free keeps FreeModel as-is: FreeModel means "pre-consume was
		// skipped", which is not true once a session exists, and settlement
		// already yields 0 for a zero group ratio.
		return nil
	}

	// The selected group is paid; clear a FreeModel flag frozen when the
	// initial group was free so downstream state stays consistent.
	relayInfo.PriceData.FreeModel = false

	if relayInfo.Billing == nil {
		return PreConsumeBilling(c, snap.EstimatedQuotaAfterGroup, relayInfo)
	}
	if err := relayInfo.Billing.Reserve(snap.EstimatedQuotaAfterGroup); err != nil {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	relayInfo.FinalPreConsumedQuota = relayInfo.Billing.GetPreConsumedQuota()
	return nil
}

// TryTieredSettle checks if the request uses tiered_expr billing and, if so,
// computes the actual quota using the captured BillingSnapshot. Returns:
//   - ok=true, quota, result  when tiered billing applies
//   - ok=false, 0, nil        when it doesn't (caller should fall through to existing logic)
func TryTieredSettle(relayInfo *relaycommon.RelayInfo, params billingexpr.TokenParams) (ok bool, quota int, result *billingexpr.TieredResult) {
	snap := relayInfo.TieredBillingSnapshot
	if snap == nil || snap.BillingMode != "tiered_expr" {
		return false, 0, nil
	}

	requestInput := billingexpr.RequestInput{}
	if relayInfo.BillingRequestInput != nil {
		requestInput = *relayInfo.BillingRequestInput
	}

	tr, err := billingexpr.ComputeTieredQuotaWithRequest(snap, params, requestInput)
	if err != nil {
		quota = relayInfo.FinalPreConsumedQuota
		if quota <= 0 {
			quota = snap.EstimatedQuotaAfterGroup
		}
		return true, quota, nil
	}

	// Surface any single-request saturation from settlement onto RelayInfo so the
	// consume log records it under admin_info, regardless of which caller
	// (text, audio, WSS) consumes the returned quota. First non-nil wins.
	noteQuotaClamp(relayInfo, tr.Clamp)

	return true, tr.ActualQuotaAfterGroup, &tr
}

// TryTieredSettleConservative is used when a Responses stream ended
// abnormally and the expression cannot be evaluated at settlement time. The
// ordinary TryTieredSettle fallback intentionally returns the frozen
// reservation, which is safe for most relay errors but can be grossly larger
// than work observed on a disconnect (for example when max_output_tokens was
// set very high). This variant scales the frozen reservation by the ratio of
// observed token dimensions to the pre-consume estimate and caps the result
// at that reservation. It never treats a client-supplied output ceiling as
// actual generated usage.
func TryTieredSettleConservative(relayInfo *relaycommon.RelayInfo, params billingexpr.TokenParams) (ok bool, quota int, result *billingexpr.TieredResult) {
	if relayInfo == nil {
		return false, 0, nil
	}
	snap := relayInfo.TieredBillingSnapshot
	if snap == nil || snap.BillingMode != "tiered_expr" {
		return false, 0, nil
	}

	requestInput := billingexpr.RequestInput{}
	if relayInfo.BillingRequestInput != nil {
		requestInput = *relayInfo.BillingRequestInput
	}
	tr, err := billingexpr.ComputeTieredQuotaWithRequest(snap, params, requestInput)
	if err == nil {
		noteQuotaClamp(relayInfo, tr.Clamp)
		return true, tr.ActualQuotaAfterGroup, &tr
	}

	return true, conservativeTieredFallbackQuota(relayInfo, params), nil
}

func conservativeTieredFallbackQuota(relayInfo *relaycommon.RelayInfo, params billingexpr.TokenParams) int {
	if relayInfo == nil || relayInfo.TieredBillingSnapshot == nil {
		return 0
	}
	snap := relayInfo.TieredBillingSnapshot

	// Use the final selected-group reservation where available. Both values are
	// bounded by the single-request quota policy; clamp defensively in case a
	// legacy snapshot was populated before the shared quota helpers existed.
	reservation := snap.EstimatedQuotaAfterGroup
	if relayInfo.FinalPreConsumedQuota > 0 {
		reservation = relayInfo.FinalPreConsumedQuota
	}
	if reservation <= 0 {
		return 0
	}
	if reservation > common.MaxQuota {
		reservation = common.MaxQuota
	}

	actualUnits := positiveFinite(params.Len)
	inputUnits := positiveFinite(params.P) + positiveFinite(params.CR) + positiveFinite(params.CC) +
		positiveFinite(params.CC1h) + positiveFinite(params.Img) + positiveFinite(params.AI)
	if inputUnits > actualUnits {
		actualUnits = inputUnits
	}
	outputUnits := positiveFinite(params.C) + positiveFinite(params.ImgO) + positiveFinite(params.AO)
	actualUnits += outputUnits
	if actualUnits <= 0 {
		return 0
	}

	estimateTokens := safeTokenTotal(
		maxInt(relayInfo.TieredBillingSnapshot.EstimatedPromptTokens, 0),
		maxInt(relayInfo.TieredBillingSnapshot.EstimatedCompletionTokens, 0),
	)
	estimateUnits := float64(estimateTokens)
	if estimateUnits <= 0 || math.IsNaN(estimateUnits) || math.IsInf(estimateUnits, 0) {
		return reservation
	}

	// Keep the fallback at or below the reservation. This is deliberately a
	// conservative *upper* bound for an abnormal stream; a later reconciliation
	// can charge additional usage if a provider supplies authoritative totals.
	candidate := float64(reservation) * actualUnits / estimateUnits
	if math.IsNaN(candidate) || math.IsInf(candidate, 0) || candidate < 0 {
		return reservation
	}
	if candidate > float64(reservation) {
		candidate = float64(reservation)
	}
	quota, clamp := common.QuotaRoundChecked(candidate)
	if clamp != nil {
		noteQuotaClamp(relayInfo, clamp)
		if quota > reservation {
			return reservation
		}
	}
	if quota < 0 {
		return 0
	}
	return quota
}

func positiveFinite(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func maxInt(value, fallback int) int {
	if value > fallback {
		return value
	}
	return fallback
}
