package agent_test

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/agent/run/runtest"

	"github.com/Tangerg/scope/agent/run"
)

func TestDispatcherCallsInheritTheirActualDispatchSpan(t *testing.T) {
	harness := newObserverHarness(t)
	provider := harness.provider
	ctx, entry := provider.Tracer("test").Start(t.Context(), "entry")
	next := tracingDispatcher{tracer: provider.Tracer("test")}
	dispatcher, err := harness.observer.WrapDispatcher(next)
	if err != nil {
		t.Fatal(err)
	}
	// Hold delivery before even the process span exists. Propagation into
	// real I/O must not depend on tracing callback scheduling.
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(), EventListeners: []run.EventListener{run.EventListenerFunc(func(ctx context.Context, e run.Event) { <-release; harness.observer.OnEvent(ctx, e) })}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload(testInput{Value: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(ctx, engine, testDeploymentWithDispatcher(t, dispatcher), input)
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("Run = %s, %v", result.Termination().Status(), err)
	}
	unblock()
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	entry.End()
	spans := harness.recorder.Ended()
	effect := spanByName(t, spans, "agent.dispatch", 0)
	downstream := spanByName(t, spans, "downstream", 0)
	if downstream.Parent().SpanID() != effect.SpanContext().SpanID() {
		t.Fatalf("downstream parent = %s, want effect %s", downstream.Parent().SpanID(), effect.SpanContext().SpanID())
	}
	projection := spanByName(t, spans, "agent.effect", 0)
	for _, k := range []string{"agent.effect.id", "agent.effect.attempt_id", "agent.tree.incarnation_id"} {
		if id := stringAttribute(effect.Attributes(), k); id == "" || id != stringAttribute(projection.Attributes(), k) {
			t.Fatalf("effect correlation %s lost", k)
		}
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

func (t tracingDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	ctx, span := t.tracer.Start(ctx, "downstream")
	defer span.End()
	return t.testDispatcher.Dispatch(ctx, request, emit)
}
