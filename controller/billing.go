package controller

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func GetSubscription(c *gin.Context) {
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	paymentConfig := operation_setting.GetPaymentRuntimeConfig()
	quotaPerUnit := common.GetQuotaPerUnit()
	runtimeConfig := common.GetGeneralRuntimeConfig()
	var remainQuota int
	var usedQuota int
	var err error
	var token *model.Token
	var expiredTime int64
	if runtimeConfig.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		if err == nil {
			if token == nil {
				err = errors.New("token lookup returned no record")
			} else {
				expiredTime = token.ExpiredTime
				remainQuota = token.RemainQuota
				usedQuota = token.UsedQuota
			}
		}
	} else {
		userId := c.GetInt("id")
		remainQuota, err = model.GetUserQuota(userId, false)
		if err == nil {
			usedQuota, err = model.GetUserUsedQuota(userId)
		}
	}
	if expiredTime <= 0 {
		expiredTime = 0
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: common.MaskSensitiveInfo(err.Error()),
			Type:    "upstream_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	// Add through the shared saturating helper. Both columns are stored as Go
	// ints and legacy/corrupt rows can independently approach the platform limit;
	// even an int64 promotion would overflow when both values are maximal.
	quota := common.SaturatingAddNonNegativeInt(remainQuota, usedQuota)
	amount := float64(quota)
	// OpenAI 兼容接口中的 *_USD 字段含义保持“额度单位”对应值：
	// 我们将其解释为以“站点展示类型”为准：
	// - USD: 直接除以 QuotaPerUnit
	// - CNY: 先转 USD 再乘汇率
	// - TOKENS: 直接使用 tokens 数量
	switch generalSetting.QuotaDisplayType {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / quotaPerUnit * paymentConfig.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// amount 保持 tokens 数值
	default:
		amount = amount / quotaPerUnit
	}
	if token != nil && token.UnlimitedQuota {
		amount = 100000000
	}
	subscription := OpenAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       amount,
		HardLimitUSD:       amount,
		SystemHardLimitUSD: amount,
		AccessUntil:        expiredTime,
	}
	c.JSON(200, subscription)
	return
}

func GetUsage(c *gin.Context) {
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	paymentConfig := operation_setting.GetPaymentRuntimeConfig()
	quotaPerUnit := common.GetQuotaPerUnit()
	runtimeConfig := common.GetGeneralRuntimeConfig()
	var quota int
	var err error
	var token *model.Token
	if runtimeConfig.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		if err == nil {
			if token == nil {
				err = errors.New("token lookup returned no record")
			} else {
				quota = token.UsedQuota
			}
		}
	} else {
		userId := c.GetInt("id")
		quota, err = model.GetUserUsedQuota(userId)
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: common.MaskSensitiveInfo(err.Error()),
			Type:    "new_api_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	amount := float64(quota)
	switch generalSetting.QuotaDisplayType {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / quotaPerUnit * paymentConfig.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// tokens 保持原值
	default:
		amount = amount / quotaPerUnit
	}
	usage := OpenAIUsageResponse{
		Object:     "list",
		TotalUsage: amount * 100,
	}
	c.JSON(200, usage)
	return
}
