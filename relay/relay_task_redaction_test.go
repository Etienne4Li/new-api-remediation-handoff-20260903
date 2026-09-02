package relay

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskModel2DtoRedactsHistoricalDataAndResultURL(t *testing.T) {
	task := &model.Task{
		ID:         42,
		TaskID:     "task-history",
		Data:       []byte(`{"api_key":"old-secret","result":{"url":"https://cdn.example/result.mp4?X-Amz-Signature=private"}}`),
		FailReason: "provider failed at https://cdn.example/result.mp4?token=private Authorization: Bearer secret-token",
		PrivateData: model.TaskPrivateData{
			ResultURL: "https://cdn.example/result.mp4?token=private",
		},
	}

	dto := TaskModel2Dto(task)
	require.NotNil(t, dto)
	assert.NotContains(t, string(dto.Data), "old-secret")
	assert.NotContains(t, string(dto.Data), "X-Amz-Signature")
	assert.Equal(t, "https://cdn.example/result.mp4", dto.ResultURL)
	assert.NotContains(t, dto.FailReason, "token=private")
	assert.NotContains(t, dto.FailReason, "secret-token")
	assert.Contains(t, dto.FailReason, "provider failed")
}

func TestTaskModel2DtoBoundsFailureReason(t *testing.T) {
	task := &model.Task{TaskID: "task-failure-reason", FailReason: strings.Repeat("x", 5000)}

	dto := TaskModel2Dto(task)
	require.NotNil(t, dto)
	assert.LessOrEqual(t, len(dto.FailReason), 4096)
}

func TestTaskModel2DtoNilIsSafe(t *testing.T) {
	assert.Nil(t, TaskModel2Dto(nil))
}
