package agent_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
	agentotel "github.com/Tangerg/scope/otel/agent"
)

type observerHarness struct {
	observer *agentotel.Observer
	spans    *tracetest.SpanRecorder
	metrics  *sdkmetric.ManualReader
}

func newObserverHarness(t testing.TB) observerHarness {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{TracerProvider: tracer, MeterProvider: meter})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, close := range []func(context.Context) error{observer.Close, meter.Shutdown, tracer.Shutdown} {
			if err := close(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	return observerHarness{observer: observer, spans: spans, metrics: reader}
}

type testDefinition struct{ descriptor run.Descriptor }

func (t testDefinition) Descriptor() run.Descriptor                             { return t.descriptor }
func (t testDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }
func (t testDefinition) Start(run.Payload) (run.Execution, error)               { return &testExecution{}, nil }
func (t testDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[testExecution](t.descriptor.Name())
	return &value, err
}

type testExecution struct {
	Called bool `json:"called"`
}

func (t *testExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("test.otel", t)
}
func (t *testExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	if !t.Called {
		t.Called = true
		effect, err := run.NewDispatcherEffect(json.RawMessage(`{"private":"secret-prompt"}`))
		if err != nil {
			return run.Transition{}, err
		}
		return run.Yield(0, []run.Effect{effect})
	}
	if len(turn.Messages) != 1 {
		return run.Transition{}, run.ErrInvalidMessage
	}
	completion, ok := turn.Messages[0].(run.Completion)
	if !ok {
		return run.Transition{}, run.ErrInvalidMessage
	}
	payload, err := run.ParsePayload(completion.Outcome.Payload())
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(1, payload)
}

type testDispatcher struct{ err error }

func (t testDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}
func (t testDispatcher) Dispatch(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
	if t.err != nil {
		return run.Settlement{}, t.err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, json.RawMessage(`"secret-result"`))
}

func startObservedExecution(t *testing.T, h observerHarness, next run.Dispatcher) (*run.Handle[string], *run.MemoryStore) {
	t.Helper()
	store := run.NewMemoryStore()
	observedStore, err := h.observer.WrapStore(store)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := run.New(t.Context(), run.Config{Build: "otel-test", Store: observedStore})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "test.otel", Description: "Record durable work", InputSchema: schema, OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := h.observer.WrapDispatcher(next)
	if err != nil {
		t.Fatal(err)
	}
	program, err := run.Bind[string, string](runtime.Compiler(), run.BindingConfig{Definition: testDefinition{descriptor}, Dispatcher: dispatcher, Revision: "test"})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "otel", Key: t.Name()}, Input: "secret-input"})
	if err != nil {
		t.Fatal(err)
	}
	return handle, store
}

func observedExecution(t *testing.T, h observerHarness) (*run.Handle[string], *run.MemoryStore) {
	handle, store := startObservedExecution(t, h, testDispatcher{})
	if err := handle.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := handle.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatal(result, err)
	}
	return handle, store
}

func TestCommittedLogProjectsDurableLifecycleAndRedactsPayloads(t *testing.T) {
	h := newObserverHarness(t)
	handle, store := observedExecution(t, h)
	cursor, err := h.observer.Consume(t.Context(), handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	log, ok, err := store.Load(t.Context(), handle.ID())
	if err != nil || !ok || cursor != log.Head() {
		t.Fatal(cursor, log.Head(), ok, err)
	}
	var process, effect sdktrace.ReadOnlySpan
	for _, span := range h.spans.Ended() {
		switch span.Name() {
		case "invoke_agent test.otel":
			process = span
		case "agent.effect":
			effect = span
		}
		encoded, err := jsonv2.Marshal(struct {
			Attributes []attribute.KeyValue
			Events     []sdktrace.Event
			Status     sdktrace.Status
		}{span.Attributes(), span.Events(), span.Status()})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "secret-") {
			t.Fatalf("sensitive payload reached telemetry: %s", encoded)
		}
	}
	if process == nil || effect == nil || effect.Parent().SpanID() != process.SpanContext().SpanID() {
		t.Fatalf("missing process/effect hierarchy: process=%v effect=%v spans=%v", process, effect, h.spans.Ended())
	}
	if process.Status().Code == codes.Error || effect.Status().Code == codes.Error {
		t.Fatal("successful committed work reported failure")
	}
	var metrics metricdata.ResourceMetrics
	if err := h.metrics.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, scope := range metrics.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if sum, ok := measurement.Data.(metricdata.Sum[int64]); ok {
				for _, point := range sum.DataPoints {
					counts[measurement.Name] += point.Value
					for _, value := range point.Attributes.ToSlice() {
						if strings.Contains(string(value.Key), ".id") || value.Key == "agent.epoch" || value.Key == "agent.log.lsn" {
							t.Errorf("identity metric label %s", value.Key)
						}
					}
				}
			}
		}
	}
	for name, want := range map[string]int64{"agent.process.starts": 1, "agent.process.exits": 1, "agent.process.committed_steps": 2, "agent.process.prepared_effects": 1} {
		if counts[name] != want {
			t.Errorf("%s=%d,want %d", name, counts[name], want)
		}
	}
}

type recordedSource struct {
	root    run.ProcessID
	entries []run.Entry
}

func (r recordedSource) ID() run.ProcessID { return r.root }
func (r recordedSource) Observe(_ context.Context, after run.LSN) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) {
		for _, entry := range r.entries {
			if entry.LSN > after && !yield(entry, nil) {
				return
			}
		}
	}
}

func TestObservationCursorResumesWithoutRepeatingRecords(t *testing.T) {
	h := newObserverHarness(t)
	handle, store := observedExecution(t, h)
	log, _, err := store.Load(t.Context(), handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	var cut int
	for index, entry := range log.Entries {
		if _, ok := entry.Record.(run.EffectIntent); ok {
			cut = index + 1
			break
		}
	}
	if cut == 0 {
		t.Fatal("no effect intent")
	}
	after, err := h.observer.Consume(t.Context(), recordedSource{log.Root, log.Entries[:cut]}, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstSpans := len(h.spans.Ended())
	resumed, err := h.observer.Consume(t.Context(), recordedSource{log.Root, log.Entries}, after)
	if err != nil || resumed != log.Head() {
		t.Fatal(resumed, err)
	}
	if len(h.spans.Ended()) != firstSpans {
		t.Fatal("resume fabricated a historical start or duration")
	}
}

func TestObservationRejectsNoncontiguousPositions(t *testing.T) {
	h := newObserverHarness(t)
	root, _ := run.ParseProcessID("root")
	cursor, err := h.observer.Consume(t.Context(), recordedSource{root, []run.Entry{{LSN: 2, Epoch: 1, Record: run.Activated{Epoch: 1}}}}, 0)
	if cursor != 0 || !errors.Is(err, agentotel.ErrObservationSequence) {
		t.Fatal(cursor, err)
	}
}

type blockedSource struct {
	root    run.ProcessID
	entered chan struct{}
}

func (b blockedSource) ID() run.ProcessID { return b.root }
func (b blockedSource) Observe(ctx context.Context, _ run.LSN) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) { close(b.entered); <-ctx.Done(); yield(run.Entry{}, ctx.Err()) }
}

func TestCloseCancelsAndJoinsReaders(t *testing.T) {
	h := newObserverHarness(t)
	root, _ := run.ParseProcessID("root")
	source := blockedSource{root, make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := h.observer.Consume(t.Context(), source, 0); done <- err }()
	<-source.entered
	if err := h.observer.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := h.observer.Consume(t.Context(), recordedSource{root: root}, 0); !errors.Is(err, agentotel.ErrObservationClosed) {
		t.Fatal(err)
	}
}

type rejectedStore struct {
	cause          error
	appends, loads int
}

func (r *rejectedStore) Append(context.Context, run.Batch) error { r.appends++; return r.cause }
func (r *rejectedStore) Load(context.Context, run.ProcessID) (run.Log, bool, error) {
	r.loads++
	return run.Log{}, false, r.cause
}

func TestStoreDecorationPreservesErrorsAndDoesNotLeakDiagnostics(t *testing.T) {
	for _, cause := range []error{run.ErrEpochConflict, run.ErrCommitConflict, errors.New("secret-database-diagnostic")} {
		t.Run(cause.Error(), func(t *testing.T) {
			h := newObserverHarness(t)
			next := &rejectedStore{cause: cause}
			store, err := h.observer.WrapStore(next)
			if err != nil {
				t.Fatal(err)
			}
			root, _ := run.ParseProcessID("root")
			if err := store.Append(t.Context(), run.Batch{Root: root}); !errors.Is(err, cause) {
				t.Fatal(err)
			}
			if _, found, err := store.Load(t.Context(), root); !errors.Is(err, cause) || found {
				t.Fatal(found, err)
			}
			if next.appends != 1 || next.loads != 1 {
				t.Fatal("decoration changed call count")
			}
			for _, span := range h.spans.Ended() {
				if strings.Contains(span.Status().Description, "secret-") {
					t.Fatal("diagnostic leaked")
				}
				for _, event := range span.Events() {
					for _, value := range event.Attributes {
						if strings.Contains(value.Value.AsString(), "secret-") {
							t.Fatal("diagnostic leaked")
						}
					}
				}
			}
		})
	}
}

func TestWrapperConstructionRejectsTypedNilAndClosedObserver(t *testing.T) {
	h := newObserverHarness(t)
	var store *run.MemoryStore
	if _, err := h.observer.WrapStore(store); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatal(err)
	}
	var dispatcher *testDispatcher
	if _, err := h.observer.WrapDispatcher(dispatcher); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatal(err)
	}
	var zero agentotel.Observer
	if _, err := zero.WrapStore(run.NewMemoryStore()); !errors.Is(err, agentotel.ErrInvalidObserverConfig) {
		t.Fatal(err)
	}
}

func BenchmarkLogObservation(b *testing.B) {
	h := newObserverHarness(b)
	root, _ := run.ParseProcessID("root")
	batch := run.Batch{Root: root, Epoch: 1, After: 2, Records: []run.Record{run.ControlRequested{Process: root, Kind: run.ControlPause, Reason: "benchmark", At: time.Unix(1, 0)}}}
	store, err := h.observer.WrapStore(&rejectedStore{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := store.Append(b.Context(), batch); err != nil {
			b.Fatal(err)
		}
	}
}
