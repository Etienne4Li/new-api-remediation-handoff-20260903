package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupContextForSelectedChannelClearsPreviousOrganization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	organization := "first-channel-organization"
	first := &model.Channel{
		Id:                 3101,
		Type:               constant.ChannelTypeOpenAI,
		Key:                "first-key",
		OpenAIOrganization: &organization,
	}
	second := &model.Channel{
		Id:   3102,
		Type: constant.ChannelTypeOpenAI,
		Key:  "second-key",
	}

	require.Nil(t, SetupContextForSelectedChannel(c, first, "gpt-test"))
	require.Equal(t, organization, common.GetContextKeyString(c, constant.ContextKeyChannelOrganization))

	require.Nil(t, SetupContextForSelectedChannel(c, second, "gpt-test"))
	assert.Empty(t, common.GetContextKeyString(c, constant.ContextKeyChannelOrganization))
}
