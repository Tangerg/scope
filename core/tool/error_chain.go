package tool

// transparentCause follows one error scope. Joining independent errors does
// not let either child speak for the enclosing admission or execution.
//
//nolint:errorlint // errors.As would skip the enclosing scope's decision.
func transparentCause(err error) error {
	switch wrapped := err.(type) {
	case interface{ Unwrap() error }:
		return wrapped.Unwrap()
	case interface{ Unwrap() []error }:
		var cause error
		for _, candidate := range wrapped.Unwrap() {
			if candidate == nil {
				continue
			}
			if cause != nil {
				return nil
			}
			cause = candidate
		}
		return cause
	default:
		return nil
	}
}
