package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

var benchmarkSnapshotBytesSink []byte

type treeRecoveryBenchmarkCase struct {
	name         string
	contextBytes int
	historyCount int
	effectCount  int
}

// The fixtures pass through the Engine to preserve real mailbox cursors,
// deduplication history, and unresolved batch identities. These measurements
// cover framework CPU and allocations; they contain no Host storage I/O.
func BenchmarkTreeRecoveryBoundary(b *testing.B) {
	for _, sample := range []treeRecoveryBenchmarkCase{
		{name: "baseline", contextBytes: 1 << 10},
		{name: "context_1MiB", contextBytes: 1 << 20},
		{name: "history_1024", contextBytes: 1 << 10, historyCount: 1024},
		{name: "active_batch_64", contextBytes: 1 << 10, effectCount: 64},
		{name: "combined", contextBytes: 1 << 20, historyCount: 1024, effectCount: 64},
	} {
		b.Run(sample.name, func(b *testing.B) {
			engine, deployment, process := benchmarkRecoverableProcess(b, sample)
			snapshot, err := engine.CaptureTree(b.Context(), process.ID())
			if err != nil {
				b.Fatal(err)
			}
			data := snapshot.JSON()
			wire, err := snapshot.wire()
			if err != nil {
				b.Fatal(err)
			}
			operations := []struct {
				name string
				run  func() error
			}{
				{name: "capture", run: func() error {
					benchmarkTreeSnapshotSink, err = engine.CaptureTree(b.Context(), process.ID())
					return err
				}},
				{name: "validate", run: func() error { return validateTreeSnapshot(wire) }},
				{name: "encode", run: func() error {
					benchmarkSnapshotBytesSink, err = json.Marshal(wire)
					return err
				}},
				{name: "parse", run: func() error {
					benchmarkTreeSnapshotSink, err = ParseTreeSnapshot(data)
					return err
				}},
			}
			for _, operation := range operations {
				b.Run(operation.name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if err := operation.run(); err != nil {
							b.Fatal(err)
						}
					}
					b.ReportMetric(float64(len(data)), "snapshot_bytes")
				})
			}
			b.Run("restore", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					b.StopTimer()
					restoredEngine, err := NewEngine(EngineConfig{})
					if err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
					restored, err := restoredEngine.RestoreTree(b.Context(), deployment, snapshot)
					b.StopTimer()
					if err != nil || restored.ID() != process.ID() {
						b.Fatalf("RestoreTree error=%v", err)
					}
					stopRecoveryBenchmarkProcess(b, restored)
					if err := restoredEngine.Close(context.WithoutCancel(b.Context())); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
				b.ReportMetric(float64(len(data)), "snapshot_bytes")
			})
		})
	}
}

func benchmarkRecoverableProcess(
	b *testing.B,
	sample treeRecoveryBenchmarkCase,
) (*Engine, Deployment, *Process) {
	b.Helper()
	schema, err := SchemaFor[executionReplayBenchmarkState]()
	if err != nil {
		b.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "benchmark.tree_recovery", Description: "Measure portable tree recovery boundaries.",
		InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		b.Fatal(err)
	}
	definition := &treeRecoveryBenchmarkDefinition{descriptor: descriptor, effectCount: sample.effectCount}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: definition, Dispatcher: &failingEngineTestDispatcher{},
		ImplementationDigest: ComputeDigest([]byte("tree-recovery-benchmark")),
		ConfigurationDigest:  ComputeDigest(fmt.Appendf(nil, "effects:%d", sample.effectCount)),
	})
	if err != nil {
		b.Fatal(err)
	}
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(b.Context())); closeErr != nil {
			b.Error(closeErr)
		}
	})
	input, err := EncodeInput(executionReplayBenchmarkState{Payload: strings.Repeat("x", sample.contextBytes)})
	if err != nil {
		b.Fatal(err)
	}
	process, err := engine.Start(b.Context(), deployment, input)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { stopRecoveryBenchmarkProcess(b, process) })
	waitForStatus(b, process, StatusPaused)
	requests := make([]SignalRequest, sample.historyCount)
	for index := range requests {
		id, parseErr := ParseSignalID(fmt.Sprintf("signal:benchmark-history-%d", index))
		if parseErr != nil {
			b.Fatal(parseErr)
		}
		requests[index], err = NewSignalRequest(id, WaitID{}, json.RawMessage(`{"value":"history"}`))
		if err != nil {
			b.Fatal(err)
		}
	}
	if len(requests) > 0 {
		if accepted, err := process.DeliverSignals(b.Context(), requests...); err != nil || !accepted {
			b.Fatalf("history accepted=%t error=%v", accepted, err)
		}
	}
	if err := process.Resume(b.Context()); err != nil {
		b.Fatal(err)
	}
	waitForStatus(b, process, StatusPaused)
	if sample.effectCount > 0 {
		if err := process.Resume(b.Context()); err != nil {
			b.Fatal(err)
		}
		waitForUnknownSettlement(b, process)
	}
	wantUsage := Usage{
		CommittedSteps: 2, PreparedEffects: uint64(sample.effectCount), AcceptedSignals: uint64(sample.historyCount),
	}
	if inspectProcessSnapshot(b, process).Usage() != wantUsage {
		b.Fatalf("benchmark fixture usage=%+v, want %+v", inspectProcessSnapshot(b, process).Usage(), wantUsage)
	}
	if len(requests) > 0 {
		if accepted, err := process.DeliverSignals(b.Context(), requests[0]); err != nil || accepted {
			b.Fatalf("consumed history replay accepted=%t error=%v", accepted, err)
		}
	}
	return engine, deployment, process
}

// benchmarkCleanupTimeout only prevents a hung teardown from blocking the test
// binary. It is deliberately far above any plausible termination time: the
// heaviest cases here allocate tens of megabytes per iteration, so a budget
// tuned for an idle machine expires under their own GC pressure and reports a
// slow teardown as "status=invalid" — a failure signature that reads like a
// kernel defect and makes the measurement unusable as evidence.
const benchmarkCleanupTimeout = 2 * time.Minute

// stopRecoveryBenchmarkProcess runs from b.Cleanup. It reports instead of
// calling Fatal, because Fatal would Goexit inside cleanup and skip the
// remaining teardown. It cannot use b.Context, which is already canceled by the
// time cleanup functions run.
func stopRecoveryBenchmarkProcess(b *testing.B, process *Process) {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), benchmarkCleanupTimeout)
	defer cancel()
	if err := process.Kill(ctx, "benchmark cleanup"); err != nil {
		b.Errorf("benchmark cleanup kill: %v", err)
		return
	}
	result, err := process.Await(ctx)
	if err != nil || result.Status() != StatusKilled {
		b.Errorf("benchmark cleanup status=%s error=%v", result.Status(), err)
	}
}

type treeRecoveryBenchmarkDefinition struct {
	descriptor  Descriptor
	effectCount int
	children    []ChildSpec
}

func (t *treeRecoveryBenchmarkDefinition) Descriptor() Descriptor { return t.descriptor }

func (t *treeRecoveryBenchmarkDefinition) Start(input Input) (Execution, error) {
	state, err := input.Decode[executionReplayBenchmarkState]()
	if err != nil {
		return nil, err
	}
	return &treeRecoveryBenchmarkExecution{definition: t, state: state}, nil
}

func (t *treeRecoveryBenchmarkDefinition) Restore(state ExecutionState) (Execution, error) {
	if state.Kind() != t.descriptor.Name() {
		return nil, ErrInvalidExecutionState
	}
	var decoded executionReplayBenchmarkState
	if err := json.Unmarshal(state.Payload(), &decoded); err != nil {
		return nil, err
	}
	return &treeRecoveryBenchmarkExecution{definition: t, state: decoded}, nil
}

type treeRecoveryBenchmarkExecution struct {
	definition *treeRecoveryBenchmarkDefinition
	state      executionReplayBenchmarkState
}

func (t *treeRecoveryBenchmarkExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	t.state.Sequence++
	if t.state.Sequence == 1 && len(t.definition.children) != 0 {
		effects := make([]Effect, len(t.definition.children))
		for index, child := range t.definition.children {
			effect, err := StartChild(child)
			if err != nil {
				return Transition{}, err
			}
			effects[index] = effect
		}
		return Continue(0, effects...)
	}
	if t.state.Sequence != 3 || t.definition.effectCount == 0 {
		return Pause(uint32(len(signals)), "benchmark boundary")
	}
	effects := make([]Effect, t.definition.effectCount)
	for index := range effects {
		effect, err := NewDispatcherEffect(json.RawMessage(`{"request":"benchmark"}`))
		if err != nil {
			return Transition{}, err
		}
		effects[index] = effect
	}
	return Continue(uint32(len(signals)), effects...)
}

func (t *treeRecoveryBenchmarkExecution) Snapshot() (ExecutionState, error) {
	payload, err := json.Marshal(t.state)
	if err != nil {
		return ExecutionState{}, err
	}
	return NewExecutionState(t.definition.descriptor.Name(), payload)
}
