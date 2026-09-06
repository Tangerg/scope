package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// Exported defaults keep constructor behavior visible and overridable.
const (
	maxTreeSnapshotBytes = 512 << 20
	// CurrentTreeSnapshotVersion preserves pending failures across restoration.
	// Earlier control wire contracts cannot represent that terminal intent.
	CurrentTreeSnapshotVersion TreeSnapshotVersion = 2
)

var (
	ErrInvalidTreeSnapshot            = errors.New("agent: invalid process tree snapshot")
	ErrUnsupportedTreeSnapshotVersion = errors.New("agent: unsupported process tree snapshot version")
)

// TreeSnapshotVersion identifies one exact durable wire contract.
type TreeSnapshotVersion uint16

// TreeSnapshot is an immutable, portable capture of one complete Process tree.
// It owns Framework execution facts, a canonical content digest, and the
// optional active-writer identity of durable state. Persistence, transactions,
// revisions, and cleanup policy remain Host responsibilities.
type TreeSnapshot struct {
	data           json.RawMessage
	version        TreeSnapshotVersion
	digest         Digest
	rootID         ProcessID
	incarnationID  TreeIncarnationID
	hasIncarnation bool
	processes      []ProcessSnapshot
}

// ParseTreeSnapshot validates one complete Process tree snapshot. A syntactically
// valid foreign version is classified before the current wire shape is enforced.
func ParseTreeSnapshot(data json.RawMessage) (TreeSnapshot, error) {
	if len(data) == 0 || len(data) > maxTreeSnapshotBytes {
		return TreeSnapshot{}, fmt.Errorf(
			"%w: JSON must contain at most %d bytes", ErrInvalidTreeSnapshot, maxTreeSnapshotBytes,
		)
	}
	version, err := parseTreeSnapshotVersion(data)
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: decode version: %w", ErrInvalidTreeSnapshot, err)
	}
	if versionErr := validateTreeSnapshotVersion(version); versionErr != nil {
		return TreeSnapshot{}, versionErr
	}
	wire, err := wireJSON.decode[treeSnapshotWire](data)
	if err != nil {
		return TreeSnapshot{}, fmt.Errorf("%w: decode: %w", ErrInvalidTreeSnapshot, err)
	}
	return treeSnapshotFromWire(wire)
}

func newTreeSnapshot(wire treeSnapshotWire) (TreeSnapshot, error) {
	owned := wire
	owned.ProcessSnapshots = slices.Clone(wire.ProcessSnapshots)
	owned.ChildWaits = slices.Clone(wire.ChildWaits)
	if wire.IncarnationID != nil {
		incarnationID := *wire.IncarnationID
		owned.IncarnationID = &incarnationID
	}
	return treeSnapshotFromWire(owned)
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
	incarnationID, hasIncarnation := treeSnapshotIncarnation(wire.IncarnationID)
	return TreeSnapshot{
		data: normalized, digest: ComputeDigest(normalized), version: wire.Version, rootID: wire.RootID,
		incarnationID: incarnationID, hasIncarnation: hasIncarnation,
		processes: slices.Clone(wire.ProcessSnapshots),
	}, nil
}

// JSON returns an independently owned tree snapshot representation.
func (t TreeSnapshot) JSON() json.RawMessage { return bytes.Clone(t.data) }

// Version lets a Host route explicit migration before asking Engine to restore.
func (t TreeSnapshot) Version() TreeSnapshotVersion { return t.version }

// RootID returns the identity of the tree's root Process.
func (t TreeSnapshot) RootID() ProcessID { return t.rootID }

// Digest returns the canonical content identity of this complete tree state.
func (t TreeSnapshot) Digest() Digest { return t.digest }

// IncarnationID returns the active writer identity carried by a durable tree.
// Ephemeral snapshots return false.
func (t TreeSnapshot) IncarnationID() (TreeIncarnationID, bool) {
	return t.incarnationID, t.hasIncarnation
}

// ProcessSnapshots returns immutable captures ordered by depth and ProcessID.
func (t TreeSnapshot) ProcessSnapshots() []ProcessSnapshot {
	return slices.Clone(t.processes)
}

func (t TreeSnapshot) Valid() bool {
	return len(t.data) > 0 && t.version == CurrentTreeSnapshotVersion && t.digest.Valid() && t.rootID.Valid() &&
		(!t.hasIncarnation || t.incarnationID.Valid()) && len(t.processes) > 0
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
	wire, err := wireJSON.decode[treeSnapshotWire](t.data)
	if err != nil {
		return treeSnapshotWire{}, fmt.Errorf("%w: decode: %w", ErrInvalidTreeSnapshot, err)
	}
	return wire, validateTreeSnapshot(wire)
}

type childWaitSnapshotWire struct {
	ParentProcessID ProcessID         `json:"parent_process_id"`
	WaitID          WaitID            `json:"wait_id"`
	Spec            childWaitSpecWire `json:"spec"`
}

type treeSnapshotEnvelope struct {
	Version TreeSnapshotVersion `json:"version"`
}

type treeSnapshotWire struct {
	Version          TreeSnapshotVersion     `json:"version"`
	RootID           ProcessID               `json:"root_id"`
	IncarnationID    *TreeIncarnationID      `json:"incarnation_id,omitempty"`
	ProcessSnapshots []ProcessSnapshot       `json:"process_snapshots"`
	ChildWaits       []childWaitSnapshotWire `json:"child_waits,omitempty"`
}

func parseTreeSnapshotVersion(data json.RawMessage) (TreeSnapshotVersion, error) {
	var envelope treeSnapshotEnvelope
	if err := jsonv2.Unmarshal(data, &envelope); err != nil {
		return 0, err
	}
	return envelope.Version, nil
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
	if err := validateTreeSnapshotVersion(wire.Version); err != nil {
		return err
	}
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
	return validation.validateChildWaits()
}

func validateTreeSnapshotVersion(version TreeSnapshotVersion) error {
	if version == CurrentTreeSnapshotVersion {
		return nil
	}
	return fmt.Errorf(
		"%w: got %d, want %d",
		ErrUnsupportedTreeSnapshotVersion, version, CurrentTreeSnapshotVersion,
	)
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
		processWire, err := snapshot.wire()
		if err != nil {
			return nil, fmt.Errorf("%w: Process: %w", ErrInvalidTreeSnapshot, err)
		}
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
			processWire.StartedAt.Before(parent.StartedAt) || !parent.Capabilities.Allows(processWire.Capabilities) {
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
	waits := make(map[WaitID]struct{}, len(t.wire.ChildWaits))
	for _, encoded := range t.wire.ChildWaits {
		parent, exists := t.processes[encoded.ParentProcessID]
		spec, err := encoded.Spec.value()
		if !exists || err != nil || !encoded.WaitID.Valid() || parent.Status.Terminal() {
			return fmt.Errorf("%w: invalid child wait", ErrInvalidTreeSnapshot)
		}
		if _, duplicate := waits[encoded.WaitID]; duplicate {
			return fmt.Errorf("%w: duplicate child WaitID", ErrInvalidTreeSnapshot)
		}
		waitRecord, exists := findWaitRecord(parent.Mailbox, encoded.WaitID)
		if !exists || waitRecord.ExternallyAddressable || waitRecord.Closed || waitRecord.WaitKey != spec.Key {
			return fmt.Errorf("%w: child wait is absent from parent mailbox", ErrInvalidTreeSnapshot)
		}
		for _, childID := range spec.Children {
			child, exists := t.processes[childID]
			relation, _ := processRelationFromWire(childID, child.Relation)
			parentID, isChild := relation.ParentID()
			if !exists || !isChild || parentID != encoded.ParentProcessID {
				return fmt.Errorf("%w: wait references a non-direct child", ErrInvalidTreeSnapshot)
			}
		}
		waits[encoded.WaitID] = struct{}{}
	}
	for _, processWire := range t.processes {
		for _, wait := range processWire.Mailbox.Waits {
			if !wait.ExternallyAddressable && !wait.Closed {
				if _, exists := waits[wait.WaitID]; !exists {
					return fmt.Errorf("%w: active child wait registration is missing", ErrInvalidTreeSnapshot)
				}
			}
		}
	}
	return nil
}

func findWaitRecord(mailbox mailboxWire, id WaitID) (waitRecordWire, bool) {
	for _, record := range mailbox.Waits {
		if record.WaitID == id {
			return record, true
		}
	}
	return waitRecordWire{}, false
}

// CaptureTree quiesces one complete Engine-owned tree at Strategy-safe
// boundaries and captures a consistent portable cut. In-flight Effects settle
// according to their existing contract before a Process joins the barrier.
func (e *Engine) CaptureTree(ctx context.Context, rootID ProcessID) (TreeSnapshot, error) {
	if e == nil || !rootID.Valid() {
		return TreeSnapshot{}, ErrInvalidProcessRelation
	}
	if e.durability != nil {
		return TreeSnapshot{}, ErrTreeCaptureUnavailable
	}
	ctx = requireContext(ctx)
	source, err := e.quiesceOwnedTree(ctx, rootID, treeFreezeModeSnapshot)
	if err != nil {
		return TreeSnapshot{}, err
	}
	defer source.release()
	return source.snapshot, nil
}

// treeFreeze is an unforgeable, one-shot authority over a tree-level safe
// boundary. State remains private to treeRuntime; this value can only ask that
// owner to apply an exact projection or resume the source tree.
type treeFreeze struct {
	runtime  *treeRuntime
	mu       sync.Mutex
	resolved bool
}

func (t *treeFreeze) release() error {
	return t.resolve(treeCommandReleaseFreeze, nil)
}

func (t *treeFreeze) apply(projection *treeStateProjection) error {
	return t.resolve(treeCommandApplyFreeze, projection)
}

func (t *treeFreeze) resolve(kind treeCommandKind, projection *treeStateProjection) error {
	if t == nil || t.runtime == nil {
		return ErrEngineQuiescenceUnavailable
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolved {
		return nil
	}
	response := make(chan error, 1)
	select {
	case t.runtime.controls <- treeCommand{
		kind: kind, freeze: t, projection: projection, response: response,
	}:
	case <-t.runtime.done:
		return ErrEngineQuiescenceUnavailable
	}
	select {
	case err := <-response:
		if err == nil {
			t.resolved = true
		}
		return err
	case <-t.runtime.done:
		return ErrEngineQuiescenceUnavailable
	}
}

// quiescedTree owns the root-scoped operation and its Strategy-safe barrier as
// one private capability. Releasing it is idempotent and always releases the
// barrier before admitting the next operation on the same tree.
type quiescedTree struct {
	operation        *treeOperation
	freeze           *treeFreeze
	snapshot         TreeSnapshot
	acknowledgedHead Digest
	releaseOnce      sync.Once
}

func (e *Engine) quiesceOwnedTree(
	ctx context.Context,
	rootID ProcessID,
	mode treeFreezeMode,
) (*quiescedTree, error) {
	operation, err := e.acquireTreeOperation(ctx, rootID)
	if err != nil {
		return nil, err
	}
	runtime, err := e.runtimeForTree(rootID)
	if err != nil {
		operation.release()
		return nil, err
	}
	freeze, snapshot, err := runtime.acquireTreeFreeze(ctx, mode)
	if err != nil {
		operation.release()
		return nil, err
	}
	return &quiescedTree{
		operation: operation, freeze: freeze, snapshot: snapshot,
		acknowledgedHead: runtime.headDigest,
	}, nil
}

func (q *quiescedTree) release() {
	if q == nil {
		return
	}
	q.releaseOnce.Do(func() {
		_ = q.freeze.release()
		q.operation.release()
	})
}

func (q *quiescedTree) valid(engine *Engine, rootID ProcessID) bool {
	return q != nil && q.operation != nil && q.freeze != nil && q.snapshot.Valid() &&
		q.operation.engine == engine && q.operation.rootID == rootID &&
		q.snapshot.RootID() == rootID
}

func (e *Engine) runtimeForTree(rootID ProcessID) (*treeRuntime, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	root := e.processes[rootID]
	runtime := e.trees[rootID]
	if root == nil || runtime == nil || !root.relation.IsRoot() ||
		root.relation.RootID() != rootID || root.runtime.Load() != runtime {
		return nil, ErrInvalidProcessRelation
	}
	return runtime, nil
}

func (t *treeRuntime) acquireTreeFreeze(
	ctx context.Context,
	mode treeFreezeMode,
) (*treeFreeze, TreeSnapshot, error) {
	ctx = requireContext(ctx)
	if freeze, snapshot, err, done := t.finishedTreeFreeze(); done {
		return freeze, snapshot, err
	}
	acquisition := &treeFreezeAcquisition{
		response: make(chan treeFreezeAcquisitionResult, 1),
		canceled: make(chan struct{}),
		mode:     mode,
	}
	select {
	case t.controls <- treeCommand{
		kind: treeCommandAcquireFreeze, acquisition: acquisition,
	}:
	case <-t.done:
		freeze, snapshot, err, _ := t.finishedTreeFreeze()
		return freeze, snapshot, err
	case <-ctx.Done():
		return nil, TreeSnapshot{}, ctx.Err()
	}
	select {
	case result := <-acquisition.response:
		return result.freeze, result.snapshot, result.err
	case <-t.done:
		freeze, snapshot, err, _ := t.finishedTreeFreeze()
		return freeze, snapshot, err
	case <-ctx.Done():
		close(acquisition.canceled)
		return nil, TreeSnapshot{}, ctx.Err()
	}
}

func (t *treeRuntime) finishedTreeFreeze() (*treeFreeze, TreeSnapshot, error, bool) {
	select {
	case <-t.done:
		snapshot, err := t.captureTree()
		if err != nil {
			return nil, TreeSnapshot{}, err, true
		}
		return &treeFreeze{runtime: t, resolved: true}, snapshot, nil, true
	default:
		return nil, TreeSnapshot{}, nil, false
	}
}
