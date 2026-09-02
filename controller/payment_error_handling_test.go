package controller

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// A payment callback is already authenticated when it reaches the response
// writer.  A write failure must still be visible in the audit stream so an
// operator can distinguish a provider retry from a transport failure.
func TestWriteEpayFailureAuditsResponseWriteError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousWriter := gin.DefaultErrorWriter
	var logBuffer bytes.Buffer
	gin.DefaultErrorWriter = &logBuffer
	t.Cleanup(func() { gin.DefaultErrorWriter = previousWriter })

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/epay/notify", nil)
	c.Writer = failingPaymentResponseWriter{ResponseWriter: c.Writer}

	writeEpayFailure(c)

	require.Contains(t, logBuffer.String(), "易支付 webhook 响应写入失败")
}

type failingPaymentResponseWriter struct {
	gin.ResponseWriter
}

func (f failingPaymentResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic response write failure")
}

// Keep the compensation paths fail-closed during future edits.  These are
// intentionally source-level guards: third-party checkout failures are not
// reproducible without making real provider calls, while silently dropping a
// local status-update error is a deterministic review regression.
func TestPaymentControllersDoNotSilentlyDropOrderCompensationErrors(t *testing.T) {
	files := []string{
		"topup.go",
		"topup_creem.go",
		"topup_stripe.go",
		"topup_waffo.go",
		"topup_waffo_pancake.go",
		"subscription_payment_epay.go",
		"subscription_payment_creem.go",
		"subscription_payment_stripe.go",
		"subscription_payment_waffo_pancake.go",
	}
	for _, name := range files {
		contents, err := os.ReadFile(name)
		require.NoError(t, err)
		source := string(contents)
		require.NotContains(t, source, "_ = model.UpdatePendingTopUpStatus", name)
		require.NotContains(t, source, "_ = model.ExpireSubscriptionOrder", name)
		require.NotContains(t, source, "_, _ = c.Writer.Write", name)
	}
}
