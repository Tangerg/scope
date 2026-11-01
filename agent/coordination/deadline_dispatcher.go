package coordination

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/Tangerg/scope/agent/run"
)

// DeadlineDispatcher is the stateless Dispatcher for Deadline. Its call owns and stops its
// timer. Waiting for the same absolute instant has no external side effect, so
// pending recovery can safely repeat that operation with the same identity.
// A reached deadline settles Succeeded with an empty object. Malformed timer
// payloads and cancellation settle Failed with a JSON diagnostic string, so
// cancellation never leaves an unknown external outcome.
type DeadlineDispatcher struct{}

type timerRequest struct {
	Deadline time.Time `json:"deadline"`
}

// timerReached is the empty result of a reached deadline: the settlement
// status owns whether it was reached, and the request owns the deadline.
type timerReached struct{}

func newTimerEffect(deadline time.Time) (run.Effect, error) {
	payload, err := jsonv2.Marshal(timerRequest{Deadline: deadline})
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func decodeTimerEffect(effect run.Effect) (timerRequest, error) {
	if !effect.Valid() || effect.Target() != run.EffectTargetDispatcher {
		return timerRequest{}, ErrInvalidProtocol
	}
	payload, err := run.ParsePayload(effect.Payload())
	if err != nil {
		return timerRequest{}, err
	}
	request, err := payload.Decode[timerRequest]()
	if err != nil {
		return timerRequest{}, fmt.Errorf("%w: decode timer request: %w", ErrInvalidProtocol, err)
	}
	if request.Deadline.IsZero() {
		return timerRequest{}, fmt.Errorf("%w: timer requires an absolute deadline", ErrInvalidProtocol)
	}
	return request, nil
}

func (DeadlineDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	if _, err := decodeTimerEffect(effect); err != nil {
		return run.EffectPolicy{Replay: run.ReplayPolicyNever}
	}
	return run.EffectPolicy{Replay: run.ReplayPolicySameIdentity}
}

// Dispatch requires a non-nil context and panics if ctx is nil.
func (DeadlineDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	ctx = run.RequireContext(ctx)
	if !request.Valid() {
		return run.Settlement{}, ErrInvalidProtocol
	}
	operation, err := decodeTimerEffect(request.Effect())
	if err != nil {
		return timerFailureSettlement(err)
	}
	if ctx.Err() != nil || !waitUntil(ctx, operation.Deadline) {
		return timerFailureSettlement(errors.New("timer returned before its deadline"))
	}
	payload, err := jsonv2.Marshal(timerReached{})
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
			return false
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

func timerFailureSettlement(cause error) (run.Settlement, error) {
	payload, err := jsonv2.Marshal(run.NormalizeDiagnostic(cause.Error()))
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusFailed, payload)
}
