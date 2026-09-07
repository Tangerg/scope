package agent

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	agent "github.com/Tangerg/scope/agent"
)

func (o *Observer) stopRuntime(ctx context.Context, event agent.Event) {
	fact, ok := event.RuntimeStopped()
	if !ok {
		return
	}
	key := processKeyFor(event)
	o.stateMu.Lock()
	record, found := o.processes[key]
	delete(o.processes, key)
	var spans []trace.Span
	for step, span := range o.steps {
		if step.process == key {
			spans = append(spans, span)
			delete(o.steps, step)
		}
	}
	for effect, span := range o.effects {
		if effect.process == key {
			spans = append(spans, span)
			delete(o.effects, effect)
		}
	}
	o.stateMu.Unlock()
	observedError := runtimeFactError{kind: fact.FailureKind(), code: fact.FailureCode()}
	if found {
		spans = append(spans, record.span)
		attributes := []attribute.KeyValue{
			semconv.GenAIAgentName(event.DeploymentRef().Name()),
			processActivationAttribute.String(string(record.activation)),
			semconv.ErrorType(observedError),
		}
		o.instruments.processActivationDuration.Record(
			trace.ContextWithSpan(ctx, record.span), elapsedSeconds(record.startedAt, event.OccurredAt()),
			metric.WithAttributes(attributes...),
		)
	}
	for _, span := range spans {
		span.SetAttributes(
			processFailureKindAttribute.String(fact.FailureKind().String()),
			processFailureCodeAttribute.String(fact.FailureCode()),
		)
		recordSpanFailure(span, observedError, event.OccurredAt())
		span.End(trace.WithTimestamp(event.OccurredAt()))
	}
}

type runtimeFactError struct {
	kind agent.FailureKind
	code string
}

func (r runtimeFactError) Error() string {
	return "agent runtime stopped: " + r.kind.String() + "/" + r.code
}

func (r runtimeFactError) ErrorType() string { return r.code }
