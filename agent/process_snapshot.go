package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

var ErrInvalidSnapshot = errors.New("agent: invalid process snapshot")

// WaitKind identifies who may answer the current wait.
type WaitKind string

const (
	WaitKindInvalid  WaitKind = ""
	WaitKindExternal WaitKind = "external"
	WaitKindChildren WaitKind = "children"
)

func (w WaitKind) Valid() bool {
	switch w {
	case WaitKindExternal, WaitKindChildren:
		return true
	default:
		return false
	}
}

func (w WaitKind) String() string {
	if !w.Valid() {
		return invalidEnumName
	}
	return string(w)
}

// ProcessSnapshot is an immutable diagnostic capture of one Engine-owned
// Process. Strategy state and Effect payloads remain opaque. A ProcessSnapshot
// is not a recovery unit; only a complete TreeSnapshot can be restored.
// An interrupted terminal Process retains its prepared batch as evidence:
// settled operations keep their actual results, planned operations never run,
// and the candidate state and input cursor were not adopted.
// Its JSON keeps only the parent link of its relation, so it decodes only
// within the [TreeSnapshot] that supplies the root and depth, and that tree
// owns the TreeLimits it must satisfy.
// [Engine.InspectTree] identifies the acknowledged head of its durable captures.
type ProcessSnapshot struct {
	data  json.RawMessage
	state processSnapshotWire
	// awaitedWaitID projects the restored mailbox's answer to the entered
	// wait, so status reads one rule with the live Process.
	awaitedWaitID WaitID
	// openChildWaits projects the mailbox replay validation already performed,
	// so tree validation can check each wait without replaying it again.
	openChildWaits []openedChildWait
}

// processSnapshotRecord carries a Process record's own fields without the
// wire's encoding methods.
type processSnapshotRecord processSnapshotWire

// processSnapshotDocument is the persisted form of one Process record. Its
// relation's root and depth follow from the enclosing tree, so it keeps only
// the parent link; its prepared Effects are numbered by its own identity and
// committed progress; its pending child-wait answers render from its retained
// children. Only a tree, which owns that context, decodes it.
type processSnapshotDocument struct {
	// ProcessID persists the identity the in-memory relation owns.
	ProcessID ProcessID `json:"process_id"`
	processSnapshotRecord
	ParentID *ProcessID        `json:"parent_id,omitzero"`
	ChildKey *ChildKey         `json:"child_key,omitzero"`
	Mailbox  mailboxDocument   `json:"mailbox"`
	Prepared *preparedStepWire `json:"prepared,omitzero"`
}

// Every always-emitted member is required: a decoded zero would silently
// reset usage, authority, mailbox history, or pending control intent.
var processSnapshotRequiredMembers = []string{
	"process_id", "deployment_ref", "started_at", "committed_steps",
	"budget", "capabilities", "dropped_deltas",
	"committed_execution_state", "mailbox", "pending_control",
}

func decodeProcessSnapshotDocument(data json.RawMessage) (processSnapshotDocument, error) {
	document, err := jsonwire.Decode[processSnapshotDocument](data, processSnapshotRequiredMembers...)
	if err != nil {
		return processSnapshotDocument{}, fmt.Errorf("%w: decode: %w", ErrInvalidSnapshot, err)
	}
	return document, nil
}

// link reports the parent and ChildKey the document names, or false for a root.
func (p processSnapshotDocument) link() (childIdentity, bool, error) {
	switch {
	case p.ParentID == nil && p.ChildKey == nil:
		return childIdentity{}, false, nil
	case p.ParentID != nil && p.ChildKey != nil:
		return childIdentity{parent: *p.ParentID, key: *p.ChildKey}, true, nil
	default:
		return childIdentity{}, false, fmt.Errorf("%w: a child needs both its parent and ChildKey", ErrInvalidSnapshot)
	}
}

// snapshot completes the record at relation, which the tree derived from its
// link, with the outcomes its children reached and the grants its started
// children hold, and validates it.
func (p processSnapshotDocument) snapshot(relation ProcessRelation, outcomes childOutcomeSource, grants childGrantSource) (ProcessSnapshot, error) {
	wire := processSnapshotWire(p.processSnapshotRecord)
	wire.Relation = relation
	mailbox, err := p.Mailbox.wire(outcomes)
	if err != nil {
		return ProcessSnapshot{}, fmt.Errorf("%w: mailbox: %w", ErrInvalidSnapshot, err)
	}
	wire.Mailbox = mailbox
	if p.Prepared != nil {
		if wire.CommittedSteps == math.MaxUint64 {
			return ProcessSnapshot{}, fmt.Errorf("%w: prepared Step sequence overflows", ErrInvalidSnapshot)
		}
		prepared, err := p.Prepared.step(relation.ProcessID(), wire.CommittedSteps+1, grants)
		if err != nil {
			return ProcessSnapshot{}, fmt.Errorf("%w: prepared Step: %w", ErrInvalidSnapshot, err)
		}
		wire.Prepared = &prepared
	}
	return processSnapshotFromWire(wire)
}

func (p processSnapshotWire) MarshalJSON() ([]byte, error) {
	mailbox, err := p.Mailbox.document()
	if err != nil {
		return nil, err
	}
	document := processSnapshotDocument{ProcessID: p.processID(), processSnapshotRecord: processSnapshotRecord(p), Mailbox: mailbox}
	if identity, child := p.Relation.childIdentity(); child {
		document.ParentID, document.ChildKey = &identity.parent, &identity.key
	}
	if p.Prepared != nil {
		prepared, err := p.Prepared.wire()
		if err != nil {
			return nil, err
		}
		document.Prepared = &prepared
	}
	return jsonv2.Marshal(document)
}

// The caller transfers the wire's mutable containers. After validation, state
// is immutable and data is its encoded projection; neither is updated in place.
func processSnapshotFromWire(wire processSnapshotWire) (ProcessSnapshot, error) {
	// A decoded instant may carry any offset; the canonical encoding, and so
	// the digest, must not depend on how a store rendered it.
	wire.StartedAt = canonicalTime(wire.StartedAt)
	if wire.Finish != nil {
		finish := *wire.Finish
		finish.FinishedAt = canonicalTime(finish.FinishedAt)
		wire.Finish = &finish
	}
	mailbox, err := wire.validate()
	if err != nil {
		return ProcessSnapshot{}, err
	}
	normalized, err := jsonv2.Marshal(wire, jsonv2.Deterministic(true))
	if err != nil {
		return ProcessSnapshot{}, fmt.Errorf("%w: encode: %w", ErrInvalidSnapshot, err)
	}
	snapshot := ProcessSnapshot{data: normalized, state: wire, awaitedWaitID: mailbox.awaited(lo.FromPtr(wire.CurrentWaitID))}
	// A terminal Process's waits end with it; its mailbox only closes the
	// waits it consumed.
	if !wire.terminal() {
		snapshot.openChildWaits = mailbox.openChildWaits()
	}
	return snapshot, nil
}

// JSON returns an independently owned snapshot representation.
func (p ProcessSnapshot) JSON() json.RawMessage { return bytes.Clone(p.data) }

// SignalReceipts returns admitted Signal facts in mailbox arrival order. The
// committed cursor distinguishes consumption from admission, including inputs
// accepted after a final Step obtained its Signal window. These facts have the
// same acknowledgment boundary as this snapshot; absence in an older capture
// does not prove rejection. No mailbox or consumption authority is transferred.
func (p ProcessSnapshot) SignalReceipts() []SignalReceipt {
	return p.state.Mailbox.receipts()
}

func (p ProcessSnapshot) ProcessID() ProcessID {
	return p.state.processID()
}

func (p ProcessSnapshot) DeploymentRef() DeploymentRef {
	return p.state.DeploymentRef
}

func (p ProcessSnapshot) Relation() ProcessRelation {
	if !p.Valid() {
		return ProcessRelation{}
	}
	return p.state.Relation
}

func (p ProcessSnapshot) Budget() Budget {
	return p.state.Budget
}

func (p ProcessSnapshot) Capabilities() CapabilitySet {
	return p.state.Capabilities
}

func (p ProcessSnapshot) Status() Status {
	return p.state.status(p.awaitedWaitID)
}

func (p ProcessSnapshot) Usage() Usage {
	return p.state.usage()
}

// UnknownEffectIDs returns Effects whose captured settlement requires explicit
// resolution. RuntimeError separately owns outcomes an instance could not confirm.
// A terminal capture retains unresolved evidence; its Process cannot resume or
// accept further resolution commands.
func (p ProcessSnapshot) UnknownEffectIDs() []EffectID {
	if p.state.Prepared == nil {
		return nil
	}
	return p.state.Prepared.Effects.unknownEffectIDs()
}

// CommittedExecutionState returns the latest committed opaque Strategy state.
// A prepared candidate, when present, remains an uncommitted Engine detail.
// Only the owning Definition or its typed inspection helpers may interpret the
// returned state's payload.
func (p ProcessSnapshot) CommittedExecutionState() ExecutionState {
	return p.state.CommittedExecutionState.clone()
}

// Settlements yields retained Effect outcomes, keyed by the Effect each
// answers, in declaration order. These are execution facts even when
// cancellation prevented candidate-state adoption. Adopted outcomes move into
// SignalReceipts until consumed by the Strategy; this is not a historical
// journal and absence does not imply non-execution.
func (p ProcessSnapshot) Settlements() iter.Seq2[EffectID, Settlement] {
	return func(yield func(EffectID, Settlement) bool) {
		if p.state.Prepared == nil {
			return
		}
		for _, record := range p.state.Prepared.Effects {
			if record.settlement() != nil && !yield(record.ID, record.settlement().clone()) {
				return
			}
		}
	}
}

// EffectDiagnostic returns the bounded diagnostic retained for an uncertain
// dispatch attempt while its prepared Step remains captured, including after
// restoration or explicit resolution. It does not establish failure of the
// external operation or authorize replay.
func (p ProcessSnapshot) EffectDiagnostic(id EffectID) (Failure, bool) {
	if p.state.Prepared != nil {
		for _, effect := range p.state.Prepared.Effects {
			if effect.ID == id && effect.diagnostic() != nil {
				return *effect.diagnostic(), true
			}
		}
	}
	return Failure{}, false
}

// Result returns the captured immutable terminal outcome, when present. Its
// persistence authority is that of the enclosing TreeSnapshot transaction.
func (p ProcessSnapshot) Result() (Result, bool) { return p.state.result() }

// WaitID returns the current unanswered Engine-minted wait identity, including
// while the captured Process is Paused.
func (p ProcessSnapshot) WaitID() (WaitID, bool) {
	return p.awaitedWaitID, p.awaitedWaitID.Valid()
}

// WaitKind distinguishes Host input from Framework child completion while the
// captured Process has an unanswered wait, including while Paused.
func (p ProcessSnapshot) WaitKind() (WaitKind, bool) {
	waitID, waiting := p.WaitID()
	if !waiting {
		return WaitKindInvalid, false
	}
	return p.state.Mailbox.waitKind(waitID)
}

func (p ProcessSnapshot) Valid() bool { return len(p.data) > 0 }

func (p ProcessSnapshot) MarshalJSON() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidSnapshot
	}
	return bytes.Clone(p.data), nil
}

func (p ProcessSnapshot) wire() (processSnapshotWire, error) {
	if !p.Valid() {
		return processSnapshotWire{}, ErrInvalidSnapshot
	}
	return p.state.clone(), nil
}

type pendingControlWire struct {
	Failure      *Failure                `json:"failure,omitzero"`
	KillReason   string                  `json:"kill_reason,omitempty"`
	Deadline     *deadlineIntentWire     `json:"deadline,omitzero"`
	Cancellation *cancellationIntentWire `json:"cancellation,omitzero"`
	PauseReason  string                  `json:"pause_reason,omitempty"`
}

// deadlineIntentWire and cancellationIntentWire keep each intent's owner and
// reason together, so the intent is present exactly when its record is.
type deadlineIntentWire struct {
	Owner  deadlineOwner `json:"owner"`
	Reason string        `json:"reason"`
}

type cancellationIntentWire struct {
	Owner  cancellationOwner `json:"owner"`
	Reason string            `json:"reason"`
}

// processSnapshotWire persists lifecycle facts, never the Status they project.
type processSnapshotWire struct {
	// Relation is complete in memory and owns the Process identity; the
	// document persists only that identity and its parent link.
	Relation                ProcessRelation    `json:"-"`
	DeploymentRef           DeploymentRef      `json:"deployment_ref"`
	StartedAt               time.Time          `json:"started_at"`
	CommittedSteps          uint64             `json:"committed_steps"`
	Budget                  Budget             `json:"budget"`
	Capabilities            CapabilitySet      `json:"capabilities"`
	DroppedDeltas           uint64             `json:"dropped_deltas"`
	CommittedExecutionState ExecutionState     `json:"committed_execution_state"`
	Mailbox                 mailboxWire        `json:"mailbox"`
	Prepared                *preparedStep      `json:"prepared,omitzero"`
	CurrentWaitID           *WaitID            `json:"current_wait_id,omitzero"`
	PauseReason             string             `json:"pause_reason,omitempty"`
	PendingControl          pendingControlWire `json:"pending_control"`
	Finish                  *processFinish     `json:"finish,omitzero"`
}

// A one-byte placeholder keeps optional fields present in the real wire codec.
// JSON expands each diagnostic UTF-8 byte by at most six bytes; qualified
// failure codes need no escaping. Only the byte count grows, never a buffer.
const (
	snapshotReservationText = "x"
	snapshotFailureGrowth   = uint64(maxQualifiedNameBytes - len(snapshotReservationText) + 6*MaxDiagnosticBytes - len(snapshotReservationText))
)

// Finite byte quotas reserve mandatory lifecycle growth and Framework settlements.
// These projections measure bytes only; they never become lifecycle facts.
// Dispatcher payloads are external outcomes, not predictable admission facts.
func (p processSnapshotWire) admissionSize(limits TreeLimits) (uint64, error) {
	var pendingSize, terminalGrowth, effectGrowth uint64
	if !p.terminal() && (limits.MaxProcessSnapshotBytes.limited || limits.MaxSnapshotBytes.limited) {
		reservation := snapshotTextReservation{}
		failure := reservation.failure()
		if p.Prepared != nil {
			prepared := *p.Prepared
			prepared.Effects = slices.Clone(prepared.Effects)
			p.Prepared = &prepared
			for index := range prepared.Effects {
				record := &prepared.Effects[index]
				projection, growth, err := record.snapshotReservation()
				if err != nil {
					return 0, err
				}
				if !resourceQuantitiesFit(math.MaxUint64, effectGrowth, growth) {
					return 0, ErrCounterExhausted
				}
				prepared.Effects[index] = projection
				effectGrowth += growth
			}
		}
		// Current and pending control fields reserve independently, including
		// a Step pause racing a Host pause.
		p.PauseReason = reservation.reason(maxPauseReasonBytes)
		p.DroppedDeltas = math.MaxUint64
		p.PendingControl = pendingControlWire{
			Failure: &failure, KillReason: reservation.reason(maxTerminationReasonBytes), PauseReason: reservation.reason(maxPauseReasonBytes),
			Deadline:     &deadlineIntentWire{Owner: deadlineOwnerParent, Reason: reservation.reason(maxTerminationReasonBytes)},
			Cancellation: &cancellationIntentWire{Owner: cancellationOwnerParent, Reason: reservation.reason(maxTerminationReasonBytes)},
		}
		pending, err := jsonv2.Marshal(p)
		if err != nil {
			return 0, err
		}
		pendingSize = uint64(len(pending)) + reservation.growth
		terminalGrowth = snapshotFailureGrowth
		p.PendingControl = pendingControlWire{}
		p.PauseReason = ""
		p.CurrentWaitID = nil
		// The maximal failure object is larger than a control cause and reason.
		p.Finish = &processFinish{
			Termination: failure.termination(),
			FinishedAt:  time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC),
		}
	}
	encoded, err := jsonv2.Marshal(p)
	if err != nil {
		return 0, err
	}
	size := max(pendingSize, uint64(len(encoded))+terminalGrowth)
	if !resourceQuantitiesFit(math.MaxUint64, size, effectGrowth) {
		return 0, ErrCounterExhausted
	}
	size += effectGrowth
	if !limits.MaxProcessSnapshotBytes.Allows(size) {
		return 0, ErrResourceLimitExceeded
	}
	return size, nil
}

// Domain values are immutable. The wire's pointers, slices, and raw mailbox
// payloads need independent ownership before retention or mutable restoration.
func (p processSnapshotWire) clone() processSnapshotWire {
	clone := p
	if p.CurrentWaitID != nil {
		clone.CurrentWaitID = new(*p.CurrentWaitID)
	}
	if p.PendingControl.Failure != nil {
		clone.PendingControl.Failure = new(*p.PendingControl.Failure)
	}
	if p.Finish != nil {
		clone.Finish = new(*p.Finish)
	}
	clone.Mailbox.Signals = slices.Clone(p.Mailbox.Signals)
	for index, signal := range p.Mailbox.Signals {
		clone.Mailbox.Signals[index].Payload = bytes.Clone(signal.Payload)
		if signal.PayloadDigest != nil {
			clone.Mailbox.Signals[index].PayloadDigest = new(*signal.PayloadDigest)
		}
		if signal.WaitID != nil {
			clone.Mailbox.Signals[index].WaitID = new(*signal.WaitID)
		}
		if signal.Opens != nil {
			clone.Mailbox.Signals[index].Opens = new(signal.Opens.clone())
		}
	}
	if p.Prepared != nil {
		prepared := p.Prepared.clone()
		clone.Prepared = &prepared
	}
	return clone
}

func (p processSnapshotWire) processID() ProcessID { return p.Relation.ProcessID() }

func (p processSnapshotWire) validateContract() error {
	if !p.processID().Valid() {
		return fmt.Errorf("%w: Process identity is invalid", ErrInvalidSnapshot)
	}
	if !p.DeploymentRef.Valid() {
		return fmt.Errorf("%w: Deployment reference is invalid", ErrInvalidSnapshot)
	}
	if p.StartedAt.IsZero() {
		return fmt.Errorf("%w: Process start time is missing", ErrInvalidSnapshot)
	}
	if !p.CommittedExecutionState.Valid() {
		return fmt.Errorf("%w: committed Execution state is invalid", ErrInvalidSnapshot)
	}
	if !p.Capabilities.Valid() {
		return fmt.Errorf("%w: capability set is invalid", ErrInvalidSnapshot)
	}
	return nil
}

func (p processSnapshotWire) validateRelation() error {
	relation := p.Relation
	if !relation.Valid() {
		return fmt.Errorf("%w: relation: %w", ErrInvalidSnapshot, ErrInvalidProcessRelation)
	}
	return nil
}

func (p processSnapshotWire) validateProgress(mailbox signalMailbox) error {
	if err := p.validatePrepared(mailbox); err != nil {
		return err
	}
	return p.validateCapacity(resourceAmounts{})
}

// validateCapacity checks usage and prepared work against the budget left after
// childAllocation. A Process alone knows no child grants; tree validation
// repeats the check with the debits of the captured children.
func (p processSnapshotWire) validateCapacity(childAllocation resourceAmounts) error {
	if !p.Budget.contains(p.usage(), childAllocation) {
		return fmt.Errorf("%w: usage and child allocations exceed the Process budget", ErrInvalidSnapshot)
	}
	_, reserved, preparedSteps := p.pendingSignals()
	if !p.Budget.Signals.Allows(p.usage().AcceptedSignals, childAllocation.Signals, reserved) ||
		!p.Budget.Steps.Allows(p.CommittedSteps, childAllocation.Steps, preparedSteps) {
		return fmt.Errorf("%w: execution capacity exceeds budget", ErrInvalidSnapshot)
	}
	return nil
}

// pendingSignals derives mailbox occupancy after the prepared Step, if any.
// Callers validate the mailbox and prepared consumption first, so the prepared
// consumption cannot exceed the pending suffix.
func (p processSnapshotWire) pendingSignals() (remaining, reserved, preparedSteps uint64) {
	remaining = uint64(len(p.Mailbox.Signals)) - p.Mailbox.SignalCursor
	if p.Prepared != nil && !p.terminal() {
		remaining -= p.Prepared.consumedSignals()
		reserved = p.Prepared.settlementSignalCount()
		preparedSteps = 1
	}
	return remaining, reserved, preparedSteps
}

// validateCapacity checks this validated capture against the tree policy that
// owns its depth, mailbox, and encoded-size bounds.
func (p ProcessSnapshot) validateCapacity(limits TreeLimits) error {
	if !limits.admitsDepth(p.state.Relation.Depth()) {
		return fmt.Errorf("%w: relation depth exceeds MaxDepth", ErrInvalidSnapshot)
	}
	pending := uint64(len(p.state.Mailbox.Signals)) - p.state.Mailbox.SignalCursor
	remaining, reserved, _ := p.state.pendingSignals()
	if !limits.admitsPendingSignals(pending, remaining, reserved, 0) {
		return fmt.Errorf("%w: pending Signals exceed MaxPendingSignals", ErrInvalidSnapshot)
	}
	if !limits.MaxProcessSnapshotBytes.Allows(uint64(len(p.data))) {
		return fmt.Errorf("%w: snapshot byte quota exceeded", ErrInvalidSnapshot)
	}
	return nil
}

func (p processSnapshotWire) validatePrepared(mailbox signalMailbox) error {
	if p.Prepared == nil {
		return nil
	}
	if status := p.status(mailbox.awaited(lo.FromPtr(p.CurrentWaitID))); status != StatusRunning && !status.Terminal() || status == StatusCompleted {
		return fmt.Errorf("%w: prepared Step requires Running or interrupted terminal status", ErrInvalidSnapshot)
	}
	if p.CommittedSteps == math.MaxUint64 {
		return fmt.Errorf("%w: prepared Step sequence overflows", ErrInvalidSnapshot)
	}
	if err := p.Prepared.validate(mailbox); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	for _, record := range p.Prepared.Effects {
		if p.terminal() && record.phase() == effectPhasePending {
			return fmt.Errorf("%w: terminal Process cannot retain pending Effects", ErrInvalidSnapshot)
		}
	}
	return nil
}

// validate returns the mailbox its replay restored.
func (p processSnapshotWire) validate() (signalMailbox, error) {
	if err := p.validateContract(); err != nil {
		return signalMailbox{}, err
	}
	if err := p.validateRelation(); err != nil {
		return signalMailbox{}, err
	}
	mailbox, err := restoreSignalMailbox(p.Mailbox)
	if err != nil {
		return signalMailbox{}, fmt.Errorf("%w: mailbox: %w", ErrInvalidSnapshot, err)
	}
	if err := p.validateProgress(mailbox); err != nil {
		return signalMailbox{}, err
	}
	if err := p.validateLifecycle(mailbox); err != nil {
		return signalMailbox{}, err
	}
	if _, err := pendingControlFromWire(p.PendingControl); err != nil {
		return signalMailbox{}, fmt.Errorf("%w: pending control: %w", ErrInvalidSnapshot, err)
	}
	return mailbox, nil
}

// status needs the wait the restored mailbox still awaits; terminal does not.
func (p processSnapshotWire) status(awaited WaitID) Status {
	return lifecycleStatus(lo.FromPtr(p.Finish).Termination, p.PauseReason != "", awaited.Valid())
}

func (p processSnapshotWire) terminal() bool { return lo.FromPtr(p.Finish).Termination.Valid() }

func (p processSnapshotWire) validateLifecycle(mailbox signalMailbox) error {
	if err := p.validateFinish(); err != nil {
		return err
	}
	status := p.status(mailbox.awaited(lo.FromPtr(p.CurrentWaitID)))
	if _, err := parsePause(p.PauseReason); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	if !status.Terminal() {
		return p.validateCurrentWait(mailbox)
	}
	if p.PauseReason != "" || p.CurrentWaitID != nil {
		return fmt.Errorf("%w: terminal Process cannot retain a pause or current wait", ErrInvalidSnapshot)
	}
	return p.validateTerminalEvidence()
}

func (p processSnapshotWire) validateFinish() error {
	if p.Finish == nil {
		return nil
	}
	if p.Finish.FinishedAt.IsZero() {
		return fmt.Errorf("%w: finished time is required", ErrInvalidSnapshot)
	}
	if !p.Finish.Termination.Valid() {
		return fmt.Errorf("%w: termination is invalid", ErrInvalidSnapshot)
	}
	return nil
}

func (p processSnapshotWire) validateCurrentWait(mailbox signalMailbox) error {
	if p.CurrentWaitID == nil {
		return nil
	}
	if err := mailbox.enterWait(*p.CurrentWaitID); err != nil {
		return fmt.Errorf("%w: current WaitID requires an open wait", ErrInvalidSnapshot)
	}
	return nil
}

func (p processSnapshotWire) validateTerminalEvidence() error {
	if p.PendingControl != (pendingControlWire{}) {
		return fmt.Errorf("%w: terminal Process cannot retain control state", ErrInvalidSnapshot)
	}
	if len(p.Finish.Termination.UnresolvedEffectIDs()) != 0 {
		return fmt.Errorf("%w: termination stores a copy of its interrupted Effects", ErrInvalidSnapshot)
	}
	return nil
}

// publishedTermination attaches the prepared Effects left unknown, which own
// the identities a captured termination leaves unresolved.
func (p processSnapshotWire) publishedTermination() Termination {
	termination := lo.FromPtr(p.Finish).Termination
	if p.Finish == nil || p.Prepared == nil {
		return termination
	}
	return termination.withUnresolvedEffectIDs(p.Prepared.Effects.unknownEffectIDs())
}

// result requires a validated capture, whose terminal status guarantees its
// finish time and termination.
func (p processSnapshotWire) result() (Result, bool) {
	if !p.terminal() {
		return Result{}, false
	}
	return Result{
		processID: p.processID(), startedAt: p.StartedAt, finishedAt: p.Finish.FinishedAt,
		termination: p.publishedTermination(), usage: p.usage(),
	}, true
}

func (p processSnapshotWire) usage() Usage {
	return processUsage(p.CommittedSteps, uint64(len(p.Mailbox.Signals)), p.Mailbox.settlementCount(), p.Prepared, p.DroppedDeltas)
}
