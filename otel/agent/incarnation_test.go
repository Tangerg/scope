package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

func TestObserverSeparatesRepeatedStepSequencesAcrossIncarnations(t *testing.T) {
	harness := newObserverHarness(t)
	events := captureObserverEvents(t)
	var started, stepStarted, stepFinished agent.Event
	for _, event := range events {
		if event.Name() == agent.EventProcessStarted {
			started = event
		}
		sequence, _ := event.StepSequence()
		if sequence == 1 && event.Name() == agent.EventStepStarted {
			stepStarted = event
		}
		if sequence == 1 && event.Name() == agent.EventStepFinished {
			stepFinished = event
		}
	}
	incarnations := []string{
		"incarnation:00000000000000000000000000000001",
		"incarnation:00000000000000000000000000000002",
	}
	for _, incarnation := range incarnations {
		harness.observer.OnEvent(t.Context(), eventWithIncarnation(t, started, incarnation))
		harness.observer.OnEvent(t.Context(), eventWithIncarnation(t, stepStarted, incarnation))
	}
	for index, incarnation := range incarnations {
		harness.observer.OnEvent(t.Context(), eventWithIncarnation(t, stepFinished, incarnation))
		steps := spansByName(harness.recorder.Ended(), "agent.step")
		if len(steps) != index+1 || stringAttribute(steps[index].Attributes(), "agent.tree.incarnation_id") != incarnation {
			t.Fatal("Step completion closed or reused another incarnation's Step")
		}
	}
}

func eventWithIncarnation(t *testing.T, event agent.Event, incarnation string) agent.Event {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if decodeErr := json.Unmarshal(data, &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	wire["tree_incarnation_id"], err = json.Marshal(incarnation)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var copy agent.Event
	if decodeErr := json.Unmarshal(data, &copy); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return copy
}

func TestObserverIsolatesOverlappingDurableIncarnations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		harness := newObserverHarness(t)
		store := agenttest.NewMemoryTreeDurability()
		next := overlappingDispatcher{entered: make(chan observedDispatch, 2), tracer: harness.provider.Tracer("test")}
		dispatcher, err := harness.observer.WrapDispatcher(next)
		if err != nil {
			t.Fatal(err)
		}
		deployment := testDeploymentWithDispatcher(t, dispatcher)
		config := agent.EngineConfig{TreeDurability: store, EventListeners: []agent.EventListener{harness.observer}}
		source, err := agent.NewEngine(config)
		if err != nil {
			t.Fatal(err)
		}
		input, err := agent.EncodeInput(testInput{Value: "overlap"})
		if err != nil {
			t.Fatal(err)
		}
		original, err := source.Start(t.Context(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		first := <-next.entered
		head, found, err := store.LoadTree(t.Context(), original.ID())
		if err != nil || !found {
			t.Fatalf("durable head found=%t error=%v", found, err)
		}
		restoredEngine, err := agent.NewEngine(config)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.RestoreTree(t.Context(), deployment, head)
		if err != nil {
			t.Fatal(err)
		}
		second := <-next.entered
		firstIncarnation, firstDurable := first.request.TreeIncarnationID()
		secondIncarnation, secondDurable := second.request.TreeIncarnationID()
		if !firstDurable || !secondDurable || firstIncarnation == secondIncarnation || first.request.ID() != second.request.ID() {
			t.Fatal("restore did not separate writer identity while preserving Effect identity")
		}
		if ended := spansByName(harness.recorder.Ended(), "invoke_agent test.otel"); len(ended) != 0 {
			t.Fatal("overlapping activation ended a live Process span")
		}
		close(first.release)
		result, err := original.Await(t.Context())
		if runtimeErr, ok := errors.AsType[*agent.RuntimeError](err); !ok || result.Valid() || !errors.Is(runtimeErr, agent.ErrTreeIncarnationConflict) {
			t.Fatalf("old writer result=%v error=%v", result.Valid(), err)
		}
		ended := spansByName(harness.recorder.Ended(), "invoke_agent test.otel")
		if len(ended) != 1 || stringAttribute(ended[0].Attributes(), "agent.tree.incarnation_id") != firstIncarnation.String() || ended[0].Status().Code != codes.Error {
			t.Fatal("old writer failure did not end only its own activation")
		}
		if effects := spansByName(harness.recorder.Ended(), "agent.effect"); len(effects) != 1 || stringAttribute(effects[0].Attributes(), "agent.tree.incarnation_id") != firstIncarnation.String() {
			t.Fatal("runtime stop did not close only its own outstanding Effect")
		}
		close(second.release)
		if result, err := restored.Await(t.Context()); err != nil || result.Status() != agent.StatusCompleted {
			t.Fatalf("restored result=%s error=%v", result.Status(), err)
		}
		for _, engine := range []*agent.Engine{source, restoredEngine} {
			if err := engine.ReleaseTree(t.Context(), original.ID()); err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		}
		spans := harness.recorder.Ended()
		processes := spansByName(spans, "invoke_agent test.otel")
		effects := spansByName(spans, "agent.effect")
		downstream := spansByName(spans, "downstream")
		if len(processes) != 2 || len(effects) != 2 || len(downstream) != 2 {
			t.Fatalf("Process=%d Effect=%d downstream=%d", len(processes), len(effects), len(downstream))
		}
		for index := range 2 {
			if effects[index].Parent().SpanID() != processes[index].SpanContext().SpanID() || downstream[index].Parent().SpanID() != effects[index].SpanContext().SpanID() {
				t.Fatal("dispatch or Effect span attached to another incarnation")
			}
		}
		var metrics metricdata.ResourceMetrics
		if err := harness.reader.Collect(t.Context(), &metrics); err != nil {
			t.Fatal(err)
		}
		if exits := int64Sum(t, metricByName(t, metrics, "agent.process.exits")); exits != 1 {
			t.Fatalf("logical exits=%d, want only the restored completion", exits)
		}
		if count := histogramCount(t, metricByName(t, metrics, "gen_ai.invoke_agent.duration")); count != 2 {
			t.Fatalf("activation durations=%d, want both instances", count)
		}
	})
}

type observedDispatch struct {
	request agent.EffectRequest
	release chan struct{}
}

type overlappingDispatcher struct {
	testDispatcher
	entered chan observedDispatch
	tracer  trace.Tracer
}

func (o overlappingDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	ctx, span := o.tracer.Start(ctx, "downstream")
	defer span.End()
	release := make(chan struct{})
	o.entered <- observedDispatch{request: request, release: release}
	<-release
	return o.testDispatcher.Dispatch(ctx, request, emit)
}
