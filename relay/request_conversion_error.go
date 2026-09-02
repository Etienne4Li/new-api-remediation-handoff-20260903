package relay

import "github.com/QuantumNous/new-api/relaykit/types"

// requestConversionError translates an adaptor conversion failure into the
// relay error used by the outer channel retry loop. A capability miss is safe
// to retry on another channel because no upstream request was sent; malformed
// input and internal conversion failures remain fenced to avoid replaying the
// same request indefinitely.
func requestConversionError(err error) *types.NewAPIError {
	if types.IsUnsupportedEndpointError(err) {
		return types.NewError(err, types.ErrorCodeConvertRequestFailed)
	}
	return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
}
