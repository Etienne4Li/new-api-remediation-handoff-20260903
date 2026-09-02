package oauth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingReader struct {
	reader io.Reader
	reads  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.reader.Read(p)
}

func TestReadOAuthResponseBodyRejectsDeclaredOversizeBeforeReading(t *testing.T) {
	body := &countingReader{reader: strings.NewReader("small")}
	resp := &http.Response{ContentLength: maxOAuthResponseBodyBytes + 1, Body: io.NopCloser(body)}

	_, err := readOAuthResponseBody(resp)
	require.ErrorIs(t, err, ErrOAuthResponseTooLarge)
	require.Zero(t, body.reads, "known oversized responses should be rejected before reading")
}

func TestReadOAuthResponseBodyRejectsChunkedOversize(t *testing.T) {
	resp := &http.Response{
		ContentLength: -1,
		Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", int(maxOAuthResponseBodyBytes)+1))),
	}

	_, err := readOAuthResponseBody(resp)
	require.ErrorIs(t, err, ErrOAuthResponseTooLarge)
}

func TestReadOAuthResponseBodyAcceptsSmallUnknownLength(t *testing.T) {
	const payload = `{"access_token":"token"}`
	resp := &http.Response{ContentLength: -1, Body: io.NopCloser(strings.NewReader(payload))}

	body, err := readOAuthResponseBody(resp)
	require.NoError(t, err)
	require.Equal(t, payload, string(body))
}

func TestDecodeOAuthJSONResponseUsesBoundedReader(t *testing.T) {
	var decoded struct {
		AccessToken string `json:"access_token"`
	}
	resp := &http.Response{ContentLength: int64(len(`{"access_token":"ok"}`)), Body: io.NopCloser(bytes.NewBufferString(`{"access_token":"ok"}`))}
	require.NoError(t, decodeOAuthJSONResponse(resp, &decoded))
	require.Equal(t, "ok", decoded.AccessToken)

	oversized := &http.Response{ContentLength: -1, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(maxOAuthResponseBodyBytes)+1)))}
	require.ErrorIs(t, decodeOAuthJSONResponse(oversized, &decoded), ErrOAuthResponseTooLarge)
}

func TestGenericOAuthExchangeRejectsOversizedProviderResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Deliberately omit Content-Length to exercise the chunked/unknown
		// length path in readOAuthResponseBody.
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxOAuthResponseBodyBytes)+1)))
	}))
	defer server.Close()

	provider := NewGenericOAuthProvider(&model.CustomOAuthProvider{
		Name:          "Bounded Provider",
		Slug:          "bounded-provider",
		ClientId:      "client",
		ClientSecret:  "secret",
		TokenEndpoint: server.URL,
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	_, err := provider.ExchangeToken(context.Background(), "code", c)
	require.ErrorIs(t, err, ErrOAuthResponseTooLarge)
}

func TestGenericOAuthUserInfoRejectsOversizedProviderResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxOAuthResponseBodyBytes)+1)))
	}))
	defer server.Close()

	provider := NewGenericOAuthProvider(&model.CustomOAuthProvider{
		Name:             "Bounded Provider",
		Slug:             "bounded-provider",
		UserInfoEndpoint: server.URL,
	})
	_, err := provider.GetUserInfo(context.Background(), &OAuthToken{AccessToken: "token"})
	assert.ErrorIs(t, err, ErrOAuthResponseTooLarge)
}
