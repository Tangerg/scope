package agent

import (
	"errors"
	"testing"
)

func TestMemoryCommitSequenceRejectsConflictsWithoutAdvancingHead(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	store := runtime.engine.committer.(*MemoryTreeCommitter)
	initial := runtime.head
	process.status, process.pauseReason = StatusPaused, "review"
	paused := controlValue(runtime.captureTree())
	checkpoint := controlValue(newTreeCheckpoint(2, TreeCheckpointKindParked, initial.Digest(), paused))
	skipped := checkpoint
	skipped.sequence = 3
	if err := store.CommitCheckpoint(t.Context(), skipped); !errors.Is(err, ErrTreeIncarnationConflict) {
		t.Fatalf("skipped sequence: %v", err)
	}
	for range 2 {
		if err := store.CommitCheckpoint(t.Context(), checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutation := range []struct {
		name   string
		change func(*TreeCheckpoint)
	}{
		{"kind", func(c *TreeCheckpoint) { c.kind = TreeCheckpointKindSignals }},
		{"predecessor", func(c *TreeCheckpoint) { c.previousTreeDigest = ComputeDigest([]byte("different predecessor")) }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			conflict := checkpoint
			mutation.change(&conflict)
			if !conflict.Valid() {
				t.Fatal("conflict fixture is invalid")
			}
			if err := store.CommitCheckpoint(t.Context(), conflict); !errors.Is(err, ErrCommitConflict) {
				t.Fatalf("changed commit content: %v", err)
			}
		})
	}
	// Returning to the initial content must not make the old creation current.
	running := controlValue(newTreeCheckpoint(3, TreeCheckpointKindProgress, paused.Digest(), initial))
	if err := store.CommitCheckpoint(t.Context(), running); err != nil {
		t.Fatal(err)
	}
	start := controlValue(newTreeCheckpoint(1, TreeCheckpointKindStart, Digest{}, initial))
	if err := store.CommitCheckpoint(t.Context(), start); !errors.Is(err, ErrCommitConflict) {
		t.Fatalf("historical creation: %v", err)
	}
	if err := store.CommitCheckpoint(t.Context(), checkpoint); !errors.Is(err, ErrCommitConflict) {
		t.Fatalf("historical checkpoint: %v", err)
	}
	head, exists, err := store.LoadTree(t.Context(), process.handle.processID)
	if err != nil || !exists || head.Digest() != initial.Digest() {
		t.Fatalf("rejected commits changed head: %v", err)
	}
}

func TestCommitSequenceValidation(t *testing.T) {
	runtime, _ := newChildCompletionTestProcess(t)
	for _, sequence := range []uint64{0, 2} {
		if _, err := newTreeCheckpoint(sequence, TreeCheckpointKindStart, Digest{}, runtime.head); err == nil {
			t.Fatalf("start accepted sequence %d", sequence)
		}
	}
}

func TestEffectBoundaryDigestIncludesProcessRelation(t *testing.T) {
	_, request, snapshot := effectBoundaryFixture(t, 2, 64)
	boundary, err := newEffectBoundary(1, EffectBoundaryKindPending, request, Settlement{}, ComputeDigest([]byte("previous")), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	original, err := effectBoundaryDigest(boundary)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name   string
		change func(*ProcessRelation)
	}{
		{"root", func(relation *ProcessRelation) { relation.rootID = newProcessID() }},
		{"parent", func(relation *ProcessRelation) { relation.parentID = newProcessID() }},
		{"child key", func(relation *ProcessRelation) { relation.childKey = controlValue(ParseChildKey("other")) }},
		{"depth", func(relation *ProcessRelation) { relation.depth++ }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := boundary
			mutation.change(&changed.request.relation)
			digest, err := effectBoundaryDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if digest == original {
				t.Fatal("relation change did not change the committed fact digest")
			}
		})
	}
}
