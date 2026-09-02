package helper

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// StreamResult is passed to each dataHandler invocation, providing methods
// to record soft errors, signal fatal stops, or mark normal completion.
// StreamScannerHandler checks IsStopped() after each callback invocation.
type StreamResult struct {
	status   *relaycommon.StreamStatus
	stopped  bool
	draining func() bool
}

func newStreamResult(status *relaycommon.StreamStatus, draining ...func() bool) *StreamResult {
	result := &StreamResult{status: status}
	if len(draining) > 0 {
		result.draining = draining[0]
	}
	return result
}

// Error records a soft error. The stream continues processing.
// Can be called multiple times per chunk.
func (r *StreamResult) Error(err error) {
	if err == nil {
		return
	}
	r.status.RecordError(err.Error())
}

// Stop records a fatal error and marks the stream to stop after this chunk.
func (r *StreamResult) Stop(err error) {
	if err != nil {
		r.status.RecordError(err.Error())
	}
	r.status.SetEndReason(relaycommon.StreamEndReasonHandlerStop, err)
	r.stopped = true
}

// Done signals that the handler has finished processing normally
// (e.g., Dify "message_end"). The stream stops after this chunk.
func (r *StreamResult) Done() {
	r.status.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	r.stopped = true
}

// IsStopped returns whether Stop() or Done() was called during this chunk.
func (r *StreamResult) IsStopped() bool {
	return r.stopped
}

// IsDraining reports that the downstream client has disconnected and the
// adapter is consuming a short, bounded tail of the upstream stream.  A
// drain-capable adapter should continue parsing/accounting events but skip
// writes to the now-closed downstream connection.
func (r *StreamResult) IsDraining() bool {
	return r != nil && r.draining != nil && r.draining()
}

// reset clears the per-chunk stopped flag so the object can be reused.
func (r *StreamResult) reset() {
	r.stopped = false
}
