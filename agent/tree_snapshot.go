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
// input array order. Every active child wait must have a registration matching
// its Process and opening Signal, pending satisfaction Signals must agree with
// the captured terminal results, and a retained successful child-start
// settlement must identify a captured child matching the complete request.
func ParseTreeSnapshot(data json.RawMessage) (TreeSnapshot, error) {
	wire, err := jsonwire.Decode[treeSnapshotWire](data, "root_id", "incarnation_id", "tree_limits", "process_snapshots")
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: decode: %w", ErrInvalidTreeSnapshot, err)
	}
	if !wire.TreeLimits.MaxSnapshotBytes.Allows(uint64(len(data))) {
		return TreeSnapshot{}, fmt.Errorf("%w: snapshot byte quota exceeded", ErrInvalidTreeSnapshot)
	}
	return treeSnapshotFromWire(wire)
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

type childWaitSnapshotWire struct {
	ParentProcessID ProcessID         `json:"parent_process_id"`
	WaitID          WaitID            `json:"wait_id"`
	Spec            childWaitSpecWire `json:"spec"`
}

type treeSnapshotWire struct {
	RootID           ProcessID               `json:"root_id"`
	IncarnationID    TreeIncarnationID       `json:"incarnation_id"`
	TreeLimits       TreeLimits              `json:"tree_limits"`
	ProcessSnapshots []ProcessSnapshot       `json:"process_snapshots"`
	ChildWaits       []childWaitSnapshotWire `json:"child_waits,omitempty"`
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
	var waits []byte
	if len(t.ChildWaits) != 0 {
		if waits, err = jsonv2.Marshal(t.ChildWaits, jsonv2.Deterministic(true)); err != nil {
			return nil, err
		}
	}
	size := len(header) + len(`,"process_snapshots":[]`) + len(`,"child_waits":`) + len(waits)
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
	if len(waits) != 0 {
		encoded = append(encoded, `,"child_waits":`...)
		encoded = append(encoded, waits...)
	}
	return append(encoded, '}'), nil
}

func (t treeSnapshotWire) clone() treeSnapshotWire {
	clone := t
	clone.ProcessSnapshots = slices.Clone(t.ProcessSnapshots)
	clone.ChildWaits = slices.Clone(t.ChildWaits)
	for index, wait := range t.ChildWaits {
		clone.ChildWaits[index].Spec.Children = slices.Clone(wait.Spec.Children)
	}
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
	slices.SortFunc(t.ChildWaits, func(left, right childWaitSnapshotWire) int {
		return cmp.Compare(left.WaitID.String(), right.WaitID.String())
	})
}

type treeSnapshotValidation struct {
	wire               treeSnapshotWire
	processes          map[ProcessID]processSnapshotWire
	childrenByParent   map[ProcessID][]ProcessID
	children           map[childIdentity]ProcessID
	childCounts        map[ProcessID]uint64
	activeChildCounts  map[ProcessID]uint64
	allocatedResources map[ProcessID]resourceAmounts
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
		if parent := processWire.Relation.ParentID; parent != nil {
			childrenByParent[*parent] = append(childrenByParent[*parent], processWire.ProcessID)
		}
	}
	root, exists := processes[wire.RootID]
	if !exists {
		return nil, fmt.Errorf("%w: root Process is missing", ErrInvalidTreeSnapshot)
	}
	rootRelation, _ := processRelationFromWire(root.ProcessID, root.Relation)
	if !rootRelation.IsRoot() || rootRelation.RootID() != wire.RootID ||
		!wire.TreeLimits.admitsTreeSize(uint64(len(processes))) {
		return nil, fmt.Errorf("%w: invalid root or tree size", ErrInvalidTreeSnapshot)
	}
	return &treeSnapshotValidation{
		wire:               wire,
		processes:          processes,
		childrenByParent:   childrenByParent,
		children:           make(map[childIdentity]ProcessID, len(processes)-1),
		childCounts:        make(map[ProcessID]uint64),
		activeChildCounts:  make(map[ProcessID]uint64),
		allocatedResources: make(map[ProcessID]resourceAmounts),
	}, nil
}

func (t *treeSnapshotValidation) validateRelations() error {
	for _, snapshot := range t.wire.ProcessSnapshots {
		id, processWire := snapshot.ProcessID(), snapshot.state
		relation, _ := processRelationFromWire(id, processWire.Relation)
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
	parentRelation := mustProcessRelation(identity.parent, parent.Relation)
	if relation.Depth() != parentRelation.Depth()+1 || !parent.Capabilities.Allows(child.Capabilities) {
		return fmt.Errorf("%w: invalid child relation or attenuation", ErrInvalidTreeSnapshot)
	}
	if _, duplicate := t.children[identity]; duplicate {
		return fmt.Errorf("%w: duplicate parent-scoped ChildKey", ErrInvalidTreeSnapshot)
	}
	t.children[identity] = relation.ProcessID()
	t.childCounts[identity.parent]++
	if !child.Status.Terminal() {
		t.activeChildCounts[identity.parent]++
	}
	debit, ok := parent.Budget.allocation(child.Budget)
	if !ok {
		return fmt.Errorf("%w: child grant exceeds parent authority", ErrInvalidTreeSnapshot)
	}
	allocated, ok := t.allocatedResources[identity.parent].add(debit)
	if !ok {
		return fmt.Errorf("%w: child budget overflow", ErrInvalidTreeSnapshot)
	}
	t.allocatedResources[identity.parent] = allocated
	return nil
}

func (t *treeSnapshotValidation) validateChildAccounting() error {
	for _, snapshot := range t.wire.ProcessSnapshots {
		id, processWire := snapshot.ProcessID(), snapshot.state
		if !t.wire.TreeLimits.admitsChildren(t.childCounts[id], t.activeChildCounts[id]) ||
			t.allocatedResources[id] != processWire.AllocatedResources {
			return fmt.Errorf("%w: child limits or allocated resources disagree", ErrInvalidTreeSnapshot)
		}
	}
	return nil
}

type openChildWait struct {
	key     WaitKey
	signals []signalRecordWire
}

func newOpenChildWaits(snapshot ProcessSnapshot) map[WaitID]*openChildWait {
	waits := make(map[WaitID]*openChildWait, len(snapshot.openChildWaits))
	for id, key := range snapshot.openChildWaits {
		waits[id] = &openChildWait{key: key}
	}
	if len(waits) == 0 {
		return waits
	}
	for _, signal := range snapshot.state.Mailbox.Signals {
		if signal.WaitID == nil {
			continue
		}
		if facts := waits[*signal.WaitID]; facts != nil {
			facts.signals = append(facts.signals, signal)
		}
	}
	return waits
}

func (t *treeSnapshotValidation) validateChildWaits() error {
	openWaits := make(map[ProcessID]map[WaitID]*openChildWait, len(t.wire.ProcessSnapshots))
	for _, snapshot := range t.wire.ProcessSnapshots {
		openWaits[snapshot.ProcessID()] = newOpenChildWaits(snapshot)
	}
	waitOwners := make(map[WaitID]ProcessID, len(t.wire.ChildWaits))
	for _, encoded := range t.wire.ChildWaits {
		if _, duplicate := waitOwners[encoded.WaitID]; duplicate {
			return fmt.Errorf("%w: duplicate child WaitID", ErrInvalidTreeSnapshot)
		}
		facts := openWaits[encoded.ParentProcessID][encoded.WaitID]
		if err := t.validateChildWaitRegistration(encoded, facts); err != nil {
			return err
		}
		waitOwners[encoded.WaitID] = encoded.ParentProcessID
	}
	for _, snapshot := range t.wire.ProcessSnapshots {
		for waitID := range openWaits[snapshot.ProcessID()] {
			if waitOwners[waitID] != snapshot.ProcessID() {
				return fmt.Errorf("%w: active child wait registration does not belong to Process", ErrInvalidTreeSnapshot)
			}
		}
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildWaitRegistration(
	encoded childWaitSnapshotWire,
	facts *openChildWait,
) error {
	parent, exists := t.processes[encoded.ParentProcessID]
	spec, err := encoded.Spec.value()
	if !exists || err != nil || !encoded.WaitID.Valid() || parent.Status.Terminal() {
		return fmt.Errorf("%w: invalid child wait", ErrInvalidTreeSnapshot)
	}
	if facts == nil || facts.key != spec.Key {
		return fmt.Errorf("%w: child wait is absent from parent mailbox", ErrInvalidTreeSnapshot)
	}
	if err := t.validateChildWaitSignals(facts.signals, encoded.WaitID, spec); err != nil {
		return err
	}
	if err := spec.validateRelations(encoded.ParentProcessID, t.processRelation); err != nil {
		return fmt.Errorf("%w: child wait: %w", ErrInvalidTreeSnapshot, err)
	}
	return nil
}

func (t *treeSnapshotValidation) processRelation(id ProcessID) ProcessRelation {
	if process, exists := t.processes[id]; exists {
		return mustProcessRelation(id, process.Relation)
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

func (t *treeSnapshotValidation) validateChildWaitSignals(signals []signalRecordWire, waitID WaitID, spec ChildWaitSpec) error {
	opened, err := encodeChildWaitOpened(spec)
	if err != nil {
		return fmt.Errorf("%w: encode child wait: %w", ErrInvalidTreeSnapshot, err)
	}
	opened, err = normalizeJSON(opened, MaxPayloadBytes)
	if err != nil {
		return fmt.Errorf("%w: normalize child wait: %w", ErrInvalidTreeSnapshot, err)
	}
	openedDigest := ComputeDigest(opened)
	for _, record := range signals {
		if record.Opens == nil {
			if err := t.validateChildWaitSatisfaction(record, waitID, spec); err != nil {
				return err
			}
			continue
		}
		if record.PayloadDigest != openedDigest {
			return fmt.Errorf("%w: child wait disagrees with its opening Signal", ErrInvalidTreeSnapshot)
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
		return fmt.Errorf("%w: child wait satisfaction disagrees with its registration", ErrInvalidTreeSnapshot)
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
	if !exists || child.Relation.ChildKey == nil || *child.Relation.ChildKey != outcome.key {
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
		func(id ProcessID) Termination {
			if termination := t.processes[id].Termination; termination != nil {
				return *termination
			}
			return Termination{}
		})
}

func (t *treeSnapshotValidation) subtreeTerminal(processID ProcessID) bool {
	if !t.processes[processID].Status.Terminal() {
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
