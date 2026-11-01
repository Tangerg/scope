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
	// The real call starts here. A lagging (or overflowing) event listener
	// cannot supply its parent. Attempt identity correlates this span with
	// the separately projected framework lifecycle.
	if !o.observer.beginObservation() {
		return o.next.Dispatch(ctx, request, emit)
	}
	ctx, span := func() (context.Context, trace.Span) {
		defer o.observer.inFlight.Done()
		incarnation, _ := request.TreeIncarnationID()
		attempt, _ := request.AttemptID()
		return o.observer.tracer.Start(ctx, "agent.dispatch", trace.WithAttributes(
			processIDAttribute.String(request.Relation().ProcessID().String()),
			treeIncarnationIDAttribute.String(incarnation.String()),
			effectIDAttribute.String(request.ID().String()), effectAttemptIDAttribute.String(attempt.String()),
		))
	}()
	defer span.End()
	return o.next.Dispatch(ctx, request, emit)
}

func (o *observedDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	return o.next.Policy(effect)
}

var _ run.Dispatcher = (*observedDispatcher)(nil)
