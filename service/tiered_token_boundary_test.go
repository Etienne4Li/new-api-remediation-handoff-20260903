package service

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
)

func TestBuildTieredTokenParamsNormalizesNegativeUsage(t *testing.T) {
	params := BuildTieredTokenParams(&dto.Usage{
		PromptTokens:     -1,
		CompletionTokens: -2,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: -3,
			ImageTokens:  -4,
			AudioTokens:  -5,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{
			ImageTokens: -6,
			AudioTokens: -7,
		},
		ClaudeCacheCreation5mTokens: -8,
		ClaudeCacheCreation1hTokens: -9,
	}, true, map[string]bool{"cr": true, "cc": true, "cc1h": true})

	assert.Zero(t, params.P)
	assert.Zero(t, params.C)
	assert.Zero(t, params.Len)
	assert.Zero(t, params.CR)
	assert.Zero(t, params.CC)
	assert.Zero(t, params.CC1h)
	assert.Zero(t, params.Img)
	assert.Zero(t, params.ImgO)
	assert.Zero(t, params.AI)
	assert.Zero(t, params.AO)
}

func TestBuildTieredTokenParamsNilUsageIsEmpty(t *testing.T) {
	assert.Equal(t, 0.0, BuildTieredTokenParams(nil, false, nil).P)
}

func TestConservativeTieredFallbackEstimateSumSaturates(t *testing.T) {
	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			EstimatedPromptTokens:     math.MaxInt,
			EstimatedCompletionTokens: 1,
			EstimatedQuotaAfterGroup:  100,
		},
	}
	// A wrapped estimate would be negative and trigger the conservative
	// reservation fallback (100). Saturation keeps it positive, yielding a
	// tiny but valid ratio that rounds to zero instead.
	assert.Zero(t, conservativeTieredFallbackQuota(info, billingexpr.TokenParams{P: 1}))
}
