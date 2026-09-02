package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type accountBalanceData struct {
	Remaining float64 `json:"remaining"`
	Unit      string  `json:"unit"`
}

func accountBalanceForDisplay(quota int) accountBalanceData {
	quotaPerUnit := common.GetQuotaPerUnit()
	return accountBalanceData{
		Remaining: float64(quota) / quotaPerUnit,
		Unit:      "CNY",
	}
}

func GetAccountBalance(c *gin.Context) {
	c.Header("Cache-Control", "no-store")

	quota, err := model.GetUserQuota(c.GetInt("id"), false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": common.TranslateMessage(c, i18n.MsgDatabaseError),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    accountBalanceForDisplay(quota),
	})
}
