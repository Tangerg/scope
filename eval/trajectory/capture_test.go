package trajectory_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval/trajectory"
)

type capturedSource struct {
	head    run.TreeInspection
	entries []run.Entry
}

func (c capturedSource) ID() run.ProcessID                                   { return c.head.Root }
func (c capturedSource) Inspect(context.Context) (run.TreeInspection, error) { return c.head, nil }

func (c capturedSource) Observe(ctx context.Context, after run.LSN) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) {
		for _, entry := range c.entries {
			if err := ctx.Err(); err != nil {
				yield(run.Entry{}, err)
				return
			}
			if entry.LSN > after && !yield(entry, nil) {
				return
			}
		}
	}
}

func TestCaptureFencesAtTheObservedHeadAndIsRepeatable(t *testing.T) {
	complete := coveredInteraction(t)
	config := trajectoryConfig(t, complete)
	prefix := config.Log
	for index, entry := range prefix.Entries {
		if _, ok := entry.Record.(run.EffectIntent); ok {
			prefix.Entries = prefix.Entries[:index+1]
			break
		}
	}
	head, err := prefix.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	source := capturedSource{head: head, entries: config.Log.Entries}
	coverage := interactionCoverage(t, prefix)
	var first []byte
	for range 2 {
		actual, err := trajectory.Capture(t.Context(), source, coverage)
		if err != nil {
			t.Fatal(err)
		}
		if actual.HistoryComplete() || len(actual.ModelCalls()) != 1 || actual.ModelCalls()[0].Settlement != nil {
			t.Fatal("capture read beyond its committed fence")
		}
		encoded, err := jsonv2.Marshal(actual)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = encoded
		} else if string(first) != string(encoded) {
			t.Fatal("capture consumed or changed retained history")
		}
	}
	source.entries = source.entries[:len(prefix.Entries)-1]
	if _, err := trajectory.Capture(t.Context(), source, coverage); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatal("short source fabricated retained head", err)
	}
}

func TestCaptureRejectsNilSourceAndCancellation(t *testing.T) {
	var missing *capturedSource
	if _, err := trajectory.Capture(t.Context(), missing, nil); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatal(err)
	}
	actual := runTrajectory(t)
	log, err := actual.Log()
	if err != nil {
		t.Fatal(err)
	}
	head, err := log.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := trajectory.Capture(ctx, capturedSource{head: head, entries: log.Entries}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCaptureRetainsTerminalRecoveryActivations(t *testing.T) {
	store := run.NewMemoryStore()
	model := &fixtureInteractionClient{usage: &chat.Usage{InputTokens: 3, OutputTokens: 2}}
	handle, original, program := startInteraction(t, model, []tool.Tool{fixtureWeatherTool{}}, store)
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := captureInteraction(t, handle)
	if err := original.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	replacement := newRuntime(t, store)
	recovered, err := replacement.Recover(t.Context(), program, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	after := captureInteraction(t, recovered)
	log, err := after.Log()
	if err != nil {
		t.Fatal(err)
	}
	head, err := recovered.Inspect(t.Context())
	if err != nil || log.Head() != head.Head || !after.HistoryComplete() {
		t.Fatalf("terminal recovery was not fully captured: log=%d head=%d complete=%t error=%v", log.Head(), head.Head, after.HistoryComplete(), err)
	}
	beforeDigest, err := before.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	afterDigest, err := after.BehaviorDigest(rawOutputProjection)
	if err != nil || afterDigest != beforeDigest || after.RootUsage() != before.RootUsage() {
		t.Fatalf("writer activation changed committed behavior or usage: error=%v", err)
	}
	if model.calls.Load() != 2 || len(after.ModelCalls()) != 2 || len(after.ToolCalls()) != 1 {
		t.Fatal("terminal recovery repeated effects or lost retained calls")
	}
}

func TestBackwardWallClockCannotProduceAnElapsedMeasurement(t *testing.T) {
	actual := runTrajectory(t)
	config := trajectoryConfig(t, actual)
	first := config.Log.Entries[0].Record.(run.Started)
	first.At = first.At.Add(time.Hour)
	config.Log.Entries[0].Record = first
	changed, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, known := changed.Elapsed(); known {
		t.Fatal("backward clock became a duration")
	}
	firstDigest, err := actual.BehaviorDigest(rawOutputProjection)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := changed.BehaviorDigest(rawOutputProjection)
	if err != nil || firstDigest != secondDigest {
		t.Fatal("clock changed semantic behavior", err)
	}
	if _, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: changed, Expected: trajectory.Expectation{Status: run.StatusCompleted, Limits: trajectory.Limits{Elapsed: new(time.Hour)}}}); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatal("unknown duration became a verdict", err)
	}
}

func TestInvalidSemanticSettlementIsRejectedWithoutAlternativeDecoding(t *testing.T) {
	actual := coveredInteraction(t)
	config := trajectoryConfig(t, actual)
	target := config.Coverage.Tools[0]
	for index, entry := range config.Log.Entries {
		settled, ok := entry.Record.(run.EffectSettled)
		if !ok || settled.ID != target {
			continue
		}
		other, err := run.EncodePayload(map[string]bool{"other_protocol": true})
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := run.NewSettlement(run.SettlementStatusSucceeded, other)
		if err != nil {
			t.Fatal(err)
		}
		settled.Outcome = outcome
		config.Log.Entries[index].Record = settled
		break
	}
	if _, err := trajectory.New(config); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatal("malformed interaction result admitted", err)
	}
}
