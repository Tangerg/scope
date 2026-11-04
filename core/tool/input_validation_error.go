package tool

// InputValidationError contains a defect in an input validator. It carries no
// public input correction or execution outcome. Inspect Cause explicitly for
// diagnostics, including a contained InputValidationPanicError.
type InputValidationError struct{ cause error }

func (*InputValidationError) Error() string { return "tool: input validation failed" }

func (i *InputValidationError) Cause() error {
	if i == nil {
		return nil
	}
	return i.cause
}
