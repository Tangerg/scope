package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
)

type memoryTreeHead struct {
	snapshot TreeSnapshot
	sequence uint64
}

// MemoryTreeCommitter atomically accepts tree commits in volatile memory; an
// acknowledgment survives only as long as this instance. Share the instance
// when restoring trees into another Engine. Heads and replay-protection facts
// are retained for the store lifetime, including after Engine.ReleaseTree, so
// retention grows with commit count. Discard the store only after all its
// writers and callers have stopped. Construct it with NewMemoryTreeCommitter.
type MemoryTreeCommitter struct {
	mu    sync.Mutex
	heads map[ProcessID]memoryTreeHead
	facts map[string]Digest
}

// NewMemoryTreeCommitter starts a separate volatile ledger; it cannot fence
// writers or reconcile acknowledgments from an earlier instance.
func NewMemoryTreeCommitter() *MemoryTreeCommitter {
	return &MemoryTreeCommitter{
		heads: make(map[ProcessID]memoryTreeHead),
		facts: make(map[string]Digest),
	}
}

func (m *MemoryTreeCommitter) LoadTree(
	_ context.Context,
	rootID ProcessID,
) (TreeSnapshot, bool, error) {
	if m == nil || !rootID.Valid() {
		return TreeSnapshot{}, false, ErrInvalidTreeSnapshot
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	head, exists := m.heads[rootID]
	return head.snapshot, exists, nil
}

func (m *MemoryTreeCommitter) ActivateTree(
	_ context.Context,
	activation TreeActivation,
) error {
	if m == nil {
		return errors.New("agent: invalid tree activation")
	}
	content, err := activation.ContentDigest()
	if err != nil {
		return err
	}
	prospective := activation.TreeSnapshot()
	rootID := prospective.RootID()
	key := activation.Identity()
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, exists := m.facts[key]; exists {
		head := m.heads[rootID]
		if previous == content && head.sequence == 0 && head.snapshot.Digest() == prospective.Digest() {
			return nil
		}
		return commitContentConflict()
	}
	head, exists := m.heads[rootID]
	incarnationID := head.snapshot.IncarnationID()
	if !exists || incarnationID != activation.PreviousIncarnationID() ||
		head.snapshot.Digest() != activation.PreviousTreeDigest() {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = memoryTreeHead{snapshot: prospective}
	return nil
}

func (m *MemoryTreeCommitter) CommitEffect(
	_ context.Context,
	boundary EffectBoundary,
) error {
	if m == nil {
		return errors.New("agent: invalid Effect boundary")
	}
	prospective := boundary.TreeSnapshot()
	content, err := boundary.ContentDigest()
	if err != nil {
		return err
	}
	key := boundary.Identity()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.advanceHead(
		key, content, boundary.Sequence(), boundary.PreviousTreeDigest(), prospective,
	)
}

func (m *MemoryTreeCommitter) CommitCheckpoint(
	_ context.Context,
	checkpoint TreeCheckpoint,
) error {
	if m == nil {
		return errors.New("agent: invalid tree checkpoint")
	}
	prospective := checkpoint.TreeSnapshot()
	content, err := checkpoint.ContentDigest()
	if err != nil {
		return err
	}
	key := checkpoint.Identity()
	m.mu.Lock()
	defer m.mu.Unlock()
	if checkpoint.Kind() == TreeCheckpointKindStart {
		return m.createHead(key, content, checkpoint.Sequence(), prospective)
	}
	return m.advanceHead(
		key, content, checkpoint.Sequence(), checkpoint.PreviousTreeDigest(), prospective,
	)
}

func (m *MemoryTreeCommitter) createHead(
	key string,
	content Digest,
	sequence uint64,
	prospective TreeSnapshot,
) error {
	rootID := prospective.RootID()
	if previous, exists := m.facts[key]; exists {
		head := m.heads[rootID]
		if previous == content && head.sequence == sequence && head.snapshot.Digest() == prospective.Digest() {
			return nil
		}
		return commitContentConflict()
	}
	if _, exists := m.heads[rootID]; exists {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = memoryTreeHead{snapshot: prospective, sequence: sequence}
	return nil
}

func (m *MemoryTreeCommitter) advanceHead(
	key string,
	content Digest,
	sequence uint64,
	previousDigest Digest,
	prospective TreeSnapshot,
) error {
	rootID := prospective.RootID()
	incarnationID := prospective.IncarnationID()
	head, exists := m.heads[rootID]
	headIncarnationID := head.snapshot.IncarnationID()
	if !exists || headIncarnationID != incarnationID {
		return treeIncarnationConflict()
	}
	if previous, committed := m.facts[key]; committed {
		if previous == content && head.sequence == sequence && head.snapshot.Digest() == prospective.Digest() {
			return nil
		}
		return commitContentConflict()
	}
	if head.sequence == math.MaxUint64 || sequence != head.sequence+1 || head.snapshot.Digest() != previousDigest {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = memoryTreeHead{snapshot: prospective, sequence: sequence}
	return nil
}

func commitContentConflict() error {
	return fmt.Errorf("%w: idempotency content differs", ErrCommitConflict)
}

func treeIncarnationConflict() error {
	return fmt.Errorf("%w: authoritative head changed", ErrTreeIncarnationConflict)
}
