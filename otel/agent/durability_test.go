package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	agentotel "github.com/Tangerg/scope/otel/agent"
)

func TestObservedTreeCommitterPreservesConformance(t *testing.T) {
	harness := newObserverHarness(t)
	agenttest.RunTreeCommitterConformance(t, func() agenttest.TreeCommitterConformanceDriver {
		store := agent.NewMemoryTreeCommitter()
		observed, err := harness.observer.WrapTreeCommitter(store)
		if err != nil {
			t.Fatal(err)
		}
		return observedDurabilityDriver{MemoryTreeCommitter: store, observed: observed}
	})
}

type observedDurabilityDriver struct {
	*agent.MemoryTreeCommitter
	observed agent.TreeCommitter
}

func (o observedDurabilityDriver) TreeCommitter() agent.TreeCommitter { return o.observed }

func TestObservedTreeCommitterRecordsAcknowledgedBoundaries(t *testing.T) {
	harness := newObserverHarness(t)
	store := agent.NewMemoryTreeCommitter()
	committer, err := harness.observer.WrapTreeCommitter(store)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodePayload(testInput{Value: "private input"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), testDeployment(t), input)
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Status(), err)
	}
	if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	spans := harness.recorder.Ended()
	if len(spans) != 4 {
		t.Fatalf("committer spans=%d, want start, pending, settled, terminal", len(spans))
	}
	wantBoundaries := []struct{ operation, boundary string }{
		{"checkpoint", "start"}, {"effect", "pending"},
		{"effect", "settled"}, {"checkpoint", "terminal"},
	}
	for index, span := range spans {
		want := wantBoundaries[index]
		if span.Name() != "agent.committer."+want.operation ||
			stringAttribute(span.Attributes(), "agent.committer.operation") != want.operation ||
			stringAttribute(span.Attributes(), "agent.committer.boundary") != want.boundary {
			t.Fatalf("boundary %d: name=%s attributes=%v", index, span.Name(), span.Attributes())
		}
		if stringAttribute(span.Attributes(), "agent.committer.outcome") != "acknowledged" || span.Status().Code == codes.Error {
			t.Fatal("successful boundary was not acknowledged")
		}
		if strings.Contains(fmt.Sprint(span.Attributes()), "private input") {
			t.Fatal("committer trace exposed input")
		}
	}
	head, found, err := store.LoadTree(t.Context(), result.ProcessID())
	if err != nil || !found {
		t.Fatalf("head found=%t error=%v", found, err)
	}
	var metrics metricdata.ResourceMetrics
	if err := harness.reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	if count := histogramCount(t, metricByName(t, metrics, "agent.committer.duration")); count != 4 {
		t.Fatalf("committer attempts=%d, want 4", count)
	}
	sizes := metricByName(t, metrics, "agent.committer.snapshot.size").Data.(metricdata.Histogram[int64])
	terminal := false
	for _, point := range sizes.DataPoints {
		for _, attr := range point.Attributes.ToSlice() {
			switch string(attr.Key) {
			case "agent.committer.operation", "agent.committer.boundary", "agent.committer.outcome":
			default:
				t.Fatalf("unexpected metric label %s", attr.Key)
			}
		}
		if stringAttribute(point.Attributes.ToSlice(), "agent.committer.boundary") == "terminal" {
			terminal = true
			if point.Count != 1 || point.Sum != int64(len(head.JSON())) {
				t.Fatalf("terminal snapshot count=%d bytes=%d", point.Count, point.Sum)
			}
		}
	}
	if !terminal {
		t.Fatal("terminal snapshot measurement missing")
	}
}

func TestObservedTreeCommitterPreservesErrorsAndRedactsDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		panicked bool
		outcome  string
	}{
		{name: "response lost", err: errors.New("secret storage connection"), outcome: "unresolved"},
		{name: "ownership", err: fmt.Errorf("secret storage connection: %w", agent.ErrTreeIncarnationConflict), outcome: "ownership_conflict"},
		{name: "content", err: fmt.Errorf("secret storage connection: %w", agent.ErrCommitConflict), outcome: "content_conflict"},
		{name: "panic", panicked: true, outcome: "unresolved"},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newObserverHarness(t)
			next := &failingObservedDurability{err: test.err, panicked: test.panicked}
			observed, err := harness.observer.WrapTreeCommitter(next)
			if err != nil {
				t.Fatal(err)
			}
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				err = observed.CommitCheckpoint(t.Context(), agent.TreeCheckpoint{})
			}()
			if next.calls != 1 || !errors.Is(err, test.err) || (recovered != nil) != test.panicked {
				t.Fatalf("calls=%d error=%v panic=%v", next.calls, err, recovered)
			}
			if test.panicked && recovered != "secret storage connection" {
				t.Fatal("decorator replaced the adapter panic")
			}
			span := spanByName(t, harness.recorder.Ended(), "agent.committer.checkpoint", 0)
			if stringAttribute(span.Attributes(), "agent.committer.outcome") != test.outcome || span.Status().Code != codes.Error {
				t.Fatalf("incorrect committer outcome: %v", span.Attributes())
			}
			if strings.Contains(fmt.Sprint(span.Attributes(), span.Events(), span.Status()), "secret") {
				t.Fatal("committer observation exposed adapter diagnostics")
			}
		})
	}
}

type failingObservedDurability struct {
	agent.TreeCommitter
	err      error
	panicked bool
	calls    int
}

func (f *failingObservedDurability) CommitCheckpoint(context.Context, agent.TreeCheckpoint) error {
	f.calls++
	if f.panicked {
		panic("secret storage connection")
	}
	return f.err
}

func TestWrapTreeCommitterRejectsInvalidConstruction(t *testing.T) {
	harness := newObserverHarness(t)
	var typedNil *agent.MemoryTreeCommitter
	if _, err := harness.observer.WrapTreeCommitter(typedNil); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatalf("typed nil error=%v", err)
	}
	var observer *agentotel.Observer
	if _, err := observer.WrapTreeCommitter(agent.NewMemoryTreeCommitter()); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatalf("nil observer error=%v", err)
	}
}

func ExampleObserver_WrapTreeCommitter() {
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{})
	if err != nil {
		panic(err)
	}
	defer observer.Close()
	committer, err := observer.WrapTreeCommitter(agent.NewMemoryTreeCommitter())
	if err != nil {
		panic(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeCommitter:  committer,
		EventListeners: []agent.EventListener{observer},
	})
	if err != nil {
		panic(err)
	}
	if err := engine.Close(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println("committer observation configured")
	// Output: committer observation configured
}

type settlementFailureCommitter struct {
	*agent.MemoryTreeCommitter
	cause error
}

func (s *settlementFailureCommitter) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	if boundary.Kind() == agent.EffectBoundaryKindSettled {
		return s.cause
	}
	return s.MemoryTreeCommitter.CommitEffect(ctx, boundary)
}

func TestObserverRetainsDispatchOutcomeWhenSettlementCommitFails(t *testing.T) {
	harness := newObserverHarness(t)
	cause := errors.New("settlement storage failure")
	store := &settlementFailureCommitter{MemoryTreeCommitter: agent.NewMemoryTreeCommitter(), cause: cause}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store, EventListeners: []agent.EventListener{harness.observer}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodePayload(testInput{Value: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Run(t.Context(), testDeployment(t), input)
	if !errors.Is(err, cause) {
		t.Fatalf("Run error=%v", err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	var metrics metricdata.ResourceMetrics
	if err := harness.reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	duration := metricByName(t, metrics, "agent.effect.duration")
	if count := histogramCount(t, duration); count != 1 {
		t.Fatalf("attempt durations=%d", count)
	}
	assertHistogramAttribute(t, duration, "agent.effect.status", "succeeded")
	found := false
	for _, span := range harness.recorder.Ended() {
		if stringAttribute(span.Attributes(), "agent.effect.status") == "succeeded" {
			found = true
			if span.Status().Code == codes.Error {
				t.Fatal("successful dispatch inherited storage failure")
			}
		}
	}
	if !found {
		t.Fatal("missing successful effect span")
	}
}
