package controller

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type legacyRefundSettler struct {
	refundCalls int
}

func (*legacyRefundSettler) Settle(int) error { return nil }

func (s *legacyRefundSettler) Refund(*gin.Context) {
	s.refundCalls++
}

func (*legacyRefundSettler) NeedsRefund() bool { return true }

func (*legacyRefundSettler) GetPreConsumedQuota() int { return 1 }

func (*legacyRefundSettler) Reserve(int) error { return nil }

type observableRefundSettler struct {
	legacyRefundSettler
	refundWithErrorCalls int
	refundErr            error
}

func (s *observableRefundSettler) RefundWithError(*gin.Context) error {
	s.refundWithErrorCalls++
	return s.refundErr
}

func TestRefundBillingAfterRelayFailureReportsDurablePendingRefund(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(common.RequestIdKey, "refund-observability-test")

	var logOutput bytes.Buffer
	common.LogWriterMu.Lock()
	previousErrorWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logOutput
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousErrorWriter
		common.LogWriterMu.Unlock()
	})

	settler := &observableRefundSettler{refundErr: errors.New("temporary database outage")}
	refundBillingAfterRelayFailure(c, settler)

	assert.Equal(t, 1, settler.refundWithErrorCalls)
	assert.Zero(t, settler.refundCalls, "an observable failure must remain pending instead of falling back to the void refund path")
	require.Contains(t, logOutput.String(), "billing refund remains pending for durable retry")
	assert.Contains(t, logOutput.String(), "temporary database outage")
}

func TestRefundBillingAfterRelayFailureKeepsLegacyBillingSettlerCompatible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	settler := &legacyRefundSettler{}
	var billing relaycommon.BillingSettler = settler
	refundBillingAfterRelayFailure(c, billing)

	assert.Equal(t, 1, settler.refundCalls)
}
