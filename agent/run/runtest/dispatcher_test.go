package runtest_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/core/jsonschema"
)

// scriptedEffectStateKind names the Definition and its ExecutionState kind.
const scriptedEffectStateKind = "agenttest.scripted_effect"

func TestNewScriptedDispatcherRejectsContradictorySteps(t *testing.T) {
	injected := errors.New("injected dispatch failure")
	tests := []struct {
		name   string
		config runtest.ScriptedDispatcherConfig
	}{
		{name: "invalid replay policy", config: runtest.ScriptedDispatcherConfig{}},
		{
			name: "invalid delta",
			config: runtest.ScriptedDispatcherConfig{
				ReplayPolicy: run.ReplayPolicyNever,
				Calls: []runtest.ScriptedCall{{
					Deltas:            []json.RawMessage{json.RawMessage(`{`)},
					SettlementStatus:  run.SettlementStatusSucceeded,
					SettlementPayload: json.RawMessage(`{}`),
				}},
			},
		},
		{
			name: "error and settlement",
			config: runtest.ScriptedDispatcherConfig{
				ReplayPolicy: run.ReplayPolicyNever,
				Calls: []runtest.ScriptedCall{{
					SettlementStatus:  run.SettlementStatusFailed,
					SettlementPayload: json.RawMessage(`{}`),
					Error:             injected,
				}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runtest.NewScriptedDispatcher(test.config); !errors.Is(err, runtest.ErrInvalidDispatchScript) {
				t.Fatalf("NewScriptedDispatcher error=%v", err)
			}
		})
	}
}

func TestScriptedDispatcherRunsThroughPublicEngineBoundary(t *testing.T) {
	for _, status := range []run.SettlementStatus{run.SettlementStatusSucceeded, run.SettlementStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			effect, err := run.NewDispatcherEffect(json.RawMessage(`{"operation":"scripted"}`))
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{
				ReplayPolicy: run.ReplayPolicySameIdentity,
				Calls: []runtest.ScriptedCall{{
					ExpectedEffect:    &effect,
					Deltas:            []json.RawMessage{json.RawMessage(`{"token":"hello"}`)},
					SettlementStatus:  status,
					SettlementPayload: json.RawMessage(`{"ok":true}`),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			recorder := runScriptedEffect(t, dispatcher, effect)
			if dispatcher.Remaining() != 0 || len(dispatcher.Requests()) != 1 {
				t.Fatalf("remaining=%d requests=%d", dispatcher.Remaining(), len(dispatcher.Requests()))
			}
			request := dispatcher.Requests()[0]
			if _, dispatchErr := dispatcher.Dispatch(t.Context(), request, nil); !errors.Is(dispatchErr, runtest.ErrUnexpectedDispatch) {
				t.Fatalf("overrun=%v", dispatchErr)
			}
			other, err := run.NewDispatcherEffect([]byte(`{"operation":"other"}`))
			if err != nil {
				t.Fatal(err)
			}
			mismatched, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []runtest.ScriptedCall{{ExpectedEffect: &other, SettlementStatus: status, SettlementPayload: []byte(`{}`)}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mismatched.Dispatch(t.Context(), request, nil); !errors.Is(err, runtest.ErrEffectMismatch) {
				t.Fatalf("mismatch=%v", err)
			}
			if _, err := recorder.AwaitEvent(t.Context(), nil); !errors.Is(err, runtest.ErrInvalidEventPredicate) {
				t.Fatalf("nil predicate=%v", err)
			}
			deltas := recorder.Deltas()
			if len(deltas) != 1 || string(deltas[0].Payload()) != `{"token":"hello"}` {
				t.Fatalf("deltas=%v", deltas)
			}
		})
	}
}

func runScriptedEffect(t *testing.T, dispatcher *runtest.ScriptedDispatcher, effect run.Effect) *runtest.ObservationRecorder {
	t.Helper()
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition:           newScriptedEffectDefinition(t, effect),
		Dispatcher:           dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("agenttest scripted fixture implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte("agenttest scripted fixture configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &runtest.ObservationRecorder{}
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(),
		EventListeners: []run.EventListener{recorder},
		DeltaListeners: []run.DeltaListener{recorder},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := run.EncodePayload("start")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, deployment, input)
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("Run result=%+v error=%v", result, err)
	}
	awaitCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := recorder.AwaitEvent(awaitCtx, func(event run.Event) bool {
		return event.Name() == run.EventProcessFinished
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	return recorder
}

type scriptedEffectDefinition struct {
	descriptor run.Descriptor
	effect     run.Effect
}

func newScriptedEffectDefinition(t *testing.T, effect run.Effect) *scriptedEffectDefinition {
	t.Helper()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: scriptedEffectStateKind, Description: "Exercises the public scripted dispatcher fixture.",
		InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &scriptedEffectDefinition{descriptor: descriptor, effect: effect}
}

func (s *scriptedEffectDefinition) Descriptor() run.Descriptor {
	return s.descriptor
}

func (*scriptedEffectDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (s *scriptedEffectDefinition) Start(input run.Payload) (run.Execution, error) {
	if err := s.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &scriptedEffectExecution{effect: s.effect}, nil
}

func (s *scriptedEffectDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if state.Kind() != scriptedEffectStateKind {
		return nil, errors.New("unexpected scripted-effect state")
	}
	var restored scriptedEffectState
	if err := jsonv2.Unmarshal(state.Payload(), &restored); err != nil {
		return nil, fmt.Errorf("decode scripted-effect state: %w", err)
	}
	return &scriptedEffectExecution{effect: s.effect, dispatched: restored.Dispatched}, nil
}

type scriptedEffectState struct {
	Dispatched bool `json:"dispatched"`
}

type scriptedEffectExecution struct {
	effect     run.Effect
	dispatched bool
}

func (s *scriptedEffectExecution) Step(
	_ context.Context,
	signals []run.Signal,
) (run.Transition, error) {
	if !s.dispatched {
		s.dispatched = true
		return run.Continue(0, s.effect)
	}
	output, err := run.EncodePayload("done")
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(uint32(len(signals)), output)
}

func (s *scriptedEffectExecution) Snapshot() (run.ExecutionState, error) {
	payload, err := jsonv2.Marshal(scriptedEffectState{Dispatched: s.dispatched})
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState(scriptedEffectStateKind, payload)
}

func TestScriptedDispatcherSettlementStatuses(t *testing.T) {
	for _, status := range []run.SettlementStatus{run.SettlementStatusSucceeded, run.SettlementStatusFailed, run.SettlementStatusUnknown, "", "t", "failure"} {
		t.Run(string(status), func(t *testing.T) {
			_, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{
				ReplayPolicy: run.ReplayPolicyNever,
				Calls:        []runtest.ScriptedCall{{SettlementStatus: status, SettlementPayload: json.RawMessage(`{}`)}},
			})
			valid := status == run.SettlementStatusSucceeded || status == run.SettlementStatusFailed || status == run.SettlementStatusUnknown
			if valid && err != nil || !valid && !errors.Is(err, runtest.ErrInvalidDispatchScript) {
				t.Fatalf("status=%q error=%v", status, err)
			}
		})
	}
}

func TestScriptedDispatcherKeepsCancellationAndUncertainOutcomesDistinct(t *testing.T) {
	effect, err := run.NewDispatcherEffect([]byte(`{"operation":"scripted"}`))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{
		ReplayPolicy: run.ReplayPolicyNever,
		Calls:        []runtest.ScriptedCall{{ExpectedEffect: &effect, SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(`null`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runScriptedEffect(t, reference, effect)
	request := reference.Requests()[0]
	cause := errors.New("transport lost after a delta")
	delta := json.RawMessage(`{"token":"first"}`)
	dispatcher, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{
		ReplayPolicy: run.ReplayPolicyNever,
		Calls:        []runtest.ScriptedCall{{ExpectedEffect: &effect, Deltas: []json.RawMessage{delta}, Error: cause}},
	})
	if err != nil {
		t.Fatal(err)
	}
	clear(delta)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if settlement, dispatchErr := dispatcher.Dispatch(ctx, request, nil); settlement.Valid() || !errors.Is(dispatchErr, context.Canceled) || dispatcher.Remaining() != 1 || len(dispatcher.Requests()) != 0 {
		t.Fatalf("canceled call consumed a script or reported an outcome: %v, %v", settlement, dispatchErr)
	}
	var observed []string
	settlement, err := dispatcher.Dispatch(t.Context(), request, func(data json.RawMessage) {
		observed = append(observed, string(data))
		clear(data)
	})
	if settlement.Valid() || !errors.Is(err, cause) || len(observed) != 1 || observed[0] != `{"token":"first"}` || dispatcher.Remaining() != 0 {
		t.Fatalf("streaming failure lost evidence: outcome=%v, err=%v, deltas=%v", settlement, err, observed)
	}
	requests := dispatcher.Requests()
	requests[0] = run.EffectRequest{}
	if !dispatcher.Requests()[0].Valid() {
		t.Fatal("request history escaped its owner")
	}
}

func TestScriptedDispatcherRejectsUnusableScriptAndMissingRequests(t *testing.T) {
	effect, err := run.NewDispatcherEffect([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	invalid := run.Effect{}
	for _, call := range []runtest.ScriptedCall{
		{ExpectedEffect: &invalid, SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(`null`)},
		{SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(`{`)},
	} {
		if _, configErr := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []runtest.ScriptedCall{call}}); !errors.Is(configErr, runtest.ErrInvalidDispatchScript) {
			t.Fatalf("unusable script = %v", configErr)
		}
	}
	dispatcher, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{
		ReplayPolicy: run.ReplayPolicyNever,
		Calls:        []runtest.ScriptedCall{{ExpectedEffect: &effect, SettlementStatus: run.SettlementStatusSucceeded, SettlementPayload: []byte(`null`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Dispatch(t.Context(), run.EffectRequest{}, nil); !errors.Is(err, runtest.ErrEffectMismatch) || !errors.Is(err, run.ErrInvalidEffect) {
		t.Fatalf("missing effect = %v", err)
	}
	var absent *runtest.ScriptedDispatcher
	if _, err := absent.Dispatch(t.Context(), run.EffectRequest{}, nil); !errors.Is(err, runtest.ErrInvalidDispatchScript) {
		t.Fatal(err)
	}
	if absent.Policy(effect).Replay.Valid() || dispatcher.Policy(run.Effect{}).Replay.Valid() || absent.Requests() != nil || absent.Remaining() != 0 {
		t.Fatal("unconfigured dispatcher supplied policy or history")
	}
	var recorder *runtest.ObservationRecorder
	if recorder.Events() != nil || recorder.Deltas() != nil {
		t.Fatal("missing recorder returned observations")
	}
	if _, err := recorder.AwaitEvent(t.Context(), func(run.Event) bool { return true }); !errors.Is(err, runtest.ErrInvalidEventPredicate) {
		t.Fatal(err)
	}
	recorder = &runtest.ObservationRecorder{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := recorder.AwaitEvent(ctx, func(run.Event) bool { return false }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
