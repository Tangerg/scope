package agent

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/agent/run"
)

type observedDispatcher struct {
	observer *Observer
	next     run.Dispatcher
}

func (o *observedDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	if !o.observer.enabled() {
		return o.next.Dispatch(ctx, request, emit)
	}
	ctx, span := o.observer.tracer.Start(ctx, "agent.dispatch", trace.WithAttributes(processIDAttribute.String(request.ID().Process.String()), uint64Attribute(epochAttribute, uint64(request.Epoch())), effectIDAttribute.String(request.ID().String())))
	defer span.End()
	result, err := o.next.Dispatch(ctx, request, emit)
	if err != nil {
		classifySpan(span, "agent.dispatch.unresolved")
	}
	return result, err
}

func (o *observedDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	return o.next.Policy(effect)
}

var _ run.Dispatcher = (*observedDispatcher)(nil)
