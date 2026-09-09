package agent

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
)

func TestProcessCancellationPropagatesThroughSubtreeAndResumesParentStrategy(t *testing.T) {
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	root, target, descendant := startWaitingSubtreeInEngine(t, engine, newChildTestDeployment(t))
	if err := target.RequestCancellation(t.Context(), "discard waiting branch"); err != nil {
		t.Fatal(err)
	}
	assertCanceledSubtree(t, target, descendant)
	output := childTestResult(t, mustAwait(t, root))
	if len(output.CompletedKeys) != 1 || output.CompletedKeys[0] != "target" {
		t.Fatalf("parent received child outcomes %v", output.CompletedKeys)
	}
	mustCloseEngine(t, engine)
}

func TestProcessCancellationPreservesUnsatisfiedSiblingWait(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "wait:subtree_all"})
	root, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, root, StatusWaiting)
	children := processesByChildKey(t, engine, root.ID())
	target, sibling := children["target"], children["sibling"]
	if target == nil || sibling == nil {
		t.Fatal("waiting tree is missing target or sibling")
	}
	waitForStatus(t, target, StatusWaiting)
	waitForStatus(t, sibling, StatusPaused)
	if cancelErr := target.RequestCancellation(t.Context(), "discard one branch"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if result := mustAwait(t, target); result.Status() != StatusCanceled {
		t.Fatalf("target status=%s", result.Status())
	}
	tree, err := engine.CaptureTree(t.Context(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if inspectProcessSnapshot(t, root).Status() != StatusWaiting || inspectProcessSnapshot(t, sibling).Status() != StatusPaused {
		t.Fatalf("unsatisfied parent=%s, sibling=%s", inspectProcessSnapshot(t, root).Status(), inspectProcessSnapshot(t, sibling).Status())
	}
	restoredEngine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	restoredRoot, err := restoredEngine.RestoreTree(t.Context(), deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	restoredSibling, found := restoredEngine.Process(sibling.ID())
	if !found || inspectProcessSnapshot(t, restoredRoot).Status() != StatusWaiting || inspectProcessSnapshot(t, restoredSibling).Status() != StatusPaused {
		t.Fatal("restoration changed the surviving wait")
	}
	for _, pair := range [][2]*Process{{root, sibling}, {restoredRoot, restoredSibling}} {
		if err := pair[1].Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		output := childTestResult(t, mustAwait(t, pair[0]))
		if len(output.CompletedKeys) != 2 || output.CompletedKeys[0] != "target" || output.CompletedKeys[1] != "sibling" {
			t.Fatalf("completed child keys=%v", output.CompletedKeys)
		}
	}
	mustCloseEngine(t, restoredEngine)
	mustCloseEngine(t, engine)
}

func TestSubtreeCancellationPublishesOnlyAfterCheckpointAcknowledgment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		durability := &blockingCancellationCheckpointDurability{
			recordingTreeDurability: &recordingTreeDurability{},
			entered:                 make(chan struct{}), release: make(chan struct{}),
		}
		release := sync.OnceFunc(func() { close(durability.release) })
		t.Cleanup(release)
		engine, err := NewEngine(EngineConfig{TreeDurability: durability})
		if err != nil {
			t.Fatal(err)
		}
		root, target, descendant := startWaitingSubtreeInEngine(t, engine, newChildTestDeployment(t))
		if err := target.RequestCancellation(t.Context(), "cancel through the durability port"); err != nil {
			t.Fatal(err)
		}
		<-durability.entered
		for _, process := range []*Process{root, target, descendant} {
			if inspectProcessSnapshot(t, process).Status() != StatusWaiting {
				t.Fatalf("Process %s published %s before acknowledgment", process.ID(), inspectProcessSnapshot(t, process).Status())
			}
		}
		release()
		assertCanceledSubtree(t, target, descendant)
		_ = childTestResult(t, mustAwait(t, root))
		checkpoints := durability.treeCheckpoints()
		last := checkpoints[len(checkpoints)-1]
		if last.Kind() != TreeCheckpointTerminal {
			t.Fatalf("cancellation checkpoint=%s", last.Kind())
		}
		for _, snapshot := range last.TreeSnapshot().ProcessSnapshots() {
			if !snapshot.Status().Terminal() {
				t.Fatalf("terminal checkpoint retained Process %s as %s", snapshot.ProcessID(), snapshot.Status())
			}
		}
		mustCloseEngine(t, engine)
	})
}

type blockingCancellationCheckpointDurability struct {
	*recordingTreeDurability
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func (b *blockingCancellationCheckpointDurability) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	for _, process := range checkpoint.TreeSnapshot().ProcessSnapshots() {
		if process.Status() == StatusCanceled {
			b.once.Do(func() {
				close(b.entered)
				<-b.release
			})
			if b.err != nil {
				return b.err
			}
			break
		}
	}
	return b.recordingTreeDurability.CommitCheckpoint(ctx, checkpoint)
}

func assertCanceledSubtree(t *testing.T, target, descendant *Process) {
	t.Helper()
	for _, expected := range []struct {
		process *Process
		cause   TerminationCause
	}{
		{process: target, cause: TerminationCauseHostCancellation},
		{process: descendant, cause: TerminationCauseParentCancellation},
	} {
		result := mustAwait(t, expected.process)
		if result.Status() != StatusCanceled || result.Termination().Cause() != expected.cause {
			t.Fatalf("Process %s termination=%+v", expected.process.ID(), result.Termination())
		}
	}
}

func startWaitingSubtreeInEngine(
	t *testing.T,
	engine *Engine,
	deployment Deployment,
) (*Process, *Process, *Process) {
	t.Helper()
	input, _ := EncodeInput(childTestInput{Mode: "wait:subtree"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, root, StatusWaiting)
	targets := directChildIDs(t, engine, root.ID())
	if len(targets) != 1 {
		t.Fatalf("target count = %d, want 1", len(targets))
	}
	targetID, _ := ParseProcessID(targets[0])
	target, _ := engine.Process(targetID)
	waitForStatus(t, target, StatusWaiting)
	descendants := directChildIDs(t, engine, target.ID())
	if len(descendants) != 1 {
		t.Fatalf("descendant count = %d, want 1", len(descendants))
	}
	descendantID, _ := ParseProcessID(descendants[0])
	descendant, _ := engine.Process(descendantID)
	waitForStatus(t, descendant, StatusWaiting)
	return root, target, descendant
}

func processesByChildKey(t *testing.T, engine *Engine, parentID ProcessID) map[string]*Process {
	t.Helper()
	processes := make(map[string]*Process)
	for _, encoded := range directChildIDs(t, engine, parentID) {
		processID, err := ParseProcessID(encoded)
		if err != nil {
			t.Fatal(err)
		}
		process, found := engine.Process(processID)
		if !found {
			t.Fatalf("child Process %s is missing", processID)
		}
		key, child := process.Relation().ChildKey()
		if !child {
			t.Fatalf("Process %s has no child key", processID)
		}
		processes[key.String()] = process
	}
	return processes
}
