package web

import (
	"context"
	"errors"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

// inputFailure reports a request that failed its own admission before any
// backend call as a definite, model-visible Tool failure. An ordinary error
// would instead leave the call outcome uncertain for the driving runtime.
func inputFailure(cause error) error {
	failure, err := toolcontract.NewFailure(toolcontract.FailureConfig{
		Kind:   toolcontract.FailureKindFailed,
		Output: toolcontract.NewTextOutput(cause.Error()),
		Cause:  cause,
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return failure
}

type readOnlyTool struct {
	inner interface {
		toolcontract.Tool
		toolcontract.InputValidatingTool
	}
}

func (r readOnlyTool) Definition() toolcontract.Definition { return r.inner.Definition() }

func (r readOnlyTool) Call(ctx context.Context, invocation toolcontract.Invocation) (toolcontract.Result, error) {
	return r.inner.Call(ctx, invocation)
}

func (readOnlyTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}

func (r readOnlyTool) InputValidator() func([]byte) error { return r.inner.InputValidator() }
