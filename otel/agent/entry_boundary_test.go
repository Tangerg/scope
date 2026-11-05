package agent_test

import (
	"errors"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/agent/run"
)

func TestObservationRejectsInvalidRecordsBeforeProjection(t *testing.T) {
	root, err := run.ParseProcessID("observed-tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []run.Record{
		nil, (*run.Started)(nil), run.Started{}, run.Activated{}, run.SignalAdmitted{}, run.Stepped{},
		run.EffectIntent{}, run.EffectSettled{}, run.Exited{}, run.Reaped{}, run.Checkpoint{}, run.ControlRequested{}, run.ExecutionFailed{},
	} {
		t.Run(fmt.Sprintf("%T", record), func(t *testing.T) {
			harness := newObserverHarness(t)
			const after run.LSN = 7
			entry := run.Entry{LSN: after + 1, Epoch: 1, Record: record}
			cursor, observeErr := harness.observer.Consume(t.Context(), recordedSource{root, []run.Entry{entry}}, after)
			if cursor != after || !errors.Is(observeErr, run.ErrInvalidLog) {
				t.Errorf("invalid record advanced the cursor: cursor=%d error=%v", cursor, observeErr)
			}
			assertNoProjectedTelemetry(t, harness)
		})
	}
}

func TestObservationRejectsForeignTreeFactsBeforeProjection(t *testing.T) {
	fixture := newObserverHarness(t)
	handle, store := observedExecution(t, fixture)
	log, _, err := store.Load(t.Context(), handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	root, err := run.ParseProcessID("another-tree")
	if err != nil {
		t.Fatal(err)
	}
	var records []run.Record
	var started run.Started
	var exited run.Exited
	for _, entry := range log.Entries {
		switch record := entry.Record.(type) {
		case run.Activated:
			continue
		case run.Started:
			started = record
		case run.Exited:
			exited = record
		}
		records = append(records, entry.Record)
	}
	signalID, err := run.ParseSignalID("observation")
	if err != nil {
		t.Fatal(err)
	}
	childKey, err := run.ParseChildKey("child")
	if err != nil {
		t.Fatal(err)
	}
	child, err := started.Process.Child(childKey)
	if err != nil {
		t.Fatal(err)
	}
	failure, err := run.NewFailure(run.FailureKindExecution, "test.failure", "failed")
	if err != nil {
		t.Fatal(err)
	}
	records = append(records,
		run.SignalAdmitted{Process: started.Process, Signal: run.Signal{ID: signalID, Payload: started.Input}, At: started.At},
		run.ControlRequested{Process: started.Process, Kind: run.ControlCancel, Origin: run.ControlOriginHost, Reason: "stop", At: started.At},
		run.ExecutionFailed{Process: started.Process, Failure: failure, At: started.At},
		run.Reaped{Process: child},
		run.Checkpoint{StartedProcesses: 1, Processes: []run.ProcessCheckpoint{{Started: started, Terminal: &exited.Termination, Exited: &exited}}},
	)
	for _, record := range records {
		t.Run(fmt.Sprintf("%T", record), func(t *testing.T) {
			if _, _, encodeErr := run.EncodeRecord(record); encodeErr != nil {
				t.Fatalf("foreign-tree fixture is not a valid record: %v", encodeErr)
			}
			harness := newObserverHarness(t)
			const after run.LSN = 7
			entry := run.Entry{LSN: after + 1, Epoch: 1, Record: record}
			cursor, observeErr := harness.observer.Consume(t.Context(), recordedSource{root, []run.Entry{entry}}, after)
			if cursor != after || !errors.Is(observeErr, run.ErrInvalidLog) {
				t.Errorf("foreign fact advanced the cursor: cursor=%d error=%v", cursor, observeErr)
			}
			assertNoProjectedTelemetry(t, harness)
		})
	}
}

func assertNoProjectedTelemetry(t *testing.T, harness observerHarness) {
	t.Helper()
	if spans := harness.spans.Ended(); len(spans) != 0 {
		t.Errorf("rejected record created or ended spans: %d", len(spans))
	}
	var observed metricdata.ResourceMetrics
	if err := harness.metrics.Collect(t.Context(), &observed); err != nil {
		t.Fatal(err)
	}
	for _, scope := range observed.ScopeMetrics {
		if len(scope.Metrics) != 0 {
			t.Errorf("rejected record changed metrics: %+v", scope.Metrics)
		}
	}
}
