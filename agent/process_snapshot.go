package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
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
// Parsing validates the captured state, not storage acknowledgment or tree
// capacity; the enclosing [TreeSnapshot] owns the TreeLimits it must satisfy.
// [Engine.InspectTree] identifies the acknowledged head of its durable captures.
type ProcessSnapshot struct {
	data  json.RawMessage
	state processSnapshotWire
	// openChildWaits projects the mailbox replay validation already performed,
	// so tree validation can match registrations without replaying it again.
	openChildWaits map[WaitID]WaitKey
}

// ParseProcessSnapshot strictly validates one Process snapshot wire value,
// including single-answer wait history and an open, unanswered current wait
// when the Process is Waiting or retains a wait while Paused. Prepared Effects
// must fit the captured capability grant. Terminal prepared batches contain no
// pending attempt, and their unknown identities must match the Termination.
func ParseProcessSnapshot(data json.RawMessage) (ProcessSnapshot, error) {
	// Every always-emitted member is required: a decoded zero would silently
	// reset usage, authority, mailbox history, or pending control intent.
	wire, err := jsonwire.Decode[processSnapshotWire](data,
		"process_id", "relation", "deployment_ref", "started_at", "committed_steps",
		"budget", "capabilities", "counters",
		"committed_execution_state", "mailbox", "pending_control",
	)
	if err != nil {
		return ProcessSnapshot{}, fmt.Errorf("%w: decode: %w", ErrInvalidSnapshot, err)
	}
	return processSnapshotFromWire(wire)
}

// The caller transfers the wire's mutable containers. After validation, state
// is immutable and data is its encoded projection; neither is updated in place.
func processSnapshotFromWire(wire processSnapshotWire) (ProcessSnapshot, error) {
	// A decoded instant may carry any offset; the canonical encoding, and so
	// the digest, must not depend on how a store rendered it.
	wire.StartedAt = canonicalTime(wire.StartedAt)
	if wire.FinishedAt != nil {
		finishedAt := canonicalTime(*wire.FinishedAt)
		wire.FinishedAt = &finishedAt
	}
	mailbox, err := wire.validate()
	if err != nil {
		return ProcessSnapshot{}, err
	}
	normalized, err := jsonv2.Marshal(wire, jsonv2.Deterministic(true))
	if err != nil {
		return ProcessSnapshot{}, fmt.Errorf("%w: encode: %w", ErrInvalidSnapshot, err)
	}
	return ProcessSnapshot{data: normalized, state: wire, openChildWaits: mailbox.openChildWaits()}, nil
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
	return p.state.ProcessID
}

func (p ProcessSnapshot) DeploymentRef() DeploymentRef {
	return p.state.DeploymentRef
}

func (p ProcessSnapshot) Relation() ProcessRelation {
	if !p.Valid() {
		return ProcessRelation{}
	}
	return mustProcessRelation(p.state.ProcessID, p.state.Relation)
}

func (p ProcessSnapshot) Budget() Budget {
	return p.state.Budget
}

func (p ProcessSnapshot) Capabilities() CapabilitySet {
	return p.state.Capabilities
}

func (p ProcessSnapshot) Status() Status {
	return p.state.status()
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

// Settlements returns retained Effect outcomes in declaration order. These are
// execution facts even when cancellation prevented candidate-state adoption.
// Adopted outcomes move into SignalReceipts until consumed by the Strategy;
// this is not a historical journal and absence does not imply non-execution.
func (p ProcessSnapshot) Settlements() []Settlement {
	var settlements []Settlement
	if p.state.Prepared != nil {
		for _, record := range p.state.Prepared.Effects {
			if record.Settlement != nil {
				settlements = append(settlements, record.Settlement.clone())
			}
		}
	}
	return settlements
}

func (p ProcessSnapshot) preparedEffect(stepSequence uint64, batchIndex uint32) (preparedEffect, bool) {
	prepared := p.state.Prepared
	if prepared == nil || p.state.CommittedSteps+1 != stepSequence || uint64(batchIndex) >= uint64(len(prepared.Effects)) {
		return preparedEffect{}, false
	}
	return prepared.Effects[batchIndex], true
}

// EffectDiagnostic returns the bounded diagnostic retained for an uncertain
// dispatch attempt while its prepared Step remains captured, including after
// restoration or explicit resolution. It does not establish failure of the
// external operation or authorize replay.
func (p ProcessSnapshot) EffectDiagnostic(id EffectID) (Failure, bool) {
	if p.state.Prepared != nil {
		for _, effect := range p.state.Prepared.Effects {
			if effect.ID == id && effect.Diagnostic != nil {
				return *effect.Diagnostic, true
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
	waitID := lo.FromPtr(p.state.CurrentWaitID)
	return waitID, waitID.Valid()
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

func mustProcessRelation(processID ProcessID, wire processRelationWire) ProcessRelation {
	relation, err := processRelationFromWire(processID, wire)
	if err != nil {
		panic(err)
	}
	return relation
}

func (p ProcessSnapshot) MarshalJSON() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidSnapshot
	}
	return bytes.Clone(p.data), nil
}

func (p *ProcessSnapshot) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidSnapshot)
	}
	value, err := ParseProcessSnapshot(data)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

func (p ProcessSnapshot) wire() (processSnapshotWire, error) {
	if !p.Valid() {
		return processSnapshotWire{}, ErrInvalidSnapshot
	}
	return p.state.clone(), nil
}

type pendingControlWire struct {
	Failure            *Failure          `json:"failure,omitzero"`
	KillReason         string            `json:"kill_reason,omitempty"`
	DeadlineOwner      deadlineOwner     `json:"deadline_owner,omitempty"`
	DeadlineReason     string            `json:"deadline_reason,omitempty"`
	CancellationOwner  cancellationOwner `json:"cancellation_owner,omitempty"`
	CancellationReason string            `json:"cancellation_reason,omitempty"`
	PauseReason        string            `json:"pause_reason,omitempty"`
}

// processSnapshotWire persists lifecycle facts, never the Status they project.
type processSnapshotWire struct {
	ProcessID               ProcessID           `json:"process_id"`
	Relation                processRelationWire `json:"relation"`
	ChildRequestDigest      *Digest             `json:"child_request_digest,omitzero"`
	DeploymentRef           DeploymentRef       `json:"deployment_ref"`
	StartedAt               time.Time           `json:"started_at"`
	FinishedAt              *time.Time          `json:"finished_at,omitzero"`
	CommittedSteps          uint64              `json:"committed_steps"`
	Budget                  Budget              `json:"budget"`
	Capabilities            CapabilitySet       `json:"capabilities"`
	Counters                processCounters     `json:"counters"`
	CommittedExecutionState ExecutionState      `json:"committed_execution_state"`
	Mailbox                 mailboxWire         `json:"mailbox"`
	Prepared                *preparedStep       `json:"prepared,omitzero"`
	CurrentWaitID           *WaitID             `json:"current_wait_id,omitzero"`
	PauseReason             string              `json:"pause_reason,omitempty"`
	PendingControl          pendingControlWire  `json:"pending_control"`
	Output                  Payload             `json:"output,omitzero"`
	Termination             *Termination        `json:"termination,omitzero"`
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
	if !p.status().Terminal() && (limits.MaxProcessSnapshotBytes.limited || limits.MaxSnapshotBytes.limited) {
		reservation := snapshotTextReservation{}
		failure := reservation.failure()
		var unresolved []EffectID
		if p.Prepared != nil {
			prepared := *p.Prepared
			prepared.Effects = slices.Clone(prepared.Effects)
			p.Prepared = &prepared
			for index := range prepared.Effects {
				record := &prepared.Effects[index]
				if record.unknown() || record.Phase == effectPhasePending {
					unresolved = append(unresolved, record.ID)
				}
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
		p.Counters.DroppedDeltas = math.MaxUint64
		p.PendingControl = pendingControlWire{
			Failure: &failure, KillReason: reservation.reason(maxTerminationReasonBytes), PauseReason: reservation.reason(maxPauseReasonBytes),
			DeadlineOwner: deadlineOwnerParent, DeadlineReason: reservation.reason(maxTerminationReasonBytes),
			CancellationOwner: cancellationOwnerParent, CancellationReason: reservation.reason(maxTerminationReasonBytes),
		}
		pending, err := jsonv2.Marshal(p)
		if err != nil {
			return 0, err
		}
		pendingSize = uint64(len(pending)) + reservation.growth
		terminalGrowth = snapshotFailureGrowth + uint64(6*MaxDiagnosticBytes-len(snapshotReservationText))
		p.PendingControl = pendingControlWire{}
		p.PauseReason = ""
		p.CurrentWaitID = nil
		p.FinishedAt = new(time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC))
		// The maximal failure object adds more bytes than other terminal statuses
		// and causes can add, while its message also fills the termination reason.
		termination := failure.termination().withUnresolvedEffectIDs(unresolved)
		p.Termination = &termination
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
	if p.Relation.ParentID != nil {
		clone.Relation.ParentID = new(*p.Relation.ParentID)
	}
	if p.Relation.ChildKey != nil {
		clone.Relation.ChildKey = new(*p.Relation.ChildKey)
	}
	if p.ChildRequestDigest != nil {
		clone.ChildRequestDigest = new(*p.ChildRequestDigest)
	}
	if p.FinishedAt != nil {
		clone.FinishedAt = new(*p.FinishedAt)
	}
	if p.CurrentWaitID != nil {
		clone.CurrentWaitID = new(*p.CurrentWaitID)
	}
	if p.PendingControl.Failure != nil {
		clone.PendingControl.Failure = new(*p.PendingControl.Failure)
	}
	if p.Termination != nil {
		clone.Termination = new(*p.Termination)
	}
	clone.Mailbox.Signals = slices.Clone(p.Mailbox.Signals)
	for index, signal := range p.Mailbox.Signals {
		clone.Mailbox.Signals[index].Payload = bytes.Clone(signal.Payload)
		if signal.WaitID != nil {
			clone.Mailbox.Signals[index].WaitID = new(*signal.WaitID)
		}
		if signal.Opens != nil {
			clone.Mailbox.Signals[index].Opens = new(*signal.Opens)
		}
	}
	if p.Prepared != nil {
		prepared := p.Prepared.clone()
		clone.Prepared = &prepared
	}
	return clone
}

func (p processSnapshotWire) validateContract() error {
	if !p.ProcessID.Valid() {
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
	relation, err := processRelationFromWire(p.ProcessID, p.Relation)
	if err != nil {
		return fmt.Errorf("%w: relation: %w", ErrInvalidSnapshot, err)
	}
	if relation.IsRoot() != (p.ChildRequestDigest == nil) ||
		p.ChildRequestDigest != nil && !p.ChildRequestDigest.Valid() {
		return fmt.Errorf("%w: child request digest does not match relation", ErrInvalidSnapshot)
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
	if p.Prepared != nil && !p.status().Terminal() {
		remaining -= p.Prepared.consumedSignals()
		reserved = p.Prepared.settlementSignalCount()
		preparedSteps = 1
	}
	return remaining, reserved, preparedSteps
}

// validateCapacity checks this validated capture against the tree policy that
// owns its depth, mailbox, and encoded-size bounds.
func (p ProcessSnapshot) validateCapacity(limits TreeLimits) error {
	if !limits.admitsDepth(p.state.Relation.Depth) {
		return fmt.Errorf("%w: relation depth exceeds MaxDepth", ErrInvalidSnapshot)
	}
	pending := uint64(len(p.state.Mailbox.Signals)) - p.state.Mailbox.SignalCursor
	remaining, reserved, _ := p.state.pendingSignals()
	if !resourceQuantitiesFit(limits.MaxPendingSignals, pending) ||
		!resourceQuantitiesFit(limits.MaxPendingSignals, remaining, reserved) {
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
	if p.status() != StatusRunning && !p.status().Terminal() || p.status() == StatusCompleted {
		return fmt.Errorf("%w: prepared Step requires Running or interrupted terminal status", ErrInvalidSnapshot)
	}
	if p.CommittedSteps == math.MaxUint64 {
		return fmt.Errorf("%w: prepared Step sequence overflows", ErrInvalidSnapshot)
	}
	if err := p.Prepared.validate(p.ProcessID, p.CommittedSteps+1, mailbox); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	for _, record := range p.Prepared.Effects {
		if !p.Capabilities.Allows(record.Effect.RequiredCapabilities()) {
			return fmt.Errorf("%w: prepared Effect capability denied: %w", ErrInvalidSnapshot, ErrInvalidCapability)
		}
		if p.status().Terminal() && record.Phase == effectPhasePending {
			return fmt.Errorf("%w: terminal Process cannot retain pending Effects", ErrInvalidSnapshot)
		}
	}
	if p.usage().PreparedEffects < p.Prepared.settlementSignalCount() {
		return fmt.Errorf("%w: prepared Effect identities exceed recorded usage", ErrInvalidSnapshot)
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
	mailbox, err := restoreSignalMailbox(p.Mailbox, p.status())
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

func (p processSnapshotWire) status() Status {
	return lifecycleStatus(lo.FromPtr(p.Termination), p.PauseReason != "", p.CurrentWaitID != nil)
}

func (p processSnapshotWire) validateLifecycle(mailbox signalMailbox) error {
	if err := p.validateTermination(); err != nil {
		return err
	}
	status := p.status()
	if (status == StatusCompleted) != p.Output.Valid() {
		return fmt.Errorf("%w: exactly a Completed Process contains Output", ErrInvalidSnapshot)
	}
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

func (p processSnapshotWire) validateTermination() error {
	if (p.Termination != nil) != (p.FinishedAt != nil) {
		return fmt.Errorf("%w: termination and finished time must agree", ErrInvalidSnapshot)
	}
	if p.Termination == nil {
		return nil
	}
	if p.FinishedAt.IsZero() {
		return fmt.Errorf("%w: finished time is required", ErrInvalidSnapshot)
	}
	if !p.Termination.Valid() {
		return fmt.Errorf("%w: termination is invalid", ErrInvalidSnapshot)
	}
	return nil
}

func (p processSnapshotWire) validateCurrentWait(mailbox signalMailbox) error {
	if p.CurrentWaitID == nil {
		return nil
	}
	if shouldWait, err := mailbox.enterWait(*p.CurrentWaitID); err != nil || !shouldWait {
		return fmt.Errorf("%w: current WaitID requires an open unanswered wait", ErrInvalidSnapshot)
	}
	return nil
}

func (p processSnapshotWire) validateTerminalEvidence() error {
	if p.PendingControl != (pendingControlWire{}) {
		return fmt.Errorf("%w: terminal Process cannot retain control state", ErrInvalidSnapshot)
	}
	var unresolved []EffectID
	if p.Prepared != nil {
		unresolved = p.Prepared.Effects.unknownEffectIDs()
	}
	if !slices.Equal(p.Termination.UnresolvedEffectIDs(), unresolved) {
		return fmt.Errorf("%w: termination and interrupted Effects disagree", ErrInvalidSnapshot)
	}
	return nil
}

// result requires a validated capture, whose terminal status guarantees its
// finish time and termination.
func (p processSnapshotWire) result() (Result, bool) {
	if !p.status().Terminal() {
		return Result{}, false
	}
	return Result{
		processID: p.ProcessID, startedAt: p.StartedAt, finishedAt: *p.FinishedAt,
		output: p.Output, termination: *p.Termination, usage: p.usage(),
	}, true
}

func (p processSnapshotWire) usage() Usage {
	return p.Counters.usage(p.CommittedSteps, uint64(len(p.Mailbox.Signals)))
}
