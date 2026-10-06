package agent

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"strconv"
)

const treeCommitIdentityPrefix = "commit:"

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
//
// The prospective tree owns the Effect's request and settlement; the boundary
// names the Effect and adds only whether its settlement resolved an Unknown.
type EffectBoundary struct {
	sequence           uint64
	resolution         bool
	processID          ProcessID
	effectID           EffectID
	previousTreeDigest Digest
	treeSnapshot       TreeSnapshot
}

func newEffectBoundary(
	sequence uint64,
	resolution bool,
	processID ProcessID,
	effectID EffectID,
	previousTreeDigest Digest,
	treeSnapshot TreeSnapshot,
) (EffectBoundary, error) {
	boundary := EffectBoundary{
		sequence: sequence, resolution: resolution,
		processID: processID, effectID: effectID,
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

// Kind follows from the Effect's captured phase: a pending record grants
// dispatch, and a settled one is either its first settlement or a resolution.
func (e EffectBoundary) Kind() EffectBoundaryKind {
	_, record, found := e.treeSnapshot.effect(e.processID, e.effectID)
	switch {
	case !found:
		return EffectBoundaryKindInvalid
	case record.phase() == effectPhasePending && !e.resolution:
		return EffectBoundaryKindPending
	case record.phase() != effectPhaseSettled:
		return EffectBoundaryKindInvalid
	case !e.resolution:
		return EffectBoundaryKindSettled
	case record.unknown():
		return EffectBoundaryKindInvalid
	default:
		return EffectBoundaryKindResolved
	}
}

func (e EffectBoundary) Request() EffectRequest {
	request, _, _ := e.treeSnapshot.effect(e.processID, e.effectID)
	return request
}

func (e EffectBoundary) Settlement() (Settlement, bool) {
	_, record, found := e.treeSnapshot.effect(e.processID, e.effectID)
	if !found || record.settlement() == nil {
		return Settlement{}, false
	}
	return record.settlement().clone(), true
}

func (e EffectBoundary) PreviousTreeDigest() Digest { return e.previousTreeDigest }

func (e EffectBoundary) TreeSnapshot() TreeSnapshot { return e.treeSnapshot }

// Identity is the opaque storage key for this root, Effect, and boundary kind.
// It survives writer activation; attempts, incarnation, and sequence do not
// create a second permission or settlement for the same logical Effect stage.
// Invalid boundaries have an empty identity. Persist the returned key verbatim.
func (e EffectBoundary) Identity() string {
	if !e.Valid() {
		return ""
	}
	return deriveIdentity(treeCommitIdentityPrefix, "effect-boundary",
		e.treeSnapshot.RootID().String(), e.effectID.String(), e.Kind().String()).String()
}

// ContentDigest covers the complete Scope boundary: its predecessor, sequence,
// kind, Effect, and prospective recovery cut, whose digest covers the request
// and settlement. It excludes physical dispatch attempts and Host-owned
// business write sets. Equal identity and content do not authorize historical
// replay: the store must still atomically check the current writer, head, and
// sequence.
func (e EffectBoundary) ContentDigest() (Digest, error) {
	if !e.Valid() {
		return Digest{}, errors.New("agent: invalid Effect boundary")
	}
	encoded, err := jsonv2.Marshal(struct {
		Sequence  uint64
		Kind      EffectBoundaryKind
		ProcessID ProcessID
		EffectID  EffectID
		Previous  Digest
		Snapshot  Digest
	}{e.sequence, e.Kind(), e.processID, e.effectID, e.previousTreeDigest, e.treeSnapshot.Digest()}, jsonv2.Deterministic(true))
	if err != nil {
		return Digest{}, fmt.Errorf("agent: encode Effect boundary content: %w", err)
	}
	return ComputeDigest(encoded), nil
}

func (e EffectBoundary) Valid() bool {
	return e.sequence != 0 && e.previousTreeDigest.Valid() && e.treeSnapshot.Valid() &&
		e.previousTreeDigest != e.treeSnapshot.Digest() && e.Kind().Valid()
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
// Input, child, and progress cuts can coexist with sibling jobs because those
// jobs expose only committed Execution state or already recorded Effect intent.
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

// Identity is the opaque storage key for this root, writer, and sequence.
// Repeated snapshot content cannot identify a checkpoint because execution may
// return to an earlier recovery cut. Invalid checkpoints have an empty identity.
// Persist the returned key verbatim.
func (t TreeCheckpoint) Identity() string {
	if !t.Valid() {
		return ""
	}
	return deriveIdentity(treeCommitIdentityPrefix, "tree-checkpoint",
		t.treeSnapshot.RootID().String(), t.treeSnapshot.IncarnationID().String(),
		strconv.FormatUint(t.sequence, 10)).String()
}

// ContentDigest covers the Scope checkpoint kind, predecessor, and prospective
// recovery cut. The sequence belongs to Identity. Hosts retain their business
// write-set digest separately and check both within the same transaction.
func (t TreeCheckpoint) ContentDigest() (Digest, error) {
	if !t.Valid() {
		return Digest{}, errors.New("agent: invalid tree checkpoint")
	}
	encoded, err := jsonv2.Marshal(struct {
		Kind     TreeCheckpointKind
		Previous string
		Snapshot Digest
	}{t.kind, t.previousTreeDigest.String(), t.treeSnapshot.Digest()}, jsonv2.Deterministic(true))
	if err != nil {
		return Digest{}, fmt.Errorf("agent: encode tree checkpoint content: %w", err)
	}
	return ComputeDigest(encoded), nil
}

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
	return t.kind == classifyCheckpointCut(func(yield func(Status, *preparedStep) bool) {
		for _, snapshot := range t.treeSnapshot.state.ProcessSnapshots {
			if !yield(snapshot.Status(), snapshot.state.Prepared) {
				return
			}
		}
	})
}

// classifyCheckpointCut is the one rule shared by the runtime choosing a
// checkpoint kind and a store validating it. A live member blocks progress
// only while waiting, paused, or holding an Unknown settlement.
func classifyCheckpointCut(members iter.Seq2[Status, *preparedStep]) TreeCheckpointKind {
	kind := TreeCheckpointKindTerminal
	for status, prepared := range members {
		if status.Terminal() {
			continue
		}
		if !status.parked() && !prepared.hasUnknownSettlement() {
			return TreeCheckpointKindProgress
		}
		kind = TreeCheckpointKindParked
	}
	return kind
}

// TreeActivation changes writer identity and recovery state together so the
// previous Engine cannot continue committing after restoration takes ownership.
type TreeActivation struct {
	previousIncarnationID TreeIncarnationID
	previousTreeDigest    Digest
	treeSnapshot          TreeSnapshot
}

func newTreeActivation(
	previousIncarnationID TreeIncarnationID,
	previousTreeDigest Digest,
	treeSnapshot TreeSnapshot,
) (TreeActivation, error) {
	activation := TreeActivation{
		previousIncarnationID: previousIncarnationID,
		previousTreeDigest:    previousTreeDigest,
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

func (t TreeActivation) IncarnationID() TreeIncarnationID { return t.treeSnapshot.IncarnationID() }

func (t TreeActivation) TreeSnapshot() TreeSnapshot { return t.treeSnapshot }

// Identity is the opaque storage key for this root and proposed writer.
// Invalid activations have an empty identity. Persist the returned key verbatim.
func (t TreeActivation) Identity() string {
	if !t.Valid() {
		return ""
	}
	return deriveIdentity(treeCommitIdentityPrefix, "tree-activation",
		t.treeSnapshot.RootID().String(), t.IncarnationID().String()).String()
}

// ContentDigest includes both the expected previous writer and head as well as
// the proposed snapshot. Changing a precondition under a retained identity is a
// content conflict even when the proposed writer and snapshot are unchanged.
func (t TreeActivation) ContentDigest() (Digest, error) {
	if !t.Valid() {
		return Digest{}, errors.New("agent: invalid tree activation")
	}
	encoded, err := jsonv2.Marshal(struct {
		PreviousIncarnationID TreeIncarnationID
		PreviousTreeDigest    Digest
		Snapshot              Digest
	}{t.previousIncarnationID, t.previousTreeDigest, t.treeSnapshot.Digest()}, jsonv2.Deterministic(true))
	if err != nil {
		return Digest{}, fmt.Errorf("agent: encode tree activation content: %w", err)
	}
	return ComputeDigest(encoded), nil
}

func (t TreeActivation) Valid() bool {
	return t.previousIncarnationID.Valid() && t.previousTreeDigest.Valid() &&
		t.treeSnapshot.Valid() && t.previousIncarnationID != t.IncarnationID()
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
// An Effect commit is identified by root, Effect identity, and boundary kind.
// Each Effect contributes at most one pending, one settled, and one resolved
// commit for the lifetime of its tree, including across incarnations, so its
// deduplication fact needs neither the incarnation nor the sequence. A definite
// replay result uses the resolved boundary; it never commits a second settled
// boundary over a retained Unknown.
// Activation replaces writer and head and resets the sequence atomically before
// restoration can publish a Process. Initialization acknowledgment is separate
// because failed root initialization has no execution tree to persist.
//
// EffectBoundary, TreeCheckpoint, and TreeActivation own Identity and
// ContentDigest. All implementations must use those canonical projections for
// Scope facts; they do not replace storage CAS or a Host business write set.
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

// Committer calls detach from caller cancellation: the Host owns storage
// deadlines, and an abandoned acknowledgment would leave the head uncertain.
func activateTree(ctx context.Context, committer TreeCommitter, activation TreeActivation) error {
	if committer == nil || !activation.Valid() {
		return errors.New("invalid durable tree activation")
	}
	return invokeCallbackErr("TreeCommitter.ActivateTree", func() error {
		return committer.ActivateTree(context.WithoutCancel(RequireContext(ctx)), activation)
	})
}

// Construction already checked the complete immutable boundary, so this call
// only crosses the Host I/O boundary and need not repeat tree matching.
func commitEffectBoundary(ctx context.Context, committer TreeCommitter, boundary EffectBoundary) error {
	if committer == nil || !boundary.effectID.Valid() {
		return errors.New("invalid durable Effect boundary")
	}
	return invokeCallbackErr("TreeCommitter.CommitEffect", func() error {
		return committer.CommitEffect(context.WithoutCancel(RequireContext(ctx)), boundary)
	})
}

func commitTreeCheckpoint(ctx context.Context, committer TreeCommitter, checkpoint TreeCheckpoint) error {
	if committer == nil || !checkpoint.kind.Valid() {
		return errors.New("invalid durable tree checkpoint")
	}
	return invokeCallbackErr("TreeCommitter.CommitCheckpoint", func() error {
		return committer.CommitCheckpoint(context.WithoutCancel(RequireContext(ctx)), checkpoint)
	})
}
