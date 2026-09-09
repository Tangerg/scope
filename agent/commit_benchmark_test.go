package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Each admission starts from the same paused tree so retained history does not
// grow with b.N. Timed work covers DeliverSignals through checkpoint ACK; setup,
// restoration, and teardown are excluded. The delay models storage waiting,
// not database throughput or a production adapter's durability guarantees.
func BenchmarkTreeSignalCommit(b *testing.B) {
	for _, processCount := range []int{1, 15, 63} {
		deployment, config, snapshot := benchmarkSignalCommitTree(b, processCount)
		for _, batchSize := range []int{1, 64} {
			requests := make([]SignalRequest, batchSize)
			for index := range requests {
				id, err := ParseSignalID(fmt.Sprintf("signal:commit-benchmark-%d", index))
				if err != nil {
					b.Fatal(err)
				}
				requests[index], err = NewSignalRequest(id, WaitID{}, json.RawMessage(`{"value":"admitted"}`))
				if err != nil {
					b.Fatal(err)
				}
			}
			for _, delay := range []time.Duration{0, time.Millisecond} {
				name := fmt.Sprintf("processes_%d/batch_%d/ack_%s", processCount, batchSize, delay)
				b.Run(name, func(b *testing.B) {
					var acknowledgmentTime time.Duration
					var snapshotBytes int
					b.ReportAllocs()
					for b.Loop() {
						elapsed, size := benchmarkSignalCommit(b, deployment, config, snapshot, requests, delay)
						acknowledgmentTime += elapsed
						snapshotBytes = size
						b.StartTimer()
					}
					b.ReportMetric(float64(acknowledgmentTime.Nanoseconds())/float64(b.N), "ack_ns/op")
					b.ReportMetric(float64(snapshotBytes), "snapshot_bytes")
				})
			}
		}
	}
}

func benchmarkSignalCommit(
	b *testing.B,
	deployment Deployment,
	config EngineConfig,
	snapshot TreeSnapshot,
	requests []SignalRequest,
	delay time.Duration,
) (time.Duration, int) {
	b.StopTimer()
	b.Helper()
	durability := &signalCommitBenchmarkDurability{
		recordingTreeDurability: &recordingTreeDurability{}, delay: delay,
	}
	config.TreeDurability = durability
	engine, err := NewEngine(config)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if closeErr := engine.Close(); closeErr != nil {
			b.Error(closeErr)
		}
	}()
	process, err := engine.RestoreTree(b.Context(), deployment, snapshot)
	if err != nil {
		b.Fatal(err)
	}
	defer stopRecoveryBenchmarkProcess(b, process)
	b.StartTimer()
	accepted, err := process.DeliverSignals(b.Context(), requests...)
	b.StopTimer()
	if err != nil || !accepted {
		b.Fatalf("DeliverSignals accepted=%t error=%v", accepted, err)
	}
	checkpoints := durability.treeCheckpoints()
	if len(checkpoints) != 1 {
		b.Fatalf("signal batch committed %d checkpoints, want 1", len(checkpoints))
	}
	return durability.acknowledgmentTime, len(checkpoints[0].TreeSnapshot().JSON())
}

func benchmarkSignalCommitTree(b *testing.B, processCount int) (Deployment, EngineConfig, TreeSnapshot) {
	b.Helper()
	childDeployment := newChildTestDeployment(b)
	childInput, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		b.Fatal(err)
	}
	children := make([]ChildSpec, processCount-1)
	for index := range children {
		key, keyErr := ParseChildKey(fmt.Sprintf("commit-benchmark-%d", index))
		if keyErr != nil {
			b.Fatal(keyErr)
		}
		children[index] = childTestSpec(key, childDeployment.DeploymentRef(), childInput)
	}
	definition := &treeRecoveryBenchmarkDefinition{
		descriptor: newExecutionReplayBenchmarkDefinition(b).descriptor, children: children,
	}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: ComputeDigest([]byte("tree-commit-benchmark")),
		ConfigurationDigest:  ComputeDigest(fmt.Appendf(nil, "processes:%d", processCount)),
	})
	if err != nil {
		b.Fatal(err)
	}
	durability := &recordingTreeDurability{}
	paused := make(chan struct{})
	config := EngineConfig{
		TreeDurability:     durability,
		DeploymentResolver: deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment},
		EventListeners: []EventListener{EventListenerFunc(func(_ context.Context, event Event) {
			if event.Name() == EventProcessPaused && event.Relation().IsRoot() {
				close(paused)
			}
		})},
		Limits: Limits{MaxSteps: 100_000, MaxEffects: 100_000, MaxSignals: 100_000, MaxPendingSignals: 100_000},
		TreeLimits: TreeLimits{
			MaxDepth: 1, MaxChildren: uint32(processCount),
			MaxActiveChildren: uint32(processCount), MaxTreeProcesses: uint32(processCount),
		},
	}
	engine, err := NewEngine(config)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil {
			b.Error(closeErr)
		}
	})
	input, err := EncodeInput(executionReplayBenchmarkState{Payload: "commit fixture"})
	if err != nil {
		b.Fatal(err)
	}
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { stopRecoveryBenchmarkProcess(b, root) })
	select {
	case <-paused:
	case <-b.Context().Done():
		b.Fatal(b.Context().Err())
	}
	inspection, err := engine.InspectTree(b.Context(), root.ID())
	if err != nil {
		b.Fatal(err)
	}
	for _, inspected := range inspection.Processes {
		if inspected.Snapshot.ProcessID() == root.ID() {
			continue
		}
		child, found := engine.Process(inspected.Snapshot.ProcessID())
		if !found {
			b.Fatal("benchmark child is missing")
		}
		result, err := child.Await(b.Context())
		if err != nil || result.Status() != StatusCompleted {
			b.Fatalf("benchmark child status=%s error=%v", result.Status(), err)
		}
	}
	checkpoints := durability.treeCheckpoints()
	snapshot := checkpoints[len(checkpoints)-1].TreeSnapshot()
	if len(snapshot.ProcessSnapshots()) != processCount {
		b.Fatalf("benchmark tree has %d Processes, want %d", len(snapshot.ProcessSnapshots()), processCount)
	}
	config.EventListeners = nil
	return deployment, config, snapshot
}

type signalCommitBenchmarkDurability struct {
	*recordingTreeDurability
	delay              time.Duration
	acknowledgmentTime time.Duration
}

func (s *signalCommitBenchmarkDurability) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	started := time.Now()
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := s.recordingTreeDurability.CommitCheckpoint(ctx, checkpoint)
	s.acknowledgmentTime += time.Since(started)
	return err
}
