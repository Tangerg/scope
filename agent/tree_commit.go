package agent

import (
	"context"
	"errors"
)

var (
	ErrCommitConflict          = errors.New("agent: committer boundary conflicts with committed content")
	ErrTreeIncarnationConflict = errors.New("agent: tree incarnation conflict")
)

// EffectBoundaryKind is closed because recovery needs a defined continuation
// for every acknowledged external Effect boundary.
type EffectBoundaryKind string

const (
	EffectBoundaryKindInvalid  EffectBoundaryKind = ""
	EffectBoundaryKindPending  EffectBoundaryKind = "pending"
	EffectBoundaryKindSettled  EffectBoundaryKind = "settled"
	EffectBoundaryKindResolved EffectBoundaryKind = "resolved"
)

func (e EffectBoundaryKind) Valid() bool {
	switch e {
	case EffectBoundaryKindPending, EffectBoundaryKindSettled, EffectBoundaryKindResolved:
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

// EffectBoundary binds an Effect fact to its prospective tree so a Host cannot
// acknowledge dispatch or settlement independently of recovery state. External
// dispatch uses a pending permission followed by settlement. Tree-local child
// controls settle directly, atomically with the recipient's mailbox or intent;
// they perform no external I/O requiring a pending dispatch permission.
type EffectBoundary struct {
	sequence           uint64
	kind               EffectBoundaryKind
	request            EffectRequest
	settlement         Settlement
	previousTreeDigest Digest
	treeSnapshot       TreeSnapshot
}

func newEffectBoundary(
	sequence uint64,
	kind EffectBoundaryKind,
	request EffectRequest,
	settlement Settlement,
	previousTreeDigest Digest,
	treeSnapshot TreeSnapshot,
) (EffectBoundary, error) {
	boundary := EffectBoundary{
		sequence: sequence,
		kind:     kind, request: request, settlement: settlement,
		previousTreeDigest: previousTreeDigest,
		treeSnapshot:       treeSnapshot,
	}
	if !boundary.Valid() {
		return EffectBoundary{}, errors.New("invalid durable Effect boundary")
	}
	return boundary, nil
}

// Sequence identifies this commit within its tree incarnation. Retries retain it.
func (e EffectBoundary) Sequence() uint64 { return e.sequence }

func (e EffectBoundary) Kind() EffectBoundaryKind { return e.kind }

func (e EffectBoundary) Request() EffectRequest { return e.request.clone() }

func (e EffectBoundary) Settlement() (Settlement, bool) {
	return e.settlement.clone(), e.kind == EffectBoundaryKindSettled || e.kind == EffectBoundaryKindResolved
}

func (e EffectBoundary) PreviousTreeDigest() Digest { return e.previousTreeDigest }

func (e EffectBoundary) TreeSnapshot() TreeSnapshot { return e.treeSnapshot }

func (e EffectBoundary) Valid() bool {
	if e.sequence == 0 || !e.kind.Valid() || !e.request.Valid() || !e.previousTreeDigest.Valid() ||
		!e.treeSnapshot.Valid() || e.previousTreeDigest == e.treeSnapshot.Digest() ||
		e.treeSnapshot.RootID() != e.request.Relation().RootID() {
		return false
	}
	incarnationID := e.treeSnapshot.IncarnationID()
	if !incarnationID.Valid() || e.request.incarnationID != incarnationID {
		return false
	}
	if !e.matchesProspectiveTree() {
		return false
	}
	switch e.kind {
	case EffectBoundaryKindPending:
		return !e.settlement.Valid()
	case EffectBoundaryKindSettled:
		return e.settlement.Valid() &&
			e.settlement.EffectID() == e.request.ID()
	case EffectBoundaryKindResolved:
		return e.settlement.Valid() &&
			e.settlement.Status() != SettlementStatusUnknown &&
			e.settlement.EffectID() == e.request.ID()
	default:
		return false
	}
}

func (e EffectBoundary) matchesProspectiveTree() bool {
	var processSnapshot ProcessSnapshot
	for _, candidate := range e.treeSnapshot.state.ProcessSnapshots {
		if candidate.ProcessID() == e.request.ProcessID() {
			processSnapshot = candidate
			break
		}
	}
	if !processSnapshot.Valid() || processSnapshot.DeploymentRef() != e.request.DeploymentRef() ||
		processSnapshot.Relation() != e.request.Relation() {
		return false
	}
	wire := processSnapshot.state
	if wire.Prepared == nil ||
		wire.Prepared.StepSequence != e.request.StepSequence() ||
		uint64(e.request.BatchIndex()) >= uint64(len(wire.Prepared.Effects)) {
		return false
	}
	record := wire.Prepared.Effects[e.request.BatchIndex()]
	if record.ID != e.request.ID() || !record.Effect.equal(e.request.effect) {
		return false
	}
	if e.kind == EffectBoundaryKindPending {
		return record.Phase == effectPhasePending && record.Settlement == nil
	}
	return record.Phase == effectPhaseSettled && record.Settlement != nil &&
		record.Settlement.equal(e.settlement)
}

// TreeCheckpointKind distinguishes absent-head creation from writer-fenced
// updates and classifies the saved recovery cut, not live runtime work. Parked
// means every nonterminal snapshot is waiting, paused, or blocked on an Unknown
// settlement. A requested replay may be running against that unchanged cut;
// TreeInspection reports that live work separately.
type TreeCheckpointKind string

const (
	TreeCheckpointKindInvalid    TreeCheckpointKind = ""
	TreeCheckpointKindStart      TreeCheckpointKind = "start"
	TreeCheckpointKindChildStart TreeCheckpointKind = "child_start"
	TreeCheckpointKindSignals    TreeCheckpointKind = "signals"
	TreeCheckpointKindProgress   TreeCheckpointKind = "progress"
	TreeCheckpointKindParked     TreeCheckpointKind = "parked"
	TreeCheckpointKindTerminal   TreeCheckpointKind = "terminal"
)

func (t TreeCheckpointKind) Valid() bool {
	switch t {
	case TreeCheckpointKindStart, TreeCheckpointKindChildStart, TreeCheckpointKindSignals, TreeCheckpointKindProgress, TreeCheckpointKindParked, TreeCheckpointKindTerminal:
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
	sequence           uint64
	kind               TreeCheckpointKind
	previousTreeDigest Digest
	treeSnapshot       TreeSnapshot
}

func newTreeCheckpoint(
	sequence uint64,
	kind TreeCheckpointKind,
	previousTreeDigest Digest,
	treeSnapshot TreeSnapshot,
) (TreeCheckpoint, error) {
	checkpoint := TreeCheckpoint{
		sequence: sequence,
		kind:     kind, previousTreeDigest: previousTreeDigest, treeSnapshot: treeSnapshot,
	}
	if !checkpoint.Valid() {
		return TreeCheckpoint{}, errors.New("invalid durable tree checkpoint")
	}
	return checkpoint, nil
}

// Sequence identifies this commit within its tree incarnation. Retries retain it.
func (t TreeCheckpoint) Sequence() uint64 { return t.sequence }

func (t TreeCheckpoint) Kind() TreeCheckpointKind { return t.kind }

func (t TreeCheckpoint) PreviousTreeDigest() Digest { return t.previousTreeDigest }

func (t TreeCheckpoint) TreeSnapshot() TreeSnapshot { return t.treeSnapshot }

func (t TreeCheckpoint) Valid() bool {
	if t.sequence == 0 || !t.kind.Valid() || !t.treeSnapshot.Valid() {
		return false
	}
	if t.kind == TreeCheckpointKindStart {
		if t.sequence != 1 || t.previousTreeDigest != (Digest{}) {
			return false
		}
	} else if !t.previousTreeDigest.Valid() || t.previousTreeDigest == t.treeSnapshot.Digest() {
		return false
	}
	return t.matchesSafeCut()
}

func (t TreeCheckpoint) matchesSafeCut() bool {
	if t.kind == TreeCheckpointKindStart {
		snapshots := t.treeSnapshot.state.ProcessSnapshots
		return len(snapshots) == 1 && snapshots[0].Status() == StatusRunning
	}
	if t.kind == TreeCheckpointKindSignals || t.kind == TreeCheckpointKindChildStart {
		return true
	}
	allTerminal := true
	parked := true
	for _, snapshot := range t.treeSnapshot.state.ProcessSnapshots {
		if snapshot.Status().Terminal() {
			continue
		}
		allTerminal = false
		if snapshot.Status() == StatusWaiting || snapshot.Status() == StatusPaused {
			continue
		}
		wire := snapshot.state
		if wire.Prepared == nil || len(wire.Prepared.Effects.unknownEffectIDs()) == 0 {
			parked = false
		}
	}
	return t.kind == TreeCheckpointKindTerminal && allTerminal ||
		t.kind == TreeCheckpointKindParked && !allTerminal && parked ||
		t.kind == TreeCheckpointKindProgress && !parked
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
	return t.treeSnapshot.IncarnationID() == t.incarnationID
}

// TreeCommitter keeps all recoverable state on one authoritative head so a
// restored writer cannot race its predecessor. Every commit must atomically
// compare and advance that head; accepting a duplicate requires identical
// content and a head that still matches the proposal and its sequence. Hosts own
// storage, deadlines, and reconciliation when a commit response is lost. The supplied
// context retains Host values but removes cancellation and deadlines. Hosts must
// apply an independent bounded storage deadline and a host-owned shutdown signal.
// Canceling an Await, Join or ReleaseTree wait does not abort a transaction. Hosts
// release blocked storage through that shutdown signal, return its error, and then
// Join the stopped runtime. A timeout does not prove that the authoritative head
// was unchanged and requires reconciliation.
//
// Acknowledgment guarantees installation under this protocol. Survival across
// store closure or process restart depends on the chosen backend; the Engine
// never changes its publication or recovery rules for volatile storage.
//
// A start checkpoint requires an absent head and a zero PreviousTreeDigest.
// The Engine assigns one consecutive Sequence shared by checkpoints and Effects
// within each incarnation. Start installs sequence 1; activation installs sequence
// 0 under the new incarnation. Every subsequent commit requires the current
// incarnation, previous digest, and exactly the next sequence. Hosts atomically
// retain the sequence with the head and deduplication facts, separately from the
// portable snapshot and backend revisions. An identical retry succeeds only while
// its sequence is still current; historical replay must fail even if content cycles.
// A checkpoint is identified by root, incarnation, and sequence, never by digest.
// Activation replaces writer and head and resets the sequence atomically before
// restoration can publish a Process. Initialization acknowledgment is separate
// because failed root initialization has no execution tree to persist.
type TreeCommitter interface {
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
	committer TreeCommitter,
	activation TreeActivation,
) (err error) {
	if committer == nil || !activation.Valid() {
		return errors.New("invalid durable tree activation")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = callbackPanic("TreeCommitter.ActivateTree", recovered)
		}
	}()
	defer func() { err = sealCallbackError(err) }()
	return committer.ActivateTree(context.WithoutCancel(RequireContext(ctx)), activation)
}

func commitEffectBoundary(
	ctx context.Context,
	committer TreeCommitter,
	boundary EffectBoundary,
) (err error) {
	// Construction already checked the complete immutable boundary. This call
	// only crosses the Host I/O boundary, so it need not repeat tree matching.
	if committer == nil || !boundary.kind.Valid() {
		return errors.New("invalid durable Effect boundary")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = callbackPanic("TreeCommitter.CommitEffect", recovered)
		}
	}()
	defer func() { err = sealCallbackError(err) }()
	return committer.CommitEffect(context.WithoutCancel(RequireContext(ctx)), boundary)
}

func commitTreeCheckpoint(
	ctx context.Context,
	committer TreeCommitter,
	checkpoint TreeCheckpoint,
) (err error) {
	if committer == nil || !checkpoint.kind.Valid() {
		return errors.New("invalid durable tree checkpoint")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = callbackPanic("TreeCommitter.CommitCheckpoint", recovered)
		}
	}()
	defer func() { err = sealCallbackError(err) }()
	return committer.CommitCheckpoint(
		context.WithoutCancel(RequireContext(ctx)), checkpoint,
	)
}
