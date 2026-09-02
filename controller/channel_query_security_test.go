package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestChannelEndpointsRejectMalformedNumericAndBooleanParameters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModelListControllerTestDB(t)
	for _, endpoint := range []struct {
		name string
		path string
		call func(*gin.Context)
	}{
		{name: "all type", path: "/api/channel?type=oops", call: GetAllChannels},
		{name: "all bool", path: "/api/channel?id_sort=oops", call: GetAllChannels},
		{name: "all tag bool", path: "/api/channel?tag_mode=oops", call: GetAllChannels},
		{name: "all page", path: "/api/channel?p=oops", call: GetAllChannels},
		{name: "all page size", path: "/api/channel?page_size=oops", call: GetAllChannels},
		{name: "search type", path: "/api/channel/search?type=oops", call: SearchChannels},
		{name: "search bool", path: "/api/channel/search?id_sort=oops", call: SearchChannels},
		{name: "search tag bool", path: "/api/channel/search?tag_mode=oops", call: SearchChannels},
		{name: "search page", path: "/api/channel/search?p=oops", call: SearchChannels},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, endpoint.path, nil)
			endpoint.call(ctx)
			var response struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.False(t, response.Success)
		})
	}
}

func TestDeleteChannelRejectsMalformedPathID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupModelListControllerTestDB(t)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/channel/not-an-id", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "not-an-id"}}
	DeleteChannel(ctx)
	var response struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.False(t, response.Success)
}
