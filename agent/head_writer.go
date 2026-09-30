package agent

import (
	"context"
	"sync/atomic"
)

// headWriter owns this tree instance's writer identity, its acknowledged
// head, and the single commit in flight. The commit sequence advances only
// when the committer acknowledges a prospective head, and every boundary it
// builds names that head as its predecessor. Only the tree owner goroutine
// calls its methods; committing is the one fact readable from other
// goroutines.
type headWriter struct {
	committer    TreeCommitter
	identity     TreeIncarnationID
	acknowledged TreeSnapshot
	sequence     uint64
	inFlight     *treeCommit
	done         chan treeCommitCompletion
	busy         atomic.Bool
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

func (h *headWriter) committing() bool { return h.inFlight != nil }

// commitStart durably creates the tree: the first head is the only one with no
// predecessor, and it opens this incarnation's commit sequence.
func (h *headWriter) commitStart(ctx context.Context, snapshot TreeSnapshot) error {
	checkpoint, err := newTreeCheckpoint(1, TreeCheckpointKindStart, Digest{}, snapshot)
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

// activate fences the previous writer of snapshot's tree and adopts snapshot,
// which must carry this writer's identity, as the head of a fresh sequence.
func (h *headWriter) activate(ctx context.Context, previous TreeSnapshot, snapshot TreeSnapshot) error {
	activation, err := newTreeActivation(previous.IncarnationID(), previous.Digest(), h.identity, snapshot)
	if err != nil {
		return err
	}
	if err := activateTree(ctx, h.committer, activation); err != nil {
		return err
	}
	h.establish(snapshot)
	return nil
}

func (h *headWriter) establish(snapshot TreeSnapshot) {
	if !snapshot.Valid() || snapshot.IncarnationID() != h.identity {
		panic("agent: durable tree head does not belong to this writer")
	}
	h.acknowledged = snapshot
}

// commitEffect starts the Effect boundary of kind for request, carrying
// commit's prospective snapshot. ctx must already be detached from caller
// cancellation, because the Host owns storage deadlines.
func (h *headWriter) commitEffect(ctx context.Context, commit *treeCommit, kind EffectBoundaryKind, request EffectRequest, settlement Settlement) error {
	if commit == nil || !commit.processID.Valid() {
		panic("agent: invalid tree Effect commit")
	}
	boundary, err := newEffectBoundary(h.sequence+1, kind, request, settlement, h.acknowledged.Digest(), commit.snapshot)
	if err != nil {
		return err
	}
	h.launch(commit, func() error { return commitEffectBoundary(ctx, h.committer, boundary) })
	return nil
}

func (h *headWriter) commitCheckpoint(ctx context.Context, commit *treeCommit, kind TreeCheckpointKind) error {
	checkpoint, err := newTreeCheckpoint(h.sequence+1, kind, h.acknowledged.Digest(), commit.snapshot)
	if err != nil {
		return err
	}
	h.launch(commit, func() error { return commitTreeCheckpoint(ctx, h.committer, checkpoint) })
	return nil
}

func (h *headWriter) launch(commit *treeCommit, call func() error) {
	if commit == nil || h.inFlight != nil {
		panic("agent: invalid concurrent tree commit")
	}
	h.inFlight = commit
	h.busy.Store(true)
	go func() {
		h.done <- treeCommitCompletion{commit: commit, err: call()}
	}()
}

// settle retires the in-flight commit that completion answers. A successful
// answer makes its snapshot the acknowledged head. Answers for any other
// commit are ignored.
func (h *headWriter) settle(completion treeCommitCompletion) (*treeCommit, bool) {
	commit := h.inFlight
	if commit == nil || completion.commit != commit {
		return nil, false
	}
	h.inFlight = nil
	h.busy.Store(false)
	if completion.err == nil {
		h.acknowledged = commit.snapshot
		h.sequence++
	}
	return commit, true
}
