package agent

import (
	"errors"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/panicinfo"
)

// callbackError seals the classifications consumed by the runtime while still
// retaining the original error chain for explicit Host inspection. The runtime
// must never traverse that chain after leaving the callback boundary.
type callbackError struct {
	cause    error
	message  string
	kind     FailureKind
	step     *StepError
	dispatch Failure
	runtime  Failure
}

func (c *callbackError) Error() string { return c.message }
func (c *callbackError) Unwrap() error { return c.cause }

func sealCallbackError(err error) error {
	if err == nil {
		return nil
	}
	c := &callbackError{cause: err, message: err.Error()}
	c.kind = failureKindForError(err, FailureKindExecution)
	if step, ok := errors.AsType[*StepError](err); ok {
		c.step = &StepError{}
		if !lo.IsNil(step) {
			c.step.Failure = step.Failure
		}
	}
	c.dispatch = dispatchFailure(err)
	c.runtime = newTreeRuntimeFailure(err)
	return c
}

func callbackPanic(operation string, value any) error {
	message, _ := panicinfo.Capture(value)
	cause := &CallbackPanicError{Operation: operation, Value: value}
	return &callbackError{
		cause: cause, message: "agent: " + operation + " panicked: " + message,
		kind:     FailureKindPanic,
		runtime:  newEngineFailure(FailureKindExternal, failureCodeEngineTreeCommitterFailed, errors.New("agent: "+operation+" panicked: "+message)),
		dispatch: newEngineFailure(FailureKindPanic, failureCodeEngineDispatchPanicked, errors.New("Dispatcher panicked without a definite outcome")),
	}
}
