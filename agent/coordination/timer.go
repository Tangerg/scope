package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agent "github.com/Tangerg/scope/agent"
)

// Timer is the stateless Dispatcher for Deadline. Its call owns and stops its
// timer. Waiting for the same absolute instant has no external side effect, so
// pending recovery can safely repeat that operation with the same identity.
// A settled Unknown still requires the Engine's explicit adjudication path.
type Timer struct{}

type timerRequest struct {
	Deadline time.Time `json:"deadline"`
}

type timerResult struct {
	Deadline time.Time `json:"deadline"`
	Reached  bool      `json:"reached"`
}

func newTimerEffect(deadline time.Time) (agent.Effect, error) {
	payload, err := json.Marshal(timerRequest{Deadline: deadline})
	if err != nil {
		return agent.Effect{}, err
	}
	return agent.NewDispatcherEffect(payload)
}

func decodeTimerEffect(effect agent.Effect) (timerRequest, error) {
	if !effect.Valid() || effect.Target() != agent.EffectTargetDispatcher {
		return timerRequest{}, ErrInvalidProtocol
	}
	payload, err := agent.ParseInput(effect.Payload())
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

func (Timer) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	if _, err := decodeTimerEffect(effect); err != nil {
		return agent.ReplayPolicyNever
	}
	return agent.ReplayPolicySameIdentity
}

// Dispatch requires a non-nil context and panics if ctx is nil.
func (Timer) Dispatch(ctx context.Context, request agent.EffectRequest, _ agent.DeltaEmitter) (agent.Settlement, error) {
	if ctx == nil {
		panic(errors.New("coordination: nil Context"))
	}
	if !request.Valid() {
		return agent.Settlement{}, ErrInvalidProtocol
	}
	operation, err := decodeTimerEffect(request.Effect())
	if err != nil {
		return agent.Settlement{}, err
	}
	result := timerResult{Deadline: operation.Deadline}
	if ctx.Err() == nil {
		timer := time.NewTimer(time.Until(operation.Deadline))
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				// A duration limit or wall-clock adjustment can leave the absolute deadline ahead.
				if remaining := time.Until(operation.Deadline); remaining > 0 {
					timer.Reset(remaining)
					continue
				}
				result.Reached = true
			case <-ctx.Done():
			}
			break
		}
	}
	status := agent.SettlementStatusFailed
	if result.Reached {
		status = agent.SettlementStatusSucceeded
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(request.ID(), status, payload)
}
