package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	agent "github.com/Tangerg/scope/agent"
)

// Each boundary retains its own identity scope: Effects by ID and phase,
// checkpoints by cut, and activations by the proposed writer. Using the protocol
// types avoids a second vocabulary that can drift when a boundary is added.
type memoryDurabilityFactKey struct {
	rootID         agent.ProcessID
	effectKind     agent.EffectBoundaryKind
	effectID       agent.EffectID
	checkpointKind agent.TreeCheckpointKind
	digest         agent.Digest
	writer         agent.TreeIncarnationID
}

// MemoryTreeDurability is a concurrency-safe teaching and test adapter for the
// TreeDurability CAS contract. It is intentionally in agenttest: production
// Hosts should implement the same transaction with their own durable store.
type MemoryTreeDurability struct {
	mu    sync.Mutex
	heads map[agent.ProcessID]agent.TreeSnapshot
	facts map[memoryDurabilityFactKey]agent.Digest
}

func NewMemoryTreeDurability() *MemoryTreeDurability {
	return &MemoryTreeDurability{
		heads: make(map[agent.ProcessID]agent.TreeSnapshot),
		facts: make(map[memoryDurabilityFactKey]agent.Digest),
	}
}

func (m *MemoryTreeDurability) TreeDurability() agent.TreeDurability { return m }

func (m *MemoryTreeDurability) LoadTree(
	_ context.Context,
	rootID agent.ProcessID,
) (agent.TreeSnapshot, bool, error) {
	if m == nil || !rootID.Valid() {
		return agent.TreeSnapshot{}, false, agent.ErrInvalidTreeSnapshot
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	head, exists := m.heads[rootID]
	return head, exists, nil
}

func (m *MemoryTreeDurability) ActivateTree(
	_ context.Context,
	activation agent.TreeActivation,
) error {
	if m == nil || !activation.Valid() {
		return errors.New("agenttest: invalid tree activation")
	}
	prospective := activation.TreeSnapshot()
	rootID := prospective.RootID()
	key := memoryDurabilityFactKey{
		rootID: rootID,
		writer: activation.IncarnationID(),
	}
	content := agent.ComputeDigest(prospective.JSON())
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, exists := m.facts[key]; exists {
		head := m.heads[rootID]
		if previous == content && head.Digest() == prospective.Digest() {
			return nil
		}
		return durabilityContentConflict()
	}
	head, exists := m.heads[rootID]
	incarnationID, _ := head.IncarnationID()
	if !exists || incarnationID != activation.PreviousIncarnationID() ||
		head.Digest() != activation.PreviousTreeDigest() {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = prospective
	return nil
}

func (m *MemoryTreeDurability) CommitEffect(
	_ context.Context,
	boundary agent.EffectBoundary,
) error {
	if m == nil || !boundary.Valid() {
		return errors.New("agenttest: invalid Effect boundary")
	}
	prospective := boundary.TreeSnapshot()
	key := memoryDurabilityFactKey{
		effectKind: boundary.Kind(), rootID: prospective.RootID(), effectID: boundary.Request().ID(),
	}
	content, err := effectBoundaryDigest(boundary)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.advanceHead(
		key, content, boundary.PreviousTreeDigest(), prospective,
	)
}

func (m *MemoryTreeDurability) CommitCheckpoint(
	_ context.Context,
	checkpoint agent.TreeCheckpoint,
) error {
	if m == nil || !checkpoint.Valid() {
		return errors.New("agenttest: invalid tree checkpoint")
	}
	prospective := checkpoint.TreeSnapshot()
	key := memoryDurabilityFactKey{
		checkpointKind: checkpoint.Kind(), rootID: prospective.RootID(), digest: prospective.Digest(),
	}
	if checkpoint.Kind() == agent.TreeCheckpointStart {
		// A second creation must conflict under the same root key even if its content differs.
		key.digest = agent.Digest{}
	}
	content := agent.ComputeDigest(prospective.JSON())
	m.mu.Lock()
	defer m.mu.Unlock()
	if checkpoint.Kind() == agent.TreeCheckpointStart {
		return m.createHead(key, content, prospective)
	}
	return m.advanceHead(
		key, content, checkpoint.PreviousTreeDigest(), prospective,
	)
}

func (m *MemoryTreeDurability) createHead(
	key memoryDurabilityFactKey,
	content agent.Digest,
	prospective agent.TreeSnapshot,
) error {
	rootID := prospective.RootID()
	if previous, exists := m.facts[key]; exists {
		head := m.heads[rootID]
		if previous == content && head.Digest() == prospective.Digest() {
			return nil
		}
		return durabilityContentConflict()
	}
	if _, exists := m.heads[rootID]; exists {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = prospective
	return nil
}

func (m *MemoryTreeDurability) advanceHead(
	key memoryDurabilityFactKey,
	content agent.Digest,
	previousDigest agent.Digest,
	prospective agent.TreeSnapshot,
) error {
	rootID := prospective.RootID()
	incarnationID, _ := prospective.IncarnationID()
	head, exists := m.heads[rootID]
	headIncarnationID, _ := head.IncarnationID()
	if !exists || headIncarnationID != incarnationID {
		return treeIncarnationConflict()
	}
	if previous, committed := m.facts[key]; committed {
		if previous == content && head.Digest() == prospective.Digest() {
			return nil
		}
		return durabilityContentConflict()
	}
	if head.Digest() != previousDigest {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = prospective
	return nil
}

func effectBoundaryDigest(boundary agent.EffectBoundary) (agent.Digest, error) {
	request := boundary.Request()
	settlement, hasSettlement := boundary.Settlement()
	content := struct {
		Kind          agent.EffectBoundaryKind
		ProcessID     agent.ProcessID
		DeploymentRef agent.DeploymentRef
		Relation      agent.ProcessRelation
		StepSequence  uint64
		BatchIndex    uint32
		EffectID      agent.EffectID
		Effect        agent.Effect
		Settlement    *agent.Settlement
		Previous      agent.Digest
		Snapshot      agent.Digest
	}{
		Kind: boundary.Kind(), ProcessID: request.ProcessID(),
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

func jsonDigest(value any) (agent.Digest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return agent.Digest{}, fmt.Errorf("agenttest: encode durability fact: %w", err)
	}
	return agent.ComputeDigest(encoded), nil
}

func durabilityContentConflict() error {
	return fmt.Errorf("%w: idempotency content differs", agent.ErrDurabilityConflict)
}

func treeIncarnationConflict() error {
	return fmt.Errorf("%w: authoritative head changed", agent.ErrTreeIncarnationConflict)
}
