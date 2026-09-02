package oauth

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// captureOAuthDebugLog runs fn with the logger redirected to an in-memory
// writer. The helper intentionally serializes access to the global Gin writer,
// matching logger.logHelper's lock discipline.
func captureOAuthDebugLog(t *testing.T, fn func()) string {
	t.Helper()
	oldDebug := common.DebugEnabled
	common.DebugEnabled = true
	var buf bytes.Buffer
	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &buf
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
		common.DebugEnabled = oldDebug
	})
	fn()
	return buf.String()
}

func TestGenericOAuthLogsContainMetadataOnly(t *testing.T) {
	const (
		codeSecret     = "authorization-code-secret"
		clientSecret   = "client-secret-value"
		accessSecret   = "access-token-secret"
		endpointSecret = "endpoint-query-secret"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"` + accessSecret + `","token_type":"Bearer"}`))
			return
		}
		_, _ = w.Write([]byte(`{"sub":"provider-user","name":"Example User"}`))
	}))
	defer server.Close()

	oldAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://panel.example.test"
	t.Cleanup(func() { system_setting.ServerAddress = oldAddress })

	provider := NewGenericOAuthProvider(&model.CustomOAuthProvider{
		Name:             "Test Provider",
		Slug:             "test-provider",
		ClientId:         "client-id",
		ClientSecret:     clientSecret,
		TokenEndpoint:    server.URL + "/token?client_secret=" + endpointSecret,
		UserInfoEndpoint: server.URL + "/userinfo?access_token=" + accessSecret,
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	logs := captureOAuthDebugLog(t, func() {
		token, err := provider.ExchangeToken(context.Background(), codeSecret, c)
		require.NoError(t, err)
		require.NotNil(t, token)
	})

	for _, secret := range []string{codeSecret, clientSecret, accessSecret, endpointSecret} {
		require.NotContains(t, logs, secret)
	}
	require.Contains(t, logs, "code_meta=")
	require.Contains(t, logs, "token_endpoint_meta=")
	require.Contains(t, logs, "response_meta=")
}
