package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

const maxEventBytes = 1 << 20

const (
	EventProcessStarted  = "agent.process.started"
	EventProcessRestored = "agent.process.restored"
	EventProcessPaused   = "agent.process.paused"
	EventProcessResumed  = "agent.process.resumed"
	EventProcessFinished = "agent.process.finished"
	// EventRuntimeStopped reports loss of an active instance without a committed
	// logical terminal result. It never changes the durable Process lifecycle.
	EventRuntimeStopped = "agent.runtime.stopped"
	EventSignalAccepted = "agent.signal.accepted"
	EventStepStarted    = "agent.step.started"
	EventStepFinished   = "agent.step.finished"
	EventStepPrepared   = "agent.step.prepared"
	EventStepCommitted  = "agent.step.committed"
	EventEffectStarted  = "agent.effect.started"
	EventEffectFinished = "agent.effect.finished"
	// EventEffectResolved reports an Unknown settlement replaced by a definite
	// result, through host adjudication or an explicitly requested replay.
	EventEffectResolved = "agent.effect.resolved"
	EventDeltaDropped   = "agent.delta.dropped"
)

var ErrInvalidEvent = errors.New("agent: invalid event")

// EventPhase distinguishes runtime observations from facts supported by
// authoritative Process state. Engines publish committed facts only after
// TreeCommitter acknowledges the resulting tree state. Attempt facts do not
// assert a committed Process state change.
type EventPhase string

const (
	EventPhaseInvalid   EventPhase = ""
	EventPhaseAttempt   EventPhase = "attempt"
	EventPhaseCommitted EventPhase = "committed"
)

func (e EventPhase) Valid() bool {
	switch e {
	case EventPhaseAttempt, EventPhaseCommitted:
		return true
	default:
		return false
	}
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
	eventDraft
	processSequence uint64
}

// eventDraft is validated before staging; publication alone assigns its sequence.
// Its phase is not stored: frameworkEventContracts fixes it for each name.
type eventDraft struct {
	deploymentRef DeploymentRef
	relation      ProcessRelation
	incarnationID TreeIncarnationID
	stepSequence  uint64
	effectID      EffectID
	name          string
	occurredAt    time.Time
	payload       json.RawMessage
}

func newEvent(fact eventDraft, processSequence uint64) (Event, error) {
	if processSequence == 0 {
		return Event{}, fmt.Errorf("%w: Process sequence must be greater than zero", ErrInvalidEvent)
	}
	validated, err := newEventDraft(fact)
	if err != nil {
		return Event{}, err
	}
	return validated.publish(processSequence), nil
}

// newEventDraft validates fact against its Framework contract and returns it
// with a normalized payload and UTC occurrence time.
func newEventDraft(fact eventDraft) (eventDraft, error) {
	if !fact.deploymentRef.Valid() {
		return eventDraft{}, fmt.Errorf("%w: deployment: %w", ErrInvalidEvent, ErrInvalidDeploymentRef)
	}
	if !fact.relation.Valid() {
		return eventDraft{}, fmt.Errorf("%w: relation: %w", ErrInvalidEvent, ErrInvalidProcessRelation)
	}
	if fact.incarnationID != (TreeIncarnationID{}) && !fact.incarnationID.Valid() {
		return eventDraft{}, fmt.Errorf("%w: tree incarnation is invalid", ErrInvalidEvent)
	}
	if !ValidQualifiedName(fact.name) {
		return eventDraft{}, fmt.Errorf("%w: name must be a lowercase qualified name", ErrInvalidEvent)
	}
	if fact.occurredAt.IsZero() {
		return eventDraft{}, fmt.Errorf("%w: occurrence time is required", ErrInvalidEvent)
	}
	normalized, err := normalizeJSON(fact.payload, maxEventBytes)
	if err != nil {
		return eventDraft{}, fmt.Errorf("%w: payload: %w", ErrInvalidEvent, err)
	}
	fact.occurredAt = canonicalTime(fact.occurredAt)
	fact.payload = normalized
	if err := fact.validateContract(); err != nil {
		return eventDraft{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	return fact, nil
}

func (e eventDraft) processID() ProcessID { return e.relation.ProcessID() }

func (e eventDraft) phase() EventPhase { return frameworkEventContracts[e.name].phase }

func (e eventDraft) publish(sequence uint64) Event {
	if sequence == 0 || e.name == "" {
		panic("agent: invalid Event publication")
	}
	return Event{eventDraft: e, processSequence: sequence}
}

// ProcessSequence returns the Process-local publication order within one tree
// runtime activation, starting at one. Restoration starts a new sequence;
// publication progress is observation state and is not part of a TreeSnapshot.
func (e Event) ProcessSequence() uint64 { return e.processSequence }

func (e Event) ProcessID() ProcessID { return e.processID() }

func (e Event) DeploymentRef() DeploymentRef { return e.deploymentRef }

func (e Event) Relation() ProcessRelation { return e.relation }

// TreeIncarnationID returns the active writer that emitted this event.
func (e Event) TreeIncarnationID() (TreeIncarnationID, bool) {
	return e.incarnationID, e.incarnationID.Valid()
}

// StepSequence returns the one-based Step sequence and true, or zero and false
// for a Process fact outside a Step.
func (e Event) StepSequence() (uint64, bool) {
	return e.stepSequence, e.stepSequence > 0
}

// EffectID returns the related Effect identity and true for an Effect fact.
func (e Event) EffectID() (EffectID, bool) { return e.effectID, e.effectID.Valid() }

func (e Event) Name() string { return e.name }

func (e Event) Phase() EventPhase { return e.phase() }

func (e Event) OccurredAt() time.Time { return e.occurredAt }

// Payload returns an independently owned descriptive payload.
func (e Event) Payload() json.RawMessage { return bytes.Clone(e.payload) }

func (e Event) ProcessFinished() (ProcessFinished, bool) {
	if e.name != EventProcessFinished {
		return ProcessFinished{}, false
	}
	fact, err := decodeProcessFinished(e.payload)
	return fact, err == nil
}

func (e Event) RuntimeStopped() (RuntimeStopped, bool) {
	if e.name != EventRuntimeStopped {
		return RuntimeStopped{}, false
	}
	fact, err := decodeRuntimeStopped(e.payload)
	return fact, err == nil
}

func (e Event) SignalAccepted() (SignalAccepted, bool) {
	if e.name != EventSignalAccepted {
		return SignalAccepted{}, false
	}
	fact, err := decodeSignalAccepted(e.payload)
	return fact, err == nil
}

func (e Event) StepFinished() (StepFinished, bool) {
	if e.name != EventStepFinished {
		return StepFinished{}, false
	}
	fact, err := decodeStepFinished(e.payload)
	return fact, err == nil
}

func (e Event) StepCommitted() (StepCommitted, bool) {
	if e.name != EventStepCommitted {
		return StepCommitted{}, false
	}
	fact, err := decodeStepCommitted(e.payload)
	return fact, err == nil
}

func (e Event) EffectStarted() (EffectStarted, bool) {
	if e.name != EventEffectStarted {
		return EffectStarted{}, false
	}
	fact, err := decodeEffectStarted(e.payload)
	return fact, err == nil
}

func (e Event) EffectFinished() (EffectFinished, bool) {
	if e.name != EventEffectFinished {
		return EffectFinished{}, false
	}
	fact, err := decodeEffectFinished(e.payload)
	return fact, err == nil
}

func (e Event) EffectResolved() (EffectResolved, bool) {
	if e.name != EventEffectResolved {
		return EffectResolved{}, false
	}
	fact, err := decodeEffectResolved(e.payload)
	return fact, err == nil
}

func (e Event) DeltaDropped() (DeltaDropped, bool) {
	if e.name != EventDeltaDropped {
		return DeltaDropped{}, false
	}
	fact, err := decodeDeltaDropped(e.payload)
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
		ProcessID:       e.processID(),
		DeploymentRef:   e.deploymentRef,
		Relation:        e.relation.wire(),
		StepSequence:    e.stepSequence,
		Name:            e.name,
		Phase:           e.phase(),
		OccurredAt:      e.occurredAt,
		Payload:         e.payload,
	}
	if e.effectID.Valid() {
		wire.EffectID = &e.effectID
	}
	if e.incarnationID.Valid() {
		wire.IncarnationID = &e.incarnationID
	}
	return jsonv2.Marshal(wire)
}

func (e *Event) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidEvent)
	}
	wire, err := jsonwire.Decode[eventWire](data)
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
	value, err := newEvent(eventDraft{
		deploymentRef: wire.DeploymentRef,
		relation:      relation,
		incarnationID: lo.FromPtr(wire.IncarnationID),
		stepSequence:  wire.StepSequence,
		effectID:      effectID,
		name:          wire.Name,
		occurredAt:    wire.OccurredAt,
		payload:       wire.Payload,
	}, wire.ProcessSequence)
	if err != nil {
		return err
	}
	if wire.Phase != value.Phase() {
		return fmt.Errorf("%w: phase does not match its Framework fact", ErrInvalidEvent)
	}
	*e = value
	return nil
}

// eventContract fixes the phase, identity scope, and payload shape of one
// Framework event name.
type eventContract struct {
	phase           EventPhase
	scope           eventIdentityScope
	validatePayload func(json.RawMessage) error
}

var frameworkEventContracts = map[string]eventContract{
	EventProcessStarted:  {EventPhaseCommitted, eventIdentityProcess, validateEmptyEventPayload},
	EventProcessRestored: {EventPhaseCommitted, eventIdentityProcess, validateEmptyEventPayload},
	EventProcessPaused:   {EventPhaseCommitted, eventIdentityProcess, validateEmptyEventPayload},
	EventProcessResumed:  {EventPhaseCommitted, eventIdentityProcess, validateEmptyEventPayload},
	EventProcessFinished: {EventPhaseCommitted, eventIdentityProcess, decodesEventPayload(decodeProcessFinished)},
	EventRuntimeStopped:  {EventPhaseAttempt, eventIdentityProcess, decodesEventPayload(decodeRuntimeStopped)},
	EventSignalAccepted:  {EventPhaseCommitted, eventIdentityProcess, decodesEventPayload(decodeSignalAccepted)},
	EventStepStarted:     {EventPhaseAttempt, eventIdentityStep, validateEmptyEventPayload},
	EventStepPrepared:    {EventPhaseAttempt, eventIdentityStep, validateEmptyEventPayload},
	EventStepFinished:    {EventPhaseAttempt, eventIdentityStep, decodesEventPayload(decodeStepFinished)},
	EventStepCommitted:   {EventPhaseCommitted, eventIdentityStep, decodesEventPayload(decodeStepCommitted)},
	EventEffectStarted:   {EventPhaseAttempt, eventIdentityEffect, decodesEventPayload(decodeEffectStarted)},
	EventEffectFinished:  {EventPhaseAttempt, eventIdentityEffect, decodesEventPayload(decodeEffectFinished)},
	EventDeltaDropped:    {EventPhaseAttempt, eventIdentityEffect, decodesEventPayload(decodeDeltaDropped)},
	EventEffectResolved:  {EventPhaseCommitted, eventIdentityEffect, decodesEventPayload(decodeEffectResolved)},
}

func decodesEventPayload[T any](decode func(json.RawMessage) (T, error)) func(json.RawMessage) error {
	return func(payload json.RawMessage) error {
		_, err := decode(payload)
		return err
	}
}

func validateEmptyEventPayload(payload json.RawMessage) error {
	_, err := jsonwire.Decode[struct{}](payload)
	return err
}

func (e eventDraft) validateContract() error {
	contract, known := frameworkEventContracts[e.name]
	if !known {
		return errors.New("unknown Framework event name")
	}
	if err := e.validateIdentity(contract.scope); err != nil {
		return err
	}
	return contract.validatePayload(e.payload)
}

func (e eventDraft) validateIdentity(scope eventIdentityScope) error {
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
	IncarnationID   *TreeIncarnationID  `json:"tree_incarnation_id,omitzero"`
	StepSequence    uint64              `json:"step_sequence,omitzero"`
	EffectID        *EffectID           `json:"effect_id,omitzero"`
	Name            string              `json:"name"`
	Phase           EventPhase          `json:"phase"`
	OccurredAt      time.Time           `json:"occurred_at"`
	Payload         json.RawMessage     `json:"payload"`
}

type eventIdentityScope uint8

const (
	eventIdentityProcess eventIdentityScope = iota + 1
	eventIdentityStep
	eventIdentityEffect
)
