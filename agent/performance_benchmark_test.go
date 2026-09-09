package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var (
	benchmarkTreeSnapshotSink TreeSnapshot
	benchmarkExecutionSink    Execution
	benchmarkStateSink        ExecutionState
	benchmarkTransitionSink   Transition
)

type treeSnapshotBenchmarkCase struct {
	mode         string
	processCount uint32
	maxDepth     uint32
}

func BenchmarkTreeSnapshotBoundary(b *testing.B) {
	cases := []treeSnapshotBenchmarkCase{
		{mode: "leaf", processCount: 1, maxDepth: 1},
		{mode: "binary:3", processCount: 15, maxDepth: 3},
		{mode: "binary:5", processCount: 63, maxDepth: 5},
		{mode: "binary:7", processCount: 255, maxDepth: 7},
	}
	for _, sample := range cases {
		name := fmt.Sprintf("processes_%03d", sample.processCount)
		snapshot := benchmarkCompletedTree(b, sample)
		data := snapshot.JSON()
		wire, err := snapshot.wire()
		if err != nil {
			b.Fatal(err)
		}

		b.Run(name+"/build", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchmarkTreeSnapshotSink, err = newTreeSnapshot(wire)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(data)), "snapshot_bytes")
		})
		b.Run(name+"/parse", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchmarkTreeSnapshotSink, err = ParseTreeSnapshot(data)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(data)), "snapshot_bytes")
		})
	}
}

func benchmarkCompletedTree(b *testing.B, sample treeSnapshotBenchmarkCase) TreeSnapshot {
	b.Helper()
	deployment := newChildTestDeployment(b)
	limits := DefaultLimits()
	limits.MaxSteps = 100_000
	limits.MaxEffects = 100_000
	limits.MaxSignals = 100_000
	limits.MaxPendingSignals = 100_000
	engine, err := NewEngine(EngineConfig{
		Limits: limits,
		TreeLimits: TreeLimits{
			MaxDepth:          sample.maxDepth,
			MaxChildren:       2,
			MaxActiveChildren: 2,
			MaxTreeProcesses:  sample.processCount,
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	input, err := EncodeInput(childTestInput{Mode: sample.mode})
	if err != nil {
		b.Fatal(err)
	}
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		b.Fatal(err)
	}
	result, err := root.Await(context.Background())
	if err != nil || result.Status() != StatusCompleted {
		b.Fatalf("tree result status=%s error=%v", result.Status(), err)
	}
	snapshot, err := engine.CaptureTree(context.Background(), root.ID())
	if err != nil {
		b.Fatal(err)
	}
	if got := uint32(len(snapshot.ProcessSnapshots())); got != sample.processCount {
		b.Fatalf("tree Process count=%d, want %d", got, sample.processCount)
	}
	if err := engine.Close(context.WithoutCancel(b.Context())); err != nil {
		b.Fatal(err)
	}
	return snapshot
}

type executionReplayBenchmarkState struct {
	Sequence uint64 `json:"sequence"`
	Payload  string `json:"payload"`
}

type executionReplayBenchmarkDefinition struct {
	descriptor Descriptor
}

func newExecutionReplayBenchmarkDefinition(b *testing.B) executionReplayBenchmarkDefinition {
	b.Helper()
	schema, err := SchemaFor[executionReplayBenchmarkState]()
	if err != nil {
		b.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name:         "benchmark.execution_replay",
		Description:  "Measure the pure Execution replay boundary.",
		InputSchema:  schema,
		OutputSchema: schema,
	})
	if err != nil {
		b.Fatal(err)
	}
	return executionReplayBenchmarkDefinition{descriptor: descriptor}
}

func (e executionReplayBenchmarkDefinition) Descriptor() Descriptor { return e.descriptor }

func (executionReplayBenchmarkDefinition) Start(input Input) (Execution, error) {
	state, err := input.Decode[executionReplayBenchmarkState]()
	if err != nil {
		return nil, err
	}
	return &executionReplayBenchmarkExecution{state: state}, nil
}

func (executionReplayBenchmarkDefinition) Restore(state ExecutionState) (Execution, error) {
	if state.Kind() != "benchmark.execution_replay" {
		return nil, ErrInvalidExecutionState
	}
	var decoded executionReplayBenchmarkState
	if err := json.Unmarshal(state.Payload(), &decoded); err != nil {
		return nil, err
	}
	return &executionReplayBenchmarkExecution{state: decoded}, nil
}

type executionReplayBenchmarkExecution struct {
	state executionReplayBenchmarkState
}

func (e *executionReplayBenchmarkExecution) Step(context.Context, []Signal) (Transition, error) {
	e.state.Sequence++
	return Continue(0)
}

func (e *executionReplayBenchmarkExecution) Snapshot() (ExecutionState, error) {
	payload, err := json.Marshal(e.state)
	if err != nil {
		return ExecutionState{}, err
	}
	return NewExecutionState("benchmark.execution_replay", payload)
}

func BenchmarkExecutionReplayBoundary(b *testing.B) {
	for _, payloadBytes := range []int{1 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("state_bytes_%d", payloadBytes), func(b *testing.B) {
			payload, err := json.Marshal(executionReplayBenchmarkState{
				Payload: strings.Repeat("x", payloadBytes),
			})
			if err != nil {
				b.Fatal(err)
			}
			state, err := NewExecutionState("benchmark.execution_replay", payload)
			if err != nil {
				b.Fatal(err)
			}
			definition := newExecutionReplayBenchmarkDefinition(b)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				execution, restoreErr := restoreExecution(definition, state)
				if restoreErr != nil {
					b.Fatal(restoreErr)
				}
				ctx, cancel := context.WithCancel(context.Background())
				transition, stepErr := stepExecution(ctx, execution, nil)
				cancel()
				if stepErr != nil {
					b.Fatal(stepErr)
				}
				candidate, captureErr := captureExecution(execution)
				if captureErr != nil {
					b.Fatal(captureErr)
				}
				restored, restoreErr := restoreExecution(definition, candidate)
				if restoreErr != nil {
					b.Fatal(restoreErr)
				}
				benchmarkExecutionSink = restored
				benchmarkStateSink = candidate
				benchmarkTransitionSink = transition
			}
			b.ReportMetric(float64(len(state.Payload())), "state_bytes")
		})
	}
}

func BenchmarkTreeRuntimeFastSiblingLatency(b *testing.B) {
	for range b.N {
		b.StopTimer()
		deployment, probe := newTreeRuntimeTestDeployment(b)
		probe.fastStepRelease = make(chan struct{})
		engine, err := NewEngine(EngineConfig{})
		if err != nil {
			b.Fatal(err)
		}
		input, err := EncodeInput(treeRuntimeTestInput{Role: treeRuntimeRoleRoot})
		if err != nil {
			b.Fatal(err)
		}
		root, err := engine.Start(context.Background(), deployment, input)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkReceive(b, probe.blockedStepStarted)
		benchmarkReceive(b, probe.fastStepReady)
		fastID := deriveChildProcessID(deriveEffectID(root.ID(), 1, 1))
		fast, exists := engine.Process(fastID)
		if !exists {
			b.Fatal("fast sibling was not published")
		}

		b.StartTimer()
		close(probe.fastStepRelease)
		result, err := fast.Await(context.Background())
		b.StopTimer()
		if err != nil || result.Status() != StatusCompleted {
			b.Fatalf("fast sibling status=%s error=%v", result.Status(), err)
		}

		blockedID := deriveChildProcessID(deriveEffectID(root.ID(), 1, 0))
		blocked, exists := engine.Process(blockedID)
		if !exists {
			b.Fatal("blocked sibling was not published")
		}
		if err := blocked.Kill(context.Background(), "benchmark cleanup"); err != nil {
			b.Fatal(err)
		}
		if _, err := blocked.Await(context.Background()); err != nil {
			b.Fatal(err)
		}
		if err := root.Kill(context.Background(), "benchmark cleanup"); err != nil {
			b.Fatal(err)
		}
		if _, err := root.Await(context.Background()); err != nil {
			b.Fatal(err)
		}
		if err := engine.Close(context.WithoutCancel(b.Context())); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

func benchmarkReceive[T any](b *testing.B, values <-chan T) T {
	b.Helper()
	select {
	case value := <-values:
		return value
	case <-b.Context().Done():
		b.Fatal(b.Context().Err())
		var zero T
		return zero
	}
}
