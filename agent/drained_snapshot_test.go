package agent

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestCapturesKeepDrainedChildrenStableWhileParentChanges(t *testing.T) {
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodeInput(childTestInput{Mode: "wait:paused"})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	root, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if killErr := root.Kill(ctx, "capture test cleanup"); killErr != nil && !errors.Is(killErr, ErrProcessFinished) {
			t.Error(killErr)
		}
		if joinErr := root.Join(ctx); joinErr != nil {
			t.Error(joinErr)
		}
		mustCloseEngine(t, engine)
	})
	waitForStatus(t, root, StatusWaiting)
	children := directChildIDs(t, engine, root.ID())
	childID, err := ParseProcessID(children[0])
	if err != nil {
		t.Fatal(err)
	}
	child, _ := engine.Process(childID)
	waitForStatus(t, child, StatusPaused)
	before, err := engine.CaptureTree(t.Context(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if resumeErr := child.Resume(t.Context()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	if joinErr := child.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	completed, err := engine.CaptureTree(t.Context(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	stable, err := engine.CaptureTree(t.Context(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	beforeChild := capturedProcess(t, before, childID)
	completedChild := capturedProcess(t, completed, childID)
	if beforeChild.Status() != StatusPaused || completedChild.Status() != StatusCompleted ||
		capturedProcess(t, completed, root.ID()).Status() != StatusWaiting ||
		!bytes.Equal(completedChild.JSON(), capturedProcess(t, stable, childID).JSON()) {
		t.Fatal("capture confused active and drained Process state")
	}
	if killErr := root.Kill(t.Context(), "stop remaining children"); killErr != nil {
		t.Fatal(killErr)
	}
	if joinErr := root.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	terminated, err := engine.CaptureTree(t.Context(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if capturedProcess(t, terminated, root.ID()).Status() != StatusKilled ||
		!bytes.Equal(completedChild.JSON(), capturedProcess(t, terminated, childID).JSON()) {
		t.Fatal("terminal capture froze its active parent or rewrote a drained child")
	}
	runtime, err := engine.runtimeForTree(root.ID())
	if err != nil {
		t.Fatal(err)
	}
	<-runtime.done
	assertTreeMembership(t, runtime)

	restoredEngine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
	restored, err := restoredEngine.RestoreTree(t.Context(), deployment, terminated)
	if err != nil {
		t.Fatal(err)
	}
	if joinErr := restored.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	restoredRuntime, err := restoredEngine.runtimeForTree(restored.ID())
	if err != nil {
		t.Fatal(err)
	}
	<-restoredRuntime.done
	assertTreeMembership(t, restoredRuntime)
}

func capturedProcess(t *testing.T, snapshot TreeSnapshot, id ProcessID) ProcessSnapshot {
	t.Helper()
	for _, process := range snapshot.ProcessSnapshots() {
		if process.ProcessID() == id {
			return process
		}
	}
	t.Fatalf("snapshot omits Process %s", id)
	return ProcessSnapshot{}
}

func assertTreeMembership(t *testing.T, runtime *treeRuntime) {
	t.Helper()
	seen := make(map[ProcessID]bool)
	for parentID, children := range runtime.childrenByParent {
		if runtime.processes[parentID] == nil || len(children) == 0 {
			t.Fatal("child index retained an absent parent or an empty entry")
		}
		for _, childID := range children {
			child := runtime.processes[childID]
			if child == nil || seen[childID] {
				t.Fatal("child index contains an absent or duplicate Process")
			}
			actualParent, isChild := child.handle.relation.ParentID()
			if !isChild || actualParent != parentID {
				t.Fatal("child index disagrees with the authoritative relation")
			}
			seen[childID] = true
		}
	}
	if len(seen) != len(runtime.processes)-1 {
		t.Fatal("child index omits a tree member")
	}
}
