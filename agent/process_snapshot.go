package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	maxSnapshotBytes = 128 << 20
)

var ErrInvalidSnapshot = errors.New("agent: invalid process snapshot")

// WaitKind identifies who may answer the current wait.
type WaitKind string

const (
	WaitKindExternal WaitKind = "external"
	WaitKindChildren WaitKind = "children"
)

// ProcessSnapshot is an immutable diagnostic capture of one Engine-owned
// Process. Strategy state and Effect payloads remain opaque. A ProcessSnapshot
// is not a recovery unit; only a complete TreeSnapshot can be restored.
// An interrupted terminal Process retains its prepared batch as evidence:
// settled operations keep their actual results, planned operations never run,
// and the candidate state and input cursor were not adopted.
// Parsing validates the captured state, not storage acknowledgment.
// [Engine.InspectTree] identifies the acknowledged head of its durable captures.
type ProcessSnapshot struct {
	data                    json.RawMessage
	processID               ProcessID
	deploymentRef           DeploymentRef
	status                  Status
	usage                   Usage
	committedExecutionState ExecutionState
	waitID                  WaitID
	waitKind                WaitKind
	relation                ProcessRelation
	budget                  Budget
	capabilities            CapabilitySet
	unknownEffectIDs        []EffectID
}

// ParseProcessSnapshot strictly validates one Process snapshot wire value,
// including single-answer wait history and an open, unanswered current wait
// when the Process is Waiting. Prepared Effects must fit the captured Process
// capability grant. Terminal prepared batches contain no pending attempt and
// their unknown identities must exactly match the Termination.
func ParseProcessSnapshot(data json.RawMessage) (ProcessSnapshot, error) {
	wire, err := decodeProcessSnapshot(data)
	if err != nil {
		return ProcessSnapshot{}, err
	}
	normalized, err := json.Marshal(wire)
	if err != nil {
		return ProcessSnapshot{}, fmt.Errorf("%w: encode: %w", ErrInvalidSnapshot, err)
	}
	if len(normalized) > maxSnapshotBytes {
		return ProcessSnapshot{}, fmt.Errorf("%w: exceeds %d bytes", ErrInvalidSnapshot, maxSnapshotBytes)
	}
	var unknownEffectIDs []EffectID
	if wire.Prepared != nil {
		unknownEffectIDs = wire.Prepared.Effects.unknownEffectIDs()
	}
	var waitKind WaitKind
	for _, wait := range wire.Mailbox.Waits {
		if wire.CurrentWaitID != nil && wait.WaitID == *wire.CurrentWaitID {
			waitKind = WaitKindChildren
			if wait.ExternallyAddressable {
				waitKind = WaitKindExternal
			}
			break
		}
	}
	return ProcessSnapshot{
		data:                    normalized,
		processID:               wire.ProcessID,
		deploymentRef:           wire.DeploymentRef,
		status:                  wire.Status,
		usage:                   wire.Usage,
		committedExecutionState: wire.CommittedExecutionState,
		waitID:                  snapshotWaitID(wire.CurrentWaitID),
		waitKind:                waitKind,
		relation:                mustProcessRelation(wire.ProcessID, wire.Relation),
		budget:                  wire.Budget,
		capabilities:            wire.Capabilities,
		unknownEffectIDs:        unknownEffectIDs,
	}, nil
}

func newProcessSnapshot(wire processSnapshotWire) (ProcessSnapshot, error) {
	data, err := json.Marshal(wire)
	if err != nil {
		return ProcessSnapshot{}, fmt.Errorf("%w: encode: %w", ErrInvalidSnapshot, err)
	}
	return ParseProcessSnapshot(data)
}

// JSON returns an independently owned snapshot representation.
func (p ProcessSnapshot) JSON() json.RawMessage { return bytes.Clone(p.data) }

// ProcessID returns the captured Process identity.
func (p ProcessSnapshot) ProcessID() ProcessID { return p.processID }

// DeploymentRef returns the exact execution binding required for restoration.
func (p ProcessSnapshot) DeploymentRef() DeploymentRef { return p.deploymentRef }

// Relation returns the immutable parent/root/depth location captured with the
// Process.
func (p ProcessSnapshot) Relation() ProcessRelation { return p.relation }

// Budget returns the Process work allocation captured by this snapshot.
func (p ProcessSnapshot) Budget() Budget { return p.budget }

// Capabilities returns the Process authority set captured by this snapshot.
func (p ProcessSnapshot) Capabilities() CapabilitySet { return p.capabilities }

// Status returns the captured common lifecycle state.
func (p ProcessSnapshot) Status() Status { return p.status }

// Usage returns the Framework counters recorded in this capture.
func (p ProcessSnapshot) Usage() Usage { return p.usage }

// UnknownEffectIDs returns Effects whose captured settlement requires explicit
// resolution. RuntimeError separately owns outcomes an instance could not confirm.
// A terminal capture retains unresolved evidence; its Process cannot resume or
// accept further resolution commands.
func (p ProcessSnapshot) UnknownEffectIDs() []EffectID {
	return slices.Clone(p.unknownEffectIDs)
}

// CommittedExecutionState returns the latest committed opaque Strategy state.
// A prepared candidate, when present, remains an uncommitted Engine detail.
// Only the owning Definition or its typed inspection helpers may interpret the
// returned state's payload.
func (p ProcessSnapshot) CommittedExecutionState() ExecutionState {
	return p.committedExecutionState.clone()
}

// WaitID returns the current Engine-minted wait identity and true when the
// captured Process is Waiting.
func (p ProcessSnapshot) WaitID() (WaitID, bool) {
	return p.waitID, p.status == StatusWaiting && p.waitID.Valid()
}

// WaitKind distinguishes Host input from Framework child completion while the
// captured Process is Waiting. It derives from the existing wait authority.
func (p ProcessSnapshot) WaitKind() (WaitKind, bool) {
	if p.status != StatusWaiting || !p.waitID.Valid() {
		return "", false
	}
	return p.waitKind, true
}

func (p ProcessSnapshot) Valid() bool {
	return len(p.data) > 0 && p.processID.Valid() && p.deploymentRef.Valid() &&
		p.status.Valid() && p.committedExecutionState.Valid() && p.relation.Valid() &&
		p.budget.Valid() && p.capabilities.Valid()
}

func mustProcessRelation(processID ProcessID, wire processRelationWire) ProcessRelation {
	relation, _ := processRelationFromWire(processID, wire)
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
	return decodeProcessSnapshot(p.data)
}

type preparedEffectWire struct {
	ID         EffectID    `json:"id"`
	Effect     Effect      `json:"effect"`
	Phase      effectPhase `json:"phase"`
	WaitID     *WaitID     `json:"wait_id,omitempty"`
	Settlement *Settlement `json:"settlement,omitempty"`
}

type preparedStepWire struct {
	StepSequence                  uint64          `json:"step_sequence"`
	CommittedExecutionStateDigest Digest          `json:"committed_execution_state_digest"`
	CandidateState                ExecutionState  `json:"candidate_state"`
	SignalCursor                  uint64          `json:"signal_cursor"`
	Transition                    Transition      `json:"transition"`
	Effects                       preparedEffects `json:"effects,omitempty"`
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
	Budget                  Budget              `json:"budget"`
	ReservedBudget          Budget              `json:"reserved_child_budget"`
	Capabilities            CapabilitySet       `json:"capabilities"`
	Usage                   Usage               `json:"usage"`
	CommittedExecutionState ExecutionState      `json:"committed_execution_state"`
	Mailbox                 mailboxWire         `json:"mailbox"`
	Prepared                *preparedStepWire   `json:"prepared,omitempty"`
	CurrentWaitID           *WaitID             `json:"current_wait_id,omitempty"`
	PauseReason             string              `json:"pause_reason,omitempty"`
	PendingControl          pendingControlWire  `json:"pending_control"`
	Output                  *Output             `json:"output,omitempty"`
	Termination             *Termination        `json:"termination,omitempty"`
}

func decodeProcessSnapshot(data []byte) (processSnapshotWire, error) {
	if len(data) == 0 || len(data) > maxSnapshotBytes {
		return processSnapshotWire{}, fmt.Errorf("%w: JSON must contain at most %d bytes", ErrInvalidSnapshot, maxSnapshotBytes)
	}
	wire, err := wireJSON.decode[processSnapshotWire](data)
	if err != nil {
		return processSnapshotWire{}, fmt.Errorf("%w: decode: %w", ErrInvalidSnapshot, err)
	}
	if err := validateProcessSnapshot(wire); err != nil {
		return processSnapshotWire{}, err
	}
	return wire, nil
}

func validateProcessSnapshot(wire processSnapshotWire) error {
	if err := wire.validateContract(); err != nil {
		return err
	}
	if err := wire.validateRelation(); err != nil {
		return err
	}
	mailbox, err := restoreSignalMailbox(wire.Mailbox, wire.Status)
	if err != nil {
		return fmt.Errorf("%w: mailbox: %w", ErrInvalidSnapshot, err)
	}
	if err := wire.validateProgress(mailbox); err != nil {
		return err
	}
	if err := validateSnapshotLifecycle(wire, mailbox); err != nil {
		return err
	}
	if _, err := pendingControlFromWire(wire.PendingControl); err != nil {
		return fmt.Errorf("%w: pending control: %w", ErrInvalidSnapshot, err)
	}
	return nil
}

func (p processSnapshotWire) validateContract() error {
	if !p.ProcessID.Valid() || !p.DeploymentRef.Valid() || p.StartedAt.IsZero() ||
		!p.Status.Valid() || p.Status == StatusNotStarted || !p.CommittedExecutionState.Valid() ||
		!p.Limits.Valid() || !p.TreeLimits.Valid() || !p.Budget.Valid() ||
		!p.Capabilities.Valid() || !p.Usage.validFor(p.Limits) ||
		p.Usage.CommittedSteps != p.CommittedSteps ||
		p.Limits.MaxSteps != p.Budget.Steps ||
		p.Limits.MaxEffects != p.Budget.Effects ||
		p.Limits.MaxSignals != p.Budget.Signals ||
		!p.Budget.contains(p.Usage, p.ReservedBudget) {
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
	if p.Usage.AcceptedSignals != mailbox.arrivalSequence() {
		return fmt.Errorf("%w: accepted Signal count does not match mailbox", ErrInvalidSnapshot)
	}
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
		if err := validatePreparedStep(
			p.ProcessID, p.CommittedSteps+1, p.CommittedExecutionState, mailbox, *p.Prepared,
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
		if p.Usage.PreparedEffects < uint64(len(p.Prepared.Effects)) {
			return fmt.Errorf("%w: prepared Effect identities exceed recorded usage", ErrInvalidSnapshot)
		}
		if !p.Status.Terminal() {
			remainingPending -= uint64(p.Prepared.Transition.ConsumedSignals())
			reserved = uint64(len(p.Prepared.Effects))
			preparedSteps = 1
		}
	}
	if !resourceQuantitiesFit(p.Limits.MaxPendingSignals, mailbox.pendingCount()) ||
		!resourceQuantitiesFit(p.Limits.MaxPendingSignals, remainingPending, reserved) ||
		!resourceQuantitiesFit(p.Budget.Signals, p.Usage.AcceptedSignals, p.ReservedBudget.Signals, reserved) ||
		!resourceQuantitiesFit(p.Budget.Steps, p.CommittedSteps, p.ReservedBudget.Steps, preparedSteps) {
		return fmt.Errorf("%w: execution capacity exceeds limits or budget", ErrInvalidSnapshot)
	}
	return nil
}

func validateSnapshotLifecycle(wire processSnapshotWire, mailbox signalMailbox) error {
	terminal := wire.Status.Terminal()
	if terminal != (wire.Termination != nil) || terminal != (wire.FinishedAt != nil) {
		return fmt.Errorf("%w: terminal status, termination, and finished time must agree", ErrInvalidSnapshot)
	}
	if wire.FinishedAt != nil && wire.FinishedAt.Before(wire.StartedAt) {
		return fmt.Errorf("%w: finished time precedes started time", ErrInvalidSnapshot)
	}
	if terminal && (wire.Termination.Status() != wire.Status || !wire.Termination.Valid()) {
		return fmt.Errorf("%w: termination does not match status", ErrInvalidSnapshot)
	}
	if wire.Status == StatusCompleted {
		if wire.Output == nil || !wire.Output.Valid() {
			return fmt.Errorf("%w: completed process requires output", ErrInvalidSnapshot)
		}
	} else if wire.Output != nil {
		return fmt.Errorf("%w: only Completed Process may contain Output", ErrInvalidSnapshot)
	}
	if wire.Status == StatusWaiting {
		if wire.CurrentWaitID == nil || !wire.CurrentWaitID.Valid() {
			return fmt.Errorf("%w: waiting process requires current WaitID", ErrInvalidSnapshot)
		}
		if shouldWait, err := mailbox.enterWait(*wire.CurrentWaitID); err != nil || !shouldWait {
			return fmt.Errorf("%w: current WaitID requires an open unanswered wait", ErrInvalidSnapshot)
		}
	} else if wire.CurrentWaitID != nil {
		return fmt.Errorf("%w: current WaitID requires Waiting status", ErrInvalidSnapshot)
	}
	if wire.Status == StatusPaused {
		if err := validateTerminationReason(wire.PauseReason); err != nil {
			return fmt.Errorf("%w: invalid pause reason", ErrInvalidSnapshot)
		}
	} else if wire.PauseReason != "" {
		return fmt.Errorf("%w: pause reason requires Paused status", ErrInvalidSnapshot)
	}
	if terminal {
		if !emptyPendingControl(wire.PendingControl) {
			return fmt.Errorf("%w: terminal Process cannot retain control state", ErrInvalidSnapshot)
		}
		var unresolved []EffectID
		if wire.Prepared != nil {
			unresolved = wire.Prepared.Effects.unknownEffectIDs()
		}
		if !slices.Equal(wire.Termination.UnresolvedEffectIDs(), unresolved) {
			return fmt.Errorf("%w: termination and interrupted Effects disagree", ErrInvalidSnapshot)
		}
	}
	return nil
}

func validatePreparedStep(processID ProcessID, sequence uint64, committedState ExecutionState, mailbox signalMailbox, prepared preparedStepWire) error {
	if prepared.StepSequence != sequence || !prepared.CandidateState.Valid() || !prepared.Transition.Valid() ||
		prepared.SignalCursor < mailbox.committedSignalCursor() || prepared.SignalCursor > mailbox.arrivalSequence() {
		return errors.New("invalid prepared Step boundary")
	}
	digest, err := executionStateDigest(committedState)
	if err != nil || digest != prepared.CommittedExecutionStateDigest {
		return errors.New("prepared Step does not identify committed Execution state")
	}
	if prepared.SignalCursor != mailbox.committedSignalCursor()+uint64(prepared.Transition.ConsumedSignals()) {
		return errors.New("prepared Step consumption does not match Transition")
	}
	effects := prepared.Transition.Effects()
	if len(effects) != len(prepared.Effects) {
		return errors.New("prepared Effect count does not match Transition")
	}
	for index, record := range prepared.Effects {
		if effectErr := validatePreparedEffect(processID, sequence, index, effects[index], record); effectErr != nil {
			return effectErr
		}
	}
	_, err = prepared.Effects.next()
	return err
}

func validatePreparedEffect(
	processID ProcessID,
	sequence uint64,
	index int,
	effect Effect,
	record preparedEffectWire,
) error {
	wantID := deriveEffectID(processID, sequence, index)
	if record.ID != wantID || !equalEffect(record.Effect, effect) {
		return errors.New("prepared Effect identity or payload changed")
	}
	if record.Effect.Target() != EffectTargetFramework {
		if record.WaitID != nil {
			return errors.New("dispatcher Effect cannot contain WaitID")
		}
		return nil
	}
	return validatePreparedFrameworkEffect(record)
}

func validatePreparedFrameworkEffect(record preparedEffectWire) error {
	operation, err := decodeFrameworkEffectOperation(record.Effect.Payload())
	if err != nil {
		return err
	}
	switch operation {
	case frameworkEffectWait:
		return validatePreparedWaitEffect(record, "wait Effect")
	case frameworkEffectStartChild:
		if record.WaitID != nil ||
			record.Settlement != nil && record.Settlement.Status() == SettlementStatusUnknown {
			return errors.New("child-start Effect has an invalid settlement")
		}
		return nil
	case frameworkEffectWaitChildren:
		return validatePreparedWaitEffect(record, "child-wait Effect")
	default:
		return errors.New("unsupported framework Effect")
	}
}

func validatePreparedWaitEffect(record preparedEffectWire, name string) error {
	if record.WaitID != nil && *record.WaitID != deriveWaitID(record.ID) {
		return fmt.Errorf("%s contains a non-derived WaitID", name)
	}
	if (record.WaitID == nil) != (record.Phase != effectPhaseSettled) ||
		record.Settlement != nil && record.Settlement.Status() == SettlementStatusUnknown {
		return fmt.Errorf("%s has an incomplete or unknown settlement", name)
	}
	return nil
}

func emptyPendingControl(control pendingControlWire) bool { return control == pendingControlWire{} }

func executionStateDigest(state ExecutionState) (Digest, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return Digest{}, err
	}
	return digestBytes(data), nil
}

func deriveEffectID(processID ProcessID, step uint64, index int) EffectID {
	digest := digestBytes([]byte(fmt.Sprintf("%s\x00%d\x00%d", processID.String(), step, index)))
	id, err := ParseEffectID(effectIDPrefix + digest.hex())
	if err != nil {
		panic(err)
	}
	return id
}

func deriveWaitID(effectID EffectID) WaitID {
	digest := digestBytes([]byte("wait\x00" + effectID.String()))
	id, err := ParseWaitID(waitIDPrefix + digest.hex())
	if err != nil {
		panic(err)
	}
	return id
}

func deriveSettlementSignalID(effectID EffectID) SignalID {
	digest := digestBytes([]byte("signal\x00" + effectID.String()))
	id, err := ParseSignalID(signalIDPrefix + digest.hex())
	if err != nil {
		panic(err)
	}
	return id
}

func equalEffect(left, right Effect) bool {
	return left.Target() == right.Target() && bytes.Equal(left.Payload(), right.Payload()) &&
		slices.Equal(left.RequiredCapabilities().values, right.RequiredCapabilities().values)
}
