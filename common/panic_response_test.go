package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWritePanicResponseDoesNotExposePanicValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/panic", func(c *gin.Context) {
		c.Set(RequestIdKey, "req-panic-123")
		WritePanicResponse(c)
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.Equal(t, `{"error":{"message":"Internal server error (request id: req-panic-123)","type":"new_api_panic"}}`, response.Body.String())
	require.Contains(t, response.Body.String(), "req-panic-123")
}
