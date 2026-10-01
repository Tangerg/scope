package agent

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
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
	event, err := newEvent(eventDraft{
		deploymentRef: deployment.DeploymentRef(),
		relation:      relation,
		stepSequence:  2,
		effectID:      effectID,
		name:          EventEffectStarted,
		occurredAt:    time.Unix(20, 0),
		payload:       marshalEventPayload(effectStartedEventPayload{EffectTarget: EffectTargetDispatcher, AttemptID: newEffectAttemptID()}),
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	data, err := jsonv2.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Event
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
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
	tests := []struct {
		name string
		fact eventDraft
	}{
		{
			name: "unknown name",
			fact: eventDraft{name: "agent.unknown.fact", payload: emptyEventPayload()},
		},
		{
			name: "missing Effect identity",
			fact: eventDraft{
				name: EventEffectStarted, stepSequence: 1,
				payload: marshalEventPayload(effectStartedEventPayload{EffectTarget: EffectTargetDispatcher, AttemptID: newEffectAttemptID()}),
			},
		},
		{
			name: "runtime stop requires a classification",
			fact: eventDraft{name: EventRuntimeStopped, payload: emptyEventPayload()},
		},
		{
			name: "invalid payload",
			fact: eventDraft{
				name: EventEffectStarted, stepSequence: 1,
				effectID: effectID, payload: marshalEventPayload(effectStartedEventPayload{EffectTarget: EffectTargetInvalid, AttemptID: newEffectAttemptID()}),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fact := test.fact
			fact.deploymentRef = deployment.DeploymentRef()
			fact.relation = rootProcessRelation(processID)
			fact.occurredAt = time.Unix(20, 0)
			if _, err := newEvent(fact, 1); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("newEvent() error = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

// A decoded phase is a projection of the event name's contract; a wire value
// that disagrees with it is rejected rather than trusted.
func TestEventDecodingRejectsAPhaseItsNameDoesNotFix(t *testing.T) {
	processID, _ := ParseProcessID("process:event-phase")
	deployment := newChildTestDeployment(t)
	for _, fact := range []eventDraft{
		{name: EventProcessStarted, payload: emptyEventPayload()},
		{name: EventRuntimeStopped, payload: json.RawMessage(`{"failure_kind":"external","failure_code":"engine.tree.committer_failed"}`)},
	} {
		fact.deploymentRef = deployment.DeploymentRef()
		fact.relation, fact.occurredAt = rootProcessRelation(processID), time.Unix(20, 0)
		event, err := newEvent(fact, 1)
		if err != nil {
			t.Fatal(err)
		}
		data := controlValue(jsonv2.Marshal(event))
		wrong := EventPhaseAttempt
		if event.Phase() == EventPhaseAttempt {
			wrong = EventPhaseCommitted
		}
		tampered := bytes.Replace(data, []byte(`"phase":"`+event.Phase().String()+`"`), []byte(`"phase":"`+wrong.String()+`"`), 1)
		if bytes.Equal(tampered, data) {
			t.Fatal("fixture did not encode its phase")
		}
		var decoded Event
		if err := jsonv2.Unmarshal(tampered, &decoded); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s decoded with phase %s: %v", fact.name, wrong, err)
		}
	}
}

func FuzzEventJSONRoundTrip(f *testing.F) {
	descriptor := testDescriptorForFuzz(f)
	reference, err := newDeploymentRef(
		descriptor,
		ComputeDigest([]byte("event fuzz implementation")),
		ComputeDigest([]byte("event fuzz configuration")),
		noChildBindings(),
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
		stepSequence uint64
		effectID     EffectID
		payload      any
	}{
		{name: EventProcessStarted, payload: struct{}{}},
		{name: EventProcessFinished, payload: processFinishedEventPayload{
			ProcessStatus: StatusCompleted, TerminationCause: TerminationCauseCompletion, Usage: &usage,
		}},
		{name: EventSignalAccepted, payload: signalAcceptedEventPayload{
			SignalID: "signal:event-fuzz",
		}},
		{name: EventRuntimeStopped, payload: runtimeStoppedEventPayload{
			FailureKind: FailureKindExternal, FailureCode: failureCodeEngineTreeCommitterFailed,
		}},
		{name: EventStepFinished, stepSequence: 1, payload: stepFinishedEventPayload{
			StepStatus: StepStatusSucceeded, WorkDurationNS: &durationMS, AdoptionDelayNS: new(int64),
		}},
		{name: EventStepCommitted, stepSequence: 1, payload: stepCommittedEventPayload{
			ProcessStatus: StatusRunning,
		}},
		{name: EventEffectStarted, stepSequence: 1, effectID: effectID, payload: effectStartedEventPayload{AttemptID: newEffectAttemptID(),
			EffectTarget: EffectTargetDispatcher,
		}},
		{name: EventEffectResolved, stepSequence: 1, effectID: effectID, payload: effectResolvedEventPayload{
			EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusSucceeded,
		}},
		{name: EventEffectFinished, stepSequence: 1, effectID: effectID, payload: effectFinishedEventPayload{AttemptID: newEffectAttemptID(),
			EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusSucceeded, DurationMS: &durationMS,
		}},
		{name: EventEffectFinished, stepSequence: 1, effectID: effectID, payload: effectFinishedEventPayload{AttemptID: newEffectAttemptID(),
			EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusUnknown, DurationMS: &durationMS,
			FailureKind: FailureKindExternal, FailureCode: "engine.dispatch.failed",
		}},
		{name: EventDeltaDropped, stepSequence: 1, effectID: effectID, payload: deltaDroppedEventPayload{AttemptID: newEffectAttemptID(),
			DroppedDeltaCount: 1,
		}},
	}
	for index, fixture := range payloads {
		payload, marshalErr := jsonv2.Marshal(fixture.payload)
		if marshalErr != nil {
			f.Fatal(marshalErr)
		}
		event, eventErr := newEvent(eventDraft{
			deploymentRef: reference, relation: rootProcessRelation(processID),
			stepSequence: fixture.stepSequence, effectID: fixture.effectID,
			name: fixture.name, occurredAt: time.Unix(20, 0), payload: payload,
		}, uint64(index+1))
		if eventErr != nil {
			f.Fatal(eventErr)
		}
		seed, marshalErr := jsonv2.Marshal(event)
		if marshalErr != nil {
			f.Fatal(marshalErr)
		}
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var event Event
		if err := jsonv2.Unmarshal(data, &event); err != nil {
			return
		}
		if !event.Valid() {
			t.Fatal("decoded Event is invalid")
		}
		encoded, err := jsonv2.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip Event
		if unmarshalErr := jsonv2.Unmarshal(encoded, &roundTrip); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		reencoded, err := jsonv2.Marshal(roundTrip)
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
	delta, err := newDelta(processID, effectID, TreeIncarnationID{}, newEffectAttemptID(), 1, time.Unix(30, 0), payload)
	if err != nil {
		t.Fatal(err)
	}
	payload[3] = 'x'
	copyOfPayload := delta.Payload()
	copyOfPayload[0] = '['
	if delta.EffectSequence() != 1 || string(delta.Payload()) != `{"text":"partial"}` {
		t.Fatalf("Delta = sequence %d payload %s", delta.EffectSequence(), delta.Payload())
	}
	data, err := jsonv2.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Delta
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
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
	delta, err := newDelta(processID, effectID, TreeIncarnationID{}, newEffectAttemptID(), 1, time.Unix(30, 0), json.RawMessage(`{"text":"partial"}`))
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

func TestStepPausePublishesCommittedProcessPausedEvent(t *testing.T) {
	paused := make(chan struct{}, 1)
	var events []Event
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), EventListeners: []EventListener{
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
	input, err := EncodePayload(childTestInput{Mode: "leaf_pause"})
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
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), EventListeners: []EventListener{
		EventListenerFunc(func(_ context.Context, event Event) { events = append(events, event) }),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close(context.WithoutCancel(t.Context())) })
	process := &processState{
		handle: &processHandle{
			processID: processID, relation: relation, deployment: deployment,
		},
	}

	runtime := newTreeRuntime(engine, process.handle.processID, DefaultTreeLimits(), context.Background())
	process.processEventSequence = 7
	func() {
		defer func() {
			if recover() == nil {
				t.Error("invalid kernel fact was silently omitted")
			}
		}()
		runtime.events.emit(process, "invalid event name",
			0, EffectID{}, emptyEventPayload(),
		)
	}()
	if process.processEventSequence != 7 || len(events) != 0 {
		t.Fatalf("invalid Event changed sequence to %d or published %d facts", process.processEventSequence, len(events))
	}

	runtime.events.emit(process, EventProcessStarted,
		0, EffectID{}, emptyEventPayload(),
	)
	if process.processEventSequence != 8 || len(events) != 1 || events[0].ProcessSequence() != 8 {
		t.Fatalf("valid Event sequence = %d, events = %#v", process.processEventSequence, events)
	}
	paused := runtime.events.prepare(process, EventProcessPaused, 0, EffectID{}, emptyEventPayload())
	if process.processEventSequence != 8 {
		t.Error("preparing an unpublished Event advanced publication order")
	}
	runtime.events.emit(process, EventStepStarted, 1, EffectID{}, emptyEventPayload())
	runtime.events.publish(process, paused)
	if len(events) != 3 || events[1].ProcessSequence() != 9 || events[2].ProcessSequence() != 10 {
		t.Fatalf("delayed committed Event broke publication order: %v", events)
	}

	process.processEventSequence = math.MaxUint64
	runtime.events.emit(process, EventProcessResumed,
		0, EffectID{}, emptyEventPayload(),
	)
	if process.processEventSequence != math.MaxUint64 || len(events) != 3 || engine.ObservationFailures().DroppedEvents() != 1 {
		t.Fatalf("exhausted Event sequence wrapped to %d or published %d facts", process.processEventSequence, len(events))
	}
}

func TestDeltaRejectsMissingAttemptIdentity(t *testing.T) {
	delta, err := newDelta(newProcessID(), controlValue(ParseEffectID("effect:test")), TreeIncarnationID{}, newEffectAttemptID(), 1, time.Now(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if decodeErr := jsonv2.Unmarshal(encoded, &fields); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	delete(fields, "attempt_id")
	obsolete, err := jsonv2.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Delta
	if decodeErr := jsonv2.Unmarshal(obsolete, &decoded); !errors.Is(decodeErr, ErrInvalidDelta) {
		t.Fatalf("missing attempt admitted: %v", decodeErr)
	}
}
