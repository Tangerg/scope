package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

func TestRejectsDecisionBatchBeforeDeclaringActions(t *testing.T) {
	definition, _ := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	for name, decision := range map[string]Decision{
		"mode":               {Mode: "invalid", State: input("x")},
		"state schema":       {Mode: Continue, State: require(agent.EncodeInput(1))},
		"missing output":     {Mode: Complete, State: input("x")},
		"complete with task": {Mode: Complete, State: input("x"), Tasks: []TaskRequest{request("work", "test.echo", "x")}, Output: requireOutput("x")},
		"output schema":      {Mode: Complete, State: input("x"), Output: requireOutput(1)},
		"nonterminal output": {Mode: Continue, State: input("x"), Output: requireOutput("x")},
		"unavailable worker": {Mode: Continue, State: input("x"), Tasks: []TaskRequest{request("work", "absent", "x")}},
		"reserved key":       {Mode: Continue, State: input("x"), Tasks: []TaskRequest{request("collaboration.turn.1", "test.echo", "x")}},
		"duplicate key":      {Mode: Continue, State: input("x"), Tasks: []TaskRequest{request("work", "test.echo", "x"), request("work", "test.echo", "y")}},
		"input schema":       {Mode: Continue, State: input("x"), Tasks: []TaskRequest{{Key: require(agent.ParseChildKey("work")), Worker: "test.echo", Input: require(agent.EncodeInput(1))}}},
		"empty wait":         {Mode: Wait, State: input("x")},
		"foreign control":    {Mode: Continue, State: input("x"), Controls: []Control{{Task: require(agent.ParseChildKey("absent"))}}},
		"concurrency bound":  {Mode: Continue, State: input("x"), Tasks: []TaskRequest{request("a", "test.echo", "x"), request("b", "test.echo", "x"), request("c", "test.echo", "x"), request("d", "test.echo", "x"), request("e", "test.echo", "x")}},
	} {
		t.Run(name, func(t *testing.T) {
			execution := require(definition.Start(input("initial"))).(*execution)
			before := require(execution.Snapshot())
			transition, err := execution.applyDecision(decision, 0)
			if !errors.Is(err, ErrInvalidDecision) || transition.Valid() {
				t.Fatalf("decision=%+v err=%v", transition, err)
			}
			after := require(execution.Snapshot())
			if string(before.Payload()) != string(after.Payload()) {
				t.Fatal("rejected batch changed state")
			}
		})
	}
}

func requireOutput[T any](value T) *agent.Output {
	output := require(agent.EncodeOutput(value))
	return &output
}

func TestConfigurationAndProtocolContracts(t *testing.T) {
	definition, _ := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	for name, mutate := range map[string]func(*DefinitionConfig){
		"zero bound":                   func(config *DefinitionConfig) { config.MaxTurns = 0 },
		"concurrency exceeds lifetime": func(config *DefinitionConfig) { config.MaxConcurrentTasks = config.MaxTasks + 1 },
		"missing workers":              func(config *DefinitionConfig) { config.Workers = nil },
		"duplicate workers":            func(config *DefinitionConfig) { config.Workers = append(config.Workers, config.Workers[0]) },
		"invalid worker":               func(config *DefinitionConfig) { config.Workers = []WorkerConfig{{}} },
		"wrong coordinator contract":   func(config *DefinitionConfig) { config.Coordinator = workerConfig(echo()) },
		"invalid descriptor":           func(config *DefinitionConfig) { config.Name = "UPPER CASE" },
	} {
		t.Run(name, func(t *testing.T) {
			config := definition.config
			mutate(&config)
			if _, err := NewDefinition(config); !errors.Is(err, ErrInvalidConfig) {
				t.Fatal(err)
			}
		})
	}
	var missing *Definition
	if missing.Descriptor().Valid() {
		t.Fatal("nil descriptor is valid")
	}
	if _, err := missing.Start(input("x")); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, err := missing.Restore(agent.ExecutionState{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, err := definition.Start(require(agent.EncodeInput(1))); !errors.Is(err, agent.ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err := definition.Restore(agent.ExecutionState{}); !errors.Is(err, ErrInvalidState) {
		t.Fatal(err)
	}
	execution := require(definition.Start(input("x"))).(*execution)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := execution.Step(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := json.Unmarshal([]byte(`{"id":"signal:unexpected","payload":"x"}`), &signal); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Step(context.Background(), []agent.Signal{signal}); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal(err)
	}
	for _, phase := range []phase{phaseStartingTurn, phaseApplying, phaseOpening, phaseWaiting, phaseCompleted} {
		execution.state.Phase = phase
		if _, err := execution.Step(context.Background(), nil); !errors.Is(err, ErrInvalidProtocol) {
			t.Fatal(err)
		}
	}
}

type tracedDefinition struct {
	agent.Definition
	mu    sync.Mutex
	cases []agenttest.ExecutionConformanceCase
}

func (t *tracedDefinition) Start(input agent.Input) (agent.Execution, error) {
	execution, err := t.Definition.Start(input)
	return &tracedExecution{Execution: execution, owner: t}, err
}
func (t *tracedDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := t.Definition.Restore(state)
	return &tracedExecution{Execution: execution, owner: t}, err
}

type tracedExecution struct {
	agent.Execution
	owner *tracedDefinition
}

func (t *tracedExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	before, err := t.Snapshot()
	if err != nil {
		return agent.Transition{}, err
	}
	transition, err := t.Execution.Step(ctx, signals)
	if err == nil {
		t.owner.mu.Lock()
		t.owner.cases = append(t.owner.cases, agenttest.ExecutionConformanceCase{State: before, Signals: signals})
		t.owner.mu.Unlock()
	}
	return transition, err
}

func TestEveryExecutionPhaseRestoresAndRejectsContradictions(t *testing.T) {
	definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
		switch turn.Number {
		case 1:
			return Decision{Mode: Continue, State: input("working"), Tasks: []TaskRequest{request("a", "test.gate", "wait")}}, nil
		case 2:
			reason := "finished"
			return Decision{Mode: Wait, State: turn.State, Controls: []Control{{Task: turn.Tasks[0].Request.Key, CancelReason: &reason}}}, nil
		default:
			return finish(turn, "done"), nil
		}
	}, gate())
	trace := &tracedDefinition{Definition: definition}
	engine := require(agent.NewEngine(agent.EngineConfig{DeploymentResolver: deployments}))
	defer engine.Close(t.Context())
	process := require(engine.Start(t.Context(), binding(trace), input("initial")))
	completed(t, process)
	trace.mu.Lock()
	cases := append([]agenttest.ExecutionConformanceCase{}, trace.cases...)
	trace.mu.Unlock()
	phases := make(map[phase]bool)
	for index := range cases {
		var state executionState
		if err := json.Unmarshal(cases[index].State.Payload(), &state); err != nil {
			t.Fatal(err)
		}
		cases[index].Name = string(state.Phase) + "-" + string(rune('a'+index))
		phases[state.Phase] = true
		mutations := map[string]func(*executionState){
			"unknown phase": func(state *executionState) { state.Phase = "unknown" },
			"excess turns":  func(state *executionState) { state.Number = definition.config.MaxTurns + 1 },
			"changed state": func(state *executionState) { state.State = require(agent.EncodeInput(1)) },
		}
		if state.Turn != nil {
			mutations["changed turn number"] = func(state *executionState) { state.Turn.Input.Number++ }
			mutations["changed worker catalog"] = func(state *executionState) { state.Turn.Input.Workers = nil }
			mutations["different carried state"] = func(state *executionState) { state.State = input("forged") }
		}
		if len(state.Tasks) > 0 {
			mutations["duplicate task"] = func(state *executionState) { state.Tasks = append(state.Tasks, state.Tasks[0]) }
			mutations["changed task input"] = func(state *executionState) { state.Tasks[0].Request.Input = input("forged") }
		}
		for name, mutate := range mutations {
			t.Run(cases[index].Name+"/"+name, func(t *testing.T) {
				var altered executionState
				if err := json.Unmarshal(cases[index].State.Payload(), &altered); err != nil {
					t.Fatal(err)
				}
				mutate(&altered)
				encoded := require(agent.NewExecutionState(stateKind, require(json.Marshal(altered))))
				if _, err := definition.Restore(encoded); !errors.Is(err, ErrInvalidState) {
					t.Fatalf("forged snapshot accepted: %v", err)
				}
			})
		}
		payload := strings.TrimSuffix(string(cases[index].State.Payload()), "}") + `,"unknown":true}`
		if _, err := definition.Restore(require(agent.NewExecutionState(stateKind, []byte(payload)))); !errors.Is(err, ErrInvalidState) {
			t.Fatal(err)
		}
	}
	for _, phase := range []phase{phaseReady, phaseStartingTurn, phaseApplying, phaseOpening, phaseWaiting} {
		if !phases[phase] {
			t.Fatalf("phase %s untested", phase)
		}
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{Definition: definition, Input: input("initial"), RestoredCases: cases})
}

func TestCompletedSnapshotRejectsForgedOutputAndWorkerSchema(t *testing.T) {
	definition, deployments := fixture(func(_ context.Context, turn Turn) (Decision, error) {
		if turn.Number == 1 {
			return Decision{Mode: Wait, State: turn.State, Tasks: []TaskRequest{request("work", "test.echo", "value")}}, nil
		}
		return finish(turn, "done"), nil
	}, echo())
	engine, process := run(t, definition, deployments, nil)
	completed(t, process)
	tree := require(engine.InspectTree(t.Context(), process.ID()))
	root, _ := tree.Process(process.ID())
	state := root.Snapshot.CommittedExecutionState()
	if _, err := definition.Restore(state); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(wire map[string]any) { wire["output"] = "forged final answer" },
		func(wire map[string]any) {
			tasks := wire["tasks"].([]any)
			outcome := tasks[0].(map[string]any)["outcome"].(map[string]any)
			outcome["result"].(map[string]any)["output"] = 42
		},
	} {
		var wire map[string]any
		if err := json.Unmarshal(state.Payload(), &wire); err != nil {
			t.Fatal(err)
		}
		mutate(wire)
		altered := require(agent.NewExecutionState(stateKind, require(json.Marshal(wire))))
		if _, err := definition.Restore(altered); !errors.Is(err, ErrInvalidState) {
			t.Fatal("forged completed state accepted", err)
		}
	}
}
