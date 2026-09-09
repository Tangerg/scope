package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrDurabilityConflict      = errors.New("agent: durability boundary conflicts with committed content")
	ErrTreeIncarnationConflict = errors.New("agent: tree incarnation conflict")
	ErrTreeDurabilityMismatch  = errors.New("agent: tree durability mode mismatch")
	ErrTreeCaptureUnavailable  = errors.New("agent: tree capture is unavailable in durable mode")
)

// EffectBoundaryKind is closed because recovery needs a defined continuation
// for every acknowledged external Effect boundary.
type EffectBoundaryKind string

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

// EffectBoundary binds an external Effect fact to its prospective tree so a
// Host cannot acknowledge dispatch or settlement independently of recovery state.
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

func (e EffectBoundary) Request() EffectRequest { return e.request.clone() }

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

// TreeCheckpointKind distinguishes absent-head creation from writer-fenced
// updates, and stable owner cuts from fully parked or terminal trees.
type TreeCheckpointKind string

const (
	TreeCheckpointInvalid  TreeCheckpointKind = ""
	TreeCheckpointStart    TreeCheckpointKind = "start"
	TreeCheckpointChild    TreeCheckpointKind = "child"
	TreeCheckpointInput    TreeCheckpointKind = "input"
	TreeCheckpointProgress TreeCheckpointKind = "progress"
	TreeCheckpointParked   TreeCheckpointKind = "parked"
	TreeCheckpointTerminal TreeCheckpointKind = "terminal"
)

func (t TreeCheckpointKind) Valid() bool {
	switch t {
	case TreeCheckpointStart, TreeCheckpointChild, TreeCheckpointInput, TreeCheckpointProgress, TreeCheckpointParked, TreeCheckpointTerminal:
		return true
	default:
		return false
	}
}

func (t TreeCheckpointKind) String() string {
	if !t.Valid() {
		return invalidEnumName
	}
	return string(t)
}

// TreeCheckpoint keeps child publication, input acceptance, and execution
// progress on the same head so recovery cannot observe partially accepted work.
// Input, child, and progress cuts can coexist with sibling jobs because those jobs expose
// only committed Execution state or already recorded Effect intent.
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
	if !t.kind.Valid() || !t.treeSnapshot.Valid() {
		return false
	}
	if t.kind == TreeCheckpointStart {
		if t.previousTreeDigest != (Digest{}) {
			return false
		}
	} else if !t.previousTreeDigest.Valid() || t.previousTreeDigest == t.treeSnapshot.Digest() {
		return false
	}
	_, durable := t.treeSnapshot.IncarnationID()
	return durable && t.matchesSafeCut()
}

func (t TreeCheckpoint) matchesSafeCut() bool {
	if t.kind == TreeCheckpointStart {
		snapshots := t.treeSnapshot.ProcessSnapshots()
		return len(snapshots) == 1 && snapshots[0].Status() == StatusRunning
	}
	if t.kind == TreeCheckpointInput || t.kind == TreeCheckpointChild {
		return true
	}
	allTerminal := true
	parked := true
	for _, snapshot := range t.treeSnapshot.ProcessSnapshots() {
		if snapshot.Status().Terminal() {
			continue
		}
		allTerminal = false
		if snapshot.Status() == StatusWaiting || snapshot.Status() == StatusPaused {
			continue
		}
		wire, err := snapshot.wire()
		if err != nil {
			return false
		}
		if wire.Prepared == nil || len(wire.Prepared.Effects.unknownEffectIDs()) == 0 {
			parked = false
		}
	}
	return t.kind == TreeCheckpointTerminal && allTerminal ||
		t.kind == TreeCheckpointParked && !allTerminal && parked ||
		t.kind == TreeCheckpointProgress && !parked
}

// TreeActivation changes writer identity and recovery state together so the
// previous Engine cannot continue committing after restoration takes ownership.
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

// TreeDurability keeps all recoverable state on one authoritative head so a
// restored writer cannot race its predecessor. Every commit must atomically
// compare and advance that head; accepting a duplicate requires identical
// content and a head that still matches the proposal. Hosts own storage,
// deadlines, and reconciliation when a commit response is lost.
//
// A start checkpoint requires an absent head and a zero PreviousTreeDigest.
// Other checkpoints and Effects require the current incarnation and digest.
// Activation must replace both together to fence the previous writer before
// restoration can publish a Process. Initialization acknowledgment is separate
// because an aborted root has no execution tree to persist.
type TreeDurability interface {
	// ActivateTree must fence the previous writer before restored work can run.
	ActivateTree(ctx context.Context, activation TreeActivation) error
	// CommitEffect must keep the Effect fact and tree head atomic so recovery
	// cannot disagree with the dispatch or settlement that was acknowledged.
	CommitEffect(ctx context.Context, boundary EffectBoundary) error
	// CommitCheckpoint must compare an absent head for start, or the current
	// head otherwise, to prevent publication from overwriting another writer.
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
