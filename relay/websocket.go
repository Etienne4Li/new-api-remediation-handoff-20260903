package relay

import (
	"fmt"
	"net/http"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func WssHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	//var requestBody io.Reader
	//firstWssRequest, _ := c.Get("first_wss_request")
	//requestBody = bytes.NewBuffer(firstWssRequest.([]byte))

	statusCodeMappingStr := c.GetString("status_code_mapping")
	resp, err := adaptor.DoRequest(c, info, nil)
	if err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed)
	}

	if resp != nil {
		target, ok := resp.(*websocket.Conn)
		if !ok || target == nil {
			return types.NewErrorWithStatusCode(fmt.Errorf("invalid upstream WebSocket response type %T", resp), types.ErrorCodeBadResponse, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
		}
		info.TargetWs = target
		defer info.TargetWs.Close()
	}

	usage, newAPIError := adaptor.DoResponse(c, nil, info)
	// Realtime handlers may return both a transport error and a partial usage
	// snapshot. Settle that observed usage before propagating the error so an
	// upstream/client disconnect cannot silently turn billable work into a full
	// reservation refund. A nil snapshot means no usage was observed; the outer
	// relay failure path will refund the pending reservation as before.
	realtimeUsage, realtimeUsageOK := usage.(*dto.RealtimeUsage)
	if realtimeUsageOK && realtimeUsage != nil {
		service.PostWssConsumeQuota(c, info, info.UpstreamModelName, realtimeUsage, "")
	}
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}
	if !realtimeUsageOK || realtimeUsage == nil {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid adaptor realtime usage type %T", usage), types.ErrorCodeBadResponseBody, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
	}
	return nil
}
