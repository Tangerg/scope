package agent_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"

	agent "github.com/Tangerg/scope/agent"
)

func TestDispatcherCallsInheritTheirEffectSpan(t *testing.T) {
	harness := newObserverHarness(t)
	provider := harness.provider
	ctx, entry := provider.Tracer("test").Start(t.Context(), "entry")
	next := tracingDispatcher{tracer: provider.Tracer("test")}
	dispatcher, err := harness.observer.WrapDispatcher(next)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{harness.observer}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(testInput{Value: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(ctx, testDeploymentWithDispatcher(t, dispatcher), input)
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("Run = %s, %v", result.Status(), err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	entry.End()
	spans := harness.recorder.Ended()
	effect := spanByName(t, spans, "agent.effect", 0)
	downstream := spanByName(t, spans, "downstream", 0)
	if downstream.Parent().SpanID() != effect.SpanContext().SpanID() {
		t.Fatalf("downstream parent = %s, want effect %s", downstream.Parent().SpanID(), effect.SpanContext().SpanID())
	}
	process := spanByName(t, spans, "invoke_agent test.otel", 0)
	if process.Parent().SpanID() != entry.SpanContext().SpanID() {
		t.Error("agent invocation lost the host parent")
	}
}

type tracingDispatcher struct {
	testDispatcher
	tracer trace.Tracer
}

func (t tracingDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	ctx, span := t.tracer.Start(ctx, "downstream")
	defer span.End()
	return t.testDispatcher.Dispatch(ctx, request, emit)
}
