package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// parseLogIntQuery treats an omitted query parameter as its historical zero
// value, but never turns malformed input into an unrestricted query.  The
// previous `value, _ := Atoi(...)` pattern made `?channel=oops` silently mean
// all channels and made typoed time bounds scan the entire log table.
func parseLogIntQuery(c *gin.Context, key string, min int) (int, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min {
		common.ApiErrorMsg(c, "参数错误")
		return 0, false
	}
	return value, true
}

func parseLogInt64Query(c *gin.Context, key string, min int64) (int64, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < min {
		common.ApiErrorMsg(c, "参数错误")
		return 0, false
	}
	return value, true
}

func GetAllLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	logType, ok := parseLogIntQuery(c, "type", 0)
	if !ok {
		return
	}
	startTimestamp, ok := parseLogInt64Query(c, "start_timestamp", 0)
	if !ok {
		return
	}
	endTimestamp, ok := parseLogInt64Query(c, "end_timestamp", 0)
	if !ok {
		return
	}
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, ok := parseLogIntQuery(c, "channel", 0)
	if !ok {
		return
	}
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetAllLogsForRole(logType, startTimestamp, endTimestamp, modelName, username, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), channel, group, requestId, upstreamRequestId, c.GetInt("role"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

func GetUserLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	userId := c.GetInt("id")
	logType, ok := parseLogIntQuery(c, "type", 0)
	if !ok {
		return
	}
	startTimestamp, ok := parseLogInt64Query(c, "start_timestamp", 0)
	if !ok {
		return
	}
	endTimestamp, ok := parseLogInt64Query(c, "end_timestamp", 0)
	if !ok {
		return
	}
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetUserLogs(userId, logType, startTimestamp, endTimestamp, modelName, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func SearchAllLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func SearchUserLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

func GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		c.JSON(200, gin.H{
			"success": false,
			"message": "无效的令牌",
		})
		return
	}
	logs, err := model.GetLogByTokenId(tokenId)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": common.MaskSensitiveInfo(err.Error()),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data":    logs,
	})
}

func GetLogsStat(c *gin.Context) {
	logType, ok := parseLogIntQuery(c, "type", 0)
	if !ok {
		return
	}
	startTimestamp, ok := parseLogInt64Query(c, "start_timestamp", 0)
	if !ok {
		return
	}
	endTimestamp, ok := parseLogInt64Query(c, "end_timestamp", 0)
	if !ok {
		return
	}
	tokenName := c.Query("token_name")
	username := c.Query("username")
	modelName := c.Query("model_name")
	channel, ok := parseLogIntQuery(c, "channel", 0)
	if !ok {
		return
	}
	group := c.Query("group")
	stat, err := model.SumUsedQuotaForRole(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group, c.GetInt("role"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, "")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": stat.Quota,
			"rpm":   stat.Rpm,
			"tpm":   stat.Tpm,
		},
	})
	return
}

func GetLogsSelfStat(c *gin.Context) {
	username := c.GetString("username")
	logType, ok := parseLogIntQuery(c, "type", 0)
	if !ok {
		return
	}
	startTimestamp, ok := parseLogInt64Query(c, "start_timestamp", 0)
	if !ok {
		return
	}
	endTimestamp, ok := parseLogInt64Query(c, "end_timestamp", 0)
	if !ok {
		return
	}
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, ok := parseLogIntQuery(c, "channel", 0)
	if !ok {
		return
	}
	group := c.Query("group")
	quotaNum, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, tokenName)
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": quotaNum.Quota,
			"rpm":   quotaNum.Rpm,
			"tpm":   quotaNum.Tpm,
			//"token": tokenNum,
		},
	})
	return
}
