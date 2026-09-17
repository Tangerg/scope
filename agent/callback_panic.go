package agent

import "fmt"

// CallbackPanicError identifies a panic contained at a Host callback boundary.
// Operation names the callback; Value is the original recovered value. When
// Value is an error, Unwrap preserves it for errors.Is and errors.As.
//
// The boundary still owns the outcome: execution failures use FailureKindPanic,
// uncertain dispatch remains unknown, and storage errors stop the active writer
// through RuntimeError. Observational callbacks retain bounded diagnostics in
// ObservationFailures instead of failing execution.
// This error is an in-memory diagnostic and must not be serialized as a result.
type CallbackPanicError struct {
	Operation string
	Value     any
}

func (c *CallbackPanicError) Error() string {
	return fmt.Sprintf("agent: %s panicked: %v", c.Operation, c.Value)
}

func (c *CallbackPanicError) Unwrap() error {
	cause, _ := c.Value.(error)
	return cause
}
