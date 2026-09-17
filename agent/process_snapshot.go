package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

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
// Parsing validates the captured state, not storage acknowledgment.
// [Engine.InspectTree] identifies the acknowledged head of its durable captures.
type ProcessSnapshot struct {
	data  json.RawMessage
	state processSnapshotWire
}

// ParseProcessSnapshot strictly validates one Process snapshot wire value,
// including single-answer wait history and an open, unanswered current wait
// when the Process is Waiting. Prepared Effects must fit the captured Process
// capability grant. Terminal prepared batches contain no pending attempt and
// their unknown identities must exactly match the Termination.
func ParseProcessSnapshot(data json.RawMessage) (ProcessSnapshot, error) {
	wire, err := jsonwire.Decode[processSnapshotWire](data, "limits", "allocated_resources")
	if err != nil {
		return ProcessSnapshot{}, fmt.Errorf("%w: decode: %w", ErrInvalidSnapshot, err)
	}
	if !wire.Limits.MaxSnapshotBytes.Allows(uint64(len(data))) {
		return ProcessSnapshot{}, fmt.Errorf("%w: snapshot byte quota exceeded", ErrInvalidSnapshot)
	}
	return processSnapshotFromWire(wire)
}

func newProcessSnapshot(wire processSnapshotWire) (ProcessSnapshot, error) {
	return processSnapshotFromWire(wire.clone())
}

// The caller transfers the wire's mutable containers. After validation, state
// is immutable and data is its encoded projection; neither is updated in place.
func processSnapshotFromWire(wire processSnapshotWire) (ProcessSnapshot, error) {
	if err := wire.validate(); err != nil {
		return ProcessSnapshot{}, err
	}
	normalized, err := json.Marshal(wire)
	if err != nil {
		return ProcessSnapshot{}, fmt.Errorf("%w: encode: %w", ErrInvalidSnapshot, err)
	}
	if !wire.Limits.MaxSnapshotBytes.Allows(uint64(len(normalized))) {
		return ProcessSnapshot{}, fmt.Errorf("%w: snapshot byte quota exceeded", ErrInvalidSnapshot)
	}
	return ProcessSnapshot{data: normalized, state: wire}, nil
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

// ProcessID returns the captured Process identity.
func (p ProcessSnapshot) ProcessID() ProcessID {
	return p.state.ProcessID
}

// DeploymentRef returns the exact execution binding required for restoration.
func (p ProcessSnapshot) DeploymentRef() DeploymentRef {
	return p.state.DeploymentRef
}

// Relation returns the immutable parent/root/depth location captured with the
// Process.
func (p ProcessSnapshot) Relation() ProcessRelation {
	if !p.Valid() {
		return ProcessRelation{}
	}
	return mustProcessRelation(p.state.ProcessID, p.state.Relation)
}

// Budget returns the Process work allocation captured by this snapshot.
func (p ProcessSnapshot) Budget() Budget {
	return p.state.Limits.Budget
}

// Capabilities returns the Process authority set captured by this snapshot.
func (p ProcessSnapshot) Capabilities() CapabilitySet {
	return p.state.Capabilities
}

// Status returns the captured common lifecycle state.
func (p ProcessSnapshot) Status() Status {
	return p.state.Status
}

// Usage returns the Framework counters recorded in this capture.
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

// Result returns the captured immutable terminal outcome, when present. Its
// persistence authority is that of the enclosing TreeSnapshot transaction.
func (p ProcessSnapshot) Result() (Result, bool) {
	if !p.Status().Terminal() {
		return Result{}, false
	}
	result := Result{processID: p.state.ProcessID, startedAt: p.state.StartedAt,
		finishedAt: *p.state.FinishedAt, termination: *p.state.Termination,
		usage: p.state.usage()}
	if p.state.Output != nil {
		result.output = *p.state.Output
	}
	return result, true
}

// WaitID returns the current Engine-minted wait identity and true when the
// captured Process is Waiting.
func (p ProcessSnapshot) WaitID() (WaitID, bool) {
	if p.state.Status != StatusWaiting {
		return WaitID{}, false
	}
	waitID := snapshotWaitID(p.state.CurrentWaitID)
	return waitID, waitID.Valid()
}

// WaitKind distinguishes Host input from Framework child completion while the
// captured Process is Waiting. It derives from the existing wait authority.
func (p ProcessSnapshot) WaitKind() (WaitKind, bool) {
	waitID, waiting := p.WaitID()
	if !waiting {
		return "", false
	}
	wait, found := p.state.Mailbox.waitRecord(waitID)
	if !found {
		return "", false
	}
	return wait.Kind, true
}

func (p ProcessSnapshot) Valid() bool { return len(p.data) > 0 }

func mustProcessRelation(processID ProcessID, wire processRelationWire) ProcessRelation {
	relation, err := processRelationFromWire(processID, wire)
	if err != nil {
		panic(err)
	}
	return relation
}

func snapshotWaitID(waitID *WaitID) WaitID {
	if waitID == nil {
		return WaitID{}
	}
	return *waitID
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
	Failure            *Failure          `json:"failure,omitempty"`
	KillReason         string            `json:"kill_reason,omitempty"`
	DeadlineOwner      deadlineOwner     `json:"deadline_owner,omitempty"`
	DeadlineReason     string            `json:"deadline_reason,omitempty"`
	CancellationOwner  cancellationOwner `json:"cancellation_owner,omitempty"`
	CancellationReason string            `json:"cancellation_reason,omitempty"`
	PauseReason        string            `json:"pause_reason,omitempty"`
}

type processSnapshotWire struct {
	ProcessID               ProcessID           `json:"process_id"`
	Relation                processRelationWire `json:"relation"`
	ChildRequestDigest      *Digest             `json:"child_request_digest,omitempty"`
	DeploymentRef           DeploymentRef       `json:"deployment_ref"`
	StartedAt               time.Time           `json:"started_at"`
	FinishedAt              *time.Time          `json:"finished_at,omitempty"`
	Status                  Status              `json:"status"`
	CommittedSteps          uint64              `json:"committed_steps"`
	Limits                  Limits              `json:"limits"`
	TreeLimits              TreeLimits          `json:"tree_limits"`
	AllocatedResources      resourceAmounts     `json:"allocated_resources"`
	Capabilities            CapabilitySet       `json:"capabilities"`
	Counters                processCounters     `json:"counters"`
	CommittedExecutionState ExecutionState      `json:"committed_execution_state"`
	Mailbox                 mailboxWire         `json:"mailbox"`
	Prepared                *preparedStep       `json:"prepared,omitempty"`
	CurrentWaitID           *WaitID             `json:"current_wait_id,omitempty"`
	PauseReason             string              `json:"pause_reason,omitempty"`
	PendingControl          pendingControlWire  `json:"pending_control"`
	Output                  *Payload            `json:"output,omitempty"`
	Termination             *Termination        `json:"termination,omitempty"`
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
	if p.Output != nil {
		clone.Output = new(*p.Output)
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
	}
	clone.Mailbox.Waits = slices.Clone(p.Mailbox.Waits)
	if p.Prepared != nil {
		prepared := p.Prepared.clone()
		clone.Prepared = &prepared
	}
	return clone
}

func (p processSnapshotWire) validateContract() error {
	if !p.ProcessID.Valid() || !p.DeploymentRef.Valid() || p.StartedAt.IsZero() ||
		!p.Status.Valid() || !p.CommittedExecutionState.Valid() ||
		!p.Limits.Valid() || !p.TreeLimits.Valid() ||
		!p.Capabilities.Valid() ||
		!p.Limits.Budget.contains(p.usage(), p.AllocatedResources) {
		return fmt.Errorf("%w: incomplete Process identity or state", ErrInvalidSnapshot)
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
	remainingPending := mailbox.pendingCount()
	var reserved uint64
	var preparedSteps uint64
	if p.Prepared != nil {
		if p.Status != StatusRunning && !p.Status.Terminal() || p.Status == StatusCompleted {
			return fmt.Errorf("%w: prepared Step requires Running or interrupted terminal status", ErrInvalidSnapshot)
		}
		const maxUint64 = ^uint64(0)
		if !resourceQuantitiesFit(maxUint64, p.CommittedSteps, 1) {
			return fmt.Errorf("%w: prepared Step sequence overflows", ErrInvalidSnapshot)
		}
		if err := p.Prepared.validate(
			p.ProcessID, p.CommittedSteps+1, p.CommittedExecutionState, mailbox,
		); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
		}
		for _, record := range p.Prepared.Effects {
			if !p.Capabilities.Allows(record.Effect.RequiredCapabilities()) {
				return fmt.Errorf("%w: prepared Effect capability denied: %w", ErrInvalidSnapshot, ErrInvalidCapability)
			}
			if p.Status.Terminal() && record.Phase == effectPhasePending {
				return fmt.Errorf("%w: terminal Process cannot retain pending Effects", ErrInvalidSnapshot)
			}
		}
		if p.usage().PreparedEffects < p.Prepared.settlementSignalCount() {
			return fmt.Errorf("%w: prepared Effect identities exceed recorded usage", ErrInvalidSnapshot)
		}
		if !p.Status.Terminal() {
			// Prepared validation bounds its cursor by accepted Signals and anchors
			// consumption at the mailbox committed cursor, so this cannot underflow.
			remainingPending -= p.Prepared.consumedSignals()
			reserved = p.Prepared.settlementSignalCount()
			preparedSteps = 1
		}
	}
	if !resourceQuantitiesFit(p.Limits.MaxPendingSignals, mailbox.pendingCount()) ||
		!resourceQuantitiesFit(p.Limits.MaxPendingSignals, remainingPending, reserved) ||
		!p.Limits.Budget.Signals.Allows(p.usage().AcceptedSignals, p.AllocatedResources.Signals, reserved) ||
		!p.Limits.Budget.Steps.Allows(p.CommittedSteps, p.AllocatedResources.Steps, preparedSteps) {
		return fmt.Errorf("%w: execution capacity exceeds limits or budget", ErrInvalidSnapshot)
	}
	return nil
}

func (p processSnapshotWire) validate() error {
	if err := p.validateContract(); err != nil {
		return err
	}
	if err := p.validateRelation(); err != nil {
		return err
	}
	mailbox, err := restoreSignalMailbox(p.Mailbox, p.Status)
	if err != nil {
		return fmt.Errorf("%w: mailbox: %w", ErrInvalidSnapshot, err)
	}
	if err := p.validateProgress(mailbox); err != nil {
		return err
	}
	if err := p.validateLifecycle(mailbox); err != nil {
		return err
	}
	if _, err := pendingControlFromWire(p.PendingControl); err != nil {
		return fmt.Errorf("%w: pending control: %w", ErrInvalidSnapshot, err)
	}
	return nil
}

func (p processSnapshotWire) validateLifecycle(mailbox signalMailbox) error {
	terminal := p.Status.Terminal()
	if terminal != (p.Termination != nil) || terminal != (p.FinishedAt != nil) {
		return fmt.Errorf("%w: terminal status, termination, and finished time must agree", ErrInvalidSnapshot)
	}
	if p.FinishedAt != nil && p.FinishedAt.IsZero() {
		return fmt.Errorf("%w: finished time is required", ErrInvalidSnapshot)
	}
	if terminal && (p.Termination.Status() != p.Status || !p.Termination.Valid()) {
		return fmt.Errorf("%w: termination does not match status", ErrInvalidSnapshot)
	}
	if p.Status == StatusCompleted {
		if p.Output == nil || !p.Output.Valid() {
			return fmt.Errorf("%w: completed process requires output", ErrInvalidSnapshot)
		}
	} else if p.Output != nil {
		return fmt.Errorf("%w: only Completed Process may contain Output", ErrInvalidSnapshot)
	}
	if p.Status == StatusWaiting {
		if p.CurrentWaitID == nil || !p.CurrentWaitID.Valid() {
			return fmt.Errorf("%w: waiting process requires current WaitID", ErrInvalidSnapshot)
		}
		if shouldWait, err := mailbox.enterWait(*p.CurrentWaitID); err != nil || !shouldWait {
			return fmt.Errorf("%w: current WaitID requires an open unanswered wait", ErrInvalidSnapshot)
		}
	} else if p.CurrentWaitID != nil {
		return fmt.Errorf("%w: current WaitID requires Waiting status", ErrInvalidSnapshot)
	}
	if p.Status == StatusPaused {
		if err := validateTerminationReason(p.PauseReason); err != nil {
			return fmt.Errorf("%w: invalid pause reason", ErrInvalidSnapshot)
		}
	} else if p.PauseReason != "" {
		return fmt.Errorf("%w: pause reason requires Paused status", ErrInvalidSnapshot)
	}
	if terminal {
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
	}
	return nil
}

func (p processSnapshotWire) usage() Usage {
	return Usage{CommittedSteps: p.CommittedSteps, AcceptedSignals: uint64(len(p.Mailbox.Signals)), PreparedEffects: p.Counters.PreparedEffects, DroppedDeltas: p.Counters.DroppedDeltas}
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
