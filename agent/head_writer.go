package agent

import (
	"context"
	"sync/atomic"
)

// headWriter owns this tree instance's writer identity, its acknowledged
// head, and the single commit in flight. The commit sequence advances only
// when the committer acknowledges a prospective head, and every boundary it
// builds names that head as its predecessor. Only the tree owner goroutine
// calls its methods; inFlight is atomic because Engine.Close also reads it
// without the owner.
type headWriter struct {
	committer    TreeCommitter
	identity     TreeIncarnationID
	acknowledged TreeSnapshot
	sequence     uint64
	inFlight     atomic.Pointer[treeCommit]
	done         chan treeCommitCompletion
}

type treeCommitCompletion struct {
	commit *treeCommit
	err    error
}

func newHeadWriter(committer TreeCommitter) *headWriter {
	return &headWriter{committer: committer, identity: newTreeIncarnationID(), done: make(chan treeCommitCompletion)}
}

func (h *headWriter) incarnation() TreeIncarnationID { return h.identity }

func (h *headWriter) head() TreeSnapshot { return h.acknowledged }

func (h *headWriter) committing() bool { return h.inFlight.Load() != nil }

// commitStart durably creates the tree: the first head is the only one with no
// predecessor, and it opens this incarnation's commit sequence.
func (h *headWriter) commitStart(ctx context.Context, snapshot TreeSnapshot) error {
	checkpoint, err := newTreeCheckpoint(1, checkpointCauseCut, Digest{}, snapshot)
	if err != nil {
		return err
	}
	if err := commitTreeCheckpoint(ctx, h.committer, checkpoint); err != nil {
		return err
	}
	h.establish(snapshot)
	h.sequence = checkpoint.Sequence()
	return nil
}

// activate fences the previous writer of the tree and adopts wire, stamped
// with this writer's identity, as the head of a fresh sequence.
func (h *headWriter) activate(ctx context.Context, previous TreeSnapshot, wire treeSnapshotWire) (TreeSnapshot, error) {
	wire.IncarnationID = h.identity
	snapshot, err := newTreeSnapshot(wire)
	if err != nil {
		return TreeSnapshot{}, err
	}
	activation, err := newTreeActivation(previous.IncarnationID(), previous.Digest(), snapshot)
	if err != nil {
		return TreeSnapshot{}, err
	}
	if err := activateTree(ctx, h.committer, activation); err != nil {
		return TreeSnapshot{}, err
	}
	h.establish(snapshot)
	return snapshot, nil
}

func (h *headWriter) establish(snapshot TreeSnapshot) {
	if !snapshot.Valid() {
		panic("agent: durable tree head is invalid")
	}
	h.acknowledged = snapshot
}

// commitEffect starts the Effect boundary for commit's Effect, carrying its
// prospective snapshot. ctx must already be detached from caller
// cancellation, because the Host owns storage deadlines.
func (h *headWriter) commitEffect(ctx context.Context, commit *treeCommit) error {
	if commit == nil || !commit.processID.Valid() {
		panic("agent: invalid tree Effect commit")
	}
	boundary, err := newEffectBoundary(h.sequence+1, commit.kind == treeCommitEffectResolved,
		commit.processID, commit.effectID, h.acknowledged.Digest(), commit.snapshot)
	if err != nil {
		return err
	}
	h.launch(commit, func() error { return commitEffectBoundary(ctx, h.committer, boundary) })
	return nil
}

func (h *headWriter) commitCheckpoint(ctx context.Context, commit *treeCommit, cause checkpointCause) error {
	checkpoint, err := newTreeCheckpoint(h.sequence+1, cause, h.acknowledged.Digest(), commit.snapshot)
	if err != nil {
		return err
	}
	h.launch(commit, func() error { return commitTreeCheckpoint(ctx, h.committer, checkpoint) })
	return nil
}

func (h *headWriter) launch(commit *treeCommit, call func() error) {
	if commit == nil || !h.inFlight.CompareAndSwap(nil, commit) {
		panic("agent: invalid concurrent tree commit")
	}
	go func() {
		h.done <- treeCommitCompletion{commit: commit, err: call()}
	}()
}

// settle retires the in-flight commit that completion answers. A successful
// answer makes its snapshot the acknowledged head. Answers for any other
// commit are ignored.
func (h *headWriter) settle(completion treeCommitCompletion) (*treeCommit, bool) {
	commit := h.inFlight.Load()
	if commit == nil || completion.commit != commit {
		return nil, false
	}
	h.inFlight.Store(nil)
	if completion.err == nil {
		h.acknowledged = commit.snapshot
		h.sequence++
	}
	return commit, true
}
