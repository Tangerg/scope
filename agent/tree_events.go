package agent

import (
	"context"
	"encoding/json"
	"math"
	"time"
)

func (t *treeRuntime) publishEvent(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event, ok := t.prepareEvent(process, name, phase, step, effectID, payload)
	if !ok {
		return
	}
	t.publishPreparedEvent(process, event)
}

func (t *treeRuntime) publishEventAfterCommit(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event, ok := t.prepareEvent(process, name, phase, step, effectID, payload)
	if !ok {
		return
	}
	t.publishPreparedEventAfterCommit(process, event)
}

func (t *treeRuntime) publishPreparedEventAfterCommit(process *processState, event Event) {
	if t.engine.durability == nil {
		t.publishPreparedEvent(process, event)
		return
	}
	t.stageCommittedEvent(event)
}

func (t *treeRuntime) prepareEvent(
	process *processState,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) (Event, bool) {
	if process.processEventSequence == math.MaxUint64 {
		return Event{}, false
	}
	nextSequence := process.processEventSequence + 1
	event, err := newEvent(eventSpec{
		processSequence: nextSequence,
		processID:       process.handle.processID,
		deploymentRef:   process.deployment.DeploymentRef(),
		relation:        process.handle.relation,
		incarnationID:   t.incarnation,
		stepSequence:    step,
		effectID:        effectID,
		name:            name,
		phase:           phase,
		occurredAt:      time.Now(),
		payload:         payload,
	})
	if err != nil {
		return Event{}, false
	}
	return event, true
}

func (t *treeRuntime) publishPreparedEvent(process *processState, event Event) {
	if process.processEventSequence == math.MaxUint64 {
		return
	}
	// Prepared facts may wait for a durable acknowledgment while newer attempts
	// are observed. Only publication establishes their listener-visible order.
	process.processEventSequence++
	event.processSequence = process.processEventSequence
	t.engine.observation.publishEvent(context.WithoutCancel(t.context), event)
}

func (t *treeRuntime) publishEffectStarted(
	process *processState,
	step uint64,
	effectID EffectID,
	target EffectTarget,
) time.Time {
	payload, _ := json.Marshal(effectStartedEventPayload{EffectTarget: target})
	t.publishEvent(process, EventEffectStarted, EventPhaseAttempt, step, effectID, payload)
	return time.Now()
}

func (t *treeRuntime) publishSettlementEvent(
	process *processState,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	startedAt time.Time,
	cause error,
) {
	event, ok := t.prepareSettlementEvent(process, effectID, target, status, startedAt, cause)
	if !ok {
		return
	}
	t.publishPreparedEvent(process, event)
}

func (t *treeRuntime) prepareSettlementEvent(
	process *processState,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	startedAt time.Time,
	cause error,
) (Event, bool) {
	durationMS := time.Since(startedAt).Milliseconds()
	failureKind, failureCode := dispatchFailure(cause)
	payload, err := json.Marshal(effectFinishedEventPayload{
		EffectTarget: target, SettlementStatus: status,
		DurationMS: &durationMS, FailureKind: failureKind, FailureCode: failureCode,
	})
	if err != nil {
		return Event{}, false
	}
	return t.prepareEvent(process,
		EventEffectFinished, EventPhaseAttempt,
		process.prepared.StepSequence, effectID, payload,
	)
}

func emptyEventPayload() json.RawMessage { return json.RawMessage("{}") }

func (t *treeRuntime) prepareSignalEvents(process *processState, signals []Signal) []Event {
	var events []Event
	for _, signal := range signals {
		waitID, _ := signal.WaitID()
		payload, _ := json.Marshal(signalAcceptedEventPayload{SignalID: signal.ID().String(), WaitID: waitID.String()})
		if event, ok := t.prepareEvent(process, EventSignalAccepted, EventPhaseCommitted, 0, EffectID{}, payload); ok {
			events = append(events, event)
		}
	}
	return events
}

func (t *treeRuntime) publishEphemeralStatus(process *processState) {
	if t.engine.durability == nil {
		process.handle.updateStatus(process.status)
	}
}
