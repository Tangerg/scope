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

func dispatchFailure(err error) (FailureKind, string) {
	if err == nil {
		return FailureKindInvalid, ""
	}
	if _, panicked := errors.AsType[dispatcherPanicError](err); panicked {
		return FailureKindPanic, "engine.dispatch.panicked"
	}
	switch {
	case errors.Is(err, ErrInvalidSettlement):
		return FailureKindContract, "engine.dispatch.settlement.invalid"
	case errors.Is(err, context.DeadlineExceeded):
		return FailureKindExternal, "engine.dispatch.deadline"
	case errors.Is(err, context.Canceled):
		return FailureKindExternal, "engine.dispatch.canceled"
	default:
		return FailureKindExternal, "engine.dispatch.failed"
	}
}
