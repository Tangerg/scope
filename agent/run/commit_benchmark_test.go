package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Each width uses one fully recovered, paused tree and the same number of
// distinct admissions. Recovery and its garbage collection are outside the
// measurement, so it measures the steady commit path rather than startup.
func BenchmarkTreeSignalCommit(b *testing.B) {
	for _, sample := range []struct {
		name      string
		processes int
	}{{"default_root", 1}, {"default_width", 17}, {"expanded_63", 63}} {
		b.Run(sample.name+"/batch_1/ack_0s", func(b *testing.B) {
			program, log := signalCommitFixture(b, sample.processes, uint64(b.N)+uint64(sample.processes))
			records := make([]Record, len(log.Entries))
			for index, entry := range log.Entries {
				records[index] = entry.Record
			}
			store := NewMemoryStore()
			if err := store.Append(b.Context(), Batch{Root: log.Root, Records: records}); err != nil {
				b.Fatal(err)
			}
			runtime := controlValue(New(b.Context(), Config{Build: kernelTestBuild, Store: store}))
			handle := controlValue(runtime.Recover(b.Context(), program, log.Root))
			if err := handle.Pause(b.Context(), "synchronize recovered actor"); !errors.Is(err, ErrInvalidProcessControl) {
				b.Fatal(err)
			}
			signals := make([]Signal, b.N)
			for index := range signals {
				signals[index] = Signal{ID: controlValue(ParseSignalID(fmt.Sprintf("benchmark-%d", index))), Payload: controlValue(EncodePayload(1))}
			}
			latencies := make([]time.Duration, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for index := range b.N {
				started := time.Now()
				accepted, err := handle.DeliverSignals(b.Context(), signals[index])
				latencies[index] = time.Since(started)
				if err != nil || !accepted {
					b.Fatalf("signal admission=%t error=%v", accepted, err)
				}
			}
			b.StopTimer()
			if err := runtime.Close(b.Context()); err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(sample.processes), "processes")
			b.ReportMetric(float64(durationMedian(latencies).Nanoseconds()), "commit_p50_ns")
		})
	}
}

func signalCommitFixture(b *testing.B, processes int, pendingMessages uint64) (Program[int, int], Log) {
	b.Helper()
	store := NewMemoryStore()
	const expandedChildCapacity = 63
	runtime := controlValue(New(b.Context(), Config{Build: kernelTestBuild, Store: store, TreeLimits: TreeLimits{MaxActiveChildren: expandedChildCapacity, MaxPendingMessages: max(defaultMaxPendingMessages, pendingMessages)}}))
	program := treeFixtureProgram(b, runtime, processes-1, true, true, Budget{})
	handle := controlValue(runtime.Start(b.Context(), program, StartRequest[int]{ID: hostStartID()}))
	waiting := 0
	ctx, cancel := context.WithTimeout(b.Context(), kernelTestTimeout)
	defer cancel()
	for entry, err := range handle.Observe(ctx, 0) {
		if err != nil {
			b.Fatal(err)
		}
		if stepped, ok := entry.Record.(Stepped); ok && len(stepped.Transition.Waits()) > 0 {
			waiting++
		}
		if waiting == max(1, processes-1) {
			break
		}
	}
	if err := handle.Pause(ctx, "hold the benchmark fixture"); err != nil {
		b.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		b.Fatal(err)
	}
	log, found, err := store.Load(ctx, handle.ID())
	if err != nil || !found {
		b.Fatalf("fixture load found=%t error=%v", found, err)
	}
	view := controlValue(log.Inspect())
	if len(view.Processes) != processes {
		b.Fatalf("fixture processes=%d want=%d", len(view.Processes), processes)
	}
	return program, log
}

const benchmarkEffectCount = 10_000
const benchmarkEarlyEffect = 100
const benchmarkSampleWindow = 40

// Step-to-dispatch samples include runtime admission, serialization, durable
// append and publication, not merely the Store's inner write. Nearby samples
// suppress scheduling noise while preserving the actual history sizes.
func BenchmarkLongRunEffectCommit(b *testing.B) {
	var early, late, exactEarly, exactLate []time.Duration
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		marks := make(chan time.Time, 1)
		runtime := controlValue(New(b.Context(), Config{Build: kernelTestBuild, Store: NewMemoryStore()}))
		definition := &effectCommitDefinition{descriptor: kernelDescriptor(b, "benchmark.effect_commit"), marks: marks}
		dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(_ context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
			elapsed := time.Since(<-marks)
			step := request.ID().Step
			if step == benchmarkEarlyEffect {
				exactEarly = append(exactEarly, elapsed)
			}
			if step == benchmarkEffectCount {
				exactLate = append(exactLate, elapsed)
			}
			if step >= benchmarkEarlyEffect-benchmarkSampleWindow && step <= benchmarkEarlyEffect {
				early = append(early, elapsed)
			}
			if step >= benchmarkEffectCount-benchmarkSampleWindow {
				late = append(late, elapsed)
			}
			return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`0`))
		}}
		program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "benchmark"}))
		b.StartTimer()
		if result, err := runtime.Run(b.Context(), program, 0); err != nil || result != benchmarkEffectCount {
			b.Fatalf("long run output=%d error=%v", result, err)
		}
		b.StopTimer()
		if err := runtime.Close(b.Context()); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
	earlyMedian, lateMedian := durationMedian(early), durationMedian(late)
	b.ReportMetric(float64(earlyMedian.Nanoseconds()), "commit100_p50_ns")
	b.ReportMetric(float64(lateMedian.Nanoseconds()), "commit10000_p50_ns")
	b.ReportMetric(float64(lateMedian)/float64(earlyMedian), "history_ratio")
	b.ReportMetric(benchmarkEffectCount, "effects/run")
	b.ReportMetric(float64(durationMedian(exactEarly).Nanoseconds()), "commit100_exact_p50_ns")
	b.ReportMetric(float64(durationMedian(exactLate).Nanoseconds()), "commit10000_exact_p50_ns")
	b.ReportMetric(float64(durationMedian(exactLate))/float64(durationMedian(exactEarly)), "exact_history_ratio")
}

type effectCommitDefinition struct {
	descriptor Descriptor
	marks      chan<- time.Time
}

func (e *effectCommitDefinition) Descriptor() Descriptor                   { return e.descriptor }
func (*effectCommitDefinition) ChildPrograms() []Program[Payload, Payload] { return nil }

func (e *effectCommitDefinition) Start(Payload) (Execution, error) {
	return &effectCommitExecution{marks: e.marks}, nil
}

func (e *effectCommitDefinition) Restore(_ context.Context, state ExecutionState) (Execution, error) {
	completed, err := state.Decode[int]("benchmark.effects")
	if err != nil {
		return nil, err
	}
	return &effectCommitExecution{completed: completed, marks: e.marks}, nil
}

type effectCommitExecution struct {
	completed int
	marks     chan<- time.Time
}

func (e *effectCommitExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("benchmark.effects", e.completed)
}

func (e *effectCommitExecution) Step(_ context.Context, turn Turn) (Transition, error) {
	for _, message := range turn.Messages {
		if _, ok := message.(Completion); !ok {
			return Transition{}, fmt.Errorf("unexpected benchmark message %T", message)
		}
		e.completed++
	}
	consumed := uint32(len(turn.Messages))
	if e.completed == benchmarkEffectCount {
		return Complete(consumed, controlValue(EncodePayload(e.completed)))
	}
	e.marks <- time.Now()
	return Yield(consumed, []Effect{controlValue(NewDispatcherEffect(json.RawMessage(`0`)))})
}

func durationMedian(samples []time.Duration) time.Duration {
	ordered := slices.Clone(samples)
	slices.Sort(ordered)
	return ordered[len(ordered)/2]
}

// Recovery reads the newest checkpoint and its suffix, so a tree ten times older
// must not cost ten times more to load and fold.
func BenchmarkRecoveryCutIsBoundedByTheCheckpointInterval(b *testing.B) {
	for _, effects := range []int{benchmarkEarlyEffect * 10, benchmarkEffectCount} {
		b.Run(strconv.Itoa(effects), func(b *testing.B) {
			store := NewMemoryStore()
			runtime := controlValue(New(b.Context(), Config{Build: kernelTestBuild, Store: store}))
			marks := make(chan time.Time, 1)
			definition := &boundedEffectDefinition{effectCommitDefinition: effectCommitDefinition{descriptor: kernelDescriptor(b, "benchmark.recovery_cut"), marks: marks}, effects: effects}
			dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
				<-marks
				return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`0`))
			}}
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "benchmark"}))
			handle := controlValue(runtime.Start(b.Context(), program, StartRequest[int]{ID: hostStartID()}))
			if _, err := handle.Await(b.Context()); err != nil {
				b.Fatal(err)
			}
			if err := runtime.Close(b.Context()); err != nil {
				b.Fatal(err)
			}
			var cut Log
			for b.Loop() {
				var found bool
				var err error
				cut, found, err = store.Load(b.Context(), handle.ID())
				if err != nil || !found {
					b.Fatal(found, err)
				}
				if _, err := projectLog(cut); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(cut.Entries)), "cut_entries")
			b.ReportMetric(float64(cut.Head()), "history_entries")
		})
	}
}

type boundedEffectDefinition struct {
	effectCommitDefinition
	effects int
}

func (b *boundedEffectDefinition) Start(Payload) (Execution, error) {
	return &boundedEffectExecution{marks: b.marks, effects: b.effects}, nil
}

func (b *boundedEffectDefinition) Restore(_ context.Context, state ExecutionState) (Execution, error) {
	completed, err := state.Decode[int]("benchmark.effects")
	if err != nil {
		return nil, err
	}
	return &boundedEffectExecution{completed: completed, marks: b.marks, effects: b.effects}, nil
}

type boundedEffectExecution struct {
	completed, effects int
	marks              chan<- time.Time
}

func (b *boundedEffectExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("benchmark.effects", b.completed)
}

func (b *boundedEffectExecution) Step(_ context.Context, turn Turn) (Transition, error) {
	b.completed += len(turn.Messages)
	consumed := uint32(len(turn.Messages))
	if b.completed == b.effects {
		return Complete(consumed, controlValue(EncodePayload(b.completed)))
	}
	b.marks <- time.Now()
	return Yield(consumed, []Effect{controlValue(NewDispatcherEffect(json.RawMessage(`0`)))})
}
