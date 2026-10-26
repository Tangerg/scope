package agenttest_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/core/jsonschema"
)

// scriptedEffectStateKind names the Definition and its ExecutionState kind.
const scriptedEffectStateKind = "agenttest.scripted_effect"

func TestNewScriptedDispatcherRejectsContradictorySteps(t *testing.T) {
	injected := errors.New("injected dispatch failure")
	tests := []struct {
		name   string
		config agenttest.ScriptedDispatcherConfig
	}{
		{name: "invalid replay policy", config: agenttest.ScriptedDispatcherConfig{}},
		{
			name: "invalid delta",
			config: agenttest.ScriptedDispatcherConfig{
				ReplayPolicy: agent.ReplayPolicyNever,
				Calls: []agenttest.ScriptedCall{{
					Deltas:            []json.RawMessage{json.RawMessage(`{`)},
					SettlementStatus:  agent.SettlementStatusSucceeded,
					SettlementPayload: json.RawMessage(`{}`),
				}},
			},
		},
		{
			name: "error and settlement",
			config: agenttest.ScriptedDispatcherConfig{
				ReplayPolicy: agent.ReplayPolicyNever,
				Calls: []agenttest.ScriptedCall{{
					SettlementStatus:  agent.SettlementStatusFailed,
					SettlementPayload: json.RawMessage(`{}`),
					Error:             injected,
				}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := agenttest.NewScriptedDispatcher(test.config); !errors.Is(err, agenttest.ErrInvalidDispatchScript) {
				t.Fatalf("NewScriptedDispatcher error=%v", err)
			}
		})
	}
}

func TestScriptedDispatcherRunsThroughPublicEngineBoundary(t *testing.T) {
	for _, status := range []agent.SettlementStatus{agent.SettlementStatusSucceeded, agent.SettlementStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			effect, err := agent.NewDispatcherEffect(json.RawMessage(`{"operation":"scripted"}`))
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{
				ReplayPolicy: agent.ReplayPolicySameIdentity,
				Calls: []agenttest.ScriptedCall{{
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
			if _, dispatchErr := dispatcher.Dispatch(t.Context(), request, nil); !errors.Is(dispatchErr, agenttest.ErrUnexpectedDispatch) {
				t.Fatalf("overrun=%v", dispatchErr)
			}
			other, err := agent.NewDispatcherEffect([]byte(`{"operation":"other"}`))
			if err != nil {
				t.Fatal(err)
			}
			mismatched, err := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{ReplayPolicy: agent.ReplayPolicyNever, Calls: []agenttest.ScriptedCall{{ExpectedEffect: &other, SettlementStatus: status, SettlementPayload: []byte(`{}`)}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mismatched.Dispatch(t.Context(), request, nil); !errors.Is(err, agenttest.ErrEffectMismatch) {
				t.Fatalf("mismatch=%v", err)
			}
			if _, err := recorder.AwaitEvent(t.Context(), nil); !errors.Is(err, agenttest.ErrInvalidEventPredicate) {
				t.Fatalf("nil predicate=%v", err)
			}
			deltas := recorder.Deltas()
			if len(deltas) != 1 || string(deltas[0].Payload()) != `{"token":"hello"}` {
				t.Fatalf("deltas=%v", deltas)
			}
		})
	}
}

func runScriptedEffect(t *testing.T, dispatcher *agenttest.ScriptedDispatcher, effect agent.Effect) *agenttest.ObservationRecorder {
	t.Helper()
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           newScriptedEffectDefinition(t, effect),
		Dispatcher:           dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("agenttest scripted fixture implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("agenttest scripted fixture configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &agenttest.ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(),
		EventListeners: []agent.EventListener{recorder},
		DeltaListeners: []agent.DeltaListener{recorder},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodePayload("start")
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil || result.Termination().Status() != agent.StatusCompleted {
		t.Fatalf("Run result=%+v error=%v", result, err)
	}
	awaitCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := recorder.AwaitEvent(awaitCtx, func(event agent.Event) bool {
		return event.Name() == agent.EventProcessFinished
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	return recorder
}

type scriptedEffectDefinition struct {
	descriptor agent.Descriptor
	effect     agent.Effect
}

func newScriptedEffectDefinition(t *testing.T, effect agent.Effect) *scriptedEffectDefinition {
	t.Helper()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: scriptedEffectStateKind, Description: "Exercises the public scripted dispatcher fixture.",
		InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &scriptedEffectDefinition{descriptor: descriptor, effect: effect}
}

func (s *scriptedEffectDefinition) Descriptor() agent.Descriptor {
	return s.descriptor
}

func (*scriptedEffectDefinition) ChildDeployments() []agent.Deployment { return nil }

func (s *scriptedEffectDefinition) Start(input agent.Payload) (agent.Execution, error) {
	if err := s.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &scriptedEffectExecution{effect: s.effect}, nil
}

func (s *scriptedEffectDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
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
	effect     agent.Effect
	dispatched bool
}

func (s *scriptedEffectExecution) Step(
	_ context.Context,
	signals []agent.Signal,
) (agent.Transition, error) {
	if !s.dispatched {
		s.dispatched = true
		return agent.Continue(0, s.effect)
	}
	output, err := agent.EncodePayload("done")
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(uint32(len(signals)), output)
}

func (s *scriptedEffectExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := jsonv2.Marshal(scriptedEffectState{Dispatched: s.dispatched})
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.ParseExecutionState(scriptedEffectStateKind, payload)
}

func TestScriptedDispatcherSettlementStatuses(t *testing.T) {
	for _, status := range []agent.SettlementStatus{agent.SettlementStatusSucceeded, agent.SettlementStatusFailed, agent.SettlementStatusUnknown, "", "t", "failure"} {
		t.Run(string(status), func(t *testing.T) {
			_, err := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{
				ReplayPolicy: agent.ReplayPolicyNever,
				Calls:        []agenttest.ScriptedCall{{SettlementStatus: status, SettlementPayload: json.RawMessage(`{}`)}},
			})
			valid := status == agent.SettlementStatusSucceeded || status == agent.SettlementStatusFailed || status == agent.SettlementStatusUnknown
			if valid && err != nil || !valid && !errors.Is(err, agenttest.ErrInvalidDispatchScript) {
				t.Fatalf("status=%q error=%v", status, err)
			}
		})
	}
}

func TestScriptedDispatcherKeepsCancellationAndUncertainOutcomesDistinct(t *testing.T) {
	effect, err := agent.NewDispatcherEffect([]byte(`{"operation":"scripted"}`))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{
		ReplayPolicy: agent.ReplayPolicyNever,
		Calls:        []agenttest.ScriptedCall{{ExpectedEffect: &effect, SettlementStatus: agent.SettlementStatusSucceeded, SettlementPayload: []byte(`null`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runScriptedEffect(t, reference, effect)
	request := reference.Requests()[0]
	cause := errors.New("transport lost after a delta")
	delta := json.RawMessage(`{"token":"first"}`)
	dispatcher, err := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{
		ReplayPolicy: agent.ReplayPolicyNever,
		Calls:        []agenttest.ScriptedCall{{ExpectedEffect: &effect, Deltas: []json.RawMessage{delta}, Error: cause}},
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
	requests[0] = agent.EffectRequest{}
	if !dispatcher.Requests()[0].Valid() {
		t.Fatal("request history escaped its owner")
	}
}

func TestScriptedDispatcherRejectsUnusableScriptAndMissingRequests(t *testing.T) {
	effect, err := agent.NewDispatcherEffect([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	invalid := agent.Effect{}
	for _, call := range []agenttest.ScriptedCall{
		{ExpectedEffect: &invalid, SettlementStatus: agent.SettlementStatusSucceeded, SettlementPayload: []byte(`null`)},
		{SettlementStatus: agent.SettlementStatusSucceeded, SettlementPayload: []byte(`{`)},
	} {
		if _, configErr := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{ReplayPolicy: agent.ReplayPolicyNever, Calls: []agenttest.ScriptedCall{call}}); !errors.Is(configErr, agenttest.ErrInvalidDispatchScript) {
			t.Fatalf("unusable script = %v", configErr)
		}
	}
	dispatcher, err := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{
		ReplayPolicy: agent.ReplayPolicyNever,
		Calls:        []agenttest.ScriptedCall{{ExpectedEffect: &effect, SettlementStatus: agent.SettlementStatusSucceeded, SettlementPayload: []byte(`null`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Dispatch(t.Context(), agent.EffectRequest{}, nil); !errors.Is(err, agenttest.ErrEffectMismatch) || !errors.Is(err, agent.ErrInvalidEffect) {
		t.Fatalf("missing effect = %v", err)
	}
	var absent *agenttest.ScriptedDispatcher
	if _, err := absent.Dispatch(t.Context(), agent.EffectRequest{}, nil); !errors.Is(err, agenttest.ErrInvalidDispatchScript) {
		t.Fatal(err)
	}
	if absent.Policy(effect).Replay.Valid() || dispatcher.Policy(agent.Effect{}).Replay.Valid() || absent.Requests() != nil || absent.Remaining() != 0 {
		t.Fatal("unconfigured dispatcher supplied policy or history")
	}
	var recorder *agenttest.ObservationRecorder
	if recorder.Events() != nil || recorder.Deltas() != nil {
		t.Fatal("missing recorder returned observations")
	}
	if _, err := recorder.AwaitEvent(t.Context(), func(agent.Event) bool { return true }); !errors.Is(err, agenttest.ErrInvalidEventPredicate) {
		t.Fatal(err)
	}
	recorder = &agenttest.ObservationRecorder{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := recorder.AwaitEvent(ctx, func(agent.Event) bool { return false }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
