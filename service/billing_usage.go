package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

const (
	usageBillingPathLocal              = "local"
	usageBillingPathUpstream           = "upstream"
	usageBillingPathOpenAI             = "billing-usage-openai"
	usageBillingPathOpenAIEstimated    = "billing-usage-openai-estimated"
	usageBillingPathAnthropic          = "billing-usage-anthropic"
	usageBillingPathAnthropicEstimated = "billing-usage-anthropic-estimated"
	usageBillingPathGemini             = "billing-usage-gemini"
	usageBillingPathGeminiEstimated    = "billing-usage-gemini-estimated"
)

func effectiveBillingUsage(usage *dto.Usage) *dto.Usage {
	if billingUsage, ok := usageFromBillingUsage(usage); ok {
		return billingUsage
	}
	return usage
}

type perfCacheUsage struct {
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Observed         bool
}

// perfCacheUsageFromUpstream returns normalized cache counters only when the
// successful relay has real upstream usage. Local and explicitly estimated
// billing usage must not be mixed into cache hit-rate statistics.
func perfCacheUsageFromUpstream(ctx *gin.Context, originalUsage *dto.Usage, effectiveUsage *dto.Usage) perfCacheUsage {
	if originalUsage == nil || effectiveUsage == nil {
		return perfCacheUsage{}
	}
	if ctx != nil && common.GetContextKeyBool(ctx, constant.ContextKeyLocalCountTokens) {
		return perfCacheUsage{}
	}
	if originalUsage.BillingUsage != nil && originalUsage.BillingUsage.Estimated {
		return perfCacheUsage{}
	}

	inputTokens := effectiveUsage.InputTokens
	if inputTokens <= 0 {
		inputTokens = effectiveUsage.PromptTokens
	}
	if inputTokens <= 0 {
		return perfCacheUsage{}
	}

	cacheReadTokens := effectiveUsage.PromptTokensDetails.CachedTokens
	if cacheReadTokens <= 0 {
		cacheReadTokens = effectiveUsage.PromptCacheHitTokens
	}
	cacheWriteTokens := effectiveUsage.PromptTokensDetails.CacheCreationTokensTotal()

	return perfCacheUsage{
		InputTokens:      int64(inputTokens),
		CacheReadTokens:  int64(max(cacheReadTokens, 0)),
		CacheWriteTokens: int64(max(cacheWriteTokens, 0)),
		Observed:         true,
	}
}

func usageBillingPathForLog(isLocalCountTokens bool, usage *dto.Usage) string {
	effectiveUsage, ok := usageFromBillingUsage(usage)
	if !ok {
		if isLocalCountTokens {
			return usageBillingPathLocal
		}
		return usageBillingPathUpstream
	}

	switch effectiveUsage.UsageSemantic {
	case dto.BillingUsageSemanticOpenAI:
		if usage.BillingUsage.Estimated {
			return usageBillingPathOpenAIEstimated
		}
		return usageBillingPathOpenAI
	case dto.BillingUsageSemanticAnthropic:
		if usage.BillingUsage.Estimated {
			return usageBillingPathAnthropicEstimated
		}
		return usageBillingPathAnthropic
	case dto.BillingUsageSemanticGemini:
		if usage.BillingUsage.Estimated {
			return usageBillingPathGeminiEstimated
		}
		return usageBillingPathGemini
	}

	return usageBillingPathUpstream
}

func appendUsageBillingPathForLog(other *model.LogOther, isLocalCountTokens bool, usage *dto.Usage) {
	if other == nil {
		return
	}
	other.SetAdmin("usage_billing_path", usageBillingPathForLog(isLocalCountTokens, usage))
}

func usageFromBillingUsage(usage *dto.Usage) (*dto.Usage, bool) {
	if usage == nil || usage.BillingUsage == nil {
		return nil, false
	}
	return usage.BillingUsage.CanonicalUsage()
}
