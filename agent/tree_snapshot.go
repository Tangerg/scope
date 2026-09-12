package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

const maxTreeSnapshotBytes = 512 << 20

var ErrInvalidTreeSnapshot = errors.New("agent: invalid process tree snapshot")

// TreeSnapshot is an immutable, portable capture of one complete Process tree.
// It owns Framework execution facts, a canonical content digest, and the
// optional active-writer identity of durable state. Persistence, transactions,
// revisions, and cleanup policy remain Host responsibilities.
type TreeSnapshot struct {
	data   json.RawMessage
	digest Digest
	state  treeSnapshotWire
}

// ParseTreeSnapshot validates the current wire shape and domain constraints of
// one complete Process tree. Unknown members are rejected. Every active child
// wait must have a registration belonging to its Process and matching its
// opening Signal. Pending satisfaction Signals must agree with that boundary
// and the terminal results in the captured tree.
func ParseTreeSnapshot(data json.RawMessage) (TreeSnapshot, error) {
	if len(data) == 0 || len(data) > maxTreeSnapshotBytes {
		return TreeSnapshot{}, fmt.Errorf(
			"%w: JSON must contain at most %d bytes", ErrInvalidTreeSnapshot, maxTreeSnapshotBytes,
		)
	}
	wire, err := wireJSON.decode[treeSnapshotWire](data)
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: decode: %w", ErrInvalidTreeSnapshot, err)
	}
	return treeSnapshotFromWire(wire)
}

func newTreeSnapshot(wire treeSnapshotWire) (TreeSnapshot, error) {
	return treeSnapshotFromWire(wire.clone())
}

func treeSnapshotFromWire(wire treeSnapshotWire) (TreeSnapshot, error) {
	if validateErr := validateTreeSnapshot(wire); validateErr != nil {
		return TreeSnapshot{}, validateErr
	}
	normalizeTreeSnapshot(&wire)
	normalized, err := json.Marshal(wire)
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: encode: %w", ErrInvalidTreeSnapshot, err)
	}
	if len(normalized) > maxTreeSnapshotBytes {
		return TreeSnapshot{}, fmt.Errorf(
			"%w: exceeds %d bytes", ErrInvalidTreeSnapshot, maxTreeSnapshotBytes,
		)
	}
	return TreeSnapshot{
		data: normalized, digest: ComputeDigest(normalized), state: wire,
	}, nil
}

// JSON returns an independently owned tree snapshot representation.
func (t TreeSnapshot) JSON() json.RawMessage { return bytes.Clone(t.data) }

// RootID returns the identity of the tree's root Process.
func (t TreeSnapshot) RootID() ProcessID { return t.state.RootID }

// Digest returns the canonical content identity of this complete tree state.
func (t TreeSnapshot) Digest() Digest { return t.digest }

// IncarnationID returns the active writer identity carried by a durable tree.
// Ephemeral snapshots return false.
func (t TreeSnapshot) IncarnationID() (TreeIncarnationID, bool) {
	return treeSnapshotIncarnation(t.state.IncarnationID)
}

// ProcessSnapshots returns immutable captures ordered by depth and ProcessID.
func (t TreeSnapshot) ProcessSnapshots() []ProcessSnapshot {
	return slices.Clone(t.state.ProcessSnapshots)
}

func (t TreeSnapshot) Valid() bool {
	return len(t.data) > 0 && t.digest.Valid() && t.state.RootID.Valid() &&
		(t.state.IncarnationID == nil || t.state.IncarnationID.Valid()) && len(t.state.ProcessSnapshots) > 0
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
	IncarnationID    *TreeIncarnationID      `json:"incarnation_id,omitempty"`
	ProcessSnapshots []ProcessSnapshot       `json:"process_snapshots"`
	ChildWaits       []childWaitSnapshotWire `json:"child_waits,omitempty"`
}

func (t treeSnapshotWire) clone() treeSnapshotWire {
	clone := t
	clone.ProcessSnapshots = slices.Clone(t.ProcessSnapshots)
	clone.ChildWaits = slices.Clone(t.ChildWaits)
	for index, wait := range t.ChildWaits {
		clone.ChildWaits[index].Spec.Children = slices.Clone(wait.Spec.Children)
	}
	if t.IncarnationID != nil {
		clone.IncarnationID = new(*t.IncarnationID)
	}
	return clone
}

func treeSnapshotIncarnation(value *TreeIncarnationID) (TreeIncarnationID, bool) {
	if value == nil {
		return TreeIncarnationID{}, false
	}
	return *value, true
}

func normalizeTreeSnapshot(wire *treeSnapshotWire) {
	slices.SortFunc(wire.ProcessSnapshots, compareSnapshots)
	slices.SortFunc(wire.ChildWaits, func(left, right childWaitSnapshotWire) int {
		return cmp.Compare(left.WaitID.String(), right.WaitID.String())
	})
}

func compareSnapshots(left, right ProcessSnapshot) int {
	if order := cmp.Compare(left.Relation().Depth(), right.Relation().Depth()); order != 0 {
		return order
	}
	return cmp.Compare(left.ProcessID().String(), right.ProcessID().String())
}

func validateTreeSnapshot(wire treeSnapshotWire) error {
	validation, err := newTreeSnapshotValidation(wire)
	if err != nil {
		return err
	}
	if err := validation.validateRelations(); err != nil {
		return err
	}
	if err := validation.validateChildAccounting(); err != nil {
		return err
	}
	if err := validation.validateChildControls(); err != nil {
		return err
	}
	return validation.validateChildWaits()
}

type treeSnapshotValidation struct {
	wire              treeSnapshotWire
	root              processSnapshotWire
	processes         map[ProcessID]processSnapshotWire
	children          map[childIdentity]ProcessID
	childCounts       map[ProcessID]uint32
	activeChildCounts map[ProcessID]uint32
	allocatedBudgets  map[ProcessID]Budget
}

func newTreeSnapshotValidation(wire treeSnapshotWire) (*treeSnapshotValidation, error) {
	if !wire.RootID.Valid() ||
		wire.IncarnationID != nil && !wire.IncarnationID.Valid() || len(wire.ProcessSnapshots) == 0 {
		return nil, fmt.Errorf("%w: incomplete tree identity", ErrInvalidTreeSnapshot)
	}
	processes := make(map[ProcessID]processSnapshotWire, len(wire.ProcessSnapshots))
	for _, snapshot := range wire.ProcessSnapshots {
		if !snapshot.Valid() {
			return nil, fmt.Errorf("%w: Process: %w", ErrInvalidTreeSnapshot, ErrInvalidSnapshot)
		}
		// Validation only reads facts already owned by the immutable snapshot.
		processWire := *snapshot.state
		if _, duplicate := processes[processWire.ProcessID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate ProcessID", ErrInvalidTreeSnapshot)
		}
		processes[processWire.ProcessID] = processWire
	}
	root, exists := processes[wire.RootID]
	if !exists {
		return nil, fmt.Errorf("%w: root Process is missing", ErrInvalidTreeSnapshot)
	}
	rootRelation, _ := processRelationFromWire(root.ProcessID, root.Relation)
	if !rootRelation.IsRoot() || rootRelation.RootID() != wire.RootID ||
		uint32(len(processes)) > root.TreeLimits.MaxTreeProcesses {
		return nil, fmt.Errorf("%w: invalid root or tree size", ErrInvalidTreeSnapshot)
	}
	return &treeSnapshotValidation{
		wire:              wire,
		root:              root,
		processes:         processes,
		children:          make(map[childIdentity]ProcessID, len(processes)-1),
		childCounts:       make(map[ProcessID]uint32),
		activeChildCounts: make(map[ProcessID]uint32),
		allocatedBudgets:  make(map[ProcessID]Budget),
	}, nil
}

func (t *treeSnapshotValidation) validateRelations() error {
	for id, processWire := range t.processes {
		relation, _ := processRelationFromWire(id, processWire.Relation)
		if relation.RootID() != t.wire.RootID || processWire.TreeLimits != t.root.TreeLimits {
			return fmt.Errorf("%w: Process belongs to another tree contract", ErrInvalidTreeSnapshot)
		}
		if id == t.wire.RootID {
			continue
		}
		parentID, child := relation.ParentID()
		key, keyed := relation.ChildKey()
		parent, parentExists := t.processes[parentID]
		parentRelation, _ := processRelationFromWire(parentID, parent.Relation)
		identity := childIdentity{parent: parentID, key: key}
		if !child || !keyed || !parentExists || relation.Depth() != parentRelation.Depth()+1 ||
			!parent.Capabilities.Allows(processWire.Capabilities) {
			return fmt.Errorf("%w: invalid child relation or attenuation", ErrInvalidTreeSnapshot)
		}
		if _, duplicate := t.children[identity]; duplicate {
			return fmt.Errorf("%w: duplicate parent-scoped ChildKey", ErrInvalidTreeSnapshot)
		}
		t.children[identity] = id
		t.childCounts[parentID]++
		if !processWire.Status.Terminal() {
			t.activeChildCounts[parentID]++
		}
		budget, ok := t.allocatedBudgets[parentID].add(processWire.Budget)
		if !ok {
			return fmt.Errorf("%w: child budget overflow", ErrInvalidTreeSnapshot)
		}
		t.allocatedBudgets[parentID] = budget
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildAccounting() error {
	for id, processWire := range t.processes {
		if t.childCounts[id] > processWire.TreeLimits.MaxChildren ||
			t.activeChildCounts[id] > processWire.TreeLimits.MaxActiveChildren ||
			t.allocatedBudgets[id] != processWire.ReservedBudget {
			return fmt.Errorf("%w: child limits or reserved budget disagree", ErrInvalidTreeSnapshot)
		}
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildWaits() error {
	waitOwners := make(map[WaitID]ProcessID, len(t.wire.ChildWaits))
	for _, encoded := range t.wire.ChildWaits {
		parent, exists := t.processes[encoded.ParentProcessID]
		spec, err := encoded.Spec.value()
		if !exists || err != nil || !encoded.WaitID.Valid() || parent.Status.Terminal() {
			return fmt.Errorf("%w: invalid child wait", ErrInvalidTreeSnapshot)
		}
		if _, duplicate := waitOwners[encoded.WaitID]; duplicate {
			return fmt.Errorf("%w: duplicate child WaitID", ErrInvalidTreeSnapshot)
		}
		waitRecord, exists := findWaitRecord(parent.Mailbox, encoded.WaitID)
		if !exists || waitRecord.ExternallyAddressable || waitRecord.Closed || waitRecord.WaitKey != spec.Key {
			return fmt.Errorf("%w: child wait is absent from parent mailbox", ErrInvalidTreeSnapshot)
		}
		if err := t.validateChildWaitSignals(parent.Mailbox, encoded.WaitID, spec); err != nil {
			return err
		}
		for _, childID := range spec.Children {
			child, exists := t.processes[childID]
			relation, _ := processRelationFromWire(childID, child.Relation)
			parentID, isChild := relation.ParentID()
			if !exists || !isChild || parentID != encoded.ParentProcessID {
				return fmt.Errorf("%w: wait references a non-direct child", ErrInvalidTreeSnapshot)
			}
		}
		waitOwners[encoded.WaitID] = encoded.ParentProcessID
	}
	for _, processWire := range t.processes {
		for _, wait := range processWire.Mailbox.Waits {
			if !wait.ExternallyAddressable && !wait.Closed {
				if waitOwners[wait.WaitID] != processWire.ProcessID {
					return fmt.Errorf("%w: active child wait registration does not belong to Process", ErrInvalidTreeSnapshot)
				}
			}
		}
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildControls() error {
	for _, parent := range t.processes {
		if parent.Prepared == nil {
			continue
		}
		for _, record := range parent.Prepared.Effects {
			if record.Effect.Target() != EffectTargetFramework || !record.definitelySettled() {
				continue
			}
			operation, err := decodeFrameworkEffectOperation(record.Effect.Payload())
			if err != nil {
				return err
			}
			if operation != frameworkEffectSignalChild && operation != frameworkEffectCancelChild {
				continue
			}
			if err := t.validateChildControl(parent.ProcessID, record); err != nil {
				return fmt.Errorf("%w: child control: %w", ErrInvalidTreeSnapshot, err)
			}
		}
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildControl(parentID ProcessID, record preparedEffect) error {
	if err := record.validateChildControl(); err != nil {
		return err
	}
	result, err := decodeChildControlResult(record.Settlement.Payload())
	if err != nil || result.failure.Valid() {
		return err
	}
	child, present := t.processes[result.childID]
	if !present || child.Relation.ParentID == nil || *child.Relation.ParentID != parentID {
		return ErrInvalidChildControl
	}
	if result.operation == frameworkEffectCancelChild {
		if !child.Status.Terminal() && !child.PendingControl.CancellationOwner.valid() {
			return ErrInvalidChildControl
		}
		return nil
	}
	request, err := decodeChildControlEffect(record.Effect.Payload())
	if err != nil {
		return err
	}
	for _, receipt := range snapshotSignalReceipts(child.Mailbox) {
		if receipt.ID() != result.signalID {
			continue
		}
		if !receipt.Matches(*request.Signal) {
			return ErrInvalidChildControl
		}
		return nil
	}
	return ErrInvalidChildControl
}

func (t *treeSnapshotValidation) validateChildWaitSignals(mailbox mailboxWire, waitID WaitID, spec ChildWaitSpec) error {
	opened, err := encodeChildWaitOpened(spec)
	if err != nil {
		return fmt.Errorf("%w: encode child wait: %w", ErrInvalidTreeSnapshot, err)
	}
	opened, err = wireJSON.normalize(opened, maxWireBytes)
	if err != nil {
		return fmt.Errorf("%w: normalize child wait: %w", ErrInvalidTreeSnapshot, err)
	}
	for _, record := range mailbox.Signals {
		if record.WaitID == nil || *record.WaitID != waitID {
			continue
		}
		if record.OpensWait {
			if record.PayloadDigest != ComputeDigest(opened) {
				return fmt.Errorf("%w: child wait disagrees with its opening Signal", ErrInvalidTreeSnapshot)
			}
			continue
		}
		signal, signalErr := newSignal(record.ID, waitID, record.Payload)
		if signalErr != nil {
			return fmt.Errorf("%w: invalid child wait Signal: %w", ErrInvalidTreeSnapshot, signalErr)
		}
		satisfied, parseErr := ParseChildWaitSatisfied(signal)
		if parseErr != nil || record.ID != deriveChildWaitSignalID(waitID) ||
			satisfied.Key() != spec.Key || satisfied.Boundary() != spec.Boundary {
			return fmt.Errorf("%w: child wait satisfaction disagrees with its registration", ErrInvalidTreeSnapshot)
		}
		required, _ := spec.Condition.required(len(spec.Children))
		if uint32(len(satisfied.outcomes)) < required {
			return fmt.Errorf("%w: child wait condition is unsatisfied", ErrInvalidTreeSnapshot)
		}
		previous := -1
		for _, outcome := range satisfied.outcomes {
			index := slices.Index(spec.Children, outcome.result.ProcessID())
			if index <= previous || !t.matchesChildWaitOutcome(outcome, spec.Boundary) {
				return fmt.Errorf("%w: child wait outcome disagrees with its tree", ErrInvalidTreeSnapshot)
			}
			previous = index
		}
	}
	return nil
}

func (t *treeSnapshotValidation) matchesChildWaitOutcome(outcome ChildOutcome, boundary ChildWaitBoundary) bool {
	child, exists := t.processes[outcome.result.ProcessID()]
	if !exists || !child.Status.Terminal() || child.Relation.ChildKey == nil || *child.Relation.ChildKey != outcome.key {
		return false
	}
	expected := resultWire{
		ProcessID: child.ProcessID, StartedAt: child.StartedAt, FinishedAt: *child.FinishedAt,
		Output: child.Output, Termination: *child.Termination, Usage: child.Usage,
	}
	expectedJSON, expectedErr := json.Marshal(expected)
	actualJSON, actualErr := json.Marshal(resultWireFromValue(outcome.result))
	if expectedErr != nil || actualErr != nil || !bytes.Equal(expectedJSON, actualJSON) {
		return false
	}
	return boundary != ChildWaitBoundaryDrained || t.subtreeTerminal(child.ProcessID)
}

func (t *treeSnapshotValidation) subtreeTerminal(processID ProcessID) bool {
	if !t.processes[processID].Status.Terminal() {
		return false
	}
	for _, child := range t.processes {
		if child.Relation.ParentID != nil && *child.Relation.ParentID == processID && !t.subtreeTerminal(child.ProcessID) {
			return false
		}
	}
	return true
}

func findWaitRecord(mailbox mailboxWire, id WaitID) (waitRecordWire, bool) {
	for _, record := range mailbox.Waits {
		if record.WaitID == id {
			return record, true
		}
	}
	return waitRecordWire{}, false
}

// treeFreeze identifies the active snapshot barrier. Only CaptureTree receives
// it, and releasing it lets the same tree owner resume scheduling.
type treeFreeze struct {
	runtime *treeRuntime
}

func (t *treeFreeze) release() error {
	response := make(chan error, 1)
	select {
	case t.runtime.controls <- treeCommand{
		kind: treeCommandReleaseFreeze, freeze: t, response: response,
	}:
	case <-t.runtime.done:
		return ErrEngineQuiescenceUnavailable
	}
	select {
	case err := <-response:
		return err
	case <-t.runtime.done:
		return ErrEngineQuiescenceUnavailable
	}
}
