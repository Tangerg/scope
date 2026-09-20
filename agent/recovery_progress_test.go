package agent

import (
	"context"
	"testing"
	"testing/synctest"
)

type recoveryProgressCommitter struct {
	*MemoryTreeCommitter
	entered chan struct{}
	release chan struct{}
}

func (r *recoveryProgressCommitter) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	snapshot := checkpoint.TreeSnapshot().ProcessSnapshots()[0]
	if snapshot.Status() == StatusPaused && snapshot.Usage().CommittedSteps == 2 {
		close(r.entered)
		<-r.release
	}
	return r.MemoryTreeCommitter.CommitCheckpoint(ctx, checkpoint)
}

func TestRecoveryFixtureWaitsForNewPausedProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		schema := controlValue(SchemaFor[executionReplayBenchmarkState]())
		descriptor := controlValue(NewDescriptor(DescriptorConfig{
			Name: "benchmark.tree_recovery", Description: "Test recovery fixture progress.",
			InputSchema: schema, OutputSchema: schema,
		}))
		definition := &treeRecoveryBenchmarkDefinition{descriptor: descriptor}
		deployment := engineTestDeployment(t, definition, nil)
		committer := &recoveryProgressCommitter{MemoryTreeCommitter: NewMemoryTreeCommitter(), entered: make(chan struct{}), release: make(chan struct{})}
		engine := controlValue(NewEngine(EngineConfig{TreeCommitter: committer}))
		process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(executionReplayBenchmarkState{}))))
		waitForPausedStep(t, process, 1)
		if err := process.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		<-committer.entered
		snapshot := inspectProcessSnapshot(t, process)
		if snapshot.Status() != StatusPaused || snapshot.Usage().CommittedSteps != 1 {
			t.Fatal("gate did not retain the previous paused head")
		}
		returned := make(chan struct{})
		go func() { waitForPausedStep(t, process, 2); close(returned) }()
		synctest.Wait()
		select {
		case <-returned:
			t.Error("previous paused head satisfied a new progress boundary")
		default:
		}
		close(committer.release)
		<-returned
		if err := process.Kill(t.Context(), "cleanup"); err != nil {
			t.Fatal(err)
		}
		awaitResult(t, process)
		if err := process.Join(t.Context()); err != nil {
			t.Fatal(err)
		}
		mustCloseEngine(t, engine)
	})
}
