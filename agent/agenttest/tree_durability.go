package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	agent "github.com/Tangerg/scope/agent"
)

type memoryDurabilityFactKind uint8

const (
	memoryDurabilityFactInvalid memoryDurabilityFactKind = iota
	memoryDurabilityFactProcessOutcome
	memoryDurabilityFactActivation
	memoryDurabilityFactEffectPending
	memoryDurabilityFactEffectSettled
	memoryDurabilityFactEffectResolved
	memoryDurabilityFactCheckpointInput
	memoryDurabilityFactCheckpointParked
	memoryDurabilityFactCheckpointTerminal
)

type memoryDurabilityFactKey struct {
	kind      memoryDurabilityFactKind
	rootID    agent.ProcessID
	processID agent.ProcessID
	effectID  agent.EffectID
	digest    agent.Digest
	writer    agent.TreeIncarnationID
}

type memoryDurabilityHead struct {
	incarnationID agent.TreeIncarnationID
	digest        agent.Digest
	snapshot      agent.TreeSnapshot
}

// MemoryTreeDurability is a concurrency-safe teaching and test adapter for the
// TreeDurability CAS contract. It is intentionally in agenttest: production
// Hosts should implement the same transaction with their own durable store.
type MemoryTreeDurability struct {
	mu    sync.Mutex
	heads map[agent.ProcessID]memoryDurabilityHead
	facts map[memoryDurabilityFactKey]agent.Digest
}

// NewMemoryTreeDurability constructs an empty in-memory durability adapter.
func NewMemoryTreeDurability() *MemoryTreeDurability {
	return &MemoryTreeDurability{
		heads: make(map[agent.ProcessID]memoryDurabilityHead),
		facts: make(map[memoryDurabilityFactKey]agent.Digest),
	}
}

// TreeDurability returns this adapter through the conformance-driver port.
func (m *MemoryTreeDurability) TreeDurability() agent.TreeDurability { return m }

// LoadTree returns the current authoritative head for rootID.
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
	return head.snapshot, exists, nil
}

// AcknowledgeProcessStartOutcome atomically closes admission and, for durable
// started/child outcomes, installs the prospective tree head.
func (m *MemoryTreeDurability) AcknowledgeProcessStartOutcome(
	_ context.Context,
	outcome agent.ProcessStartOutcome,
) error {
	if m == nil || !outcome.Valid() {
		return errors.New("agenttest: invalid Process start outcome")
	}
	rootID := outcome.Admission().Relation().RootID()
	processID := outcome.Admission().Relation().ProcessID()
	key := memoryDurabilityFactKey{
		kind: memoryDurabilityFactProcessOutcome, rootID: rootID, processID: processID,
	}
	content, err := processOutcomeDigest(outcome)
	if err != nil {
		return err
	}
	previous, hasPrevious := outcome.PreviousTreeDigest()
	prospective, hasProspective := outcome.TreeSnapshot()

	m.mu.Lock()
	defer m.mu.Unlock()
	if !hasProspective {
		return m.commitFactWithoutHead(key, content)
	}
	incarnationID, _ := prospective.IncarnationID()
	if hasPrevious {
		return m.advanceHead(key, content, rootID, incarnationID, previous, prospective)
	}
	return m.createHead(key, content, rootID, incarnationID, prospective)
}

// ActivateTree atomically fences the previous writer and installs the new
// incarnation snapshot.
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
		kind: memoryDurabilityFactActivation, rootID: rootID,
		writer: activation.IncarnationID(),
	}
	content := agent.ComputeDigest(prospective.JSON())
	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, exists := m.facts[key]; exists {
		head := m.heads[rootID]
		if previous == content && head.incarnationID == activation.IncarnationID() &&
			head.digest == prospective.Digest() {
			return nil
		}
		return durabilityContentConflict()
	}
	head, exists := m.heads[rootID]
	if !exists || head.incarnationID != activation.PreviousIncarnationID() ||
		head.digest != activation.PreviousTreeDigest() {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = memoryHead(prospective)
	return nil
}

// CommitEffect atomically records an Effect boundary and advances its tree.
func (m *MemoryTreeDurability) CommitEffect(
	_ context.Context,
	boundary agent.EffectBoundary,
) error {
	if m == nil || !boundary.Valid() {
		return errors.New("agenttest: invalid Effect boundary")
	}
	kind := memoryEffectFactKind(boundary.Kind())
	if kind == memoryDurabilityFactInvalid {
		return errors.New("agenttest: invalid Effect boundary kind")
	}
	prospective := boundary.TreeSnapshot()
	incarnationID, _ := prospective.IncarnationID()
	key := memoryDurabilityFactKey{
		kind: kind, rootID: prospective.RootID(), effectID: boundary.Request().ID(),
	}
	content, err := effectBoundaryDigest(boundary)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.advanceHead(
		key, content, prospective.RootID(), incarnationID,
		boundary.PreviousTreeDigest(), prospective,
	)
}

// CommitCheckpoint atomically records a Runtime safe cut and advances its tree.
func (m *MemoryTreeDurability) CommitCheckpoint(
	_ context.Context,
	checkpoint agent.TreeCheckpoint,
) error {
	if m == nil || !checkpoint.Valid() {
		return errors.New("agenttest: invalid tree checkpoint")
	}
	kind := memoryCheckpointFactKind(checkpoint.Kind())
	if kind == memoryDurabilityFactInvalid {
		return errors.New("agenttest: invalid tree checkpoint kind")
	}
	prospective := checkpoint.TreeSnapshot()
	incarnationID, _ := prospective.IncarnationID()
	key := memoryDurabilityFactKey{
		kind: kind, rootID: prospective.RootID(), digest: prospective.Digest(),
	}
	content := agent.ComputeDigest(prospective.JSON())
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.advanceHead(
		key, content, prospective.RootID(), incarnationID,
		checkpoint.PreviousTreeDigest(), prospective,
	)
}

func (m *MemoryTreeDurability) createHead(
	key memoryDurabilityFactKey,
	content agent.Digest,
	rootID agent.ProcessID,
	incarnationID agent.TreeIncarnationID,
	prospective agent.TreeSnapshot,
) error {
	if previous, exists := m.facts[key]; exists {
		head := m.heads[rootID]
		if previous == content && head.incarnationID == incarnationID &&
			head.digest == prospective.Digest() {
			return nil
		}
		return durabilityContentConflict()
	}
	if _, exists := m.heads[rootID]; exists {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = memoryHead(prospective)
	return nil
}

func (m *MemoryTreeDurability) advanceHead(
	key memoryDurabilityFactKey,
	content agent.Digest,
	rootID agent.ProcessID,
	incarnationID agent.TreeIncarnationID,
	previousDigest agent.Digest,
	prospective agent.TreeSnapshot,
) error {
	head, exists := m.heads[rootID]
	if !exists || head.incarnationID != incarnationID {
		return treeIncarnationConflict()
	}
	if previous, committed := m.facts[key]; committed {
		if previous == content && head.digest == prospective.Digest() {
			return nil
		}
		return durabilityContentConflict()
	}
	if head.digest != previousDigest {
		return treeIncarnationConflict()
	}
	m.facts[key] = content
	m.heads[rootID] = memoryHead(prospective)
	return nil
}

func (m *MemoryTreeDurability) commitFactWithoutHead(
	key memoryDurabilityFactKey,
	content agent.Digest,
) error {
	if previous, exists := m.facts[key]; exists {
		if previous == content {
			return nil
		}
		return durabilityContentConflict()
	}
	m.facts[key] = content
	return nil
}

func memoryHead(snapshot agent.TreeSnapshot) memoryDurabilityHead {
	incarnationID, _ := snapshot.IncarnationID()
	return memoryDurabilityHead{
		incarnationID: incarnationID, digest: snapshot.Digest(), snapshot: snapshot,
	}
}

func memoryEffectFactKind(kind agent.EffectBoundaryKind) memoryDurabilityFactKind {
	switch kind {
	case agent.EffectBoundaryPending:
		return memoryDurabilityFactEffectPending
	case agent.EffectBoundarySettled:
		return memoryDurabilityFactEffectSettled
	case agent.EffectBoundaryResolved:
		return memoryDurabilityFactEffectResolved
	default:
		return memoryDurabilityFactInvalid
	}
}

func memoryCheckpointFactKind(kind agent.TreeCheckpointKind) memoryDurabilityFactKind {
	switch kind {
	case agent.TreeCheckpointInput:
		return memoryDurabilityFactCheckpointInput
	case agent.TreeCheckpointParked:
		return memoryDurabilityFactCheckpointParked
	case agent.TreeCheckpointTerminal:
		return memoryDurabilityFactCheckpointTerminal
	default:
		return memoryDurabilityFactInvalid
	}
}

func processOutcomeDigest(outcome agent.ProcessStartOutcome) (agent.Digest, error) {
	startedAt, hasStartedAt := outcome.StartedAt()
	failure, hasFailure := outcome.Failure()
	previous, hasPrevious := outcome.PreviousTreeDigest()
	snapshot, hasSnapshot := outcome.TreeSnapshot()
	content := struct {
		Relation      agent.ProcessRelation
		DeploymentRef agent.DeploymentRef
		Descriptor    agent.Digest
		Budget        agent.Budget
		Capabilities  agent.CapabilitySet
		Status        agent.ProcessStartOutcomeStatus
		StartedAt     *time.Time
		Failure       *agent.Failure
		Previous      *agent.Digest
		Snapshot      *agent.Digest
	}{
		Relation:      outcome.Admission().Relation(),
		DeploymentRef: outcome.Admission().DeploymentRef(),
		Descriptor:    outcome.Admission().Descriptor().Digest(),
		Budget:        outcome.Admission().Budget(),
		Capabilities:  outcome.Admission().Capabilities(),
		Status:        outcome.Status(),
	}
	if hasStartedAt {
		content.StartedAt = &startedAt
	}
	if hasFailure {
		content.Failure = &failure
	}
	if hasPrevious {
		content.Previous = &previous
	}
	if hasSnapshot {
		digest := snapshot.Digest()
		content.Snapshot = &digest
	}
	return jsonDigest(content)
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
