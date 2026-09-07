package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	// ErrDurabilityConflict reports that an idempotency key was previously
	// committed with different content.
	ErrDurabilityConflict = errors.New("agent: durability boundary conflicts with committed content")
	// ErrTreeIncarnationConflict reports that a writer no longer owns the
	// authoritative tree head it attempted to advance.
	ErrTreeIncarnationConflict = errors.New("agent: tree incarnation conflict")
	// ErrTreeDurabilityMismatch reports an attempt to restore a durable tree in
	// an ephemeral Engine, or an ephemeral tree in a durable Engine.
	ErrTreeDurabilityMismatch = errors.New("agent: tree durability mode mismatch")
	// ErrTreeCaptureUnavailable reports that a durable tree cannot be captured
	// through the ephemeral, caller-driven checkpoint API.
	ErrTreeCaptureUnavailable = errors.New("agent: tree capture is unavailable in durable mode")
)

// EffectBoundaryKind identifies one monotonic durable transition of an
// external Effect. The zero value is invalid.
type EffectBoundaryKind string

// These boundaries name the exact points at which durable state is committed.
// They are a closed vocabulary because recovery reasons about them directly: a
// boundary the kernel cannot name is one it cannot resume from.
const (
	EffectBoundaryInvalid  EffectBoundaryKind = ""
	EffectBoundaryPending  EffectBoundaryKind = "pending"
	EffectBoundarySettled  EffectBoundaryKind = "settled"
	EffectBoundaryResolved EffectBoundaryKind = "resolved"
)

func (e EffectBoundaryKind) Valid() bool {
	switch e {
	case EffectBoundaryPending, EffectBoundarySettled, EffectBoundaryResolved:
		return true
	default:
		return false
	}
}

func (e EffectBoundaryKind) String() string {
	if !e.Valid() {
		return invalidEnumName
	}
	return string(e)
}

// EffectBoundary is an immutable proposal to atomically advance one tree head
// together with one external Effect fact. Values are minted by Engine only.
type EffectBoundary struct {
	kind               EffectBoundaryKind
	request            EffectRequest
	settlement         Settlement
	previousTreeDigest Digest
	treeSnapshot       TreeSnapshot
}

func newEffectBoundary(
	kind EffectBoundaryKind,
	request EffectRequest,
	settlement Settlement,
	previousTreeDigest Digest,
	treeSnapshot TreeSnapshot,
) (EffectBoundary, error) {
	boundary := EffectBoundary{
		kind: kind, request: request, settlement: settlement,
		previousTreeDigest: previousTreeDigest,
		treeSnapshot:       treeSnapshot,
	}
	if !boundary.Valid() {
		return EffectBoundary{}, errors.New("invalid durable Effect boundary")
	}
	return boundary, nil
}

func (e EffectBoundary) Kind() EffectBoundaryKind { return e.kind }

// Request returns the exact immutable dispatch request represented by this
// boundary.
func (e EffectBoundary) Request() EffectRequest { return e.request.clone() }

// Settlement returns the settlement introduced by a settled or resolved
// boundary. Pending boundaries return false.
func (e EffectBoundary) Settlement() (Settlement, bool) {
	return e.settlement.clone(), e.kind == EffectBoundarySettled || e.kind == EffectBoundaryResolved
}

func (e EffectBoundary) PreviousTreeDigest() Digest { return e.previousTreeDigest }

func (e EffectBoundary) TreeSnapshot() TreeSnapshot { return e.treeSnapshot }

func (e EffectBoundary) Valid() bool {
	if !e.kind.Valid() || !e.request.Valid() || !e.previousTreeDigest.Valid() ||
		!e.treeSnapshot.Valid() || e.previousTreeDigest == e.treeSnapshot.Digest() ||
		e.treeSnapshot.RootID() != e.request.Relation().RootID() {
		return false
	}
	incarnationID, durable := e.treeSnapshot.IncarnationID()
	if !durable || e.request.incarnationID != incarnationID {
		return false
	}
	if !e.matchesProspectiveTree() {
		return false
	}
	switch e.kind {
	case EffectBoundaryPending:
		return !e.settlement.Valid()
	case EffectBoundarySettled:
		return e.settlement.Valid() &&
			e.settlement.EffectID() == e.request.ID()
	case EffectBoundaryResolved:
		return e.settlement.Valid() &&
			e.settlement.Status() != SettlementStatusUnknown &&
			e.settlement.EffectID() == e.request.ID()
	default:
		return false
	}
}

func (e EffectBoundary) matchesProspectiveTree() bool {
	var processSnapshot ProcessSnapshot
	for _, candidate := range e.treeSnapshot.ProcessSnapshots() {
		if candidate.ProcessID() == e.request.ProcessID() {
			processSnapshot = candidate
			break
		}
	}
	if !processSnapshot.Valid() || processSnapshot.DeploymentRef() != e.request.DeploymentRef() ||
		processSnapshot.Relation() != e.request.Relation() {
		return false
	}
	wire, err := processSnapshot.wire()
	if err != nil || wire.Prepared == nil ||
		wire.Prepared.StepSequence != e.request.StepSequence() ||
		uint64(e.request.BatchIndex()) >= uint64(len(wire.Prepared.Effects)) {
		return false
	}
	record := wire.Prepared.Effects[e.request.BatchIndex()]
	if record.ID != e.request.ID() || !sameBoundaryEffect(record.Effect, e.request.Effect()) {
		return false
	}
	if e.kind == EffectBoundaryPending {
		return record.Phase == effectPhasePending && record.Settlement == nil
	}
	return record.Phase == effectPhaseSettled && record.Settlement != nil &&
		sameBoundarySettlement(*record.Settlement, e.settlement)
}

func sameBoundaryEffect(left, right Effect) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func sameBoundarySettlement(left, right Settlement) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// TreeCheckpointKind identifies a Runtime-owned durable program-counter cut.
// The zero value is invalid.
type TreeCheckpointKind string

// These boundaries name the exact points at which durable state is committed.
// They are a closed vocabulary because recovery reasons about them directly: a
// boundary the kernel cannot name is one it cannot resume from.
const (
	TreeCheckpointInvalid TreeCheckpointKind = ""
	// TreeCheckpointInput persists an admitted external Signal batch before
	// acknowledging delivery. Sibling jobs may still be computing or dispatching;
	// their snapshot contains only last-stable state or recorded Effect intent.
	TreeCheckpointInput    TreeCheckpointKind = "input"
	TreeCheckpointParked   TreeCheckpointKind = "parked"
	TreeCheckpointTerminal TreeCheckpointKind = "terminal"
)

func (t TreeCheckpointKind) Valid() bool {
	return t == TreeCheckpointInput || t == TreeCheckpointParked || t == TreeCheckpointTerminal
}

func (t TreeCheckpointKind) String() string {
	if !t.Valid() {
		return invalidEnumName
	}
	return string(t)
}

// TreeCheckpoint is an immutable Runtime-owned proposal to persist a safe
// whole-tree cut. Values are minted by Engine only.
type TreeCheckpoint struct {
	kind               TreeCheckpointKind
	previousTreeDigest Digest
	treeSnapshot       TreeSnapshot
}

func newTreeCheckpoint(
	kind TreeCheckpointKind,
	previousTreeDigest Digest,
	treeSnapshot TreeSnapshot,
) (TreeCheckpoint, error) {
	checkpoint := TreeCheckpoint{
		kind: kind, previousTreeDigest: previousTreeDigest, treeSnapshot: treeSnapshot,
	}
	if !checkpoint.Valid() {
		return TreeCheckpoint{}, errors.New("invalid durable tree checkpoint")
	}
	return checkpoint, nil
}

func (t TreeCheckpoint) Kind() TreeCheckpointKind { return t.kind }

func (t TreeCheckpoint) PreviousTreeDigest() Digest { return t.previousTreeDigest }

func (t TreeCheckpoint) TreeSnapshot() TreeSnapshot { return t.treeSnapshot }

func (t TreeCheckpoint) Valid() bool {
	if !t.kind.Valid() || !t.previousTreeDigest.Valid() || !t.treeSnapshot.Valid() ||
		t.previousTreeDigest == t.treeSnapshot.Digest() {
		return false
	}
	_, durable := t.treeSnapshot.IncarnationID()
	return durable && t.matchesSafeCut()
}

func (t TreeCheckpoint) matchesSafeCut() bool {
	if t.kind == TreeCheckpointInput {
		return true
	}
	allTerminal := true
	for _, snapshot := range t.treeSnapshot.ProcessSnapshots() {
		if snapshot.Status().Terminal() {
			continue
		}
		allTerminal = false
		if snapshot.Status() == StatusWaiting || snapshot.Status() == StatusPaused {
			continue
		}
		wire, err := snapshot.wire()
		if err != nil || wire.Prepared == nil {
			return false
		}
		unknown := false
		for _, effect := range wire.Prepared.Effects {
			unknown = unknown || effect.unknown()
		}
		if !unknown {
			return false
		}
	}
	return t.kind == TreeCheckpointTerminal && allTerminal ||
		t.kind == TreeCheckpointParked && !allTerminal
}

// TreeActivation transfers active-writer authority from one durable snapshot
// to a prospective snapshot with a freshly minted incarnation. Values are
// minted by Engine only.
type TreeActivation struct {
	previousIncarnationID TreeIncarnationID
	previousTreeDigest    Digest
	incarnationID         TreeIncarnationID
	treeSnapshot          TreeSnapshot
}

func newTreeActivation(
	previousIncarnationID TreeIncarnationID,
	previousTreeDigest Digest,
	incarnationID TreeIncarnationID,
	treeSnapshot TreeSnapshot,
) (TreeActivation, error) {
	activation := TreeActivation{
		previousIncarnationID: previousIncarnationID,
		previousTreeDigest:    previousTreeDigest,
		incarnationID:         incarnationID,
		treeSnapshot:          treeSnapshot,
	}
	if !activation.Valid() {
		return TreeActivation{}, errors.New("invalid durable tree activation")
	}
	return activation, nil
}

func (t TreeActivation) PreviousIncarnationID() TreeIncarnationID {
	return t.previousIncarnationID
}

func (t TreeActivation) PreviousTreeDigest() Digest { return t.previousTreeDigest }

func (t TreeActivation) IncarnationID() TreeIncarnationID { return t.incarnationID }

func (t TreeActivation) TreeSnapshot() TreeSnapshot { return t.treeSnapshot }

func (t TreeActivation) Valid() bool {
	if !t.previousIncarnationID.Valid() || !t.previousTreeDigest.Valid() ||
		!t.incarnationID.Valid() || t.previousIncarnationID == t.incarnationID ||
		!t.treeSnapshot.Valid() {
		return false
	}
	incarnationID, durable := t.treeSnapshot.IncarnationID()
	return durable && incarnationID == t.incarnationID
}

// TreeDurability is the complete Host port for active recovery. Every boundary
// that carries a prospective tree must atomically compare and advance the same
// authoritative head; root-aborted outcomes only close their admission fact.
// The implementation owns storage, transactions, product facts, deadlines,
// and ambiguous-commit reconciliation; Engine owns ordering and fencing.
type TreeDurability interface {
	ProcessStartOutcomeAcknowledger
	// ActivateTree atomically fences the previous incarnation and installs the
	// prospective snapshot before a restored tree is published.
	ActivateTree(ctx context.Context, activation TreeActivation) error
	// CommitEffect atomically records one Effect fact and advances the tree head.
	CommitEffect(ctx context.Context, boundary EffectBoundary) error
	// CommitCheckpoint atomically advances the tree head to a Runtime-owned safe cut.
	CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error
}

func activateTree(
	ctx context.Context,
	durability TreeDurability,
	activation TreeActivation,
) (err error) {
	if durability == nil || !activation.Valid() {
		return errors.New("invalid durable tree activation")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tree durability activation panicked: %v", recovered)
		}
	}()
	return durability.ActivateTree(context.WithoutCancel(requireContext(ctx)), activation)
}

func commitEffectBoundary(
	ctx context.Context,
	durability TreeDurability,
	boundary EffectBoundary,
) (err error) {
	if durability == nil || !boundary.Valid() {
		return errors.New("invalid durable Effect boundary")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tree durability Effect commit panicked: %v", recovered)
		}
	}()
	return durability.CommitEffect(context.WithoutCancel(requireContext(ctx)), boundary)
}

func commitTreeCheckpoint(
	ctx context.Context,
	durability TreeDurability,
	checkpoint TreeCheckpoint,
) (err error) {
	if durability == nil || !checkpoint.Valid() {
		return errors.New("invalid durable tree checkpoint")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tree durability checkpoint commit panicked: %v", recovered)
		}
	}()
	return durability.CommitCheckpoint(
		context.WithoutCancel(requireContext(ctx)), checkpoint,
	)
}
