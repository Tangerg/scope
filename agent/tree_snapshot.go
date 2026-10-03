package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidTreeSnapshot = errors.New("agent: invalid process tree snapshot")

// TreeSnapshot is an immutable, portable capture of one complete Process tree.
// It carries Framework execution facts, a canonical content digest, and a writer
// identity. A snapshot may be a prospective commit or an acknowledged head;
// the operation supplying it defines that guarantee. Validation does not establish
// storage acknowledgment or current writer ownership. Persistence, transactions,
// revisions, and cleanup policy remain Host responsibilities.
type TreeSnapshot struct {
	data   json.RawMessage
	digest Digest
	state  treeSnapshotWire
}

// ParseTreeSnapshot strictly validates the current wire shape and domain
// constraints of one complete Process tree, in canonical order regardless of
// input array order. Every open child wait must observe direct children of the
// Process whose mailbox opened it, pending satisfaction Signals must agree with
// the captured terminal results, and a retained successful child-start
// settlement must identify a captured child matching the complete request.
func ParseTreeSnapshot(data json.RawMessage) (TreeSnapshot, error) {
	document, err := jsonwire.Decode[treeSnapshotDocument](data, "root_id", "incarnation_id", "tree_limits", "process_snapshots")
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: decode: %w", ErrInvalidTreeSnapshot, err)
	}
	if !document.TreeLimits.MaxSnapshotBytes.Allows(uint64(len(data))) {
		return TreeSnapshot{}, fmt.Errorf("%w: snapshot byte quota exceeded", ErrInvalidTreeSnapshot)
	}
	processes, err := document.processSnapshots()
	if err != nil {
		return TreeSnapshot{}, err
	}
	return treeSnapshotFromWire(treeSnapshotWire{
		RootID: document.RootID, IncarnationID: document.IncarnationID,
		TreeLimits: document.TreeLimits, ProcessSnapshots: processes,
	})
}

// treeSnapshotDocument is the persisted tree. Its Process records decode
// together because each relation's root and depth follow from the tree.
type treeSnapshotDocument struct {
	RootID           ProcessID         `json:"root_id"`
	IncarnationID    TreeIncarnationID `json:"incarnation_id"`
	TreeLimits       TreeLimits        `json:"tree_limits"`
	ProcessSnapshots []json.RawMessage `json:"process_snapshots"`
}

// processSnapshots derives every relation from the parent links: the record
// without a parent must be RootID, and every other record descends from it.
func (t treeSnapshotDocument) processSnapshots() ([]ProcessSnapshot, error) {
	documents := make(map[ProcessID]processSnapshotDocument, len(t.ProcessSnapshots))
	order := make([]ProcessID, 0, len(t.ProcessSnapshots))
	for _, data := range t.ProcessSnapshots {
		document, err := decodeProcessSnapshotDocument(data)
		if err != nil {
			return nil, fmt.Errorf("%w: Process: %w", ErrInvalidTreeSnapshot, err)
		}
		if _, duplicate := documents[document.ProcessID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate ProcessID", ErrInvalidTreeSnapshot)
		}
		documents[document.ProcessID] = document
		order = append(order, document.ProcessID)
	}
	relations := make(map[ProcessID]ProcessRelation, len(documents))
	var relate func(ProcessID, int) (ProcessRelation, error)
	relate = func(id ProcessID, ancestors int) (ProcessRelation, error) {
		if relation, known := relations[id]; known {
			return relation, nil
		}
		document, exists := documents[id]
		if !exists || ancestors > len(documents) {
			return ProcessRelation{}, fmt.Errorf("%w: Process does not descend from the root", ErrInvalidTreeSnapshot)
		}
		identity, child, err := document.link()
		if err != nil {
			return ProcessRelation{}, fmt.Errorf("%w: Process: %w", ErrInvalidTreeSnapshot, err)
		}
		relation := rootProcessRelation(id)
		if child {
			parent, err := relate(identity.parent, ancestors+1)
			if err != nil {
				return ProcessRelation{}, err
			}
			relation = childProcessRelation(id, parent, identity.key)
		} else if id != t.RootID {
			return ProcessRelation{}, fmt.Errorf("%w: Process belongs to another tree contract", ErrInvalidTreeSnapshot)
		}
		relations[id] = relation
		return relation, nil
	}
	// Relating in identity order reports the same first error for any input order.
	slices.SortFunc(order, func(left, right ProcessID) int { return cmp.Compare(left.String(), right.String()) })
	snapshots := make([]ProcessSnapshot, 0, len(order))
	for _, id := range order {
		relation, err := relate(id, 0)
		if err != nil {
			return nil, err
		}
		snapshot, err := documents[id].snapshot(relation)
		if err != nil {
			return nil, fmt.Errorf("%w: Process: %w", ErrInvalidTreeSnapshot, err)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func newTreeSnapshot(wire treeSnapshotWire) (TreeSnapshot, error) {
	return treeSnapshotFromWire(wire.clone())
}

func treeSnapshotFromWire(wire treeSnapshotWire) (TreeSnapshot, error) {
	wire.normalize()
	validation, err := newTreeSnapshotValidation(wire)
	if err != nil {
		return TreeSnapshot{}, err
	}
	if validateErr := validation.validate(); validateErr != nil {
		return TreeSnapshot{}, validateErr
	}
	normalized, err := wire.encode()
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: encode: %w", ErrInvalidTreeSnapshot, err)
	}
	if !wire.TreeLimits.MaxSnapshotBytes.Allows(uint64(len(normalized))) {
		return TreeSnapshot{}, fmt.Errorf("%w: snapshot byte quota exceeded", ErrInvalidTreeSnapshot)
	}
	return TreeSnapshot{
		data: normalized, digest: ComputeDigest(normalized), state: wire,
	}, nil
}

// JSON returns an independently owned tree snapshot representation.
func (t TreeSnapshot) JSON() json.RawMessage { return bytes.Clone(t.data) }

// EncodedSize returns the canonical JSON byte length without copying the
// snapshot. The zero value has size zero.
func (t TreeSnapshot) EncodedSize() int { return len(t.data) }

func (t TreeSnapshot) RootID() ProcessID { return t.state.RootID }

func (t TreeSnapshot) Digest() Digest { return t.digest }

func (t TreeSnapshot) IncarnationID() TreeIncarnationID {
	return t.state.IncarnationID
}

// ProcessSnapshots returns immutable captures ordered by depth and ProcessID.
func (t TreeSnapshot) ProcessSnapshots() []ProcessSnapshot {
	return slices.Clone(t.state.ProcessSnapshots)
}

// EffectRequest returns a frozen request retained in a captured prepared Step,
// carrying this capture's writer identity and no AttemptID. It can supply typed
// settlement helpers after a restart but authorizes neither dispatch nor
// replay. Adopted Steps are not retained, so absence does not prove
// non-execution.
func (t TreeSnapshot) EffectRequest(processID ProcessID, id EffectID) (EffectRequest, bool) {
	for _, process := range t.state.ProcessSnapshots {
		if process.ProcessID() != processID || process.state.Prepared == nil {
			continue
		}
		for index, record := range process.state.Prepared.Effects {
			if record.ID == id {
				return newEffectRequest(t.IncarnationID(), process.DeploymentRef(),
					process.Relation(), process.state.CommittedSteps+1, uint32(index),
					record.ID, record.Effect), true
			}
		}
	}
	return EffectRequest{}, false
}

func (t TreeSnapshot) Valid() bool {
	return len(t.data) > 0 && t.digest.Valid() && t.state.RootID.Valid() &&
		t.state.IncarnationID.Valid() && len(t.state.ProcessSnapshots) > 0
}

func (t TreeSnapshot) MarshalJSON() ([]byte, error) {
	if !t.Valid() {
		return nil, ErrInvalidTreeSnapshot
	}
	return bytes.Clone(t.data), nil
}

func (t *TreeSnapshot) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidTreeSnapshot)
	}
	value, err := ParseTreeSnapshot(data)
	if err != nil {
		return err
	}
	*t = value
	return nil
}

func (t TreeSnapshot) wire() (treeSnapshotWire, error) {
	if !t.Valid() {
		return treeSnapshotWire{}, ErrInvalidTreeSnapshot
	}
	return t.state.clone(), nil
}

// treeSnapshotWire holds only tree-wide facts. Each child wait belongs to the
// mailbox of the Process that opened it.
type treeSnapshotWire struct {
	RootID           ProcessID         `json:"root_id"`
	IncarnationID    TreeIncarnationID `json:"incarnation_id"`
	TreeLimits       TreeLimits        `json:"tree_limits"`
	ProcessSnapshots []ProcessSnapshot `json:"process_snapshots"`
}

// treeSnapshotHeaderWire carries the members treeSnapshotWire encodes before
// its Process snapshots, in the same order and under the same names.
type treeSnapshotHeaderWire struct {
	RootID        ProcessID         `json:"root_id"`
	IncarnationID TreeIncarnationID `json:"incarnation_id"`
	TreeLimits    TreeLimits        `json:"tree_limits"`
}

// encode produces the canonical compact encoding of the wire. Every Process
// snapshot is already validated canonical JSON, so its bytes are spliced in
// verbatim: routing them through the encoder would re-parse and re-format
// every Process on every capture. The result is byte-identical to marshaling
// the wire with each snapshot's bytes as its value.
func (t treeSnapshotWire) encode() ([]byte, error) {
	header, err := jsonv2.Marshal(treeSnapshotHeaderWire{
		RootID: t.RootID, IncarnationID: t.IncarnationID, TreeLimits: t.TreeLimits,
	}, jsonv2.Deterministic(true))
	if err != nil {
		return nil, err
	}
	size := len(header) + len(`,"process_snapshots":[]`)
	for _, snapshot := range t.ProcessSnapshots {
		size += len(snapshot.data) + 1
	}
	encoded := make([]byte, 0, size)
	encoded = append(encoded, header[:len(header)-1]...)
	encoded = append(encoded, `,"process_snapshots":[`...)
	for index, snapshot := range t.ProcessSnapshots {
		if index > 0 {
			encoded = append(encoded, ',')
		}
		encoded = append(encoded, snapshot.data...)
	}
	encoded = append(encoded, ']')
	return append(encoded, '}'), nil
}

func (t treeSnapshotWire) clone() treeSnapshotWire {
	clone := t
	clone.ProcessSnapshots = slices.Clone(t.ProcessSnapshots)
	return clone
}

func (t treeSnapshotWire) processSnapshot(id ProcessID) ProcessSnapshot {
	for _, snapshot := range t.ProcessSnapshots {
		if snapshot.ProcessID() == id {
			return snapshot
		}
	}
	return ProcessSnapshot{}
}

func (t *treeSnapshotWire) normalize() {
	slices.SortFunc(t.ProcessSnapshots, func(left, right ProcessSnapshot) int {
		return left.Relation().compareTreeOrder(right.Relation())
	})
}

type treeSnapshotValidation struct {
	wire              treeSnapshotWire
	processes         map[ProcessID]processSnapshotWire
	childrenByParent  map[ProcessID][]ProcessID
	children          map[childIdentity]ProcessID
	childCounts       map[ProcessID]uint64
	activeChildCounts map[ProcessID]uint64
	childAllocations  map[ProcessID]resourceAmounts
}

func newTreeSnapshotValidation(wire treeSnapshotWire) (*treeSnapshotValidation, error) {
	if !wire.RootID.Valid() ||
		!wire.IncarnationID.Valid() || len(wire.ProcessSnapshots) == 0 {
		return nil, fmt.Errorf("%w: incomplete tree identity", ErrInvalidTreeSnapshot)
	}
	if err := wire.TreeLimits.validate(); err != nil {
		return nil, fmt.Errorf("%w: TreeLimits: %w", ErrInvalidTreeSnapshot, err)
	}
	processes := make(map[ProcessID]processSnapshotWire, len(wire.ProcessSnapshots))
	childrenByParent := make(map[ProcessID][]ProcessID)
	for _, snapshot := range wire.ProcessSnapshots {
		if !snapshot.Valid() {
			return nil, fmt.Errorf("%w: Process: %w", ErrInvalidTreeSnapshot, ErrInvalidSnapshot)
		}
		if err := snapshot.validateCapacity(wire.TreeLimits); err != nil {
			return nil, fmt.Errorf("%w: Process: %w", ErrInvalidTreeSnapshot, err)
		}
		processWire := snapshot.state
		if _, duplicate := processes[processWire.ProcessID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate ProcessID", ErrInvalidTreeSnapshot)
		}
		processes[processWire.ProcessID] = processWire
		if parent, child := processWire.Relation.ParentID(); child {
			childrenByParent[parent] = append(childrenByParent[parent], processWire.ProcessID)
		}
	}
	root, exists := processes[wire.RootID]
	if !exists {
		return nil, fmt.Errorf("%w: root Process is missing", ErrInvalidTreeSnapshot)
	}
	rootRelation := root.Relation
	if !rootRelation.IsRoot() || rootRelation.RootID() != wire.RootID ||
		!wire.TreeLimits.admitsTreeSize(uint64(len(processes))) {
		return nil, fmt.Errorf("%w: invalid root or tree size", ErrInvalidTreeSnapshot)
	}
	return &treeSnapshotValidation{
		wire:              wire,
		processes:         processes,
		childrenByParent:  childrenByParent,
		children:          make(map[childIdentity]ProcessID, len(processes)-1),
		childCounts:       make(map[ProcessID]uint64),
		activeChildCounts: make(map[ProcessID]uint64),
		childAllocations:  make(map[ProcessID]resourceAmounts),
	}, nil
}

func (t *treeSnapshotValidation) validateRelations() error {
	for _, snapshot := range t.wire.ProcessSnapshots {
		id, processWire := snapshot.ProcessID(), snapshot.state
		relation := processWire.Relation
		if relation.RootID() != t.wire.RootID {
			return fmt.Errorf("%w: Process belongs to another tree contract", ErrInvalidTreeSnapshot)
		}
		if id == t.wire.RootID {
			continue
		}
		if err := t.recordChild(relation, processWire); err != nil {
			return err
		}
	}
	return nil
}

func (t *treeSnapshotValidation) recordChild(relation ProcessRelation, child processSnapshotWire) error {
	identity, isChild := relation.childIdentity()
	parent, parentExists := t.processes[identity.parent]
	if !isChild || !parentExists {
		return fmt.Errorf("%w: child parent is absent", ErrInvalidTreeSnapshot)
	}
	if relation.Depth() != parent.Relation.Depth()+1 || !parent.Capabilities.Allows(child.Capabilities) {
		return fmt.Errorf("%w: invalid child relation or attenuation", ErrInvalidTreeSnapshot)
	}
	if _, duplicate := t.children[identity]; duplicate {
		return fmt.Errorf("%w: duplicate parent-scoped ChildKey", ErrInvalidTreeSnapshot)
	}
	t.children[identity] = relation.ProcessID()
	t.childCounts[identity.parent]++
	if !child.status().Terminal() {
		t.activeChildCounts[identity.parent]++
	}
	debit, ok := parent.Budget.allocation(child.Budget)
	if !ok {
		return fmt.Errorf("%w: child grant exceeds parent authority", ErrInvalidTreeSnapshot)
	}
	allocated, ok := t.childAllocations[identity.parent].add(debit)
	if !ok {
		return fmt.Errorf("%w: child budget overflow", ErrInvalidTreeSnapshot)
	}
	t.childAllocations[identity.parent] = allocated
	return nil
}

func (t *treeSnapshotValidation) validateChildAccounting() error {
	for _, snapshot := range t.wire.ProcessSnapshots {
		id, processWire := snapshot.ProcessID(), snapshot.state
		if !t.wire.TreeLimits.admitsChildren(t.childCounts[id], t.activeChildCounts[id]) {
			return fmt.Errorf("%w: child limits exceeded", ErrInvalidTreeSnapshot)
		}
		if err := processWire.validateCapacity(t.childAllocations[id]); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidTreeSnapshot, err)
		}
	}
	return nil
}

// validateChildWaits checks each open child wait against the tree its parent
// belongs to: the children it observes, and the answers already admitted.
func (t *treeSnapshotValidation) validateChildWaits() error {
	for _, snapshot := range t.wire.ProcessSnapshots {
		if len(snapshot.openChildWaits) == 0 {
			continue
		}
		parentID := snapshot.ProcessID()
		specs := make(map[WaitID]ChildWaitSpec, len(snapshot.openChildWaits))
		for _, opened := range snapshot.openChildWaits {
			if err := opened.spec.validateRelations(parentID, t.processRelation); err != nil {
				return fmt.Errorf("%w: child wait: %w", ErrInvalidTreeSnapshot, err)
			}
			specs[opened.waitID] = opened.spec
		}
		for _, record := range snapshot.state.Mailbox.Signals {
			if record.WaitID == nil || record.Opens != nil {
				continue
			}
			if spec, open := specs[*record.WaitID]; open {
				if err := t.validateChildWaitSatisfaction(record, *record.WaitID, spec); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (t *treeSnapshotValidation) processRelation(id ProcessID) ProcessRelation {
	if process, exists := t.processes[id]; exists {
		return process.Relation
	}
	return ProcessRelation{}
}

func (t *treeSnapshotValidation) validateChildSettlements() error {
	for _, snapshot := range t.wire.ProcessSnapshots {
		parent := snapshot.state
		if parent.Prepared == nil {
			continue
		}
		for _, record := range parent.Prepared.Effects {
			if record.Effect.Target() != EffectTargetFramework {
				continue
			}
			operation, err := decodeFrameworkOperation(record.Effect.Payload())
			if err != nil {
				return err
			}
			if err := operation.validateTree(t, parent.ProcessID, record); err != nil {
				return fmt.Errorf("%w: framework operation: %w", ErrInvalidTreeSnapshot, err)
			}
		}
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildWaitSatisfaction(record signalRecordWire, waitID WaitID, spec ChildWaitSpec) error {
	signal, err := NewSignal(record.ID, waitID, record.Payload)
	if err != nil {
		return fmt.Errorf("%w: invalid child wait Signal: %w", ErrInvalidTreeSnapshot, err)
	}
	satisfied, err := ParseChildWaitSatisfied(signal)
	if err != nil || record.ID != waitID.childWaitSignalID() || !satisfied.Matches(waitID, spec) {
		return fmt.Errorf("%w: child wait satisfaction disagrees with its wait", ErrInvalidTreeSnapshot)
	}
	for _, outcome := range satisfied.outcomes {
		if !t.matchesChildWaitOutcome(outcome, spec.Boundary) {
			return fmt.Errorf("%w: child wait outcome disagrees with its tree", ErrInvalidTreeSnapshot)
		}
	}
	return nil
}

func (t *treeSnapshotValidation) matchesChildWaitOutcome(outcome ChildOutcome, boundary ChildWaitBoundary) bool {
	child, exists := t.processes[outcome.result.ProcessID()]
	if key, _ := child.Relation.ChildKey(); !exists || key != outcome.key {
		return false
	}
	result, terminal := child.result()
	if !terminal {
		return false
	}
	expectedJSON, expectedErr := jsonv2.Marshal(result.wire(), jsonv2.Deterministic(true))
	actualJSON, actualErr := jsonv2.Marshal(outcome.result.wire(), jsonv2.Deterministic(true))
	if expectedErr != nil || actualErr != nil || !bytes.Equal(expectedJSON, actualJSON) {
		return false
	}
	if boundary != ChildWaitBoundaryDrained {
		return outcome.boundary == boundary && len(outcome.subtreeUnresolvedEffects) == 0
	}
	if !t.subtreeTerminal(child.ProcessID) {
		return false
	}
	return slices.Equal(outcome.subtreeUnresolvedEffects, t.subtreeUnresolvedEffects(child.ProcessID))
}

func (t *treeSnapshotValidation) subtreeUnresolvedEffects(processID ProcessID) []UnresolvedEffect {
	return subtreeUnresolvedEffects(processID,
		func(id ProcessID) []ProcessID { return t.childrenByParent[id] },
		func(id ProcessID) Termination { return t.processes[id].publishedTermination() })
}

func (t *treeSnapshotValidation) subtreeTerminal(processID ProcessID) bool {
	if !t.processes[processID].status().Terminal() {
		return false
	}
	for _, childID := range t.childrenByParent[processID] {
		if !t.subtreeTerminal(childID) {
			return false
		}
	}
	return true
}

func (t *treeSnapshotValidation) validate() error {
	if err := t.validateRelations(); err != nil {
		return err
	}
	if err := t.validateChildAccounting(); err != nil {
		return err
	}
	if err := t.validateChildSettlements(); err != nil {
		return err
	}
	return t.validateChildWaits()
}
