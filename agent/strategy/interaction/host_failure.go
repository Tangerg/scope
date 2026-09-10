package interaction

import (
	"context"
	"errors"
)

// ErrHostFailure separates host infrastructure failure from model or tool behavior.
var ErrHostFailure = errors.New("interaction: host failure")

type hostFailureError struct {
	cause error
}

func (h hostFailureError) Error() string { return h.cause.Error() }

func (h hostFailureError) Unwrap() error { return h.cause }

func (hostFailureError) Is(target error) bool { return target == ErrHostFailure }

// HostFailure marks cause as an Interaction-host failure. A nil cause remains
// nil, and an already marked error is returned unchanged. A Tool returning this
// error supplies no definite ToolResult: its Effect remains unknown until the
// Host explicitly resolves it. Marking an error does not prove that external
// work failed or authorize replay.
func HostFailure(cause error) error {
	if cause == nil || errors.Is(cause, ErrHostFailure) {
		return cause
	}
	return hostFailureError{cause: cause}
}

func isHostOrContextError(cause error) bool {
	return errors.Is(cause, ErrHostFailure) ||
		errors.Is(cause, context.Canceled) ||
		errors.Is(cause, context.DeadlineExceeded)
}
