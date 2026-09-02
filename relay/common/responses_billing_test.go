package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	rootcommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func newResponsesBillingMarkerContext(t *testing.T) (*gin.Context, *RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &RelayInfo{IsStream: true, RelayMode: relayconstant.RelayModeResponses, StreamStatus: NewStreamStatus()}
	return c, info
}

func TestResponsesBillingMarkersNativeEOFWithoutTerminalIsIncomplete(t *testing.T) {
	c, info := newResponsesBillingMarkerContext(t)
	info.StreamStatus.SetEndReason(StreamEndReasonEOF, nil)

	obs := &ResponsesStreamBillingObservation{}
	obs.ApplyResponsesStreamBillingMarkers(c, info, false, false)

	assert.True(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamIncomplete))
	assert.False(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamTerminalSeen))
}

func TestResponsesBillingMarkersNativeCompletedUsageIsAuthoritative(t *testing.T) {
	c, info := newResponsesBillingMarkerContext(t)
	info.StreamStatus.SetEndReason(StreamEndReasonEOF, nil)
	obs := &ResponsesStreamBillingObservation{}
	obs.ObserveResponsesEvent(&dto.ResponsesStreamResponse{
		Type: "response.completed",
		Response: &dto.OpenAIResponsesResponse{Usage: &dto.Usage{
			InputTokens: 11, OutputTokens: 7, TotalTokens: 18,
		}},
	})
	obs.ApplyResponsesStreamBillingMarkers(c, info, false, false)

	assert.False(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamIncomplete))
	assert.True(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamTerminalSeen))
	assert.True(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesUsageAuthoritative))
}

func TestResponsesBillingMarkersChatDoneIsTerminal(t *testing.T) {
	c, info := newResponsesBillingMarkerContext(t)
	info.StreamStatus.SetEndReason(StreamEndReasonDone, nil)
	obs := &ResponsesStreamBillingObservation{}
	obs.ApplyResponsesStreamBillingMarkers(c, info, false, true)

	assert.False(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamIncomplete))
	assert.True(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamTerminalSeen))
}

func TestResponsesBillingMarkersErrorWithObservedOutputMarksPartialUsage(t *testing.T) {
	c, info := newResponsesBillingMarkerContext(t)
	info.StreamStatus.SetEndReason(StreamEndReasonHandlerStop, nil)
	obs := &ResponsesStreamBillingObservation{}
	obs.ObserveResponsesEvent(&dto.ResponsesStreamResponse{
		Type:  "response.output_text.delta",
		Delta: "partial",
	})
	obs.ApplyResponsesStreamBillingMarkers(c, info, true, false)

	assert.True(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamIncomplete))
	assert.True(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesPartialUsage))
}

func TestResponsesBillingMarkersGeminiFinishReasonIsTerminal(t *testing.T) {
	c, info := newResponsesBillingMarkerContext(t)
	info.StreamStatus.SetEndReason(StreamEndReasonDone, nil)
	finish := "STOP"
	obs := &ResponsesStreamBillingObservation{}
	obs.ObserveGeminiResponse(&dto.GeminiChatResponse{
		Candidates: []dto.GeminiChatCandidate{{FinishReason: &finish}},
	})
	obs.ApplyResponsesStreamBillingMarkers(c, info, false, false)

	assert.False(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamIncomplete))
	assert.True(t, rootcommon.GetContextKeyBool(c, constant.ContextKeyResponsesStreamTerminalSeen))
}
