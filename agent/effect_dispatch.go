package agent

import (
	"context"
	"errors"
	"fmt"
)

var errInvalidReplayPolicy = errors.New("agent: invalid Dispatcher replay policy")

func dispatcherReplayPolicy(dispatcher Dispatcher, effect Effect) (ReplayPolicy, error) {
	policy, err := invokeCallback("Dispatcher.ReplayPolicy", func() (ReplayPolicy, error) {
		return dispatcher.ReplayPolicy(effect), nil
	})
	if err != nil {
		return ReplayPolicyInvalid, fmt.Errorf("%w: %w", errInvalidReplayPolicy, err)
	}
	if !policy.Valid() {
		return ReplayPolicyInvalid, errInvalidReplayPolicy
	}
	return policy, nil
}

func dispatchEffect(ctx context.Context, dispatcher Dispatcher, request EffectRequest, emit DeltaEmitter) (Settlement, error) {
	return invokeCallback("Dispatcher.Dispatch", func() (Settlement, error) {
		return dispatcher.Dispatch(ctx, request, emit)
	})
}

// Diagnostics cross persistence and observation boundaries; arbitrary error
// text does not. The original error remains available to the dispatch caller.
func dispatchFailure(err error) Failure {
	if err == nil {
		return Failure{}
	}
	if sealed, ok := errors.AsType[*callbackError](err); ok && sealed != nil {
		return sealed.dispatch
	}
	if _, panicked := errors.AsType[*CallbackPanicError](err); panicked {
		return dispatchPanicFailure()
	}
	switch {
	case errors.Is(err, ErrInvalidSettlement):
		return newEngineFailure(FailureKindContract, failureCodeEngineDispatchSettlementInvalid, errors.New("Dispatcher returned an invalid settlement"))
	case errors.Is(err, context.DeadlineExceeded):
		return newEngineFailure(FailureKindExternal, failureCodeEngineDispatchDeadline, errors.New("Dispatcher deadline expired without a definite outcome"))
	case errors.Is(err, context.Canceled):
		return newEngineFailure(FailureKindExternal, failureCodeEngineDispatchCanceled, errors.New("Dispatcher was canceled without a definite outcome"))
	default:
		return newEngineFailure(FailureKindExternal, failureCodeEngineDispatchFailed, errors.New("Dispatcher returned an error without a definite outcome"))
	}
}

func dispatchPanicFailure() Failure {
	return newEngineFailure(FailureKindPanic, failureCodeEngineDispatchPanicked, errors.New("Dispatcher panicked without a definite outcome"))
}
