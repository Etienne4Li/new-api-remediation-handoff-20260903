package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStatusPublishesOnlyKeylessChatsAndDisablesCaching(t *testing.T) {
	previous := setting.GetChats()
	t.Cleanup(func() { setting.Chats = previous })
	setting.Chats = []map[string]string{
		{"Safe": "https://example.com/chat"},
		{"Template": "client://open?key={key}"},
		{"Fluent": "fluentread"},
	}

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)

	GetStatus(context)

	assert.Equal(t, "no-store, no-cache, must-revalidate, private, max-age=0", response.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", response.Header().Get("Pragma"))
	assert.Equal(t, "0", response.Header().Get("Expires"))

	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.Equal(t, []any{
		map[string]any{"Safe": "https://example.com/chat"},
		map[string]any{"Fluent": "fluentread"},
	}, payload.Data["chats"])
}
