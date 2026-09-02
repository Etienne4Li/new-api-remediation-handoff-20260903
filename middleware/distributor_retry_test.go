package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSpecificChannelIDIsTypeSafe(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int
		valid bool
	}{
		{name: "string", value: "42", want: 42, valid: true},
		{name: "int", value: 42, want: 42, valid: true},
		{name: "int64", value: int64(42), want: 42, valid: true},
		{name: "uint", value: uint(42), want: 42, valid: true},
		{name: "uint64", value: uint64(42), want: 42, valid: true},
		{name: "invalid type", value: struct{}{}, valid: false},
		{name: "invalid string", value: "not-a-number", valid: false},
		{name: "zero", value: "0", valid: false},
		{name: "negative", value: "-1", valid: false},
		{name: "leading zero", value: "042", valid: false},
		{name: "whitespace", value: " 42", valid: false},
		{name: "overflow", value: "9223372036854775808", valid: false},
		{name: "zero int", value: 0, valid: false},
		{name: "negative int", value: -1, valid: false},
		{name: "zero uint", value: uint(0), valid: false},
		{name: "negative int64", value: int64(-1), valid: false},
		{name: "zero uint64", value: uint64(0), valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSpecificChannelID(test.value)
			if test.valid {
				require.NoError(t, err)
				assert.Equal(t, test.want, got)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestChannelSupportsRequestPathCanonicalizesPlaygroundPath(t *testing.T) {
	channel := &model.Channel{Type: constant.ChannelTypePaLM}
	assert.True(t, channelSupportsRequestPath(channel, "/pg/chat/completions", "chat-bison"),
		"PaLM remains eligible for its OpenAI-compatible chat endpoint")

	channel.Type = constant.ChannelTypeAdvancedCustom
	channel.SetOtherSettings(dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{
		Routes: []dto.AdvancedCustomRoute{{
			IncomingPath: "/v1/chat/completions",
			UpstreamPath: "https://upstream.example/v1/chat/completions",
			Models:       []string{"gpt-test"},
		}},
	}})
	assert.True(t, channelSupportsRequestPath(channel, "/pg/chat/completions", "gpt-test"))
}

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
