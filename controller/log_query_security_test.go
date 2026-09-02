package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAdminLogQueriesRejectMalformedNumericFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Log{}))
	user := &model.User{Username: "log-query-admin-target", AffCode: "log-query-admin-target-aff", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(&model.Log{UserId: user.Id, Username: user.Username, Type: model.LogTypeConsume, Quota: 10, CreatedAt: 100}).Error)

	for _, path := range []string{
		"/api/log?type=not-a-number",
		"/api/log?start_timestamp=not-a-number",
		"/api/log?end_timestamp=not-a-number",
		"/api/log?channel=not-a-number",
		"/api/log/stat?type=not-a-number",
		"/api/log/stat?start_timestamp=not-a-number",
		"/api/log/stat?end_timestamp=not-a-number",
		"/api/log/stat?channel=not-a-number",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, path, nil)
			c.Set("role", common.RoleAdminUser)
			if len(path) >= len("/api/log/stat") && path[:len("/api/log/stat")] == "/api/log/stat" {
				GetLogsStat(c)
			} else {
				GetAllLogs(c)
			}
			var response struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.False(t, response.Success, "malformed numeric filter must fail closed")
		})
	}
}
