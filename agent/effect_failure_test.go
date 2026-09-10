package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type effectFailureTestDispatcher struct {
	dispatch func(EffectRequest) (Settlement, error)
}

func (e effectFailureTestDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }

func (e effectFailureTestDispatcher) Dispatch(_ context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
	return e.dispatch(request)
}

func TestDispatcherUnknownRetainsControlledFailureObservation(t *testing.T) {
	const secret = "private-dispatcher-diagnostic"
	for _, test := range []struct {
		name     string
		kind     FailureKind
		code     string
		dispatch func(EffectRequest) (Settlement, error)
	}{
		{name: "external", kind: FailureKindExternal, code: "engine.dispatch.failed", dispatch: func(EffectRequest) (Settlement, error) {
			return Settlement{}, errors.New(secret)
		}},
		{name: "canceled", kind: FailureKindExternal, code: "engine.dispatch.canceled", dispatch: func(EffectRequest) (Settlement, error) {
			return Settlement{}, fmt.Errorf("%s: %w", secret, context.Canceled)
		}},
		{name: "deadline", kind: FailureKindExternal, code: "engine.dispatch.deadline", dispatch: func(EffectRequest) (Settlement, error) {
			return Settlement{}, fmt.Errorf("%s: %w", secret, context.DeadlineExceeded)
		}},
		{name: "panic", kind: FailureKindPanic, code: "engine.dispatch.panicked", dispatch: func(EffectRequest) (Settlement, error) {
			panic(secret)
		}},
		{name: "invalid settlement", kind: FailureKindContract, code: "engine.dispatch.settlement.invalid", dispatch: func(EffectRequest) (Settlement, error) {
			return Settlement{}, nil
		}},
		{name: "different effect", kind: FailureKindContract, code: "engine.dispatch.settlement.invalid", dispatch: func(EffectRequest) (Settlement, error) {
			id, _ := ParseEffectID("effect:another")
			return NewSettlement(id, SettlementStatusSucceeded, []byte(`null`))
		}},
		{name: "explicit unknown", dispatch: func(request EffectRequest) (Settlement, error) {
			return NewSettlement(request.ID(), SettlementStatusUnknown, []byte(`null`))
		}},
	} {
		for _, durable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/durable=%t", test.name, durable), func(t *testing.T) {
				finished := make(chan Event, 1)
				config := EngineConfig{EventListeners: []EventListener{EventListenerFunc(func(_ context.Context, event Event) {
					if event.Name() == EventEffectFinished {
						finished <- event
					}
				})}}
				if durable {
					config.TreeDurability = &recordingTreeDurability{}
				}
				engine, err := NewEngine(config)
				if err != nil {
					t.Fatal(err)
				}
				defer mustCloseEngine(t, engine)
				deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"),
					effectFailureTestDispatcher{dispatch: test.dispatch})
				input, _ := EncodeInput(engineTestInput{Value: "diagnostic"})
				process, err := engine.Start(context.WithoutCancel(t.Context()), deployment, input)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if killErr := process.Kill(context.WithoutCancel(t.Context()), "test cleanup"); killErr != nil {
						t.Error(killErr)
					}
					mustAwait(t, process)
				}()
				snapshot := waitForUnknownSettlement(t, process)
				event := receiveTreeRuntimeProbe(t, finished)
				encoded, err := json.Marshal(event)
				if err != nil || bytes.Contains(encoded, []byte(secret)) || bytes.Contains(snapshot.JSON(), []byte(secret)) {
					t.Fatalf("dispatcher diagnostic escaped observation boundary: %v", err)
				}
				var decoded Event
				if decodeErr := json.Unmarshal(encoded, &decoded); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				fact, ok := decoded.EffectFinished()
				kind, code, present := fact.Failure()
				if !ok || !fact.Valid() || fact.SettlementStatus() != SettlementStatusUnknown ||
					kind != test.kind || code != test.code || present != test.kind.Valid() {
					t.Fatalf("Unknown classification: fact=%+v failure=%s/%s/%t", fact, kind, code, present)
				}
				wire, err := snapshot.wire()
				if err != nil || string(wire.Prepared.Effects[0].Settlement.Payload()) != "null" {
					t.Fatalf("Unknown settlement changed its model-visible payload: %v", err)
				}
			})
		}
	}
}

func TestDispatchCompletionRetainsOriginalError(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	cause := errors.New("dispatch cause")
	process.deployment = engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"),
		effectFailureTestDispatcher{dispatch: func(EffectRequest) (Settlement, error) {
			return Settlement{}, fmt.Errorf("dispatch: %w", cause)
		}})
	effect, err := NewDispatcherEffect([]byte(`{"kind":"effect","value":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	record := preparedEffect{ID: deriveEffectID(process.handle.processID, 1, 0), Effect: effect, Phase: effectPhasePending}
	process.prepared = &preparedStep{StepSequence: 1, Effects: preparedEffects{record}}
	runtime.startDispatch(process, 0, record)
	completion := receiveTreeRuntimeProbe(t, runtime.completions)
	if !errors.Is(completion.dispatch.err, cause) || completion.dispatch.settlement.Status() != SettlementStatusUnknown {
		t.Fatalf("completion lost dispatch cause: %+v", completion.dispatch)
	}
	runtime.applyCompletion(completion)
}

func TestLocalFrameworkSettlementDoesNotInventUnknown(t *testing.T) {
	processID, _ := ParseProcessID("process:local-contract")
	for _, payload := range []string{`{`, `{"operation":"unsupported"}`, `{"operation":"wait"}`, `{"operation":"wait_children"}`, `{"operation":"start_child"}`} {
		record := preparedEffect{
			ID:     deriveEffectID(processID, 1, 0),
			Effect: Effect{target: EffectTargetFramework, payload: json.RawMessage(payload)}, Phase: effectPhasePending,
		}
		if err := record.settleFramework(); err == nil {
			t.Fatalf("invalid local operation settled: %s", payload)
		}
		if record.Phase != effectPhasePending || record.Settlement != nil || record.WaitID != nil {
			t.Fatalf("failed local preparation changed evidence: %+v", record)
		}
	}
}

func TestPreparedContractFailureRetainsRestorableSettlementEvidence(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	key, _ := ParseWaitKey("answer")
	wait, err := RequestWait(key, []byte(`{"prompt":"retained"}`))
	if err != nil {
		t.Fatal(err)
	}
	transition, err := Continue(0, wait, wait)
	if err != nil {
		t.Fatal(err)
	}
	if failure := process.prepareStepResult(stepJobResult{
		transition: transition, candidate: process.execution, candidateState: process.committedExecutionState,
	}); failure != nil {
		t.Fatal(failure.cause)
	}
	runtime.startPreparedEffect(process, 0, &process.prepared.Effects[0])
	runtime.failPreparedEffect(process, "engine.framework_effect.settlement.invalid", errors.New("local contract failed"))
	snapshot, err := runtime.captureTree()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := snapshot.ProcessSnapshots()[0].wire()
	if err != nil || wire.Prepared == nil || len(wire.Prepared.Effects) != 2 {
		t.Fatalf("contract failure discarded evidence: %v", err)
	}
	if wire.Status != StatusFailed || wire.Prepared.Effects[0].Settlement.Status() != SettlementStatusSucceeded ||
		wire.Prepared.Effects[1].Phase != effectPhasePlanned || len(wire.Mailbox.Signals) != 0 ||
		wire.Usage != (Usage{PreparedEffects: 2}) || len(wire.Termination.UnresolvedEffectIDs()) != 0 {
		t.Fatalf("contract failure changed settled evidence or adopted candidate: %+v", wire)
	}
	restoredEngine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer mustCloseEngine(t, restoredEngine)
	restored, err := restoredEngine.RestoreTree(t.Context(), process.deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result := mustAwait(t, restored); result.Status() != StatusFailed || result.Usage() != wire.Usage {
		t.Fatalf("restoration changed contract failure: %+v", result)
	}
	if !bytes.Equal(inspectProcessSnapshot(t, restored).JSON(), snapshot.ProcessSnapshots()[0].JSON()) {
		t.Fatal("restoration changed the retained settlement evidence")
	}
}

func TestEffectFinishedRejectsMisleadingFailureClassification(t *testing.T) {
	for _, fields := range []string{
		`"failure_kind":"external"`,
		`"failure_code":"engine.dispatch.failed"`,
		`"failure_kind":"invalid","failure_code":"engine.dispatch.failed"`,
		`"failure_kind":"external","failure_code":"invalid code"`,
		`"failure_kind":"external","failure_code":"engine.dispatch.failed","failure_message":"private"`,
	} {
		payload := json.RawMessage(`{"effect_target":"dispatcher","settlement_status":"unknown","duration_ms":0,` + fields + `}`)
		if _, err := decodeEffectFinishedFact(payload); err == nil {
			t.Fatalf("invalid diagnostic fields were accepted: %s", fields)
		}
	}
	for _, target := range []EffectTarget{EffectTargetFramework, EffectTargetDispatcher} {
		for _, status := range []SettlementStatus{SettlementStatusSucceeded, SettlementStatusFailed, SettlementStatusUnknown} {
			if target == EffectTargetDispatcher && status == SettlementStatusUnknown {
				continue
			}
			duration := int64(0)
			payload, err := json.Marshal(effectFinishedEventPayload{
				EffectTarget: target, SettlementStatus: status, DurationMS: &duration,
				FailureKind: FailureKindExternal, FailureCode: "engine.dispatch.failed",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeEffectFinishedFact(payload); err == nil {
				t.Fatalf("dispatch error attached to %s/%s", target, status)
			}
		}
	}
}
