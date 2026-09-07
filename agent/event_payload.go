package agent

import (
	"encoding/json"
	"errors"
	"time"
)

// StepStatus reports whether one Step reduction succeeded, and is deliberately
// narrower than [Status]: a failed Step does not by itself terminate a Process,
// because the terminal decision also depends on recorded control intent.
type StepStatus string

// Step status is separate from Process status because a failed Step does not
// by itself terminate a Process; the terminal decision also weighs recorded
// control intent.
const (
	StepStatusSucceeded StepStatus = "succeeded"
	StepStatusFailed    StepStatus = "failed"
)

func (s StepStatus) Valid() bool {
	return s == StepStatusSucceeded || s == StepStatusFailed
}

func (s StepStatus) String() string {
	if !s.Valid() {
		return invalidEnumName
	}
	return string(s)
}

type effectStartedEventPayload struct {
	EffectTarget EffectTarget `json:"effect_target"`
}

type effectFinishedEventPayload struct {
	EffectTarget     EffectTarget     `json:"effect_target"`
	SettlementStatus SettlementStatus `json:"settlement_status"`
	DurationMS       *int64           `json:"duration_ms"`
}

type signalAcceptedEventPayload struct {
	SignalID string `json:"signal_id"`
	WaitID   string `json:"wait_id,omitempty"`
}

type processFinishedEventPayload struct {
	ProcessStatus    Status           `json:"process_status"`
	TerminationCause TerminationCause `json:"termination_cause"`
	FailureKind      FailureKind      `json:"failure_kind,omitempty"`
	FailureCode      string           `json:"failure_code,omitempty"`
	Usage            *Usage           `json:"usage"`
}

type runtimeStoppedEventPayload struct {
	FailureKind FailureKind `json:"failure_kind"`
	FailureCode string      `json:"failure_code"`
}

type stepFinishedEventPayload struct {
	StepStatus StepStatus `json:"step_status"`
	DurationMS *int64     `json:"duration_ms"`
}

type stepCommittedEventPayload struct {
	ProcessStatus Status `json:"process_status"`
}

type deltaDroppedEventPayload struct {
	DroppedDeltaCount uint64 `json:"dropped_delta_count"`
}

type eventIdentityScope uint8

const (
	eventIdentityProcess eventIdentityScope = iota + 1
	eventIdentityStep
	eventIdentityEffect
)

func validateEventContract(
	name string,
	phase EventPhase,
	stepSequence uint64,
	effectID EffectID,
	payload json.RawMessage,
) error {
	switch name {
	case EventProcessStarted, EventProcessRestored, EventProcessPaused, EventProcessResumed:
		return validateEmptyEvent(phase, EventPhaseCommitted, stepSequence, effectID, eventIdentityProcess, payload)
	case EventProcessFinished:
		if err := validateEventIdentity(phase, EventPhaseCommitted, stepSequence, effectID, eventIdentityProcess); err != nil {
			return err
		}
		_, err := decodeProcessFinishedFact(payload)
		return err
	case EventRuntimeStopped:
		if err := validateEventIdentity(phase, EventPhaseAttempt, stepSequence, effectID, eventIdentityProcess); err != nil {
			return err
		}
		_, err := decodeRuntimeStoppedFact(payload)
		return err
	case EventSignalAccepted:
		if err := validateEventIdentity(phase, EventPhaseCommitted, stepSequence, effectID, eventIdentityProcess); err != nil {
			return err
		}
		_, err := decodeSignalAcceptedFact(payload)
		return err
	case EventStepStarted, EventStepPrepared:
		return validateEmptyEvent(phase, EventPhaseAttempt, stepSequence, effectID, eventIdentityStep, payload)
	case EventStepFinished:
		if err := validateEventIdentity(phase, EventPhaseAttempt, stepSequence, effectID, eventIdentityStep); err != nil {
			return err
		}
		_, err := decodeStepFinishedFact(payload)
		return err
	case EventStepCommitted:
		if err := validateEventIdentity(phase, EventPhaseCommitted, stepSequence, effectID, eventIdentityStep); err != nil {
			return err
		}
		_, err := decodeStepCommittedFact(payload)
		return err
	case EventEffectStarted:
		if err := validateEventIdentity(phase, EventPhaseAttempt, stepSequence, effectID, eventIdentityEffect); err != nil {
			return err
		}
		_, err := decodeEffectStartedFact(payload)
		return err
	case EventEffectFinished:
		if err := validateEventIdentity(phase, EventPhaseAttempt, stepSequence, effectID, eventIdentityEffect); err != nil {
			return err
		}
		_, err := decodeEffectFinishedFact(payload)
		return err
	case EventDeltaDropped:
		if err := validateEventIdentity(phase, EventPhaseAttempt, stepSequence, effectID, eventIdentityEffect); err != nil {
			return err
		}
		_, err := decodeDeltaDroppedFact(payload)
		return err
	default:
		return errors.New("unknown Framework event name")
	}
}

func validateEmptyEvent(
	phase EventPhase,
	wantPhase EventPhase,
	stepSequence uint64,
	effectID EffectID,
	scope eventIdentityScope,
	payload json.RawMessage,
) error {
	if err := validateEventIdentity(phase, wantPhase, stepSequence, effectID, scope); err != nil {
		return err
	}
	_, err := wireJSON.decode[struct{}](payload)
	return err
}

func validateEventIdentity(
	phase EventPhase,
	wantPhase EventPhase,
	stepSequence uint64,
	effectID EffectID,
	scope eventIdentityScope,
) error {
	if phase != wantPhase {
		return errors.New("event phase does not match its Framework fact")
	}
	switch scope {
	case eventIdentityProcess:
		if stepSequence != 0 || effectID.Valid() {
			return errors.New("process event cannot carry Step or Effect identity")
		}
	case eventIdentityStep:
		if stepSequence == 0 || effectID.Valid() {
			return errors.New("step event requires only a Step sequence")
		}
	case eventIdentityEffect:
		if stepSequence == 0 || !effectID.Valid() {
			return errors.New("effect event requires Step and Effect identity")
		}
	default:
		return errors.New("event identity scope is invalid")
	}
	return nil
}

// ProcessFinishedFact is the immutable terminal fact carried by a finished
// Process Event. Usage is the authoritative Framework-owned terminal usage.
type ProcessFinishedFact struct {
	status      Status
	cause       TerminationCause
	failureKind FailureKind
	failureCode string
	usage       Usage
}

func (p ProcessFinishedFact) Status() Status { return p.status }

func (p ProcessFinishedFact) Cause() TerminationCause { return p.cause }

func (p ProcessFinishedFact) Failure() (FailureKind, string, bool) {
	return p.failureKind, p.failureCode, p.status == StatusFailed
}

func (p ProcessFinishedFact) Usage() Usage { return p.usage }

func (p ProcessFinishedFact) Valid() bool {
	return validProcessFinishedFact(p)
}

// RuntimeStoppedFact describes an instance failure, not a logical Process
// termination. It contains only the failure classification, never storage error
// messages or application payloads. Event carries the Process and incarnation.
type RuntimeStoppedFact struct {
	failureKind FailureKind
	failureCode string
}

func (r RuntimeStoppedFact) FailureKind() FailureKind { return r.failureKind }

func (r RuntimeStoppedFact) FailureCode() string { return r.failureCode }

func (r RuntimeStoppedFact) Valid() bool {
	return r.failureKind.Valid() && validQualifiedName(r.failureCode) && len(r.failureCode) <= maxFailureCodeBytes
}

func decodeRuntimeStoppedFact(payload json.RawMessage) (RuntimeStoppedFact, error) {
	wire, err := wireJSON.decode[runtimeStoppedEventPayload](payload)
	if err != nil {
		return RuntimeStoppedFact{}, err
	}
	fact := RuntimeStoppedFact{failureKind: wire.FailureKind, failureCode: wire.FailureCode}
	if !fact.Valid() {
		return RuntimeStoppedFact{}, errors.New("invalid Runtime stopped event fact")
	}
	return fact, nil
}

// SignalAcceptedFact is the immutable delivery identity carried by an accepted
// Signal Event. WaitID is present only for a wait-addressed Signal.
type SignalAcceptedFact struct {
	signalID SignalID
	waitID   WaitID
}

func (s SignalAcceptedFact) SignalID() SignalID { return s.signalID }

func (s SignalAcceptedFact) WaitID() (WaitID, bool) { return s.waitID, s.waitID.Valid() }

func (s SignalAcceptedFact) Valid() bool {
	return s.signalID.Valid() && (s.waitID == (WaitID{}) || s.waitID.Valid())
}

// StepFinishedFact is the immutable outcome of one Execution.Step attempt.
type StepFinishedFact struct {
	status   StepStatus
	duration time.Duration
}

func (s StepFinishedFact) Status() StepStatus { return s.status }

func (s StepFinishedFact) Duration() time.Duration { return s.duration }

func (s StepFinishedFact) Valid() bool { return s.status.Valid() && s.duration >= 0 }

// StepCommittedFact is the Process status installed by one committed Step.
type StepCommittedFact struct{ status Status }

func (s StepCommittedFact) Status() Status { return s.status }

func (s StepCommittedFact) Valid() bool { return s.status.Valid() && s.status != StatusNotStarted }

// EffectStartedFact identifies the target of one Effect attempt.
type EffectStartedFact struct{ target EffectTarget }

func (e EffectStartedFact) Target() EffectTarget { return e.target }

func (e EffectStartedFact) Valid() bool { return e.target.Valid() }

// EffectFinishedFact is the immutable settlement observation for one Effect
// attempt. It does not replace the durable Effect boundary.
type EffectFinishedFact struct {
	target     EffectTarget
	settlement SettlementStatus
	duration   time.Duration
}

func (e EffectFinishedFact) Target() EffectTarget { return e.target }

func (e EffectFinishedFact) SettlementStatus() SettlementStatus { return e.settlement }

func (e EffectFinishedFact) Duration() time.Duration { return e.duration }

func (e EffectFinishedFact) Valid() bool {
	return e.target.Valid() && e.settlement.Valid() && e.duration >= 0
}

// DeltaDroppedFact reports the number of increments rejected during one Effect
// attempt because validation failed or the bounded observation queue was full.
type DeltaDroppedFact struct{ count uint64 }

func (d DeltaDroppedFact) Count() uint64 { return d.count }

func (d DeltaDroppedFact) Valid() bool { return d.count > 0 }

func decodeProcessFinishedFact(payload json.RawMessage) (ProcessFinishedFact, error) {
	wire, err := wireJSON.decode[processFinishedEventPayload](payload)
	if err != nil || wire.Usage == nil {
		return ProcessFinishedFact{}, errors.New("invalid Process finished event payload")
	}
	fact := ProcessFinishedFact{
		status: wire.ProcessStatus, cause: wire.TerminationCause,
		failureKind: wire.FailureKind, failureCode: wire.FailureCode,
		usage: *wire.Usage,
	}
	if !fact.Valid() {
		return ProcessFinishedFact{}, errors.New("invalid Process finished event fact")
	}
	return fact, nil
}

func validProcessFinishedFact(fact ProcessFinishedFact) bool {
	if !fact.status.Terminal() || !fact.cause.Valid() {
		return false
	}
	failed := fact.status == StatusFailed
	if failed != (fact.failureKind.Valid() && validQualifiedName(fact.failureCode) && len(fact.failureCode) <= maxFailureCodeBytes) {
		return false
	}
	if !failed && (fact.failureKind != FailureKindInvalid || fact.failureCode != "") {
		return false
	}
	switch fact.status {
	case StatusCompleted:
		return fact.cause == TerminationCauseCompletion
	case StatusFailed:
		switch fact.failureKind {
		case FailureKindExecution:
			return fact.cause == TerminationCauseExecutionFailure
		case FailureKindContract:
			return fact.cause == TerminationCauseContractFailure
		case FailureKindExternal:
			return fact.cause == TerminationCauseExternalFailure
		case FailureKindPanic:
			return fact.cause == TerminationCausePanic
		default:
			return false
		}
	case StatusCanceled:
		return fact.cause == TerminationCauseParentCancellation ||
			fact.cause == TerminationCauseHostCancellation
	case StatusTimedOut:
		return fact.cause == TerminationCauseProcessDeadline ||
			fact.cause == TerminationCauseParentDeadline ||
			fact.cause == TerminationCauseHostDeadline
	case StatusKilled:
		return fact.cause == TerminationCauseEngineKill
	default:
		return false
	}
}

func decodeSignalAcceptedFact(payload json.RawMessage) (SignalAcceptedFact, error) {
	wire, err := wireJSON.decode[signalAcceptedEventPayload](payload)
	if err != nil {
		return SignalAcceptedFact{}, err
	}
	signalID, err := ParseSignalID(wire.SignalID)
	if err != nil {
		return SignalAcceptedFact{}, err
	}
	fact := SignalAcceptedFact{signalID: signalID}
	if wire.WaitID != "" {
		fact.waitID, err = ParseWaitID(wire.WaitID)
		if err != nil {
			return SignalAcceptedFact{}, err
		}
	}
	return fact, nil
}

func decodeStepFinishedFact(payload json.RawMessage) (StepFinishedFact, error) {
	wire, err := wireJSON.decode[stepFinishedEventPayload](payload)
	if err != nil || wire.DurationMS == nil || *wire.DurationMS < 0 {
		return StepFinishedFact{}, errors.New("invalid Step finished event payload")
	}
	duration, ok := durationFromMilliseconds(*wire.DurationMS)
	if !ok {
		return StepFinishedFact{}, errors.New("step duration overflows time.Duration")
	}
	fact := StepFinishedFact{status: wire.StepStatus, duration: duration}
	if !fact.Valid() {
		return StepFinishedFact{}, errors.New("invalid Step finished event fact")
	}
	return fact, nil
}

func decodeStepCommittedFact(payload json.RawMessage) (StepCommittedFact, error) {
	wire, err := wireJSON.decode[stepCommittedEventPayload](payload)
	if err != nil {
		return StepCommittedFact{}, err
	}
	fact := StepCommittedFact{status: wire.ProcessStatus}
	if !fact.Valid() {
		return StepCommittedFact{}, errors.New("invalid Step committed event fact")
	}
	return fact, nil
}

func decodeEffectStartedFact(payload json.RawMessage) (EffectStartedFact, error) {
	wire, err := wireJSON.decode[effectStartedEventPayload](payload)
	if err != nil {
		return EffectStartedFact{}, err
	}
	fact := EffectStartedFact{target: wire.EffectTarget}
	if !fact.Valid() {
		return EffectStartedFact{}, errors.New("invalid Effect started event fact")
	}
	return fact, nil
}

func decodeEffectFinishedFact(payload json.RawMessage) (EffectFinishedFact, error) {
	wire, err := wireJSON.decode[effectFinishedEventPayload](payload)
	if err != nil || wire.DurationMS == nil || *wire.DurationMS < 0 {
		return EffectFinishedFact{}, errors.New("invalid Effect finished event payload")
	}
	duration, ok := durationFromMilliseconds(*wire.DurationMS)
	if !ok {
		return EffectFinishedFact{}, errors.New("effect duration overflows time.Duration")
	}
	fact := EffectFinishedFact{
		target: wire.EffectTarget, settlement: wire.SettlementStatus, duration: duration,
	}
	if !fact.Valid() {
		return EffectFinishedFact{}, errors.New("invalid Effect finished event fact")
	}
	return fact, nil
}

func decodeDeltaDroppedFact(payload json.RawMessage) (DeltaDroppedFact, error) {
	wire, err := wireJSON.decode[deltaDroppedEventPayload](payload)
	if err != nil {
		return DeltaDroppedFact{}, err
	}
	fact := DeltaDroppedFact{count: wire.DroppedDeltaCount}
	if !fact.Valid() {
		return DeltaDroppedFact{}, errors.New("invalid Delta dropped event fact")
	}
	return fact, nil
}

func durationFromMilliseconds(milliseconds int64) (time.Duration, bool) {
	const maxMilliseconds = int64(^uint64(0)>>1) / int64(time.Millisecond)
	if milliseconds < 0 || milliseconds > maxMilliseconds {
		return 0, false
	}
	return time.Duration(milliseconds) * time.Millisecond, true
}
