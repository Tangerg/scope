package agent

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestTransitionDecodingPreservesTheLastAcceptedIntent(t *testing.T) {
	for _, transition := range []Transition{
		controlValue(Continue(2)), controlValue(Checkpoint(2)),
		controlValue(Wait(2, controlValue(ParseWaitID("wait:approval")))),
		controlValue(Pause(2, "approval required")),
		controlValue(Complete(2, controlValue(EncodePayload(42)))),
		controlValue(Fail(2, controlValue(NewFailure(FailureKindExecution, "work.failed", "work failed")))),
	} {
		t.Run(transition.Kind().String(), func(t *testing.T) {
			before := controlValue(jsonv2.Marshal(transition))
			var restored Transition
			if err := jsonv2.Unmarshal(before, &restored); err != nil || !bytes.Equal(before, controlValue(jsonv2.Marshal(restored))) {
				t.Fatalf("intent changed on restore: %s: %v", before, err)
			}
			for _, invalid := range []string{
				`{"consumed_signals":2}`, `{"consumed_signals":2,"pause":""}`,
				`{"consumed_signals":2,"pause":" untrimmed"}`,
				`{"consumed_signals":2,"continue":[{"target":"invalid","payload":{}}]}`,
				`{"consumed_signals":2,"fail":{"kind":"external","code":"invalid code","message":"failure"}}`,
				`{"consumed_signals":2,"complete":null,"wait":"wait:approval"}`,
			} {
				if err := jsonv2.Unmarshal([]byte(invalid), &restored); !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("invalid intent accepted: %s: %v", invalid, err)
				}
				if !bytes.Equal(before, controlValue(jsonv2.Marshal(restored))) {
					t.Fatal("failed restore replaced the accepted intent")
				}
			}
		})
	}
	for name, create := range map[string]func() (Transition, error){
		"invalid effect":  func() (Transition, error) { return Continue(0, Effect{}) },
		"missing wait":    func() (Transition, error) { return Wait(0, WaitID{}) },
		"missing output":  func() (Transition, error) { return Complete(0, Payload{}) },
		"missing failure": func() (Transition, error) { return Fail(0, Failure{}) },
	} {
		t.Run(name, func(t *testing.T) {
			if transition, err := create(); transition.Valid() || !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("incomplete intent = %v, %v", transition, err)
			}
		})
	}
}

func TestDescriptorRejectsInvalidSchemaReplacementAtomically(t *testing.T) {
	descriptor := testDescriptor(t)
	before := controlValue(jsonv2.Marshal(descriptor))
	for _, field := range []string{"input_schema", "output_schema", "signal_schema"} {
		for _, invalid := range []json.RawMessage{[]byte(`null`), []byte(`{"type":"not_a_type"}`)} {
			var document map[string]json.RawMessage
			if err := jsonv2.Unmarshal(before, &document); err != nil {
				t.Fatal(err)
			}
			document[field] = invalid
			if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(document)), &descriptor); !errors.Is(err, ErrInvalidDescriptor) {
				t.Fatalf("invalid %s accepted: %s: %v", field, invalid, err)
			}
			if !bytes.Equal(before, controlValue(jsonv2.Marshal(descriptor))) {
				t.Fatal("failed schema replacement changed the frozen contract")
			}
		}
	}
	for _, schemas := range [][2]jsonschema.Schema{{{}, descriptor.OutputSchema()}, {descriptor.InputSchema(), {}}} {
		if _, err := NewDescriptor(DescriptorConfig{Name: "test.contract", Description: "Reject incomplete schemas.", InputSchema: schemas[0], OutputSchema: schemas[1]}); !errors.Is(err, ErrInvalidDescriptor) {
			t.Fatalf("incomplete contract accepted: %v", err)
		}
	}
	if _, err := descriptor.EncodeInput(make(chan int)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unrepresentable input accepted: %v", err)
	}
	if _, err := descriptor.DecodeOutput[wireFixture](controlValue(EncodePayload(42))); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("output outside the authoritative schema accepted: %v", err)
	}
}

func TestEventDecodingRejectsContradictoryObservationsAtomically(t *testing.T) {
	processID := controlValue(ParseProcessID("process:event-admission"))
	deployment := newChildTestDeployment(t)
	accepted := controlValue(newEvent(eventDraft{
		deploymentRef: deployment.DeploymentRef(), relation: rootProcessRelation(processID),
		name: EventProcessStarted, occurredAt: time.Unix(20, 0), payload: emptyEventPayload(),
	}, 1))
	before := controlValue(jsonv2.Marshal(accepted))
	attempt := newEffectAttemptID()
	for _, test := range []struct {
		name    string
		payload any
	}{
		{EventProcessFinished, processFinishedEventPayload{TerminationCause: TerminationCauseExecutionFailure, Usage: new(Usage)}},
		{EventRuntimeStopped, runtimeStoppedEventPayload{FailureKind: FailureKindExternal, FailureCode: "invalid code"}},
		{EventSignalAccepted, signalAcceptedEventPayload{SignalID: "invalid/id"}},
		{EventSignalAccepted, signalAcceptedEventPayload{SignalID: "signal:admitted", WaitID: "invalid/id"}},
		{EventStepFinished, stepFinishedEventPayload{StepStatus: StepStatusSucceeded, WorkDurationNS: new(int64(-1)), AdoptionDelayNS: new(int64(0))}},
		{EventStepCommitted, json.RawMessage(`{"process_status":"invalid"}`)},
		{EventEffectStarted, json.RawMessage(`{"attempt_id":"attempt:admitted","effect_target":"invalid"}`)},
		{EventEffectFinished, effectFinishedEventPayload{AttemptID: attempt, EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusSucceeded, DurationMS: new(int64(-1))}},
		{EventEffectFinished, effectFinishedEventPayload{AttemptID: attempt, EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusSucceeded, DurationMS: new(int64(math.MaxInt64))}},
		{EventEffectFinished, effectFinishedEventPayload{AttemptID: attempt, EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusSucceeded, DurationMS: new(int64(0)), FailureKind: FailureKindExternal, FailureCode: "external.failure"}},
		{EventEffectResolved, effectResolvedEventPayload{EffectTarget: EffectTargetDispatcher, SettlementStatus: SettlementStatusUnknown}},
		{EventDeltaDropped, deltaDroppedEventPayload{AttemptID: attempt}},
	} {
		var document map[string]json.RawMessage
		if err := jsonv2.Unmarshal(before, &document); err != nil {
			t.Fatal(err)
		}
		document["name"] = controlValue(jsonv2.Marshal(test.name))
		document["payload"] = controlValue(jsonv2.Marshal(test.payload))
		contract := frameworkEventContracts[test.name]
		if contract.scope != eventIdentityProcess {
			document["step_sequence"] = []byte(`1`)
		}
		if contract.scope == eventIdentityEffect {
			document["effect_id"] = controlValue(jsonv2.Marshal(processID.effectID(1, 0)))
		}
		if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(document)), &accepted); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("contradictory %s accepted: %+v: %v", test.name, test.payload, err)
		}
		if !bytes.Equal(before, controlValue(jsonv2.Marshal(accepted))) {
			t.Fatal("failed decoding replaced the previous observation")
		}
	}
}
