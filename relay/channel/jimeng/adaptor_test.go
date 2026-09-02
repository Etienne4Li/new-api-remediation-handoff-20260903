package jimeng

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupRequestHeaderPreparesUnsignedJimengRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	ctx.Request.Header.Set("Accept", "application/custom+json")
	ctx.Request.Header.Set("Content-Type", "application/custom+json")
	header := make(http.Header)
	info := &common.RelayInfo{ChannelMeta: &common.ChannelMeta{ApiKey: "ak|sk"}}

	err := (&Adaptor{}).SetupRequestHeader(ctx, &header, info)
	require.NoError(t, err)
	assert.Equal(t, "application/custom+json", header.Get("Content-Type"))
	assert.Equal(t, "application/custom+json", header.Get("Accept"))
	assert.Empty(t, header.Get("Authorization"), "HMAC auth depends on final URL/body and is applied by DoRequest")
}

func TestSetupRequestHeaderRejectsMissingInputs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	adaptor := &Adaptor{}

	_, err := func() (*http.Header, error) {
		header := make(http.Header)
		return &header, adaptor.SetupRequestHeader(ctx, &header, nil)
	}()
	require.Error(t, err)

	assert.Error(t, err)
	assert.Error(t, adaptor.SetupRequestHeader(nil, &http.Header{}, &common.RelayInfo{ChannelMeta: &common.ChannelMeta{ApiKey: "ak|sk"}}))
}
