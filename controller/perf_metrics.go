package controller

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func GetPerfMetricsSummary(c *gin.Context) {
	hours, err := parsePerfMetricsHours(c)
	if err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}

	activeGroups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
	result, err := perfmetrics.QuerySummaryAll(hours, activeGroups)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": common.MaskSensitiveInfo(err.Error()),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

func GetPerfMetrics(c *gin.Context) {
	modelName := c.Query("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "model is required",
		})
		return
	}

	hours, err := parsePerfMetricsHours(c)
	if err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}

	result, err := perfmetrics.Query(perfmetrics.QueryParams{
		Model: modelName,
		Group: c.Query("group"),
		Hours: hours,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": common.MaskSensitiveInfo(err.Error()),
		})
		return
	}

	result.Groups = filterActiveGroups(result.Groups)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

// parsePerfMetricsHours distinguishes an omitted query parameter (which uses
// the documented 24-hour default) from malformed or non-positive input.  The
// model package still applies its 30-day upper bound as a second defense.
func parsePerfMetricsHours(c *gin.Context) (int, error) {
	rawHours := c.Query("hours")
	if rawHours == "" {
		return 24, nil
	}
	hours, err := strconv.Atoi(rawHours)
	if err != nil || hours <= 0 {
		return 0, fmt.Errorf("hours must be a positive integer")
	}
	return hours, nil
}

func filterActiveGroups(groups []perfmetrics.GroupResult) []perfmetrics.GroupResult {
	activeRatios := ratio_setting.GetGroupRatioCopy()
	return lo.Filter(groups, func(g perfmetrics.GroupResult, _ int) bool {
		_, ok := activeRatios[g.Group]
		return ok || g.Group == "auto"
	})
}
