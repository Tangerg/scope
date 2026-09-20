package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
)

// Each boundary retains its own identity scope: Effects by ID and phase,
// checkpoints by incarnation and sequence, and activations by the proposed
// writer. Content equality cannot identify a transition through repeated states.
type memoryCommitFactKey struct {
	rootID     ProcessID
	effectKind EffectBoundaryKind
	effectID   EffectID
	checkpoint bool
	sequence   uint64
	writer     TreeIncarnationID
}

type memoryTreeHead struct {
	snapshot TreeSnapshot
	sequence uint64
}

// MemoryTreeCommitter atomically accepts tree commits in volatile memory. An
// acknowledgment survives only for the lifetime of this store instance, not a
// process restart. Share the instance when restoring trees into another Engine.
// Heads and replay-protection facts are retained for the entire store lifetime;
// Engine.ReleaseTree does not delete them. Discard the store only after all its
// writers and callers have stopped. Construct it with NewMemoryTreeCommitter.
type MemoryTreeCommitter struct {
	mu    sync.Mutex
	heads map[ProcessID]memoryTreeHead
	facts map[memoryCommitFactKey]Digest
}

func NewMemoryTreeCommitter() *MemoryTreeCommitter {
	return &MemoryTreeCommitter{
		heads: make(map[ProcessID]memoryTreeHead),
		facts: make(map[memoryCommitFactKey]Digest),
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
	if m == nil || !activation.Valid() {
		return errors.New("agent: invalid tree activation")
	}
	prospective := activation.TreeSnapshot()
	rootID := prospective.RootID()
	key := memoryCommitFactKey{
		rootID: rootID,
		writer: activation.IncarnationID(),
	}
	content := prospective.Digest()
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
	if m == nil || !boundary.Valid() {
		return errors.New("agent: invalid Effect boundary")
	}
	prospective := boundary.TreeSnapshot()
	key := memoryCommitFactKey{
		effectKind: boundary.Kind(), rootID: prospective.RootID(), effectID: boundary.Request().ID(),
	}
	content, err := effectBoundaryDigest(boundary)
	if err != nil {
		return err
	}
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
	if m == nil || !checkpoint.Valid() {
		return errors.New("agent: invalid tree checkpoint")
	}
	prospective := checkpoint.TreeSnapshot()
	key := memoryCommitFactKey{
		checkpoint: true, rootID: prospective.RootID(),
		writer: prospective.IncarnationID(), sequence: checkpoint.Sequence(),
	}
	content, err := jsonDigest(struct {
		Kind     TreeCheckpointKind
		Previous string
		Snapshot Digest
	}{checkpoint.Kind(), checkpoint.PreviousTreeDigest().String(), prospective.Digest()})
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if checkpoint.Kind() == TreeCheckpointKindStart {
		return m.createHead(key, content, prospective)
	}
	return m.advanceHead(
		key, content, checkpoint.Sequence(), checkpoint.PreviousTreeDigest(), prospective,
	)
}

func (m *MemoryTreeCommitter) createHead(
	key memoryCommitFactKey,
	content Digest,
	prospective TreeSnapshot,
) error {
	rootID := prospective.RootID()
	if previous, exists := m.facts[key]; exists {
		head := m.heads[rootID]
		if previous == content && head.sequence == 1 && head.snapshot.Digest() == prospective.Digest() {
			return nil
		}
		return commitContentConflict()
	}
	if _, exists := m.heads[rootID]; exists {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = memoryTreeHead{snapshot: prospective, sequence: 1}
	return nil
}

func (m *MemoryTreeCommitter) advanceHead(
	key memoryCommitFactKey,
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

func effectBoundaryDigest(boundary EffectBoundary) (Digest, error) {
	request := boundary.Request()
	settlement, hasSettlement := boundary.Settlement()
	content := struct {
		Sequence      uint64
		Kind          EffectBoundaryKind
		ProcessID     ProcessID
		DeploymentRef DeploymentRef
		Relation      ProcessRelation
		StepSequence  uint64
		BatchIndex    uint32
		EffectID      EffectID
		Effect        Effect
		Settlement    *Settlement
		Previous      Digest
		Snapshot      Digest
	}{
		Sequence: boundary.Sequence(), Kind: boundary.Kind(), ProcessID: request.ProcessID(),
		DeploymentRef: request.DeploymentRef(), Relation: request.Relation(),
		StepSequence: request.StepSequence(), BatchIndex: request.BatchIndex(),
		EffectID: request.ID(), Effect: request.Effect(),
		Previous: boundary.PreviousTreeDigest(), Snapshot: boundary.TreeSnapshot().Digest(),
	}
	if hasSettlement {
		content.Settlement = &settlement
	}
	return jsonDigest(content)
}

func jsonDigest(value any) (Digest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return Digest{}, fmt.Errorf("agent: encode committer fact: %w", err)
	}
	return ComputeDigest(encoded), nil
}

func commitContentConflict() error {
	return fmt.Errorf("%w: idempotency content differs", ErrCommitConflict)
}

func treeIncarnationConflict() error {
	return fmt.Errorf("%w: authoritative head changed", ErrTreeIncarnationConflict)
}
