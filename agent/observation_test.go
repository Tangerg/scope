package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func TestEventSeparatesAttemptFromCommittedFacts(t *testing.T) {
	processID, _ := ParseProcessID("process:1")
	effectID, _ := ParseEffectID("process:1:step:2:effect:0")
	deployment := newChildTestDeployment(t)
	relation := rootProcessRelation(processID)
	event, err := newEvent(eventSpec{
		processSequence: 7,
		processID:       processID,
		deploymentRef:   deployment.DeploymentRef(),
		relation:        relation,
		stepSequence:    2,
		effectID:        effectID,
		name:            EventEffectStarted,
		phase:           EventPhaseAttempt,
		occurredAt:      time.Unix(20, 0),
		payload:         json.RawMessage(`{"effect_target":"dispatcher"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Phase() != EventPhaseAttempt || decoded.Name() != EventEffectStarted || decoded.ProcessSequence() != 7 {
		t.Fatalf("decoded Event = %+v", decoded)
	}
	if decoded.DeploymentRef() != deployment.DeploymentRef() || decoded.Relation() != relation {
		t.Fatalf("decoded Deployment = %s, relation = %#v", decoded.DeploymentRef(), decoded.Relation())
	}
	if got, ok := decoded.EffectID(); !ok || got != effectID {
		t.Fatalf("decoded EffectID = %v, %t", got, ok)
	}
}

func TestEventRejectsMismatchedFrameworkFactContracts(t *testing.T) {
	processID, _ := ParseProcessID("process:event-contract")
	effectID, _ := ParseEffectID("process:event-contract:step:1:effect:0")
	deployment := newChildTestDeployment(t)
	base := eventSpec{
		processSequence: 1,
		processID:       processID,
		deploymentRef:   deployment.DeploymentRef(),
		relation:        rootProcessRelation(processID),
		occurredAt:      time.Unix(20, 0),
	}
	tests := []struct {
		name string
		spec eventSpec
	}{
		{
			name: "unknown name",
			spec: eventSpec{name: "agent.unknown.fact", phase: EventPhaseCommitted, payload: emptyEventPayload()},
		},
		{
			name: "wrong phase",
			spec: eventSpec{name: EventProcessStarted, phase: EventPhaseAttempt, payload: emptyEventPayload()},
		},
		{
			name: "missing Effect identity",
			spec: eventSpec{
				name: EventEffectStarted, phase: EventPhaseAttempt, stepSequence: 1,
				payload: json.RawMessage(`{"effect_target":"dispatcher"}`),
			},
		},
		{
			name: "runtime stop is not a committed Process outcome",
			spec: eventSpec{name: EventRuntimeStopped, phase: EventPhaseCommitted,
				payload: json.RawMessage(`{"failure_kind":"external","failure_code":"engine.tree.durability_failed"}`)},
		},
		{
			name: "runtime stop requires a classification",
			spec: eventSpec{name: EventRuntimeStopped, phase: EventPhaseAttempt, payload: emptyEventPayload()},
		},
		{
			name: "invalid payload",
			spec: eventSpec{
				name: EventEffectStarted, phase: EventPhaseAttempt, stepSequence: 1,
				effectID: effectID, payload: json.RawMessage(`{"effect_target":"invalid"}`),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := test.spec
			spec.processSequence = base.processSequence
			spec.processID = base.processID
			spec.deploymentRef = base.deploymentRef
			spec.relation = base.relation
			spec.occurredAt = base.occurredAt
			if _, err := newEvent(spec); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("newEvent() error = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func FuzzEventJSONRoundTrip(f *testing.F) {
	descriptor := testDescriptorForFuzz(f)
	reference, err := newDeploymentRef(
		descriptor,
		ComputeDigest([]byte("event fuzz implementation")),
		ComputeDigest([]byte("event fuzz configuration")),
	)
	if err != nil {
		f.Fatal(err)
	}
	processID, err := ParseProcessID("process:event-fuzz")
	if err != nil {
		f.Fatal(err)
	}
	effectID, err := ParseEffectID("process:event-fuzz:step:1:effect:0")
	if err != nil {
		f.Fatal(err)
	}
	durationMS := int64(1)
	usage := Usage{CommittedSteps: 1, PreparedEffects: 1, AcceptedSignals: 1}
	payloads := []struct {
		name         string
		phase        EventPhase
		stepSequence uint64
		effectID     EffectID
		payload      any
	}{
		{name: EventProcessStarted, phase: EventPhaseCommitted, payload: struct{}{}},
		{name: EventProcessFinished, phase: EventPhaseCommitted, payload: processFinishedEventPayload{
			ProcessStatus: StatusCompleted, TerminationCause: TerminationCauseCompletion, Usage: &usage,
		}},
		{name: EventSignalAccepted, phase: EventPhaseCommitted, payload: signalAcceptedEventPayload{
			SignalID: "signal:event-fuzz",
		}},
		{name: EventRuntimeStopped, phase: EventPhaseAttempt, payload: runtimeStoppedEventPayload{
			FailureKind: FailureKindExternal, FailureCode: treeDurabilityFailureCode,
		}},
		{name: EventStepFinished, phase: EventPhaseAttempt, stepSequence: 1, payload: stepFinishedEventPayload{
			StepStatus: StepStatusSucceeded, DurationMS: &durationMS,
		}},
		{name: EventStepCommitted, phase: EventPhaseCommitted, stepSequence: 1, payload: stepCommittedEventPayload{
			ProcessStatus: StatusRunning,
		}},
		{name: EventEffectStarted, phase: EventPhaseAttempt, stepSequence: 1, effectID: effectID, payload: effectStartedEventPayload{
			EffectTarget: EffectTargetDispatcher,
		}},
		{name: EventEffectFinished, phase: EventPhaseAttempt, stepSequence: 1, effectID: effectID, payload: effectFinishedEventPayload{
			EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusSucceeded, DurationMS: &durationMS,
		}},
		{name: EventEffectFinished, phase: EventPhaseAttempt, stepSequence: 1, effectID: effectID, payload: effectFinishedEventPayload{
			EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusUnknown, DurationMS: &durationMS,
			FailureKind: FailureKindExternal, FailureCode: "engine.dispatch.failed",
		}},
		{name: EventDeltaDropped, phase: EventPhaseAttempt, stepSequence: 1, effectID: effectID, payload: deltaDroppedEventPayload{
			DroppedDeltaCount: 1,
		}},
	}
	for index, fixture := range payloads {
		payload, marshalErr := json.Marshal(fixture.payload)
		if marshalErr != nil {
			f.Fatal(marshalErr)
		}
		event, eventErr := newEvent(eventSpec{
			processSequence: uint64(index + 1), processID: processID,
			deploymentRef: reference, relation: rootProcessRelation(processID),
			stepSequence: fixture.stepSequence, effectID: fixture.effectID,
			name: fixture.name, phase: fixture.phase, occurredAt: time.Unix(20, 0), payload: payload,
		})
		if eventErr != nil {
			f.Fatal(eventErr)
		}
		seed, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			f.Fatal(marshalErr)
		}
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var event Event
		if err := json.Unmarshal(data, &event); err != nil {
			return
		}
		if !event.Valid() {
			t.Fatal("decoded Event is invalid")
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip Event
		if unmarshalErr := json.Unmarshal(encoded, &roundTrip); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		reencoded, err := json.Marshal(roundTrip)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(reencoded, encoded) {
			t.Fatalf("Event JSON is not stable:\nfirst:  %s\nsecond: %s", encoded, reencoded)
		}
	})
}

func TestDeltaIsEffectLocalAndImmutable(t *testing.T) {
	processID, _ := ParseProcessID("process:1")
	effectID, _ := ParseEffectID("process:1:step:2:effect:0")
	payload := json.RawMessage(` { "text": "partial" } `)
	delta, err := newDelta(processID, effectID, TreeIncarnationID{}, 1, time.Unix(30, 0), payload)
	if err != nil {
		t.Fatal(err)
	}
	payload[3] = 'x'
	copyOfPayload := delta.Payload()
	copyOfPayload[0] = '['
	if delta.EffectSequence() != 1 || string(delta.Payload()) != `{"text":"partial"}` {
		t.Fatalf("Delta = sequence %d payload %s", delta.EffectSequence(), delta.Payload())
	}
	data, err := json.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Delta
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProcessID() != processID || decoded.EffectID() != effectID {
		t.Fatalf("decoded Delta = %+v", decoded)
	}
}

func TestDeltaDeliveryPreservesValuesWithoutRequestCancellation(t *testing.T) {
	type contextKey struct{}
	const wantValue = "run-correlation"
	var (
		gotValue string
		gotDone  <-chan struct{}
	)
	bus := newObservationBus(nil, []DeltaListener{
		DeltaListenerFunc(func(ctx context.Context, _ Delta) {
			gotValue, _ = ctx.Value(contextKey{}).(string)
			gotDone = ctx.Done()
		}),
	}, 1)
	t.Cleanup(bus.close)

	processID, _ := ParseProcessID("process:delta-context")
	effectID, _ := ParseEffectID("process:delta-context:step:1:effect:0")
	delta, err := newDelta(processID, effectID, TreeIncarnationID{}, 1, time.Unix(30, 0), json.RawMessage(`{"text":"partial"}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, wantValue))
	if !bus.offerDelta(ctx, delta) {
		t.Fatal("delta was not accepted")
	}
	cancel()
	if err := bus.flushDeltas(t.Context()); err != nil {
		t.Fatal(err)
	}
	if gotValue != wantValue {
		t.Fatalf("listener context value = %q, want %q", gotValue, wantValue)
	}
	if gotDone != nil {
		t.Fatal("listener inherited request cancellation")
	}
}

func TestObservationFailuresAreCountedWithoutAffectingDelivery(t *testing.T) {
	bus := newObservationBus(
		[]EventListener{
			EventListenerFunc(func(context.Context, Event) { panic("event observer failed") }),
			EventListenerFunc(func(context.Context, Event) {}),
		},
		[]DeltaListener{
			DeltaListenerFunc(func(context.Context, Delta) { panic("delta observer failed") }),
			DeltaListenerFunc(func(context.Context, Delta) {}),
		},
		1,
	)
	t.Cleanup(bus.close)

	bus.publishEvent(t.Context(), Event{})
	if !bus.offerDelta(t.Context(), Delta{}) {
		t.Fatal("delta was not accepted")
	}
	if err := bus.flushDeltas(t.Context()); err != nil {
		t.Fatal(err)
	}

	counts := bus.failureSnapshot()
	if counts.EventListenerPanics() != 1 || counts.DeltaListenerPanics() != 1 {
		t.Fatalf(
			"observation failures = event %d, delta %d, want 1 each",
			counts.EventListenerPanics(), counts.DeltaListenerPanics(),
		)
	}
	bus.failureMu.Lock()
	bus.failures.eventListenerPanics = math.MaxUint64
	bus.failureMu.Unlock()
	bus.publishEvent(t.Context(), Event{})
	if got := bus.failureSnapshot().EventListenerPanics(); got != math.MaxUint64 {
		t.Fatalf("saturated event listener panic count = %d", got)
	}
}

func TestStepPausePublishesCommittedProcessPausedFact(t *testing.T) {
	paused := make(chan struct{}, 1)
	var events []Event
	engine, err := NewEngine(EngineConfig{EventListeners: []EventListener{
		EventListenerFunc(func(_ context.Context, event Event) {
			events = append(events, event)
			if event.Name() == EventProcessPaused {
				paused <- struct{}{}
			}
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	input, err := EncodeInput(childTestInput{Mode: "leaf_pause"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	<-paused
	if inspectProcessSnapshot(t, process).Status() != StatusPaused {
		t.Fatalf("Process status = %s, want Paused", inspectProcessSnapshot(t, process).Status())
	}
	var committedIndex, pausedIndex = -1, -1
	for index, event := range events {
		switch event.Name() {
		case EventStepCommitted:
			fact, ok := event.StepCommitted()
			if ok && fact.Status() == StatusPaused {
				committedIndex = index
			}
		case EventProcessPaused:
			pausedIndex = index
		}
	}
	if committedIndex < 0 || pausedIndex != committedIndex+1 {
		t.Fatalf("Step committed index = %d, Process paused index = %d", committedIndex, pausedIndex)
	}
	if err := process.Kill(context.Background(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Await(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestProcessEventSequenceAdvancesOnlyAtPublication(t *testing.T) {
	deployment := newChildTestDeployment(t)
	processID, _ := ParseProcessID("process:event-sequence")
	relation := rootProcessRelation(processID)
	var events []Event
	engine, err := NewEngine(EngineConfig{EventListeners: []EventListener{
		EventListenerFunc(func(_ context.Context, event Event) { events = append(events, event) }),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close(context.WithoutCancel(t.Context())) })
	process := &processState{
		handle: &processHandleState{
			processID: processID, relation: relation, deploymentRef: deployment.DeploymentRef(),
		},
		deployment: deployment,
	}

	runtime := &treeRuntime{engine: engine, context: context.Background()}
	process.processEventSequence = 7
	runtime.publishEvent(process, "invalid event name", EventPhaseAttempt,
		0, EffectID{}, emptyEventPayload(),
	)
	if process.processEventSequence != 7 || len(events) != 0 {
		t.Fatalf("invalid Event changed sequence to %d or published %d facts", process.processEventSequence, len(events))
	}

	runtime.publishEvent(process, EventProcessStarted, EventPhaseCommitted,
		0, EffectID{}, emptyEventPayload(),
	)
	if process.processEventSequence != 8 || len(events) != 1 || events[0].ProcessSequence() != 8 {
		t.Fatalf("valid Event sequence = %d, events = %#v", process.processEventSequence, events)
	}
	paused, ok := runtime.prepareEvent(process, EventProcessPaused, EventPhaseCommitted, 0, EffectID{}, emptyEventPayload())
	if !ok {
		t.Fatal("could not prepare pause Event")
	}
	if process.processEventSequence != 8 {
		t.Error("preparing an unpublished Event advanced publication order")
	}
	runtime.publishEvent(process, EventStepStarted, EventPhaseAttempt, 1, EffectID{}, emptyEventPayload())
	runtime.publishPreparedEvent(process, paused)
	if len(events) != 3 || events[1].ProcessSequence() != 9 || events[2].ProcessSequence() != 10 {
		t.Fatalf("delayed committed Event broke publication order: %v", events)
	}

	process.processEventSequence = math.MaxUint64
	runtime.publishEvent(process, EventProcessResumed, EventPhaseCommitted,
		0, EffectID{}, emptyEventPayload(),
	)
	if process.processEventSequence != math.MaxUint64 || len(events) != 3 {
		t.Fatalf("exhausted Event sequence wrapped to %d or published %d facts", process.processEventSequence, len(events))
	}
}
