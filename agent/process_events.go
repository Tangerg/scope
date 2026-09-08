package agent

import (
	"context"
	"encoding/json"
	"math"
	"time"
)

func (p *processState) publishEvent(
	ctx context.Context,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event, ok := p.prepareEvent(name, phase, step, effectID, payload)
	if !ok {
		return
	}
	p.publishPreparedEvent(ctx, event)
}

func (p *processState) publishEventAfterCheckpoint(
	ctx context.Context,
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) {
	event, ok := p.prepareEvent(name, phase, step, effectID, payload)
	if !ok {
		return
	}
	if p.runtime == nil || p.engine.durability == nil {
		p.publishPreparedEvent(ctx, event)
		return
	}
	p.runtime.stageCheckpointEvent(event)
}

func (p *processState) prepareEvent(
	name string,
	phase EventPhase,
	step uint64,
	effectID EffectID,
	payload json.RawMessage,
) (Event, bool) {
	if p.processEventSequence == math.MaxUint64 {
		return Event{}, false
	}
	var incarnationID TreeIncarnationID
	if p.runtime != nil {
		incarnationID = p.runtime.incarnation
	}
	nextSequence := p.processEventSequence + 1
	event, err := newEvent(eventSpec{
		processSequence: nextSequence,
		processID:       p.handle.processID,
		deploymentRef:   p.deployment.DeploymentRef(),
		relation:        p.handle.relation,
		incarnationID:   incarnationID,
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
	p.processEventSequence = nextSequence
	return event, true
}

func (p *processState) publishPreparedEvent(ctx context.Context, event Event) {
	p.engine.observation.publishEvent(context.WithoutCancel(ctx), event)
}

func (p *processState) publishEffectStarted(
	ctx context.Context,
	step uint64,
	effectID EffectID,
	target EffectTarget,
) time.Time {
	payload, _ := json.Marshal(effectStartedEventPayload{EffectTarget: target})
	p.publishEvent(ctx, EventEffectStarted, EventPhaseAttempt, step, effectID, payload)
	return time.Now()
}

func (p *processState) publishSettlementEvent(
	ctx context.Context,
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	startedAt time.Time,
) {
	event, ok := p.prepareSettlementEvent(effectID, target, status, startedAt)
	if !ok {
		return
	}
	p.publishPreparedEvent(ctx, event)
}

func (p *processState) prepareSettlementEvent(
	effectID EffectID,
	target EffectTarget,
	status SettlementStatus,
	startedAt time.Time,
) (Event, bool) {
	durationMS := time.Since(startedAt).Milliseconds()
	payload, err := json.Marshal(effectFinishedEventPayload{
		EffectTarget: target, SettlementStatus: status,
		DurationMS: &durationMS,
	})
	if err != nil {
		return Event{}, false
	}
	return p.prepareEvent(
		EventEffectFinished, EventPhaseAttempt,
		p.prepared.wire.StepSequence, effectID, payload,
	)
}

func emptyEventPayload() json.RawMessage { return json.RawMessage("{}") }
