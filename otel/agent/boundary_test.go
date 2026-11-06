package agent_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/agent/run"
)

type boundaryFailure struct {
	cause      error
	panicValue any
	calls      int
}

func (b *boundaryFailure) fail() error {
	b.calls++
	if b.panicValue != nil {
		panic(b.panicValue)
	}
	return b.cause
}
func (b *boundaryFailure) Append(context.Context, run.Batch) error { return b.fail() }
func (b *boundaryFailure) Load(context.Context, run.ProcessID) (run.Log, bool, error) {
	return run.Log{}, false, b.fail()
}
func (b *boundaryFailure) Read(context.Context, run.ProcessID, run.LSN) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) {
		if err := b.fail(); err != nil {
			yield(run.Entry{}, err)
		}
	}
}
func (b *boundaryFailure) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}
func (b *boundaryFailure) Dispatch(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
	return run.Settlement{}, b.fail()
}

func TestPhysicalBoundariesCompleteEveryOutcome(t *testing.T) {
	const privateContent = "private-physical-diagnostic"
	for _, test := range []struct {
		name           string
		cause          error
		panicValue     any
		classification string
		outcome        string
	}{
		{name: "success", outcome: "acknowledged"},
		{name: "canceled", cause: context.Canceled, classification: "context.canceled", outcome: "unresolved"},
		{name: "deadline", cause: context.DeadlineExceeded, classification: "context.deadline_exceeded", outcome: "unresolved"},
		{name: "commit conflict", cause: run.ErrCommitConflict, classification: "agent.log.conflict", outcome: "conflict"},
		{name: "epoch conflict", cause: run.ErrEpochConflict, classification: "agent.log.conflict", outcome: "conflict"},
		{name: "failure", cause: errors.New(privateContent), classification: "*errors.errorString", outcome: "unresolved"},
		{name: "panic", panicValue: privateContent, classification: "panic", outcome: "unresolved"},
	} {
		for _, operation := range []string{"append", "load", "read", "dispatch"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				h := newObserverHarness(t)
				next := &boundaryFailure{cause: test.cause, panicValue: test.panicValue}
				store, err := h.observer.WrapStore(next)
				if err != nil {
					t.Fatal(err)
				}
				dispatcher, err := h.observer.WrapDispatcher(next)
				if err != nil {
					t.Fatal(err)
				}
				root, err := run.ParseProcessID("root")
				if err != nil {
					t.Fatal(err)
				}
				var recovered any
				func() {
					defer func() { recovered = recover() }()
					switch operation {
					case "append":
						err = store.Append(t.Context(), run.Batch{Root: root})
					case "load":
						_, _, err = store.Load(t.Context(), root)
					case "read":
						for _, failure := range store.Read(t.Context(), root, 0) {
							err = failure
						}
					case "dispatch":
						_, err = dispatcher.Dispatch(t.Context(), run.EffectRequest{}, nil)
					}
				}()
				if !errors.Is(err, test.cause) || reflect.TypeOf(err) != reflect.TypeOf(test.cause) || recovered != test.panicValue || next.calls != 1 {
					t.Fatalf("error=%v panic=%v calls=%d", err, recovered, next.calls)
				}
				spans := h.spans.Ended()
				if len(spans) != 1 {
					t.Fatalf("ended spans=%d", len(spans))
				}
				span := spans[0]
				attributes := attribute.NewSet(span.Attributes()...)
				classification, _ := attributes.Value("error.type")
				want := test.classification
				if operation == "dispatch" && test.outcome == "conflict" {
					want = "*errors.errorString"
				}
				if classification.AsString() != want {
					t.Fatalf("error.type=%q want=%q", classification.AsString(), want)
				}
				if want != "" && span.Status().Code != codes.Error {
					t.Fatalf("status=%v", span.Status())
				}
				if want == "" && span.Status().Code == codes.Error {
					t.Fatalf("successful status=%v", span.Status())
				}
				if operation != "dispatch" {
					outcome, _ := attributes.Value("agent.log.outcome")
					if outcome.AsString() != test.outcome {
						t.Fatalf("outcome=%v", outcome)
					}
				}
				if strings.Contains(span.Status().Description, privateContent) {
					t.Fatal("status leaked diagnostic")
				}
				observed := append([]attribute.KeyValue{}, span.Attributes()...)
				for _, event := range span.Events() {
					observed = append(observed, event.Attributes...)
				}
				for _, value := range observed {
					if strings.Contains(value.Value.String(), privateContent) {
						t.Fatal("telemetry leaked diagnostic")
					}
				}
				var collected metricdata.ResourceMetrics
				if err := h.metrics.Collect(t.Context(), &collected); err != nil {
					t.Fatal(err)
				}
				var durations uint64
				for _, scope := range collected.ScopeMetrics {
					for _, metric := range scope.Metrics {
						if metric.Name == "agent.log.operation.duration" {
							for _, point := range metric.Data.(metricdata.Histogram[float64]).DataPoints {
								durations += point.Count
							}
						}
					}
				}
				wantDurations := uint64(1)
				if operation == "dispatch" {
					wantDurations = 0
				}
				if durations != wantDurations {
					t.Fatalf("duration count=%d want=%d", durations, wantDurations)
				}
			})
		}
	}
}
