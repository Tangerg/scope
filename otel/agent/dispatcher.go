package agent

import (
	"context"
	"fmt"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/trace"

	agent "github.com/Tangerg/scope/agent"
)

// WrapDispatcher propagates the observed Effect span into downstream calls.
// Register this same Observer as an Engine EventListener: the Engine publishes
// EffectStarted before dispatch and EffectFinished after dispatch returns.
// Without an active observed Effect, the caller's context passes through.
// The returned decorator preserves replay policy, settlement, and Delta delivery.
func (o *Observer) WrapDispatcher(next agent.Dispatcher) (agent.Dispatcher, error) {
	if o == nil || lo.IsNil(o.tracer) {
		return nil, fmt.Errorf("%w: observer must be constructed with NewObserver", ErrInvalidObserverConfig)
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: dispatcher must not be nil", ErrInvalidObserverConfig)
	}
	return &observedDispatcher{observer: o, next: next}, nil
}

type observedDispatcher struct {
	observer *Observer
	next     agent.Dispatcher
}

func (o *observedDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	incarnationID, _ := request.TreeIncarnationID()
	key := effectKey{
		process:  processKey{processID: request.ProcessID(), incarnationID: incarnationID},
		effectID: request.ID(),
	}
	o.observer.stateMu.Lock()
	span, found := o.observer.effects[key]
	o.observer.stateMu.Unlock()
	if found {
		ctx = trace.ContextWithSpan(ctx, span)
	}
	return o.next.Dispatch(ctx, request, emit)
}

func (o *observedDispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	return o.next.ReplayPolicy(effect)
}

var _ agent.Dispatcher = (*observedDispatcher)(nil)
