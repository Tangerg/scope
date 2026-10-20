package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/jsonschema"
)

// Each admission starts from the same paused tree so retained history does not
// grow with b.N. Timed work covers DeliverSignals through checkpoint ACK; setup,
// restoration, and teardown are excluded. The delay models storage waiting,
// not database throughput or a production adapter's committer guarantees.
func BenchmarkTreeSignalCommit(b *testing.B) {
	for _, sample := range []struct {
		name      string
		processes int
		limits    TreeLimits
	}{
		{name: "default_root", processes: 1},
		{name: "default_width", processes: 1 + int(DefaultTreeLimits().MaxActiveChildren)},
		{name: "expanded_63", processes: 63, limits: TreeLimits{
			MaxActiveChildren: 63, MaxPendingSignals: 100_000,
		}},
	} {
		deployment, config, snapshot := benchmarkSignalCommitTree(b, sample.processes, sample.limits)
		batches := []int{1, 64}
		if sample.processes == 1 {
			batches = append(batches, int(DefaultTreeLimits().MaxPendingSignals))
		}
		for _, batchSize := range batches {
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
				name := fmt.Sprintf("%s/batch_%d/ack_%s", sample.name, batchSize, delay)
				b.Run(name, func(b *testing.B) {
					var acknowledgmentTime time.Duration
					var commitLatencies, ackLatencies []time.Duration
					var snapshotBytes int
					b.ReportAllocs()
					for b.Loop() {
						elapsed, ack, size := benchmarkSignalCommit(b, deployment, config, snapshot, requests, delay)
						commitLatencies = append(commitLatencies, elapsed)
						ackLatencies = append(ackLatencies, ack)
						acknowledgmentTime += ack
						snapshotBytes = size
						b.StartTimer()
					}
					reportBenchmarkLatencies(b, "commit", commitLatencies)
					reportBenchmarkLatencies(b, "ack", ackLatencies)
					b.ReportMetric(float64(acknowledgmentTime.Nanoseconds())/float64(b.N), "ack_ns/op")
					b.ReportMetric(float64(snapshotBytes), "snapshot_bytes")
					b.ReportMetric(float64(sample.processes), "processes")
					b.ReportMetric(float64(batchSize), "signals/op")
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
) (time.Duration, time.Duration, int) {
	b.StopTimer()
	b.Helper()
	committer := &signalCommitBenchmarkDurability{
		recordingTreeCommitter: &recordingTreeCommitter{}, delay: delay,
	}
	config.TreeCommitter = committer
	engine, err := NewEngine(config)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if closeErr := engine.Close(context.WithoutCancel(b.Context())); closeErr != nil {
			b.Error(closeErr)
		}
	}()
	process, err := engine.RestoreTree(b.Context(), deployment, snapshot)
	if err != nil {
		b.Fatal(err)
	}
	defer stopRecoveryBenchmarkProcess(b, process)
	b.StartTimer()
	started := time.Now()
	accepted, err := process.DeliverSignals(b.Context(), requests...)
	elapsed := time.Since(started)
	b.StopTimer()
	if err != nil || !accepted {
		b.Fatalf("DeliverSignals accepted=%t error=%v", accepted, err)
	}
	checkpoints := committer.treeCheckpoints()
	if len(checkpoints) != 1 {
		b.Fatalf("signal batch committed %d checkpoints, want 1", len(checkpoints))
	}
	return elapsed, committer.acknowledgmentTime, checkpoints[0].TreeSnapshot().EncodedSize()
}

func benchmarkSignalCommitTree(b *testing.B, processCount int, limits TreeLimits) (Deployment, EngineConfig, TreeSnapshot) {
	b.Helper()
	childDeployment := newChildTestDeployment(b)
	childInput, err := EncodePayload(childTestInput{Mode: "leaf"})
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
	schema, err := jsonschema.For[executionReplayBenchmarkState]()
	if err != nil {
		b.Fatal(err)
	}
	signalSchema, err := jsonschema.For[engineTestInput]()
	if err != nil {
		b.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "benchmark.signal_commit", Description: "Measure durable Signal admission.",
		InputSchema: schema, OutputSchema: schema, SignalSchema: signalSchema,
	})
	if err != nil {
		b.Fatal(err)
	}
	definition := &treeRecoveryBenchmarkDefinition{descriptor: descriptor, children: children}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: ComputeDigest([]byte("tree-commit-benchmark")),
		ConfigurationDigest:  ComputeDigest(fmt.Appendf(nil, "processes:%d", processCount)),
	})
	if err != nil {
		b.Fatal(err)
	}
	committer := &recordingTreeCommitter{}
	paused := make(chan struct{})
	config := EngineConfig{
		TreeCommitter:      committer,
		DeploymentResolver: deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment},
		EventListeners: []EventListener{EventListenerFunc(func(_ context.Context, event Event) {
			if event.Name() == EventProcessPaused && event.Relation().IsRoot() {
				close(paused)
			}
		})},
		TreeLimits: limits,
	}
	engine, err := NewEngine(config)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(b.Context())); closeErr != nil {
			b.Error(closeErr)
		}
	})
	input, err := EncodePayload(executionReplayBenchmarkState{Payload: "commit fixture"})
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
	inspection, err := engine.InspectTree(b.Context(), root.Relation().ProcessID())
	if err != nil {
		b.Fatal(err)
	}
	for _, inspected := range inspection.Processes {
		if inspected.Snapshot.Relation().ProcessID() == root.Relation().ProcessID() {
			continue
		}
		child, found := engine.Process(inspected.Snapshot.Relation().ProcessID())
		if !found {
			b.Fatal("benchmark child is missing")
		}
		result, err := child.Await(b.Context())
		if err != nil || result.Termination().Status() != StatusCompleted {
			b.Fatalf("benchmark child status=%s error=%v", result.Termination().Status(), err)
		}
	}
	checkpoints := committer.treeCheckpoints()
	snapshot := checkpoints[len(checkpoints)-1].TreeSnapshot()
	if len(snapshot.ProcessSnapshots()) != processCount {
		b.Fatalf("benchmark tree has %d Processes, want %d", len(snapshot.ProcessSnapshots()), processCount)
	}
	config.EventListeners = nil
	return deployment, config, snapshot
}

type signalCommitBenchmarkDurability struct {
	*recordingTreeCommitter
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
	err := s.recordingTreeCommitter.CommitCheckpoint(ctx, checkpoint)
	s.acknowledgmentTime += time.Since(started)
	return err
}
