package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayPanicRecoverDoesNotExposePanicValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestId(), RelayPanicRecover())
	router.GET("/panic", func(c *gin.Context) {
		panic("dial tcp db.internal:3306: password=super-secret")
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.NotContains(t, response.Body.String(), "super-secret")
	require.NotContains(t, response.Body.String(), "db.internal")
	requestID := response.Header().Get(common.RequestIdKey)
	require.NotEmpty(t, requestID)
	require.Contains(t, response.Body.String(), requestID)
}
