package agent

import (
	"context"
	"errors"
	"fmt"
)

const nullJSON = "null"

var errInvalidReplayPolicy = errors.New("agent: invalid Dispatcher replay policy")

func dispatcherReplayPolicy(
	dispatcher Dispatcher,
	effect Effect,
) (policy ReplayPolicy, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			policy = ReplayPolicyInvalid
			err = fmt.Errorf("%w: panic: %v", errInvalidReplayPolicy, recovered)
		}
	}()
	policy = dispatcher.ReplayPolicy(effect)
	if !policy.Valid() {
		return ReplayPolicyInvalid, errInvalidReplayPolicy
	}
	return policy, nil
}

func dispatchEffect(
	ctx context.Context,
	dispatcher Dispatcher,
	request EffectRequest,
	emit DeltaEmitter,
) (settlement Settlement, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			settlement = Settlement{}
			err = dispatcherPanicError{value: recovered}
		}
	}()
	return dispatcher.Dispatch(ctx, request, emit)
}

type dispatcherPanicError struct{ value any }

func (d dispatcherPanicError) Error() string {
	return fmt.Sprintf("dispatcher panicked: %v", d.value)
}

// Diagnostics cross persistence and observation boundaries; arbitrary error
// text does not. The original error remains available to the dispatch caller.
func dispatchFailure(err error) Failure {
	if err == nil {
		return Failure{}
	}
	if _, panicked := errors.AsType[dispatcherPanicError](err); panicked {
		return newEngineFailure(FailureKindPanic, failureCodeEngineDispatchPanicked, errors.New("Dispatcher panicked without a definite outcome"))
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
