package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const maxEventBytes = 1 << 20

const (
	// EventProcessStarted reports initial Process execution.
	EventProcessStarted = "agent.process.started"
	// EventProcessRestored reports execution resumed from a TreeSnapshot.
	EventProcessRestored = "agent.process.restored"
	// EventProcessPaused reports a committed scheduling pause.
	EventProcessPaused = "agent.process.paused"
	// EventProcessResumed reports committed scheduling resumption.
	EventProcessResumed = "agent.process.resumed"
	// EventProcessFinished reports one immutable terminal outcome.
	EventProcessFinished = "agent.process.finished"
	// EventRuntimeStopped reports loss of an active instance without a committed
	// logical terminal result. It never changes the durable Process lifecycle.
	EventRuntimeStopped = "agent.runtime.stopped"
	// EventSignalAccepted reports one newly accepted Signal.
	EventSignalAccepted = "agent.signal.accepted"
	// EventStepStarted reports an Execution.Step call about to begin.
	EventStepStarted = "agent.step.started"
	// EventStepFinished reports an Execution.Step return or failure.
	EventStepFinished = "agent.step.finished"
	// EventStepPrepared reports validated candidate Step state and fixed Effects.
	EventStepPrepared = "agent.step.prepared"
	// EventStepCommitted reports authoritative Step state publication.
	EventStepCommitted = "agent.step.committed"
	// EventEffectStarted reports a Framework or Dispatcher Effect attempt.
	EventEffectStarted = "agent.effect.started"
	// EventEffectFinished reports a definite or unknown attempt settlement.
	EventEffectFinished = "agent.effect.finished"
	// EventEffectResolved reports an Unknown settlement replaced by a definite
	// result, through host adjudication or an explicitly requested replay.
	EventEffectResolved = "agent.effect.resolved"
	// EventDeltaDropped reports best-effort increments lost to backpressure.
	EventDeltaDropped = "agent.delta.dropped"
)

var ErrInvalidEvent = errors.New("agent: invalid event")

// EventPhase distinguishes runtime observations from facts supported by
// authoritative Process state. Durable trees publish committed facts only after
// TreeDurability acknowledges the resulting tree state. Attempt facts do not
// assert a committed Process state change.
type EventPhase string

const (
	// EventPhaseInvalid is the invalid zero value.
	EventPhaseInvalid EventPhase = ""
	// EventPhaseAttempt identifies runtime work without a state commit guarantee.
	EventPhaseAttempt EventPhase = "attempt"
	// EventPhaseCommitted identifies a fact whose resulting state is acknowledged.
	EventPhaseCommitted EventPhase = "committed"
)

func (e EventPhase) Valid() bool {
	return e == EventPhaseAttempt || e == EventPhaseCommitted
}

func (e EventPhase) String() string {
	if !e.Valid() {
		return invalidEnumName
	}
	return string(e)
}

// Event is an immutable, ordered fact published by the Framework. Observers may
// project or instrument it, but observer failure never changes Process state.
// Payload is descriptive data, never a Signal or state mutation command.
type Event struct {
	eventFact
	processSequence uint64
}

// eventFact is validated before staging; publication alone assigns its sequence.
type eventFact struct {
	processID     ProcessID
	deploymentRef DeploymentRef
	relation      ProcessRelation
	incarnationID TreeIncarnationID
	stepSequence  uint64
	effectID      EffectID
	name          string
	phase         EventPhase
	occurredAt    time.Time
	payload       json.RawMessage
}

type eventSpec struct {
	processSequence uint64
	processID       ProcessID
	deploymentRef   DeploymentRef
	relation        ProcessRelation
	incarnationID   TreeIncarnationID
	stepSequence    uint64
	effectID        EffectID
	name            string
	phase           EventPhase
	occurredAt      time.Time
	payload         json.RawMessage
}

func newEvent(spec eventSpec) (Event, error) {
	if spec.processSequence == 0 {
		return Event{}, fmt.Errorf("%w: Process sequence must be greater than zero", ErrInvalidEvent)
	}
	fact, err := newEventFact(spec)
	if err != nil {
		return Event{}, err
	}
	return fact.publish(spec.processSequence), nil
}

func newEventFact(spec eventSpec) (eventFact, error) {
	if !spec.processID.Valid() {
		return eventFact{}, fmt.Errorf("%w: process ID: %w", ErrInvalidEvent, ErrInvalidIdentity)
	}
	if !spec.deploymentRef.Valid() {
		return eventFact{}, fmt.Errorf("%w: deployment: %w", ErrInvalidEvent, ErrInvalidDeploymentRef)
	}
	if !spec.relation.Valid() || spec.relation.ProcessID() != spec.processID {
		return eventFact{}, fmt.Errorf("%w: relation: %w", ErrInvalidEvent, ErrInvalidProcessRelation)
	}
	if spec.incarnationID != (TreeIncarnationID{}) && !spec.incarnationID.Valid() {
		return eventFact{}, fmt.Errorf("%w: tree incarnation is invalid", ErrInvalidEvent)
	}
	if !ValidQualifiedName(spec.name) {
		return eventFact{}, fmt.Errorf("%w: name must be a lowercase qualified name", ErrInvalidEvent)
	}
	if !spec.phase.Valid() {
		return eventFact{}, fmt.Errorf("%w: phase is required", ErrInvalidEvent)
	}
	if spec.occurredAt.IsZero() {
		return eventFact{}, fmt.Errorf("%w: occurrence time is required", ErrInvalidEvent)
	}
	normalized, err := normalizeJSON(spec.payload, maxEventBytes)
	if err != nil {
		return eventFact{}, fmt.Errorf("%w: payload: %w", ErrInvalidEvent, err)
	}
	event := eventFact{
		processID:     spec.processID,
		deploymentRef: spec.deploymentRef,
		relation:      spec.relation,
		incarnationID: spec.incarnationID,
		stepSequence:  spec.stepSequence,
		effectID:      spec.effectID,
		name:          spec.name,
		phase:         spec.phase,
		occurredAt:    spec.occurredAt.Round(0).UTC(),
		payload:       normalized,
	}
	if err := event.validateContract(); err != nil {
		return eventFact{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	return event, nil
}

func (e eventFact) publish(sequence uint64) Event {
	if sequence == 0 || e.name == "" {
		panic("agent: invalid Event publication")
	}
	return Event{eventFact: e, processSequence: sequence}
}

// ProcessSequence returns the Process-local publication order within one tree
// runtime activation, starting at one. Restoration starts a new sequence;
// publication progress is observation state and is not part of a TreeSnapshot.
func (e Event) ProcessSequence() uint64 { return e.processSequence }

// ProcessID returns the Process whose fact is described.
func (e Event) ProcessID() ProcessID { return e.processID }

// DeploymentRef returns the exact execution binding that emitted the fact.
func (e Event) DeploymentRef() DeploymentRef { return e.deploymentRef }

// Relation returns the Process tree location that emitted the fact.
func (e Event) Relation() ProcessRelation { return e.relation }

// TreeIncarnationID returns the active durable writer that emitted this event.
// Events from ephemeral trees return false.
func (e Event) TreeIncarnationID() (TreeIncarnationID, bool) {
	return e.incarnationID, e.incarnationID.Valid()
}

// StepSequence returns the one-based Step sequence and true, or zero and false
// for a Process fact outside a Step.
func (e Event) StepSequence() (uint64, bool) {
	return e.stepSequence, e.stepSequence > 0
}

// EffectID returns the related Effect identity and true when this is an Effect
// fact.
func (e Event) EffectID() (EffectID, bool) { return e.effectID, e.effectID.Valid() }

// Name returns the stable Framework fact name.
func (e Event) Name() string { return e.name }

// Phase returns whether the fact describes an attempt or committed state.
func (e Event) Phase() EventPhase { return e.phase }

// OccurredAt returns when the fact occurred.
func (e Event) OccurredAt() time.Time { return e.occurredAt }

// Payload returns an independently owned descriptive payload.
func (e Event) Payload() json.RawMessage { return bytes.Clone(e.payload) }

// ProcessFinished returns the typed terminal fact for EventProcessFinished.
func (e Event) ProcessFinished() (ProcessFinishedFact, bool) {
	if e.name != EventProcessFinished {
		return ProcessFinishedFact{}, false
	}
	fact, err := decodeProcessFinishedFact(e.payload)
	return fact, err == nil
}

// RuntimeStopped returns the typed instance failure for EventRuntimeStopped.
func (e Event) RuntimeStopped() (RuntimeStoppedFact, bool) {
	if e.name != EventRuntimeStopped {
		return RuntimeStoppedFact{}, false
	}
	fact, err := decodeRuntimeStoppedFact(e.payload)
	return fact, err == nil
}

// SignalAccepted returns the typed delivery fact for EventSignalAccepted.
func (e Event) SignalAccepted() (SignalAcceptedFact, bool) {
	if e.name != EventSignalAccepted {
		return SignalAcceptedFact{}, false
	}
	fact, err := decodeSignalAcceptedFact(e.payload)
	return fact, err == nil
}

// StepFinished returns the typed attempt fact for EventStepFinished.
func (e Event) StepFinished() (StepFinishedFact, bool) {
	if e.name != EventStepFinished {
		return StepFinishedFact{}, false
	}
	fact, err := decodeStepFinishedFact(e.payload)
	return fact, err == nil
}

// StepCommitted returns the typed state fact for EventStepCommitted.
func (e Event) StepCommitted() (StepCommittedFact, bool) {
	if e.name != EventStepCommitted {
		return StepCommittedFact{}, false
	}
	fact, err := decodeStepCommittedFact(e.payload)
	return fact, err == nil
}

// EffectStarted returns the typed target fact for EventEffectStarted.
func (e Event) EffectStarted() (EffectStartedFact, bool) {
	if e.name != EventEffectStarted {
		return EffectStartedFact{}, false
	}
	fact, err := decodeEffectStartedFact(e.payload)
	return fact, err == nil
}

// EffectFinished returns the typed settlement fact for EventEffectFinished.
func (e Event) EffectFinished() (EffectFinishedFact, bool) {
	if e.name != EventEffectFinished {
		return EffectFinishedFact{}, false
	}
	fact, err := decodeEffectFinishedFact(e.payload)
	return fact, err == nil
}

// EffectResolved returns the committed resolution fact for EventEffectResolved.
func (e Event) EffectResolved() (EffectResolvedFact, bool) {
	if e.name != EventEffectResolved {
		return EffectResolvedFact{}, false
	}
	fact, err := decodeEffectResolvedFact(e.payload)
	return fact, err == nil
}

// DeltaDropped returns the typed loss fact for EventDeltaDropped.
func (e Event) DeltaDropped() (DeltaDroppedFact, bool) {
	if e.name != EventDeltaDropped {
		return DeltaDroppedFact{}, false
	}
	fact, err := decodeDeltaDroppedFact(e.payload)
	return fact, err == nil
}

func (e Event) Valid() bool {
	return e.processSequence > 0 && e.name != ""
}

func (e Event) MarshalJSON() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidEvent
	}
	wire := eventWire{
		ProcessSequence: e.processSequence,
		ProcessID:       e.processID,
		DeploymentRef:   e.deploymentRef,
		Relation:        e.relation.wire(),
		StepSequence:    e.stepSequence,
		Name:            e.name,
		Phase:           e.phase,
		OccurredAt:      e.occurredAt,
		Payload:         e.payload,
	}
	if e.effectID.Valid() {
		wire.EffectID = &e.effectID
	}
	if e.incarnationID.Valid() {
		wire.IncarnationID = &e.incarnationID
	}
	return json.Marshal(wire)
}

func (e *Event) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidEvent)
	}
	wire, err := decodeJSON[eventWire](data)
	if err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidEvent, err)
	}
	var effectID EffectID
	if wire.EffectID != nil {
		effectID = *wire.EffectID
	}
	relation, err := processRelationFromWire(wire.ProcessID, wire.Relation)
	if err != nil {
		return fmt.Errorf("%w: relation: %w", ErrInvalidEvent, err)
	}
	value, err := newEvent(eventSpec{
		processSequence: wire.ProcessSequence,
		processID:       wire.ProcessID,
		deploymentRef:   wire.DeploymentRef,
		relation:        relation,
		incarnationID:   treeIncarnationOrZero(wire.IncarnationID),
		stepSequence:    wire.StepSequence,
		effectID:        effectID,
		name:            wire.Name,
		phase:           wire.Phase,
		occurredAt:      wire.OccurredAt,
		payload:         wire.Payload,
	})
	if err != nil {
		return err
	}
	*e = value
	return nil
}

func (e eventFact) validateContract() error {
	switch e.name {
	case EventProcessStarted, EventProcessRestored, EventProcessPaused, EventProcessResumed:
		return e.validateEmpty(EventPhaseCommitted, eventIdentityProcess)
	case EventProcessFinished:
		if err := e.validateIdentity(EventPhaseCommitted, eventIdentityProcess); err != nil {
			return err
		}
		_, err := decodeProcessFinishedFact(e.payload)
		return err
	case EventRuntimeStopped:
		if err := e.validateIdentity(EventPhaseAttempt, eventIdentityProcess); err != nil {
			return err
		}
		_, err := decodeRuntimeStoppedFact(e.payload)
		return err
	case EventSignalAccepted:
		if err := e.validateIdentity(EventPhaseCommitted, eventIdentityProcess); err != nil {
			return err
		}
		_, err := decodeSignalAcceptedFact(e.payload)
		return err
	case EventStepStarted, EventStepPrepared:
		return e.validateEmpty(EventPhaseAttempt, eventIdentityStep)
	case EventStepFinished:
		if err := e.validateIdentity(EventPhaseAttempt, eventIdentityStep); err != nil {
			return err
		}
		_, err := decodeStepFinishedFact(e.payload)
		return err
	case EventStepCommitted:
		if err := e.validateIdentity(EventPhaseCommitted, eventIdentityStep); err != nil {
			return err
		}
		_, err := decodeStepCommittedFact(e.payload)
		return err
	case EventEffectStarted:
		if err := e.validateIdentity(EventPhaseAttempt, eventIdentityEffect); err != nil {
			return err
		}
		_, err := decodeEffectStartedFact(e.payload)
		return err
	case EventEffectFinished:
		if err := e.validateIdentity(EventPhaseAttempt, eventIdentityEffect); err != nil {
			return err
		}
		_, err := decodeEffectFinishedFact(e.payload)
		return err
	case EventDeltaDropped:
		if err := e.validateIdentity(EventPhaseAttempt, eventIdentityEffect); err != nil {
			return err
		}
		_, err := decodeDeltaDroppedFact(e.payload)
		return err
	case EventEffectResolved:
		if err := e.validateIdentity(EventPhaseCommitted, eventIdentityEffect); err != nil {
			return err
		}
		_, err := decodeEffectResolvedFact(e.payload)
		return err
	default:
		return errors.New("unknown Framework event name")
	}
}

func (e eventFact) validateEmpty(wantPhase EventPhase, scope eventIdentityScope) error {
	if err := e.validateIdentity(wantPhase, scope); err != nil {
		return err
	}
	_, err := decodeJSON[struct{}](e.payload)
	return err
}

func (e eventFact) validateIdentity(wantPhase EventPhase, scope eventIdentityScope) error {
	if e.phase != wantPhase {
		return errors.New("event phase does not match its Framework fact")
	}
	switch scope {
	case eventIdentityProcess:
		if e.stepSequence != 0 || e.effectID.Valid() {
			return errors.New("process event cannot carry Step or Effect identity")
		}
	case eventIdentityStep:
		if e.stepSequence == 0 || e.effectID.Valid() {
			return errors.New("step event requires only a Step sequence")
		}
	case eventIdentityEffect:
		if e.stepSequence == 0 || !e.effectID.Valid() {
			return errors.New("effect event requires Step and Effect identity")
		}
	default:
		return errors.New("event identity scope is invalid")
	}
	return nil
}

type eventWire struct {
	ProcessSequence uint64              `json:"process_sequence"`
	ProcessID       ProcessID           `json:"process_id"`
	DeploymentRef   DeploymentRef       `json:"deployment_ref"`
	Relation        processRelationWire `json:"relation"`
	IncarnationID   *TreeIncarnationID  `json:"tree_incarnation_id,omitempty"`
	StepSequence    uint64              `json:"step_sequence,omitempty"`
	EffectID        *EffectID           `json:"effect_id,omitempty"`
	Name            string              `json:"name"`
	Phase           EventPhase          `json:"phase"`
	OccurredAt      time.Time           `json:"occurred_at"`
	Payload         json.RawMessage     `json:"payload"`
}

func treeIncarnationOrZero(value *TreeIncarnationID) TreeIncarnationID {
	if value == nil {
		return TreeIncarnationID{}
	}
	return *value
}

type eventIdentityScope uint8

const (
	eventIdentityProcess eventIdentityScope = iota + 1
	eventIdentityStep
	eventIdentityEffect
)
