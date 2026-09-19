package agent

// StepError discards the entire candidate Step, including input consumption and
// Effects, while preserving Failure in the Process result. Cause optionally
// retains in-memory evidence for errors.Is/As; only Failure is persisted.
// A valid Failure is required. Cancellation and panic containment remain owned
// by the Engine and take precedence over Strategy classification.
type StepError struct {
	Failure Failure
	Cause   error
}

func (s *StepError) Error() string { return s.Failure.Message() }

func (s *StepError) Unwrap() error { return s.Cause }
