package agent

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	agent "github.com/Tangerg/scope/agent"
)

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

func (o *observedDispatcher) Policy(effect agent.Effect) agent.EffectPolicy {
	return o.next.Policy(effect)
}

var _ agent.Dispatcher = (*observedDispatcher)(nil)
