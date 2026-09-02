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

func TestDeleteRedemptionRejectsMalformedPathID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Redemption{}))
	confirmPaymentComplianceForTest(t)
	redemption := &model.Redemption{UserId: 1, Name: "guard", Key: "guard-redemption-key", Quota: 100, Status: common.RedemptionCodeStatusEnabled}
	require.NoError(t, db.Create(redemption).Error)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/redemption/not-an-id", nil)
	c.Params = gin.Params{{Key: "id", Value: "not-an-id"}}
	DeleteRedemption(c)

	var response struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.False(t, response.Success)
	var count int64
	require.NoError(t, db.Model(&model.Redemption{}).Where("id = ?", redemption.Id).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
