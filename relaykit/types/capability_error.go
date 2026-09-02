package types

import (
	"errors"
	"fmt"
)

// UnsupportedEndpointError reports that a channel adaptor cannot serve a
// particular relay endpoint.  It is deliberately a plain Go error so callers
// can decide whether to retry on another channel without coupling adaptor
// packages to the HTTP error response format.
type UnsupportedEndpointError struct {
	ChannelName string
	Endpoint    string
}

func (e *UnsupportedEndpointError) Error() string {
	if e == nil {
		return "unsupported endpoint"
	}
	if e.ChannelName == "" {
		return fmt.Sprintf("endpoint %q is not supported", e.Endpoint)
	}
	return fmt.Sprintf("channel %q does not support endpoint %q", e.ChannelName, e.Endpoint)
}

// NewUnsupportedEndpointError constructs a capability error for an adaptor.
func NewUnsupportedEndpointError(channelName, endpoint string) *UnsupportedEndpointError {
	return &UnsupportedEndpointError{ChannelName: channelName, Endpoint: endpoint}
}

// IsUnsupportedEndpointError reports whether err (including wrapped errors)
// contains an UnsupportedEndpointError.
func IsUnsupportedEndpointError(err error) bool {
	var target *UnsupportedEndpointError
	return errors.As(err, &target)
}
