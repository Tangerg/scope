package agent

import (
	"errors"

	"github.com/Tangerg/scope/agent/internal/panicinfo"
)

// callbackError seals the classifications consumed by the runtime while still
// retaining the original error chain for explicit Host inspection. The runtime
// must never traverse that chain after leaving the callback boundary.
type callbackError struct {
	cause    error
	message  string
	step     Failure
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
	c.step, _ = StepFailure(err)
	c.dispatch = dispatchFailure(err)
	c.runtime = newTreeRuntimeFailure(err)
	return c
}

func callbackPanic(operation string, value any) error {
	message, _ := panicinfo.Capture(value)
	cause := &CallbackPanicError{Operation: operation, Value: value}
	return &callbackError{
		cause: cause, message: "agent: " + operation + " panicked: " + message,
		runtime:  newEngineFailure(FailureKindExternal, failureCodeEngineTreeCommitterFailed, errors.New("agent: "+operation+" panicked: "+message)),
		dispatch: dispatchPanicFailure(),
	}
}

// invokeCallback is the single Host callback boundary. A panic becomes a
// CallbackPanicError and every returned error is sealed, so the runtime never
// classifies a Host error chain after this call returns.
func invokeCallback[T any](operation string, call func() (T, error)) (value T, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			var zero T
			value, err = zero, callbackPanic(operation, recovered)
		}
	}()
	value, err = call()
	if err != nil {
		var zero T
		return zero, sealCallbackError(err)
	}
	return value, nil
}

func invokeCallbackErr(operation string, call func() error) error {
	_, err := invokeCallback(operation, func() (struct{}, error) { return struct{}{}, call() })
	return err
}
