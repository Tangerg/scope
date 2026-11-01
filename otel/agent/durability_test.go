package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	agentotel "github.com/Tangerg/scope/otel/agent"
)

func TestObservedTreeCommitterPreservesConformance(t *testing.T) {
	harness := newObserverHarness(t)
	runtest.RunTreeCommitterConformance(t, func() runtest.TreeCommitterConformanceDriver {
		store := run.NewMemoryStore()
		observed, err := harness.observer.WrapTreeCommitter(store)
		if err != nil {
			t.Fatal(err)
		}
		return observedDurabilityDriver{MemoryStore: store, observed: observed}
	})
}

type observedDurabilityDriver struct {
	*run.MemoryStore
	observed run.Store
}

func (o observedDurabilityDriver) TreeCommitter() run.Store { return o.observed }

func TestObservedTreeCommitterRecordsAcknowledgedBoundaries(t *testing.T) {
	harness := newObserverHarness(t)
	store := run.NewMemoryStore()
	committer, err := harness.observer.WrapTreeCommitter(store)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(t.Context(), run.Config{Store: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload(testInput{Value: "private input"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, testDeployment(t), input)
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Termination().Status(), err)
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
	checkpoint := observedCheckpoint(t, "界🙂\n\"\\\x00")
	for _, test := range []struct {
		name     string
		err      error
		panicked bool
		outcome  string
	}{
		{name: "acknowledged", outcome: "acknowledged"},
		{name: "response lost", err: errors.New("secret storage connection"), outcome: "unresolved"},
		{name: "ownership", err: fmt.Errorf("secret storage connection: %w", run.ErrTreeIncarnationConflict), outcome: "ownership_conflict"},
		{name: "content", err: fmt.Errorf("secret storage connection: %w", run.ErrCommitConflict), outcome: "content_conflict"},
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
				err = observed.CommitCheckpoint(t.Context(), checkpoint)
			}()
			if next.calls != 1 || !errors.Is(err, test.err) || (recovered != nil) != test.panicked {
				t.Fatalf("calls=%d error=%v panic=%v", next.calls, err, recovered)
			}
			if test.panicked && recovered != "secret storage connection" {
				t.Fatal("decorator replaced the adapter panic")
			}
			span := spanByName(t, harness.recorder.Ended(), "agent.committer.checkpoint", 0)
			if stringAttribute(span.Attributes(), "agent.committer.outcome") != test.outcome ||
				(span.Status().Code == codes.Error) != (test.outcome != "acknowledged") {
				t.Fatalf("incorrect committer outcome: %v", span.Attributes())
			}
			if strings.Contains(fmt.Sprint(span.Attributes(), span.Events(), span.Status()), "secret") {
				t.Fatal("committer observation exposed adapter diagnostics")
			}
			var metrics metricdata.ResourceMetrics
			if err := harness.reader.Collect(t.Context(), &metrics); err != nil {
				t.Fatal(err)
			}
			sizes := metricByName(t, metrics, "agent.committer.snapshot.size").Data.(metricdata.Histogram[int64])
			if len(sizes.DataPoints) != 1 {
				t.Fatalf("snapshot size points=%d, want 1", len(sizes.DataPoints))
			}
			point := sizes.DataPoints[0]
			if point.Count != 1 || point.Sum != int64(len(checkpoint.TreeSnapshot().JSON())) ||
				stringAttribute(point.Attributes.ToSlice(), "agent.committer.outcome") != test.outcome {
				t.Fatalf("snapshot measurement=%+v", point)
			}
		})
	}
}

type checkpointCapture struct {
	run.Store
	start run.TreeCheckpoint
}

func (c *checkpointCapture) CommitCheckpoint(ctx context.Context, checkpoint run.TreeCheckpoint) error {
	if checkpoint.Kind() == run.TreeCheckpointKindStart {
		c.start = checkpoint
	}
	return c.Store.CommitCheckpoint(ctx, checkpoint)
}

func observedCheckpoint(t testing.TB, value string) run.TreeCheckpoint {
	t.Helper()
	store := &checkpointCapture{Store: run.NewMemoryStore()}
	engine, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload(testInput{Value: value})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, testDeployment(t), input)
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("checkpoint fixture status=%s error=%v", result.Termination().Status(), err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	return store.start
}

type failingObservedDurability struct {
	run.Store
	err      error
	panicked bool
	calls    int
}

func (f *failingObservedDurability) CommitCheckpoint(context.Context, run.TreeCheckpoint) error {
	f.calls++
	if f.panicked {
		panic("secret storage connection")
	}
	return f.err
}

func TestWrapTreeCommitterRejectsInvalidConstruction(t *testing.T) {
	harness := newObserverHarness(t)
	var typedNil *run.MemoryStore
	if _, err := harness.observer.WrapTreeCommitter(typedNil); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatalf("typed nil error=%v", err)
	}
	var observer *agentotel.Observer
	if _, err := observer.WrapTreeCommitter(run.NewMemoryStore()); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatalf("nil observer error=%v", err)
	}
}

func ExampleObserver_WrapTreeCommitter() {
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{})
	if err != nil {
		panic(err)
	}
	defer observer.Close()
	committer, err := observer.WrapTreeCommitter(run.NewMemoryStore())
	if err != nil {
		panic(err)
	}
	engine, err := run.New(context.Background(), run.Config{
		Store:          committer,
		EventListeners: []run.EventListener{observer},
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
	*run.MemoryStore
	cause error
}

func (s *settlementFailureCommitter) CommitEffect(ctx context.Context, boundary run.EffectBoundary) error {
	if boundary.Kind() == run.EffectBoundaryKindSettled {
		return s.cause
	}
	return s.MemoryStore.CommitEffect(ctx, boundary)
}

func TestObserverRetainsDispatchOutcomeWhenSettlementCommitFails(t *testing.T) {
	harness := newObserverHarness(t)
	cause := errors.New("settlement storage failure")
	store := &settlementFailureCommitter{MemoryStore: run.NewMemoryStore(), cause: cause}
	engine, err := run.New(t.Context(), run.Config{Store: store, EventListeners: []run.EventListener{harness.observer}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload(testInput{Value: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtest.Observe(t.Context(), engine, testDeployment(t), input)
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

func TestStoreDecorationPreservesReadOnlyTaskViews(t *testing.T) {
	h := newObserverHarness(t)
	storage := run.NewMemoryStore()
	for _, tc := range []struct {
		name     string
		store    run.Store
		readable bool
	}{
		{"durable heads", storage, true}, {"write-only", struct{ run.Store }{storage}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decorated, err := h.observer.WrapTreeCommitter(tc.store)
			if err != nil {
				t.Fatal(err)
			}
			rt, err := run.New(t.Context(), run.Config{Store: decorated})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if closeErr := rt.Close(t.Context()); closeErr != nil {
					t.Error(closeErr)
				}
			}()
			heads, err := rt.StartedTrees()
			if !tc.readable {
				if !errors.Is(err, errors.ErrUnsupported) || heads != nil {
					t.Fatalf("granted nonexistent storage capability: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("instrumentation prevented read-only protocol registration: %v", err)
			}
			if _, ok, loadErr := heads.Load(t.Context(), run.StartID{Scope: "test", Key: "unstarted"}); ok || loadErr != nil {
				t.Fatalf("missing head = %t %v", ok, loadErr)
			}
		})
	}
}
