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

func TestObservedTreeDurabilityPreservesConformance(t *testing.T) {
	harness := newObserverHarness(t)
	agenttest.RunTreeDurabilityConformance(t, func() agenttest.TreeDurabilityConformanceDriver {
		store := agenttest.NewMemoryTreeDurability()
		observed, err := harness.observer.WrapTreeDurability(store)
		if err != nil {
			t.Fatal(err)
		}
		return observedDurabilityDriver{MemoryTreeDurability: store, observed: observed}
	})
}

type observedDurabilityDriver struct {
	*agenttest.MemoryTreeDurability
	observed agent.TreeDurability
}

func (o observedDurabilityDriver) TreeDurability() agent.TreeDurability { return o.observed }

func TestObservedTreeDurabilityRecordsAcknowledgedBoundaries(t *testing.T) {
	harness := newObserverHarness(t)
	store := agenttest.NewMemoryTreeDurability()
	durability, err := harness.observer.WrapTreeDurability(store)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(testInput{Value: "private input"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), testDeployment(t), input)
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Status(), err)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	spans := harness.recorder.Ended()
	if len(spans) != 4 {
		t.Fatalf("durability spans=%d, want start, pending, settled, terminal", len(spans))
	}
	for _, span := range spans {
		if stringAttribute(span.Attributes(), "agent.durability.outcome") != "acknowledged" || span.Status().Code == codes.Error {
			t.Fatal("successful boundary was not acknowledged")
		}
		if strings.Contains(fmt.Sprint(span.Attributes()), "private input") {
			t.Fatal("durability trace exposed input")
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
	if count := histogramCount(t, metricByName(t, metrics, "agent.durability.duration")); count != 4 {
		t.Fatalf("durability attempts=%d, want 4", count)
	}
	sizes := metricByName(t, metrics, "agent.durability.snapshot.size").Data.(metricdata.Histogram[int64])
	terminal := false
	for _, point := range sizes.DataPoints {
		for _, attr := range point.Attributes.ToSlice() {
			switch string(attr.Key) {
			case "agent.durability.operation", "agent.durability.boundary", "agent.durability.outcome":
			default:
				t.Fatalf("unexpected metric label %s", attr.Key)
			}
		}
		if stringAttribute(point.Attributes.ToSlice(), "agent.durability.boundary") == "terminal" {
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

func TestObservedTreeDurabilityPreservesErrorsAndRedactsDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		panicked bool
		outcome  string
	}{
		{name: "response lost", err: errors.New("secret storage connection"), outcome: "unresolved"},
		{name: "ownership", err: fmt.Errorf("secret storage connection: %w", agent.ErrTreeIncarnationConflict), outcome: "ownership_conflict"},
		{name: "content", err: fmt.Errorf("secret storage connection: %w", agent.ErrDurabilityConflict), outcome: "content_conflict"},
		{name: "panic", panicked: true, outcome: "unresolved"},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newObserverHarness(t)
			next := &failingObservedDurability{err: test.err, panicked: test.panicked}
			observed, err := harness.observer.WrapTreeDurability(next)
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
			span := spanByName(t, harness.recorder.Ended(), "agent.durability.checkpoint", 0)
			if stringAttribute(span.Attributes(), "agent.durability.outcome") != test.outcome || span.Status().Code != codes.Error {
				t.Fatalf("incorrect durability outcome: %v", span.Attributes())
			}
			if strings.Contains(fmt.Sprint(span.Attributes(), span.Events(), span.Status()), "secret") {
				t.Fatal("durability observation exposed adapter diagnostics")
			}
		})
	}
}

type failingObservedDurability struct {
	agent.TreeDurability
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

func TestWrapTreeDurabilityRejectsInvalidConstruction(t *testing.T) {
	harness := newObserverHarness(t)
	var typedNil *agenttest.MemoryTreeDurability
	if _, err := harness.observer.WrapTreeDurability(typedNil); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatalf("typed nil error=%v", err)
	}
	var observer *agentotel.Observer
	if _, err := observer.WrapTreeDurability(agenttest.NewMemoryTreeDurability()); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatalf("nil observer error=%v", err)
	}
}

func ExampleObserver_WrapTreeDurability() {
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{})
	if err != nil {
		panic(err)
	}
	defer observer.Close()
	durability, err := observer.WrapTreeDurability(agenttest.NewMemoryTreeDurability())
	if err != nil {
		panic(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeDurability: durability,
		EventListeners: []agent.EventListener{observer},
	})
	if err != nil {
		panic(err)
	}
	if err := engine.Close(); err != nil {
		panic(err)
	}
	fmt.Println("durability observation configured")
	// Output: durability observation configured
}
