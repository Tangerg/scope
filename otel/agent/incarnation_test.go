package agent_test

import (
	"context"
	startrand "crypto/rand"
	"encoding/json"

	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/agent/run"
)

func TestObserverSeparatesRepeatedStepSequencesAcrossIncarnations(t *testing.T) {
	harness := newObserverHarness(t)
	events := captureObserverEvents(t)
	var started, stepStarted, stepFinished run.Event
	for _, event := range events {
		if event.Name() == run.EventProcessStarted {
			started = event
		}
		sequence, _ := event.StepSequence()
		if sequence == 1 && event.Name() == run.EventStepStarted {
			stepStarted = event
		}
		if sequence == 1 && event.Name() == run.EventStepFinished {
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

func eventWithIncarnation(t *testing.T, event run.Event, incarnation string) run.Event {
	t.Helper()
	data, err := jsonv2.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if decodeErr := jsonv2.Unmarshal(data, &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	wire["tree_incarnation_id"], err = jsonv2.Marshal(incarnation)
	if err != nil {
		t.Fatal(err)
	}
	data, err = jsonv2.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var copy run.Event
	if decodeErr := jsonv2.Unmarshal(data, &copy); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return copy
}

func TestObserverIsolatesOverlappingDurableIncarnations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		harness := newObserverHarness(t)
		store := run.NewMemoryStore()
		next := overlappingDispatcher{entered: make(chan observedDispatch, 2), tracer: harness.provider.Tracer("test")}
		dispatcher, err := harness.observer.WrapDispatcher(next)
		if err != nil {
			t.Fatal(err)
		}
		deployment := testDeploymentWithDispatcher(t, dispatcher)
		config := run.Config{Store: store, EventListeners: []run.EventListener{harness.observer}}
		source, err := run.New(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		input, err := run.EncodePayload(testInput{Value: "overlap"})
		if err != nil {
			t.Fatal(err)
		}
		original, err := source.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
		if err != nil {
			t.Fatal(err)
		}
		first := <-next.entered
		head, found, err := store.LoadTree(t.Context(), original.Relation().ProcessID())
		if err != nil || !found {
			t.Fatalf("durable head found=%t error=%v", found, err)
		}
		restoredEngine, err := run.New(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.Recover(t.Context(), deployment, head)
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
		if runtimeErr, ok := errors.AsType[*run.RuntimeError](err); !ok || result.Valid() || !errors.Is(runtimeErr, run.ErrTreeIncarnationConflict) {
			t.Fatalf("old writer result=%v error=%v", result.Valid(), err)
		}
		if err = source.FlushObservations(t.Context()); err != nil {
			t.Fatal(err)
		}
		ended := spansByName(harness.recorder.Ended(), "invoke_agent test.otel")
		if len(ended) != 1 || stringAttribute(ended[0].Attributes(), "agent.tree.incarnation_id") != firstIncarnation.String() || ended[0].Status().Code != codes.Error {
			t.Fatal("old writer failure did not end only its own activation")
		}
		if effects := spansByName(harness.recorder.Ended(), "agent.effect"); len(effects) != 1 || stringAttribute(effects[0].Attributes(), "agent.tree.incarnation_id") != firstIncarnation.String() {
			t.Fatal("runtime stop did not close only its own outstanding Effect")
		}
		close(second.release)
		if result, err := restored.Await(t.Context()); err != nil || result.Termination().Status() != run.StatusCompleted {
			t.Fatalf("restored result=%s error=%v", result.Termination().Status(), err)
		}
		for _, engine := range []*run.Runtime{source, restoredEngine} {
			if err := engine.ReleaseTree(t.Context(), original.Relation().ProcessID()); err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
		}
		spans := harness.recorder.Ended()
		processes := spansByName(spans, "invoke_agent test.otel")
		effects := spansByName(spans, "agent.effect")
		dispatches := spansByName(spans, "agent.dispatch")
		downstream := spansByName(spans, "downstream")
		if len(processes) != 2 || len(effects) != 2 || len(downstream) != 2 || len(dispatches) != 2 {
			t.Fatalf("Process=%d Effect=%d downstream=%d", len(processes), len(effects), len(downstream))
		}
		for index := range 2 {
			if effects[index].Parent().SpanID() != processes[index].SpanContext().SpanID() || downstream[index].Parent().SpanID() != dispatches[index].SpanContext().SpanID() {
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
	request run.EffectRequest
	release chan struct{}
}

type overlappingDispatcher struct {
	testDispatcher
	entered chan observedDispatch
	tracer  trace.Tracer
}

func (o overlappingDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	ctx, span := o.tracer.Start(ctx, "downstream")
	defer span.End()
	release := make(chan struct{})
	o.entered <- observedDispatch{request: request, release: release}
	<-release
	return o.testDispatcher.Dispatch(ctx, request, emit)
}

func TestActivationMetricExcludesObserverLagAndRejectsNegativeDuration(t *testing.T) {
	events := captureObserverEvents(t)
	synctest.Test(t, func(t *testing.T) {
		harness := newObserverHarness(t)
		var started, finished run.Event
		for _, event := range events {
			if event.Name() == run.EventProcessStarted {
				started = event
			}
			if event.Name() == run.EventProcessFinished {
				finished = event
			}
		}
		data, marshalErr := jsonv2.Marshal(finished)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var wire map[string]json.RawMessage
		if decodeErr := jsonv2.Unmarshal(data, &wire); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		wire["occurred_at"], marshalErr = jsonv2.Marshal(started.OccurredAt().Add(-time.Hour))
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		data, marshalErr = jsonv2.Marshal(wire)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if decodeErr := jsonv2.Unmarshal(data, &finished); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		harness.observer.OnEvent(t.Context(), started)
		time.Sleep(5 * time.Second)
		harness.observer.OnEvent(t.Context(), finished)
		var metrics metricdata.ResourceMetrics
		if collectErr := harness.reader.Collect(t.Context(), &metrics); collectErr != nil {
			t.Fatal(collectErr)
		}
		for _, scope := range metrics.ScopeMetrics {
			for _, m := range scope.Metrics {
				if m.Name == "gen_ai.invoke_agent.duration" {
					t.Fatalf("backward fact timestamps produced a duration sample: %v", m.Data)
				}
			}
		}
	})
}

func TestProcessFinishClosesUnpairedChildSpans(t *testing.T) {
	harness := newObserverHarness(t)
	for _, event := range captureObserverEvents(t) {
		step, _ := event.StepSequence()
		if event.Name() == run.EventProcessStarted || event.Name() == run.EventProcessFinished ||
			(step == 1 && (event.Name() == run.EventStepStarted || event.Name() == run.EventEffectStarted)) {
			harness.observer.OnEvent(t.Context(), event)
		}
	}
	for _, name := range []string{"agent.step", "agent.effect"} {
		spans := spansByName(harness.recorder.Ended(), name)
		if len(spans) != 1 || spans[0].Status().Code != codes.Error {
			t.Fatalf("unpaired %s was not closed as incomplete", name)
		}
	}
	before := len(harness.recorder.Ended())
	harness.observer.Close()
	if after := len(harness.recorder.Ended()); after != before {
		t.Fatalf("Close ended already reclaimed spans: before=%d after=%d", before, after)
	}
}
