/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerfCacheUsageFromUpstream(t *testing.T) {
	t.Run("ordinary OpenAI upstream usage is observable", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(nil)
		usage := &dto.Usage{PromptTokens: 100}
		usage.PromptTokensDetails.CachedTokens = 40
		usage.PromptTokensDetails.CacheWriteTokens = 10

		got := perfCacheUsageFromUpstream(ctx, usage, effectiveBillingUsage(usage))

		assert.Equal(t, int64(100), got.InputTokens)
		assert.Equal(t, int64(40), got.CacheReadTokens)
		assert.Equal(t, int64(10), got.CacheWriteTokens)
		assert.True(t, got.Observed)
	})

	t.Run("Claude billing usage uses normalized total input", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(nil)
		usage := &dto.Usage{
			BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
				InputTokens:              50,
				CacheReadInputTokens:     30,
				CacheCreationInputTokens: 20,
				OutputTokens:             10,
			}),
		}
		effective := effectiveBillingUsage(usage)

		got := perfCacheUsageFromUpstream(ctx, usage, effective)

		require.NotNil(t, effective)
		assert.Equal(t, int64(100), got.InputTokens)
		assert.Equal(t, int64(30), got.CacheReadTokens)
		assert.Equal(t, int64(20), got.CacheWriteTokens)
		assert.True(t, got.Observed)
	})

	t.Run("local estimates are excluded", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(nil)
		common.SetContextKey(ctx, constant.ContextKeyLocalCountTokens, true)
		usage := &dto.Usage{PromptTokens: 100}
		usage.PromptTokensDetails.CachedTokens = 100

		got := perfCacheUsageFromUpstream(ctx, usage, usage)

		assert.False(t, got.Observed)
		assert.Zero(t, got.InputTokens)
	})

	t.Run("estimated billing usage is excluded", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(nil)
		usage := &dto.Usage{
			PromptTokens: 100,
			BillingUsage: dto.NewEstimatedGeminiChatBillingUsage(&dto.Usage{
				PromptTokens: 100,
			}),
		}

		got := perfCacheUsageFromUpstream(ctx, usage, effectiveBillingUsage(usage))

		assert.False(t, got.Observed)
		assert.Zero(t, got.InputTokens)
	})

	t.Run("Gemini billing usage falls back to prompt tokens", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(nil)
		usage := &dto.Usage{
			BillingUsage: dto.NewGeminiChatBillingUsage(&dto.GeminiUsageMetadata{
				PromptTokenCount:        120,
				CachedContentTokenCount: 45,
				CandidatesTokenCount:    10,
			}),
		}

		got := perfCacheUsageFromUpstream(ctx, usage, effectiveBillingUsage(usage))

		assert.Equal(t, int64(120), got.InputTokens)
		assert.Equal(t, int64(45), got.CacheReadTokens)
		assert.True(t, got.Observed)
	})
}
