package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReleaseTreeRemovesRegistryAndPreservesTerminalHandles(t *testing.T) {
	store := NewMemoryTreeCommitter()
	engine, err := NewEngine(EngineConfig{TreeCommitter: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	deployment := newChildTestDeployment(t)
	input, _ := EncodePayload(childTestInput{Mode: "recurse:1"})
	root, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	result := mustAwait(t, root)
	ids := childTestResult(t, result).ChildIDs
	handles := []*Process{root}
	for _, encodedID := range ids {
		id, _ := ParseProcessID(encodedID)
		child, found := engine.Process(id)
		if !found {
			t.Fatal("completed child is missing before release")
		}
		handles = append(handles, child)
	}
	if releaseErr := engine.ReleaseTree(t.Context(), handles[1].ID()); !errors.Is(releaseErr, ErrInvalidProcessRelation) {
		t.Fatalf("release child error = %v", releaseErr)
	}
	otherInput, _ := EncodePayload(childTestInput{Mode: "leaf"})
	other, err := engine.Start(t.Context(), deployment, otherInput)
	if err != nil {
		t.Fatal(err)
	}
	mustAwait(t, other)
	if err := engine.ReleaseTree(t.Context(), root.ID()); err != nil {
		t.Fatal(err)
	}
	for _, handle := range handles {
		if _, found := engine.Process(handle.ID()); found || handle.handle.runtime.Load() != nil {
			t.Fatalf("released Process %s retains its tree", handle.ID())
		}
		if result := mustAwait(t, handle); result.Status() != StatusCompleted {
			t.Fatalf("released handle status = %s", result.Status())
		}

		if err := handle.Kill(t.Context(), "already finished"); !errors.Is(err, ErrProcessFinished) {
			t.Fatalf("released handle Kill error = %v", err)
		}
	}
	if _, err := engine.InspectTree(t.Context(), root.ID()); !errors.Is(err, ErrInvalidProcessRelation) {
		t.Fatalf("released tree inspection error = %v", err)
	}
	if _, err := engine.CaptureTree(t.Context(), root.ID()); !errors.Is(err, ErrInvalidProcessRelation) {
		t.Fatalf("released tree capture error = %v", err)
	}
	engine.mu.RLock()
	remainingProcesses, remainingTrees, remainingChildren := len(engine.processes), len(engine.trees), len(engine.children)
	engine.mu.RUnlock()
	if remainingProcesses != 1 || remainingTrees != 1 || remainingChildren != 0 {
		t.Fatalf("registry sizes = %d, %d, %d; want only the independent root", remainingProcesses, remainingTrees, remainingChildren)
	}
	if err := engine.ReleaseTree(t.Context(), other.ID()); err != nil {
		t.Fatal(err)
	}
	if closeErr := engine.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	head, exists, loadErr := store.LoadTree(t.Context(), root.ID())
	if loadErr != nil || !exists || !head.Valid() || snapshotByID(head.ProcessSnapshots(), root.ID()).Status() != StatusCompleted {
		t.Fatalf("Engine release or close removed stored recovery facts: exists=%t error=%v", exists, loadErr)
	}

}

func TestReleaseTreeCancellationLeavesWaitingTreeUsable(t *testing.T) {
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, _ := EncodePayload(childTestInput{Mode: "external_wait"})
	root, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, root, StatusWaiting)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := engine.ReleaseTree(ctx, root.ID()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("release active tree error = %v", err)
	}
	if _, found := engine.Process(root.ID()); !found {
		t.Fatal("canceled release removed active root")
	}
	if snapshot, err := engine.CaptureTree(t.Context(), root.ID()); err != nil || !snapshot.Valid() {
		t.Fatalf("waiting tree capture = %v, error = %v", snapshot.Valid(), err)
	}
	if err := root.Kill(t.Context(), "settle tree"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReleaseTree(t.Context(), root.ID()); err != nil {
		t.Fatal(err)
	}
}
