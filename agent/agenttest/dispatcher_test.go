package agenttest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

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
				Steps: []agenttest.DispatchStep{{
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
				Steps: []agenttest.DispatchStep{{
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
	effect, err := agent.NewDispatcherEffect(json.RawMessage(`{"operation":"scripted"}`))
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{
		ReplayPolicy: agent.ReplayPolicySameIdentity,
		Steps: []agenttest.DispatchStep{{
			ExpectedEffect:    &effect,
			Deltas:            []json.RawMessage{json.RawMessage(`{"token":"hello"}`)},
			SettlementStatus:  agent.SettlementStatusSucceeded,
			SettlementPayload: json.RawMessage(`{"ok":true}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := newScriptedEffectDefinition(t, effect)
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		Dispatcher:           dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("agenttest scripted fixture implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("agenttest scripted fixture configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &agenttest.ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{
		EventListeners: []agent.EventListener{recorder},
		DeltaListeners: []agent.DeltaListener{recorder},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput("start")
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil || result.Status() != agent.StatusCompleted {
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
	if dispatcher.Remaining() != 0 || len(dispatcher.Requests()) != 1 {
		t.Fatalf("remaining=%d requests=%d", dispatcher.Remaining(), len(dispatcher.Requests()))
	}
	deltas := recorder.Deltas()
	if len(deltas) != 1 || string(deltas[0].Payload()) != `{"token":"hello"}` {
		t.Fatalf("deltas=%v", deltas)
	}
}

type scriptedEffectDefinition struct {
	descriptor agent.Descriptor
	effect     agent.Effect
}

func newScriptedEffectDefinition(t *testing.T, effect agent.Effect) *scriptedEffectDefinition {
	t.Helper()
	schema, err := agent.SchemaFor[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "agenttest.scripted_effect", Description: "Exercises the public scripted dispatcher fixture.",
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

func (s *scriptedEffectDefinition) Start(input agent.Input) (agent.Execution, error) {
	if err := s.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &scriptedEffectExecution{effect: s.effect}, nil
}

func (s *scriptedEffectDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if state.Kind() != "agenttest.scripted_effect" {
		return nil, errors.New("unexpected scripted-effect state")
	}
	var restored scriptedEffectState
	if err := json.Unmarshal(state.Payload(), &restored); err != nil {
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
	output, err := agent.EncodeOutput("done")
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(uint32(len(signals)), output)
}

func (s *scriptedEffectExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(scriptedEffectState{Dispatched: s.dispatched})
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("agenttest.scripted_effect", payload)
}
