package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestGetUserLogsMasksErrorDetailsWithoutChangingStoredRecord(t *testing.T) {
	truncateTables(t)

	rawContent := "provider endpoint https://secret.example and account leaked"
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:    1,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeError,
		Content:   rawContent,
		Other: common.MapToJsonStr(map[string]interface{}{
			"error_code":  "invalid_api_key",
			"status_code": 401,
			"admin_info": map[string]interface{}{
				"use_channel": []int{1, 2},
			},
		}),
	}).Error)

	logs, total, err := GetUserLogs(1, LogTypeError, 0, 0, "", "", 0, 10, "", "", "")
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, logs, 1)
	require.Equal(t, "invalid_api_key", logs[0].Content)
	require.NotContains(t, logs[0].Other, "admin_info")
	require.NotContains(t, logs[0].Other, "use_channel")

	var stored Log
	require.NoError(t, LOG_DB.Where("user_id = ? AND type = ?", 1, LogTypeError).First(&stored).Error)
	require.Equal(t, rawContent, stored.Content)
	require.Contains(t, stored.Other, "admin_info")
}

func TestSanitizeLogsForNonRootUsesStableFallbackAndCopiesRecords(t *testing.T) {
	raw := &Log{Type: LogTypeError, Content: "provider secret", Other: `{}`}
	logs := SanitizeLogsForNonRoot([]*Log{raw})

	require.Len(t, logs, 1)
	require.Equal(t, hiddenUpstreamErrorCode, logs[0].Content)
	require.Equal(t, "provider secret", raw.Content)
}
