package toolresult

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/tool"
)

// ReadFailure closes a read operation that returned no usable result. The
// boundary must own a read-only backend contract. Cancellation and explicit
// invocation evidence retain their meaning; no outer context snapshot can
// replace an outcome already established by the backend.
func ReadFailure(operation string, cause error) error {
	if cause == nil {
		return nil
	}
	diagnostic := fmt.Errorf("%s: %w", operation, cause)
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return diagnostic
	}
	if _, found := errors.AsType[*tool.AuthorizationError](cause); found {
		return diagnostic
	}
	if _, found := errors.AsType[*tool.CallError](cause); found {
		return diagnostic
	}
	if _, found := tool.ClassifyFailure(cause); found {
		return diagnostic
	}
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: diagnostic,
		Output: tool.NewTextOutput("error: " + operation + " failed"),
	})
	if err != nil {
		return errors.Join(diagnostic, err)
	}
	return failure
}
