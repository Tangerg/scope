package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"time"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

// StepStatus reports whether one Step reduction succeeded, and is deliberately
// narrower than [Status]: a failed Step does not by itself terminate a Process,
// because the terminal decision also depends on recorded control intent.
type StepStatus string

const (
	StepStatusInvalid   StepStatus = ""
	StepStatusSucceeded StepStatus = "succeeded"
	StepStatusFailed    StepStatus = "failed"
	StepStatusDiscarded StepStatus = "discarded"
)

func (s StepStatus) Valid() bool {
	switch s {
	case StepStatusSucceeded, StepStatusFailed, StepStatusDiscarded:
		return true
	default:
		return false
	}
}

func (s StepStatus) String() string {
	if !s.Valid() {
		return invalidEnumName
	}
	return string(s)
}

type effectStartedEventPayload struct {
	AttemptID    EffectAttemptID `json:"attempt_id"`
	EffectTarget EffectTarget    `json:"effect_target"`
}

type effectFinishedEventPayload struct {
	AttemptID        EffectAttemptID  `json:"attempt_id"`
	EffectTarget     EffectTarget     `json:"effect_target"`
	SettlementStatus SettlementStatus `json:"settlement_status"`
	DurationMS       *int64           `json:"duration_ms"`
	FailureKind      FailureKind      `json:"failure_kind,omitzero"`
	FailureCode      string           `json:"failure_code,omitempty"`
}

type effectResolvedEventPayload struct {
	EffectTarget     EffectTarget     `json:"effect_target"`
	SettlementStatus SettlementStatus `json:"settlement_status"`
}

// EffectResolved records the definite result that replaced an Unknown
// settlement. It has no duration: adjudication is not an execution attempt.
type EffectResolved struct {
	target     EffectTarget
	settlement SettlementStatus
}

func (e EffectResolved) Target() EffectTarget { return e.target }

func (e EffectResolved) SettlementStatus() SettlementStatus { return e.settlement }

func (e EffectResolved) Valid() bool {
	return e.target.Valid() && e.settlement.Valid() && e.settlement != SettlementStatusUnknown
}

func decodeEffectResolved(payload json.RawMessage) (EffectResolved, error) {
	wire, err := jsonwire.Decode[effectResolvedEventPayload](payload)
	if err != nil {
		return EffectResolved{}, err
	}
	fact := EffectResolved{target: wire.EffectTarget, settlement: wire.SettlementStatus}
	if !fact.Valid() {
		return EffectResolved{}, errors.New("invalid Effect resolution event fact")
	}
	return fact, nil
}

type signalAcceptedEventPayload struct {
	SignalID string `json:"signal_id"`
	WaitID   string `json:"wait_id,omitempty"`
}

type processFinishedEventPayload struct {
	ProcessStatus    Status           `json:"process_status"`
	TerminationCause TerminationCause `json:"termination_cause"`
	FailureKind      FailureKind      `json:"failure_kind,omitzero"`
	FailureCode      string           `json:"failure_code,omitempty"`
	Usage            *Usage           `json:"usage"`
}

type runtimeStoppedEventPayload struct {
	FailureKind FailureKind `json:"failure_kind"`
	FailureCode string      `json:"failure_code"`
}

type stepFinishedEventPayload struct {
	StepStatus      StepStatus `json:"step_status"`
	WorkDurationNS  *int64     `json:"work_duration_ns"`
	AdoptionDelayNS *int64     `json:"adoption_delay_ns"`
}

type stepCommittedEventPayload struct {
	ProcessStatus Status `json:"process_status"`
}

type deltaDroppedEventPayload struct {
	AttemptID         EffectAttemptID `json:"attempt_id"`
	DroppedDeltaCount uint64          `json:"dropped_delta_count"`
}

// ProcessFinished is the immutable terminal fact carried by a finished
// Process Event. Usage is the authoritative Framework-owned terminal usage.
type ProcessFinished struct {
	cause   TerminationCause
	failure FailureClassification
	usage   Usage
}

func (p ProcessFinished) Status() Status { return p.cause.status() }

func (p ProcessFinished) Cause() TerminationCause { return p.cause }

// Failure is present exactly when the Process failed.
func (p ProcessFinished) Failure() (FailureClassification, bool) {
	return p.failure, p.Status() == StatusFailed
}

func (p ProcessFinished) Usage() Usage { return p.usage }

func (p ProcessFinished) Valid() bool {
	if !p.Status().Terminal() {
		return false
	}
	if p.Status() != StatusFailed {
		return p.failure == FailureClassification{}
	}
	return p.failure.Valid() && p.cause == p.failure.kind.terminationCause()
}

// RuntimeStopped describes an instance failure, not a logical Process
// termination. It contains only the failure classification, never storage error
// messages or application payloads. Event carries the Process and incarnation.
type RuntimeStopped struct{ failure FailureClassification }

func (r RuntimeStopped) Failure() FailureClassification { return r.failure }

func (r RuntimeStopped) Valid() bool { return r.failure.Valid() }

func decodeRuntimeStopped(payload json.RawMessage) (RuntimeStopped, error) {
	wire, err := jsonwire.Decode[runtimeStoppedEventPayload](payload)
	if err != nil {
		return RuntimeStopped{}, err
	}
	fact := RuntimeStopped{failure: FailureClassification{kind: wire.FailureKind, code: wire.FailureCode}}
	if !fact.Valid() {
		return RuntimeStopped{}, errors.New("invalid Runtime stopped event fact")
	}
	return fact, nil
}

// SignalAccepted is the immutable delivery identity carried by an accepted
// Signal Event. WaitID is present only for a wait-addressed Signal.
type SignalAccepted struct {
	signalID SignalID
	waitID   WaitID
}

func (s SignalAccepted) SignalID() SignalID { return s.signalID }

func (s SignalAccepted) WaitID() (WaitID, bool) { return s.waitID, s.waitID.Valid() }

func (s SignalAccepted) Valid() bool {
	return s.signalID.Valid() && (s.waitID == (WaitID{}) || s.waitID.Valid())
}

// StepFinished closes one physical attempt, including discarded candidates.
// WorkDuration covers Step, Snapshot, and Restore in the worker. AdoptionDelay
// covers completion delivery and waiting for the tree owner, including barriers.
// Both use monotonic elapsed time, independent of lifecycle wall-clock stamps.
// Attempts with the same logical StepSequence are paired in activation-local
// event order; the previous attempt finishes before another starts.
type StepFinished struct {
	status        StepStatus
	workDuration  time.Duration
	adoptionDelay time.Duration
}

func (s StepFinished) Status() StepStatus           { return s.status }
func (s StepFinished) WorkDuration() time.Duration  { return s.workDuration }
func (s StepFinished) AdoptionDelay() time.Duration { return s.adoptionDelay }
func (s StepFinished) Valid() bool {
	return s.status.Valid() && s.workDuration >= 0 && s.adoptionDelay >= 0
}

type StepCommitted struct{ status Status }

func (s StepCommitted) Status() Status { return s.status }

func (s StepCommitted) Valid() bool { return s.status.Valid() }

type EffectStarted struct {
	target    EffectTarget
	attemptID EffectAttemptID
}

func (e EffectStarted) Target() EffectTarget { return e.target }

func (e EffectStarted) AttemptID() EffectAttemptID { return e.attemptID }

func (e EffectStarted) Valid() bool { return e.target.Valid() && e.attemptID.Valid() }

// EffectFinished is the immutable settlement observation for one Effect
// attempt. It does not replace the durable Effect boundary.
type EffectFinished struct {
	attemptID  EffectAttemptID
	target     EffectTarget
	settlement SettlementStatus
	duration   time.Duration
	failure    FailureClassification
}

func (e EffectFinished) Target() EffectTarget { return e.target }

func (e EffectFinished) AttemptID() EffectAttemptID { return e.attemptID }

func (e EffectFinished) SettlementStatus() SettlementStatus { return e.settlement }

func (e EffectFinished) Duration() time.Duration { return e.duration }

// Failure classifies the Dispatcher error that made the outcome
// Unknown, without diagnostic text. An Unknown returned directly by the
// Dispatcher has no classification.
func (e EffectFinished) Failure() (FailureClassification, bool) {
	return e.failure, e.failure != FailureClassification{}
}

func (e EffectFinished) Valid() bool {
	if !e.attemptID.Valid() || !e.target.Valid() || !e.settlement.Valid() || e.duration < 0 {
		return false
	}
	if e.failure == (FailureClassification{}) {
		return true
	}
	return e.target == EffectTargetDispatcher && e.settlement == SettlementStatusUnknown && e.failure.Valid()
}

// DeltaDropped reports the number of increments rejected during one Effect
// attempt because validation failed or the bounded observation queue was full.
type DeltaDropped struct {
	count     uint64
	attemptID EffectAttemptID
}

func (d DeltaDropped) Count() uint64 { return d.count }

func (d DeltaDropped) AttemptID() EffectAttemptID { return d.attemptID }

func (d DeltaDropped) Valid() bool { return d.count > 0 && d.attemptID.Valid() }

func decodeProcessFinished(payload json.RawMessage) (ProcessFinished, error) {
	wire, err := jsonwire.Decode[processFinishedEventPayload](payload)
	if err != nil || wire.Usage == nil {
		return ProcessFinished{}, errors.New("invalid Process finished event payload")
	}
	fact := ProcessFinished{
		cause:   wire.TerminationCause,
		failure: FailureClassification{kind: wire.FailureKind, code: wire.FailureCode},
		usage:   *wire.Usage,
	}
	if !fact.Valid() || wire.ProcessStatus != fact.Status() {
		return ProcessFinished{}, errors.New("invalid Process finished event fact")
	}
	return fact, nil
}

func decodeSignalAccepted(payload json.RawMessage) (SignalAccepted, error) {
	wire, err := jsonwire.Decode[signalAcceptedEventPayload](payload)
	if err != nil {
		return SignalAccepted{}, err
	}
	signalID, err := ParseSignalID(wire.SignalID)
	if err != nil {
		return SignalAccepted{}, err
	}
	fact := SignalAccepted{signalID: signalID}
	if wire.WaitID != "" {
		fact.waitID, err = ParseWaitID(wire.WaitID)
		if err != nil {
			return SignalAccepted{}, err
		}
	}
	return fact, nil
}

func decodeStepFinished(payload json.RawMessage) (StepFinished, error) {
	wire, err := jsonwire.Decode[stepFinishedEventPayload](payload)
	if err != nil || wire.WorkDurationNS == nil || wire.AdoptionDelayNS == nil {
		return StepFinished{}, errors.New("invalid Step finished event payload")
	}
	fact := StepFinished{status: wire.StepStatus, workDuration: time.Duration(*wire.WorkDurationNS), adoptionDelay: time.Duration(*wire.AdoptionDelayNS)}
	if !fact.Valid() {
		return StepFinished{}, errors.New("invalid Step finished event fact")
	}
	return fact, nil
}

func decodeStepCommitted(payload json.RawMessage) (StepCommitted, error) {
	wire, err := jsonwire.Decode[stepCommittedEventPayload](payload)
	if err != nil {
		return StepCommitted{}, err
	}
	fact := StepCommitted{status: wire.ProcessStatus}
	if !fact.Valid() {
		return StepCommitted{}, errors.New("invalid Step committed event fact")
	}
	return fact, nil
}

func decodeEffectStarted(payload json.RawMessage) (EffectStarted, error) {
	wire, err := jsonwire.Decode[effectStartedEventPayload](payload)
	if err != nil {
		return EffectStarted{}, err
	}
	fact := EffectStarted{target: wire.EffectTarget, attemptID: wire.AttemptID}
	if !fact.Valid() {
		return EffectStarted{}, errors.New("invalid Effect started event fact")
	}
	return fact, nil
}

func decodeEffectFinished(payload json.RawMessage) (EffectFinished, error) {
	wire, err := jsonwire.Decode[effectFinishedEventPayload](payload)
	if err != nil || wire.DurationMS == nil || *wire.DurationMS < 0 {
		return EffectFinished{}, errors.New("invalid Effect finished event payload")
	}
	duration, ok := durationFromMilliseconds(*wire.DurationMS)
	if !ok {
		return EffectFinished{}, errors.New("effect duration overflows time.Duration")
	}
	fact := EffectFinished{
		attemptID: wire.AttemptID,
		target:    wire.EffectTarget, settlement: wire.SettlementStatus, duration: duration,
		failure: FailureClassification{kind: wire.FailureKind, code: wire.FailureCode},
	}
	if !fact.Valid() {
		return EffectFinished{}, errors.New("invalid Effect finished event fact")
	}
	return fact, nil
}

func decodeDeltaDropped(payload json.RawMessage) (DeltaDropped, error) {
	wire, err := jsonwire.Decode[deltaDroppedEventPayload](payload)
	if err != nil {
		return DeltaDropped{}, err
	}
	fact := DeltaDropped{count: wire.DroppedDeltaCount, attemptID: wire.AttemptID}
	if !fact.Valid() {
		return DeltaDropped{}, errors.New("invalid Delta dropped event fact")
	}
	return fact, nil
}

func durationFromMilliseconds(milliseconds int64) (time.Duration, bool) {
	const maxMilliseconds = math.MaxInt64 / int64(time.Millisecond)
	if milliseconds < 0 || milliseconds > maxMilliseconds {
		return 0, false
	}
	return time.Duration(milliseconds) * time.Millisecond, true
}

// Kernel facts use closed payloads; failure to encode one is a programming error.
func marshalEventPayload(payload any) json.RawMessage {
	encoded, err := jsonv2.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encoded
}
