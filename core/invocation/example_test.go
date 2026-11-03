package invocation_test

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/invocation"
)

func Example() {
	admission := &invocation.Failure{
		Kind:  invocation.FailureNotExecuted,
		Cause: errors.New("unsupported model option"),
	}
	completedWork := &invocation.Failure{
		Kind:  invocation.FailureFailed,
		Cause: fmt.Errorf("follow-up request: %w", admission),
	}
	fmt.Println(invocation.ClassifyFailure(admission))
	fmt.Println(invocation.ClassifyFailure(completedWork))
	fmt.Println(errors.Is(completedWork, admission))
	// Output:
	// not_executed
	// failed
	// true
}
