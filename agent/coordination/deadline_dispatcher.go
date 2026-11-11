package coordination

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
)

// deadlineDispatcher is the stateless Dispatcher for Deadline. Its call owns and stops its
// timer. Waiting for the same absolute instant has no external side effect, so
// pending recovery can safely repeat that operation with the same identity.
// A reached deadline settles Succeeded with an empty object. Business
// cancellation before the deadline settles Failed to drain the timer, but
// local writer shutdown leaves it unresolved for an explicit SameIdentity replay.
type deadlineDispatcher struct{}

type timerRequest struct {
	Deadline time.Time `json:"deadline"`
}

// timerReached is the empty result of a reached deadline: the settlement
// status owns whether it was reached, and the request owns the deadline.
type timerReached struct{}

func newTimerEffect(deadline time.Time) (run.Effect, error) {
	payload, err := run.EncodePayload(timerRequest{Deadline: deadline})
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func decodeTimerEffect(effect run.Effect) (timerRequest, error) {
	if !effect.Valid() || effect.Target() != run.EffectTargetDispatcher {
		return timerRequest{}, ErrInvalidProtocol
	}
	payload := effect.Payload()
	request, err := payload.Decode[timerRequest]()
	if err != nil {
		return timerRequest{}, fmt.Errorf("%w: decode timer request: %w", ErrInvalidProtocol, err)
	}
	if request.Deadline.IsZero() {
		return timerRequest{}, fmt.Errorf("%w: timer requires an absolute deadline", ErrInvalidProtocol)
	}
	return request, nil
}

func (deadlineDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	if _, err := decodeTimerEffect(effect); err != nil {
		return run.EffectPolicy{Replay: run.ReplayPolicyNever}
	}
	return run.EffectPolicy{Replay: run.ReplayPolicySameIdentity}
}

func (deadlineDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	operation, err := decodeTimerEffect(request.Effect())
	if err != nil {
		return timerFailureSettlement(err)
	}
	if !waitUntil(ctx, operation.Deadline) {
		if errors.Is(context.Cause(ctx), run.ErrWriterStopped) {
			return run.Settlement{}, context.Cause(ctx)
		}
		return timerFailureSettlement(ctx.Err())
	}
	payload, err := run.EncodePayload(timerReached{})
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload)
}

func waitUntil(ctx context.Context, deadline time.Time) bool {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return !time.Now().Before(deadline)
		case <-timer.C:
			// A duration limit or wall-clock adjustment can leave the absolute deadline ahead.
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return true
			}
			timer.Reset(remaining)
		}
	}
}

const failureCodeDeadlineInterrupted = "coordination.deadline.interrupted"

func timerFailureSettlement(cause error) (run.Settlement, error) {
	return stepfail.Settlement(run.FailureKindExternal, failureCodeDeadlineInterrupted, cause.Error())
}
