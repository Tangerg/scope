package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
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

// ParseTreeSnapshot validates the current wire shape and domain constraints of
// one complete Process tree. Validation follows canonical Process and wait order
// regardless of input array order. Unknown members are rejected. Every active child
// wait must have a registration belonging to its Process and matching its
// opening Signal. Pending satisfaction Signals must agree with that boundary
// and the terminal results in the captured tree. A retained successful child-start
// settlement must identify a captured child matching the complete start request.
func ParseTreeSnapshot(data json.RawMessage) (TreeSnapshot, error) {
	wire, err := jsonwire.Decode[treeSnapshotWire](data)
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: decode: %w", ErrInvalidTreeSnapshot, err)
	}
	snapshot, err := treeSnapshotFromWire(wire)
	if err != nil {
		return TreeSnapshot{}, err
	}
	for _, process := range wire.ProcessSnapshots {
		if process.ProcessID() == wire.RootID && !process.state.TreeLimits.MaxSnapshotBytes.Allows(uint64(len(data))) {
			return TreeSnapshot{}, fmt.Errorf("%w: snapshot byte quota exceeded", ErrInvalidTreeSnapshot)
		}
	}
	return snapshot, nil
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
	normalized, err := json.Marshal(wire)
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: encode: %w", ErrInvalidTreeSnapshot, err)
	}
	if !validation.root.TreeLimits.MaxSnapshotBytes.Allows(uint64(len(normalized))) {
		return TreeSnapshot{}, fmt.Errorf("%w: snapshot byte quota exceeded", ErrInvalidTreeSnapshot)
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

// IncarnationID returns the writer identity carried by this snapshot.
func (t TreeSnapshot) IncarnationID() TreeIncarnationID {
	return t.state.IncarnationID
}

// ProcessSnapshots returns immutable captures ordered by depth and ProcessID.
func (t TreeSnapshot) ProcessSnapshots() []ProcessSnapshot {
	return slices.Clone(t.state.ProcessSnapshots)
}

// EffectRequest returns a frozen request retained in a captured prepared Step.
// The request carries this capture's writer identity, not a restored writer's
// authority. It can supply typed settlement helpers after a restart; it does not
// authorize dispatch or replay. Adopted Steps are no longer retained, so absence
// does not prove non-execution. The enclosing snapshot defines acknowledgment.
func (t TreeSnapshot) EffectRequest(processID ProcessID, id EffectID) (EffectRequest, bool) {
	for _, process := range t.state.ProcessSnapshots {
		if process.ProcessID() != processID || process.state.Prepared == nil {
			continue
		}
		for index, record := range process.state.Prepared.Effects {
			if record.ID == id {
				return newEffectRequest(processID, t.IncarnationID(), process.DeploymentRef(),
					process.Relation(), process.state.Prepared.StepSequence, uint32(index),
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
	return clone
}

func (t *treeSnapshotWire) normalize() {
	slices.SortFunc(t.ProcessSnapshots, compareSnapshots)
	slices.SortFunc(t.ChildWaits, func(left, right childWaitSnapshotWire) int {
		return cmp.Compare(left.WaitID.String(), right.WaitID.String())
	})
}

func compareSnapshots(left, right ProcessSnapshot) int {
	if order := cmp.Compare(left.Relation().Depth(), right.Relation().Depth()); order != 0 {
		return order
	}
	return cmp.Compare(left.ProcessID().String(), right.ProcessID().String())
}

type treeSnapshotValidation struct {
	wire               treeSnapshotWire
	root               processSnapshotWire
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
	processes := make(map[ProcessID]processSnapshotWire, len(wire.ProcessSnapshots))
	childrenByParent := make(map[ProcessID][]ProcessID)
	for _, snapshot := range wire.ProcessSnapshots {
		if !snapshot.Valid() {
			return nil, fmt.Errorf("%w: Process: %w", ErrInvalidTreeSnapshot, ErrInvalidSnapshot)
		}
		// Validation only reads facts already owned by the immutable snapshot.
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
		!root.TreeLimits.MaxTreeProcesses.Allows(uint64(len(processes))) {
		return nil, fmt.Errorf("%w: invalid root or tree size", ErrInvalidTreeSnapshot)
	}
	return &treeSnapshotValidation{
		wire:               wire,
		root:               root,
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
		if relation.RootID() != t.wire.RootID || processWire.TreeLimits != t.root.TreeLimits {
			return fmt.Errorf("%w: Process belongs to another tree contract", ErrInvalidTreeSnapshot)
		}
		if id == t.wire.RootID {
			continue
		}
		parentID, child := relation.ParentID()
		key, keyed := relation.ChildKey()
		parent, parentExists := t.processes[parentID]
		if !child || !keyed || !parentExists {
			return fmt.Errorf("%w: child parent is absent", ErrInvalidTreeSnapshot)
		}
		parentRelation := mustProcessRelation(parentID, parent.Relation)
		identity := childIdentity{parent: parentID, key: key}
		if relation.Depth() != parentRelation.Depth()+1 ||
			processWire.Limits.MaxPendingSignals != parent.Limits.MaxPendingSignals || processWire.Limits.MaxSnapshotBytes != parent.Limits.MaxSnapshotBytes ||
			!parent.Capabilities.Allows(processWire.Capabilities) {
			return fmt.Errorf("%w: invalid child relation, capacity, or attenuation", ErrInvalidTreeSnapshot)
		}
		if _, duplicate := t.children[identity]; duplicate {
			return fmt.Errorf("%w: duplicate parent-scoped ChildKey", ErrInvalidTreeSnapshot)
		}
		t.children[identity] = id
		t.childCounts[parentID]++
		if !processWire.Status.Terminal() {
			t.activeChildCounts[parentID]++
		}
		debit, ok := parent.Limits.Budget.allocation(processWire.Limits.Budget)
		if !ok {
			return fmt.Errorf("%w: child grant exceeds parent authority", ErrInvalidTreeSnapshot)
		}
		allocated, ok := t.allocatedResources[parentID].add(debit)
		if !ok {
			return fmt.Errorf("%w: child budget overflow", ErrInvalidTreeSnapshot)
		}
		t.allocatedResources[parentID] = allocated
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildAccounting() error {
	for _, snapshot := range t.wire.ProcessSnapshots {
		id, processWire := snapshot.ProcessID(), snapshot.state
		if !processWire.TreeLimits.MaxChildren.Allows(t.childCounts[id]) ||
			t.activeChildCounts[id] > uint64(processWire.TreeLimits.MaxActiveChildren) ||
			t.allocatedResources[id] != processWire.AllocatedResources {
			return fmt.Errorf("%w: child limits or allocated resources disagree", ErrInvalidTreeSnapshot)
		}
	}
	return nil
}

type childWaitValidationFacts struct {
	record  waitRecordWire
	signals []signalRecordWire
}

func (t *treeSnapshotValidation) validateChildWaits() error {
	mailboxes := make(map[ProcessID]map[WaitID]*childWaitValidationFacts)
	for _, snapshot := range t.wire.ProcessSnapshots {
		mailbox := snapshot.state.Mailbox
		waits := make(map[WaitID]*childWaitValidationFacts)
		for _, record := range mailbox.Waits {
			if record.Kind == WaitKindChildren && !record.Closed {
				waits[record.WaitID] = &childWaitValidationFacts{record: record}
			}
		}
		if len(waits) == 0 {
			continue
		}
		for _, signal := range mailbox.Signals {
			if signal.WaitID != nil {
				if facts := waits[*signal.WaitID]; facts != nil {
					facts.signals = append(facts.signals, signal)
				}
			}
		}
		mailboxes[snapshot.ProcessID()] = waits
	}
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
		facts := mailboxes[encoded.ParentProcessID][encoded.WaitID]
		if facts == nil || facts.record.WaitKey != spec.Key {
			return fmt.Errorf("%w: child wait is absent from parent mailbox", ErrInvalidTreeSnapshot)
		}
		if err := t.validateChildWaitSignals(facts.signals, encoded.WaitID, spec); err != nil {
			return err
		}
		if err := spec.validateRelations(encoded.ParentProcessID, t.processRelation); err != nil {
			return fmt.Errorf("%w: child wait: %w", ErrInvalidTreeSnapshot, err)
		}
		waitOwners[encoded.WaitID] = encoded.ParentProcessID
	}
	for _, snapshot := range t.wire.ProcessSnapshots {
		processWire := snapshot.state
		for _, wait := range processWire.Mailbox.Waits {
			if wait.Kind == WaitKindChildren && !wait.Closed {
				if waitOwners[wait.WaitID] != processWire.ProcessID {
					return fmt.Errorf("%w: active child wait registration does not belong to Process", ErrInvalidTreeSnapshot)
				}
			}
		}
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
			if !parent.Status.Terminal() && record.Effect.Target() == EffectTargetFramework {
				operation, err := decodeFrameworkOperation(record.Effect.Payload())
				if err != nil {
					return err
				}
				if wait, ok := operation.(childWaitOperation); ok {
					if err := wait.spec.validateRelations(parent.ProcessID, t.processRelation); err != nil {
						return fmt.Errorf("%w: prepared child wait: %w", ErrInvalidTreeSnapshot, err)
					}
				}
			}
			if record.Effect.Target() != EffectTargetFramework || !record.definitelySettled() {
				continue
			}
			operation, err := decodeFrameworkOperation(record.Effect.Payload())
			if err != nil {
				return err
			}
			if err := operation.validateTree(t, parent.ProcessID, record); err != nil {
				return fmt.Errorf("%w: framework settlement: %w", ErrInvalidTreeSnapshot, err)
			}

		}
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildStart(parentID ProcessID, record preparedEffect) error {
	result, err := decodeChildStartResult(record.Settlement.Payload())
	if err != nil {
		return err
	}
	childID, started := result.ProcessID()
	if !started {
		return nil
	}
	spec, err := decodeChildStartEffect(record.Effect.Payload())
	if err != nil {
		return err
	}
	digest, err := spec.digest()
	if err != nil {
		return err
	}
	child, exists := t.processes[childID]
	if !exists {
		return fmt.Errorf("%w: started child is missing", ErrInvalidChildStart)
	}
	if child.Relation.ParentID == nil || *child.Relation.ParentID != parentID {
		return fmt.Errorf("%w: child parent identity disagrees with start", ErrInvalidChildStart)
	}
	if child.Relation.ChildKey == nil || *child.Relation.ChildKey != spec.Key {
		return fmt.Errorf("%w: child key disagrees with start", ErrInvalidChildStart)
	}
	if child.DeploymentRef != spec.DeploymentRef {
		return fmt.Errorf("%w: child Deployment disagrees with start", ErrInvalidChildStart)
	}
	if child.Limits.Budget != spec.Budget {
		return fmt.Errorf("%w: child budget disagrees with start", ErrInvalidChildStart)
	}
	if !slices.Equal(child.Capabilities.Values(), spec.Capabilities.Values()) {
		return fmt.Errorf("%w: child capabilities disagree with start", ErrInvalidChildStart)
	}
	if child.ChildRequestDigest == nil || *child.ChildRequestDigest != digest {
		return fmt.Errorf("%w: child request digest disagrees with start", ErrInvalidChildStart)
	}
	return nil
}

func (t *treeSnapshotValidation) validateChildControl(parentID ProcessID, record preparedEffect) error {
	if err := record.validateFramework(); err != nil {
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
	for _, receipt := range child.Mailbox.receipts() {
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
		if record.OpensWait {
			if record.PayloadDigest != openedDigest {
				return fmt.Errorf("%w: child wait disagrees with its opening Signal", ErrInvalidTreeSnapshot)
			}
			continue
		}
		signal, signalErr := newSignal(record.ID, waitID, record.Payload)
		if signalErr != nil {
			return fmt.Errorf("%w: invalid child wait Signal: %w", ErrInvalidTreeSnapshot, signalErr)
		}
		satisfied, parseErr := ParseChildWaitSatisfied(signal)
		if parseErr != nil || record.ID != waitID.childWaitSignalID() ||
			satisfied.Key() != spec.Key || satisfied.Boundary() != spec.Boundary {
			return fmt.Errorf("%w: child wait satisfaction disagrees with its registration", ErrInvalidTreeSnapshot)
		}
		if uint32(len(satisfied.outcomes)) < spec.required() {
			return fmt.Errorf("%w: child wait condition is unsatisfied", ErrInvalidTreeSnapshot)
		}
		index := 0
		for _, outcome := range satisfied.outcomes {
			for index < len(spec.Children) && spec.Children[index] != outcome.result.ProcessID() {
				index++
			}
			if index == len(spec.Children) || !t.matchesChildWaitOutcome(outcome, spec.Boundary) {
				return fmt.Errorf("%w: child wait outcome disagrees with its tree", ErrInvalidTreeSnapshot)
			}
			index++
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
		Output: child.Output, Termination: *child.Termination, Usage: child.usage(),
	}
	expectedJSON, expectedErr := json.Marshal(expected)
	actualJSON, actualErr := json.Marshal(outcome.result.wire())
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

// treeFreeze identifies the active snapshot barrier. Only CaptureTree receives
// it, and releasing it lets the same tree owner resume scheduling.
type treeFreeze struct {
	runtime *treeRuntime
}

func (t *treeFreeze) release() error {
	response := make(chan error, 1)
	select {
	case t.runtime.freezeCommands <- treeCommand{
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
