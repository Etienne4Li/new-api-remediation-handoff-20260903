package relay

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// requireHTTPResponse validates the value returned by an adaptor's DoRequest
// method before relay handlers inspect it.  Adaptors return any so that a few
// specialised transports (for example WebSocket) can share the interface;
// HTTP handlers must fence an unexpected value instead of panicking on a type
// assertion.
func requireHTTPResponse(value any) (*http.Response, *types.NewAPIError) {
	response, ok := value.(*http.Response)
	if !ok || response == nil {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("invalid upstream HTTP response type %T", value),
			types.ErrorCodeBadResponse,
			http.StatusInternalServerError,
			types.ErrOptionWithSkipRetry(),
		)
	}
	return response, nil
}

// requireUsage validates the usage value returned by an adaptor's DoResponse
// method.  Billing code relies on dto.Usage; an adaptor contract violation
// must become a controlled relay error rather than a process-level panic.
func requireUsage(value any) (*dto.Usage, *types.NewAPIError) {
	usage, ok := value.(*dto.Usage)
	if !ok || usage == nil {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("invalid adaptor usage type %T", value),
			types.ErrorCodeBadResponseBody,
			http.StatusInternalServerError,
			types.ErrOptionWithSkipRetry(),
		)
	}
	return usage, nil
}
