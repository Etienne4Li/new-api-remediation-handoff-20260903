package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTaskEndpointsRejectMalformedTimestamps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		name string
		path string
		call func(*gin.Context)
	}{
		{name: "admin", path: "/api/task?start_timestamp=oops", call: GetAllTask},
		{name: "self", path: "/api/task/self?end_timestamp=oops", call: GetUserTask},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", 1)
			ctx.Set("role", common.RoleAdminUser)
			ctx.Request = httptest.NewRequest(http.MethodGet, endpoint.path, nil)
			endpoint.call(ctx)
			var payload struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			require.False(t, payload.Success)
		})
	}
}
