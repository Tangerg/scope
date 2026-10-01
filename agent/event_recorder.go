package agent

import (
	"context"
	"encoding/json"
	"math"
	"time"
)

// eventRecorder builds the Framework Event facts of one tree writer and
// publishes them in each Process's listener-visible order. The writer remains
// the only owner of the incarnation every fact carries.
type eventRecorder struct {
	observation *observationBus
	writer      *headWriter
	// context is detached from caller cancellation: observation outlives requests.
	context context.Context
}

// prepare builds a kernel fact. Every kernel fact is well formed by
// construction, so an invalid one is a programming error.
func (e eventRecorder) prepare(
	process *processState,
	name string,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) eventDraft {
	event, err := newEventDraft(eventDraft{
		deploymentRef: process.deployment().DeploymentRef(),
		relation:      process.handle.relation,
		incarnationID: e.writer.incarnation(),
		stepSequence:  step,
		effectID:      effectID,
		name:          name,
		occurredAt:    time.Now(),
		payload:       payload,
	})
	if err != nil {
		panic(err)
	}
	return event
}

// publish assigns the next per-Process sequence. Prepared facts may wait for a
// durable acknowledgment while newer attempts are observed; only publication
// establishes their listener-visible order.
func (e eventRecorder) publish(process *processState, event eventDraft) {
	if process.processEventSequence == math.MaxUint64 {
		e.observation.recordDroppedEvent()
		return
	}
	process.processEventSequence++
	e.observation.publishEvent(e.context, event.publish(process.processEventSequence))
}

func (e eventRecorder) emit(
	process *processState,
	name string,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	e.publish(process, e.prepare(process, name, step, effectID, payload))
}

func (e eventRecorder) beginEffectAttempt(process *processState, step uint64, effectID EffectID, target EffectTarget) effectAttempt {
	attempt := effectAttempt{id: newEffectAttemptID(), startedAt: time.Now()}
	payload := marshalEventPayload(effectStartedEventPayload{EffectTarget: target, AttemptID: attempt.id})
	e.emit(process, EventEffectStarted, step, effectID, payload)
	return attempt
}

// deltas opens the Delta stream of one dispatch attempt; it is nil when no
// DeltaListener is configured.
func (e eventRecorder) deltas(process *processState, effectID EffectID, attempt effectAttempt) *deltaStream {
	if len(e.observation.deltas) == 0 {
		return nil
	}
	return &deltaStream{
		observation: e.observation, context: e.context,
		processID: process.handle.processID, effectID: effectID,
		incarnationID: e.writer.incarnation(), attemptID: attempt.id,
	}
}

func (e eventRecorder) settlement(
	process *processState,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	observation effectAttempt,
	cause error,
) eventDraft {
	durationMS := time.Since(observation.startedAt).Milliseconds()
	failure := dispatchFailure(cause)
	payload := marshalEventPayload(effectFinishedEventPayload{
		EffectTarget: target, SettlementStatus: status, AttemptID: observation.id,
		DurationMS: &durationMS, FailureKind: failure.Kind(), FailureCode: failure.Code(),
	})
	return e.prepare(process, EventEffectFinished, process.prepared.StepSequence, effectID, payload)
}

func (e eventRecorder) publishSettlement(
	process *processState,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	observation effectAttempt,
	cause error,
) {
	e.publish(process, e.settlement(process, effectID, target, status, observation, cause))
}

func (e eventRecorder) signalsAccepted(process *processState, records []signalRecord) []eventDraft {
	var events []eventDraft
	for _, record := range records {
		payload := marshalEventPayload(signalAcceptedEventPayload{SignalID: record.id.String(), WaitID: record.waitID.String()})
		events = append(events, e.prepare(process, EventSignalAccepted, 0, EffectID{}, payload))
	}
	return events
}

func (e eventRecorder) stepFinished(process *processState, result stepJobResult, discarded bool) {
	status := StepStatusSucceeded
	if result.err != nil {
		status = StepStatusFailed
	}
	if discarded {
		status = StepStatusDiscarded
	}
	work, delay := int64(result.workDuration), int64(time.Since(result.finishedAt))
	payload := marshalEventPayload(stepFinishedEventPayload{StepStatus: status, WorkDurationNS: &work, AdoptionDelayNS: &delay})
	e.emit(process, EventStepFinished, process.committedSteps+1, EffectID{}, payload)
}

func (e eventRecorder) dispatchFinished(process *processState, attempt effectAttempt, result dispatchJobResult) {
	if result.dropped > 0 {
		payload := marshalEventPayload(deltaDroppedEventPayload{DroppedDeltaCount: result.dropped, AttemptID: attempt.id})
		e.emit(process, EventDeltaDropped, process.prepared.StepSequence, result.effectID, payload)
	}
	e.publishSettlement(process, result.effectID, EffectTargetDispatcher, result.settlement.Status(), attempt, result.err)
}
