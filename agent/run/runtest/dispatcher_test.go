package runtest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestScriptedDispatcherRejectsInvalidOrContradictoryCalls(t *testing.T) {
	cause := errors.New("dispatch failed")
	for _, config := range []ScriptedDispatcherConfig{
		{},
		{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(`null`), Deltas: []json.RawMessage{[]byte(`{`)}}}},
		{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{SettlementStatus: run.SettlementStatusFailed, SettlementPayload: []byte(`null`), Error: cause}}},
		{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{ExpectedEffect: &run.Effect{}, Error: cause}}},
		{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(`{`)}}},
		{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{SettlementPayload: []byte(`null`)}}},
	} {
		if _, err := NewScriptedDispatcher(config); !errors.Is(err, ErrInvalidDispatchScript) {
			t.Fatalf("invalid script accepted or misclassified: %v", err)
		}
	}
}

func TestScriptedDispatcherPreservesSettlementDeltaAndRequestOwnership(t *testing.T) {
	request := scriptedRequest(t)
	for _, status := range []run.SettlementStatus{run.SettlementStatusSucceeded, run.SettlementStatusFailed, run.SettlementStatusUnknown} {
		t.Run(string(status), func(t *testing.T) {
			payload := json.RawMessage(`"result"`)
			delta := json.RawMessage(`"first"`)
			effect := request.Effect()
			var failure run.Failure
			if status == run.SettlementStatusFailed {
				var err error
				failure, err = run.NewFailure(run.FailureKindExternal, "test.dispatch.failed", "dispatch failed")
				if err != nil {
					t.Fatal(err)
				}
			}
			dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{ExpectedEffect: &effect, SettlementStatus: status, SettlementPayload: payload, Failure: failure, Deltas: []json.RawMessage{delta}}}})
			if err != nil {
				t.Fatal(err)
			}
			payload[1] = 'x'
			delta[1] = 'x'
			var seen []json.RawMessage
			settlement, err := dispatcher.Dispatch(t.Context(), request, func(value json.RawMessage) { seen = append(seen, value) })
			if err != nil || settlement.Status() != status || string(settlement.Payload().JSON()) != `"result"` {
				t.Fatalf("settlement=%+v error=%v", settlement, err)
			}
			if len(seen) != 1 || string(seen[0]) != `"first"` {
				t.Fatalf("deltas=%s", seen)
			}
			requests := dispatcher.Requests()
			if dispatcher.Remaining() != 0 || len(requests) != 1 || requests[0].ID() != request.ID() {
				t.Fatal("script consumption changed")
			}
			requests[0] = run.EffectRequest{}
			if dispatcher.Requests()[0].ID() != request.ID() {
				t.Fatal("caller rewrote consumed request")
			}
			if _, err := dispatcher.Dispatch(t.Context(), request, nil); !errors.Is(err, ErrUnexpectedDispatch) {
				t.Fatal(err)
			}
		})
	}
}

func TestScriptedDispatcherKeepsCancellationAndUnknownOutcomeDistinct(t *testing.T) {
	request := scriptedRequest(t)
	cause := errors.New("remote acknowledgment lost")
	dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{Error: cause, Deltas: []json.RawMessage{[]byte(`1`)}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := dispatcher.Dispatch(ctx, request, nil); !errors.Is(err, context.Canceled) || len(dispatcher.Requests()) != 0 {
		t.Fatalf("canceled call consumed script: %v", err)
	}
	var deltas int
	if settlement, err := dispatcher.Dispatch(t.Context(), request, func(json.RawMessage) { deltas++ }); !errors.Is(err, cause) || settlement.Valid() || deltas != 1 {
		t.Fatalf("indeterminate call settlement=%+v deltas=%d error=%v", settlement, deltas, err)
	}
}

func TestScriptedDispatcherRejectsWrongEffect(t *testing.T) {
	request := scriptedRequest(t)
	other, err := run.NewDispatcherEffect([]byte(`"other"`))
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{ExpectedEffect: &other, SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(`null`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Dispatch(t.Context(), request, nil); !errors.Is(err, ErrEffectMismatch) {
		t.Fatal(err)
	}
}

func TestScriptedDispatcherHasNoPolicyWithoutAValidReceiverAndEffect(t *testing.T) {
	request := scriptedRequest(t)
	var absent *ScriptedDispatcher
	if policy := absent.Policy(request.Effect()); policy.Valid() {
		t.Fatal("nil dispatcher advertised an executable policy")
	}
	if absent.Requests() != nil || absent.Remaining() != 0 {
		t.Fatal("nil dispatcher retained script consumption")
	}
	if _, err := absent.Dispatch(t.Context(), request, nil); !errors.Is(err, ErrInvalidDispatchScript) {
		t.Fatalf("nil dispatcher admitted a call: %v", err)
	}
	dispatcher := conformanceDispatcher(t, run.ReplayPolicySameIdentity)
	if policy := dispatcher.Policy(run.Effect{}); policy.Valid() || dispatcher.Remaining() != 1 {
		t.Fatal("invalid effect acquired a policy or consumed the next call")
	}
}

func TestScriptedDispatcherClassifiesAnInvalidRequestEffect(t *testing.T) {
	effect := scriptedRequest(t).Effect()
	dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{ExpectedEffect: &effect, SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(`null`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Dispatch(t.Context(), run.EffectRequest{}, nil); !errors.Is(err, ErrEffectMismatch) || !errors.Is(err, run.ErrInvalidEffect) {
		t.Fatalf("invalid request escaped the effect mismatch boundary: %v", err)
	}
	if len(dispatcher.Requests()) != 1 || dispatcher.Remaining() != 0 {
		t.Fatal("rejected request was not recorded as consumed")
	}
}

func scriptedRequest(t *testing.T) run.EffectRequest {
	t.Helper()
	dispatcher := conformanceDispatcher(t, run.ReplayPolicyNever)
	runtime, program := recoveryRuntime(t, run.NewMemoryStore(), newRecoveryDefinition(t, recoveryEffect), dispatcher)
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "runtest", Key: "dispatcher"}, Input: conformanceInput(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	requests := dispatcher.Requests()
	if len(requests) != 1 {
		t.Fatalf("dispatcher requests=%d", len(requests))
	}
	return requests[0]
}
