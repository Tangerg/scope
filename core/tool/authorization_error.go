package tool

import "fmt"

// AuthorizationError means the policy could not establish a decision for the
// current invocation. The Tool was not executed. Its internal cause is not the
// Tool's outcome, public output, input request, or execution cancellation.
// Guard separately preserves cancellation of the execution context.
type AuthorizationError struct {
	name  string
	cause error
}

func (a *AuthorizationError) Error() string {
	if a == nil || a.name == "" {
		return "tool: authorization failed"
	}
	return fmt.Sprintf("tool: authorization for %q failed", a.name)
}

// Cause retains the policy diagnostic without exposing it through Error,
// errors.Is, or errors.As. Callers must explicitly choose to inspect it.
func (a *AuthorizationError) Cause() error {
	if a == nil {
		return nil
	}
	return a.cause
}
