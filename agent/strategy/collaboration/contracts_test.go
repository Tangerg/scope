package collaboration

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestRejectsDecisionBatchBeforeDeclaringActions(t *testing.T) {
	definition := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	for name, decision := range map[string]Decision{
		"mode":               {Mode: "invalid", State: input("x")},
		"state schema":       {Mode: ModeContinue, State: require(agent.EncodePayload(1))},
		"undecided":          {State: input("x")},
		"complete with task": {State: input("x"), Tasks: []TaskRequest{request("work", "test.echo", "x")}, Output: requireOutput("x")},
		"output schema":      {State: input("x"), Output: requireOutput(1)},
		"nonterminal output": {Mode: ModeContinue, State: input("x"), Output: requireOutput("x")},
		"unavailable worker": {Mode: ModeContinue, State: input("x"), Tasks: []TaskRequest{request("work", "absent", "x")}},
		"reserved key":       {Mode: ModeContinue, State: input("x"), Tasks: []TaskRequest{request("collaboration.turn.1", "test.echo", "x")}},
		"duplicate key":      {Mode: ModeContinue, State: input("x"), Tasks: []TaskRequest{request("work", "test.echo", "x"), request("work", "test.echo", "y")}},
		"input schema":       {Mode: ModeContinue, State: input("x"), Tasks: []TaskRequest{{Key: require(agent.ParseChildKey("work")), Worker: "test.echo", Input: require(agent.EncodePayload(1))}}},
		"empty wait":         {Mode: ModeWait, State: input("x")},
		"foreign control":    {Mode: ModeContinue, State: input("x"), Controls: []Control{{Task: require(agent.ParseChildKey("absent"))}}},
		"concurrency bound":  {Mode: ModeContinue, State: input("x"), Tasks: []TaskRequest{request("a", "test.echo", "x"), request("b", "test.echo", "x"), request("c", "test.echo", "x"), request("d", "test.echo", "x"), request("e", "test.echo", "x")}},
	} {
		t.Run(name, func(t *testing.T) {
			execution := require(definition.Start(input("initial"))).(*execution)
			before := require(execution.Snapshot())
			transition, err := execution.applyDecision(t.Context(), decision, 0)
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

func requireOutput[T any](value T) agent.Payload {
	output := require(agent.EncodePayload(value))
	return output
}

func TestConfigurationAndProtocolContracts(t *testing.T) {
	validConfig := fixtureConfig(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	definition := require(NewDefinition(validConfig))
	for name, mutate := range map[string]func(*DefinitionConfig){
		"zero concurrency":           func(config *DefinitionConfig) { config.MaxConcurrentTasks = 0 },
		"zero control capacity":      func(config *DefinitionConfig) { config.MaxControlsPerTurn = 0 },
		"missing workers":            func(config *DefinitionConfig) { config.Workers = nil },
		"duplicate workers":          func(config *DefinitionConfig) { config.Workers = append(config.Workers, config.Workers[0]) },
		"invalid worker":             func(config *DefinitionConfig) { config.Workers = []WorkerConfig{{}} },
		"wrong coordinator contract": func(config *DefinitionConfig) { config.Coordinator = workerConfig(echo()) },
		"invalid descriptor":         func(config *DefinitionConfig) { config.Name = "UPPER CASE" },
	} {
		t.Run(name, func(t *testing.T) {
			config := validConfig
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
	if _, err := missing.Restore(t.Context(), agent.ExecutionState{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, err := definition.Start(require(agent.EncodePayload(1))); !errors.Is(err, agent.ErrInvalidPayload) {
		t.Fatal(err)
	}
	if _, err := definition.Restore(t.Context(), agent.ExecutionState{}); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatal(err)
	}
	execution := require(definition.Start(input("x"))).(*execution)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := execution.Step(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := jsonv2.Unmarshal([]byte(`{"id":"signal:unexpected","payload":"x"}`), &signal); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Step(context.Background(), []agent.Signal{signal}); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal(err)
	}
}

type tracedDefinition struct {
	agent.Definition
	mu    sync.Mutex
	cases []agenttest.ExecutionConformanceCase
}

func (t *tracedDefinition) Start(input agent.Payload) (agent.Execution, error) {
	execution, err := t.Definition.Start(input)
	return &tracedExecution{Execution: execution, owner: t}, err
}
func (t *tracedDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	execution, err := t.Definition.Restore(ctx, state)
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
	config := fixtureConfig(func(_ context.Context, turn Turn) (Decision, error) {
		switch turn.Number {
		case 1:
			return Decision{Mode: ModeContinue, State: input("working"), Tasks: []TaskRequest{request("a", "test.gate", "wait")}}, nil
		case 2:
			reason := "finished"
			return Decision{Mode: ModeWait, State: turn.State, Controls: []Control{{Task: turn.Tasks[0].Request.Key, CancelReason: &reason}}}, nil
		default:
			return finish(turn, "done"), nil
		}
	}, gate())
	config.MaxTurns = agent.NewQuota(8)
	definition := require(NewDefinition(config))
	trace := &tracedDefinition{Definition: definition}
	engine := require(agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}))
	defer engine.Close(t.Context())
	process := require(engine.Start(t.Context(), binding(trace), input("initial")))
	completed(t, process)
	trace.mu.Lock()
	cases := append([]agenttest.ExecutionConformanceCase{}, trace.cases...)
	trace.mu.Unlock()
	phases := make(map[phase]bool)
	var declared []agent.ExecutionState
	var controlled []agent.ExecutionState
	for index := range cases {
		var state executionState
		if err := jsonv2.Unmarshal(cases[index].State.Payload(), &state); err != nil {
			t.Fatal(err)
		}
		current := state.phase()
		cases[index].Name = fmt.Sprintf("phase%d-%c", current, 'a'+index)
		phases[current] = true
		if len(state.Controls) != 0 && state.Controls[0].Result != nil {
			controlled = append(controlled, cases[index].State)
		}
		if current != phaseReady {
			restored := require(definition.Restore(t.Context(), cases[index].State))
			if _, err := restored.Step(t.Context(), nil); !errors.Is(err, ErrInvalidProtocol) {
				t.Fatalf("%s accepted a Step without its protocol Signal: %v", cases[index].Name, err)
			}
		}
		mutations := map[string]func(*executionState){}
		if state.Turn == nil {
			mutations["changed initial state"] = func(state *executionState) { state.InitialState = require(agent.EncodePayload(1)) }
		} else {
			mutations["excess turns"] = func(state *executionState) {
				maximum, _ := definition.maxTurns.Maximum()
				state.Turn.Number = maximum + 1
			}
			mutations["zero turn number"] = func(state *executionState) { state.Turn.Number = 0 }
			mutations["changed input state"] = func(state *executionState) { state.Turn.State = require(agent.EncodePayload(1)) }
			mutations["retained initial state"] = func(state *executionState) { state.InitialState = input("forged") }
		}
		if len(state.Tasks) > 0 {
			mutations["duplicate task"] = func(state *executionState) { state.Tasks = append(state.Tasks, state.Tasks[0]) }
			// The current decision owns the requests it declared; a task it
			// declared must not repeat its request.
			if decision, err := state.decision(); err == nil && len(decision.Tasks) == len(state.Tasks) {
				declared = append(declared, cases[index].State)
			}
		}
		if len(state.Tasks) > 0 && state.Tasks[0].Outcome != nil {
			mutations["worker terminal boundary"] = func(state *executionState) {
				var wire map[string]json.RawMessage
				if err := jsonv2.Unmarshal(require(jsonv2.Marshal(state.Tasks[0].Outcome)), &wire); err != nil {
					t.Fatal(err)
				}
				delete(wire, "descendant_unresolved_effects")
				var outcome agent.ChildOutcome
				if err := jsonv2.Unmarshal(require(jsonv2.Marshal(wire)), &outcome); err != nil {
					t.Fatal(err)
				}
				state.Tasks[0].Outcome = &outcome
			}
		}
		for name, mutate := range mutations {
			t.Run(cases[index].Name+"/"+name, func(t *testing.T) {
				var altered executionState
				if err := jsonv2.Unmarshal(cases[index].State.Payload(), &altered); err != nil {
					t.Fatal(err)
				}
				mutate(&altered)
				encoded := require(agent.ParseExecutionState(stateKind, require(jsonv2.Marshal(altered))))
				if _, err := definition.Restore(t.Context(), encoded); !errors.Is(err, ErrInvalidExecutionState) {
					t.Fatalf("forged snapshot accepted: %v", err)
				}
			})
		}
		payload := strings.TrimSuffix(string(cases[index].State.Payload()), "}") + `,"unknown":true}`
		if _, err := definition.Restore(t.Context(), require(agent.ParseExecutionState(stateKind, []byte(payload)))); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatal(err)
		}
	}
	if len(declared) == 0 {
		t.Fatal("no captured state holds decision-declared tasks")
	}
	for _, state := range declared {
		var wire map[string]any
		if err := jsonv2.Unmarshal(state.Payload(), &wire); err != nil {
			t.Fatal(err)
		}
		wire["tasks"].([]any)[0].(map[string]any)["request"] = map[string]any{"key": "forged", "worker": "forged", "input": "forged"}
		altered := require(agent.ParseExecutionState(stateKind, require(jsonv2.Marshal(wire))))
		if _, err := definition.Restore(t.Context(), altered); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("decision-declared task repeated its request: %v", err)
		}
	}
	if len(controlled) == 0 {
		t.Fatal("no captured state holds a settled control")
	}
	for _, state := range controlled {
		for _, operation := range []string{"signal_child", "cancel_child"} {
			var wire map[string]any
			if err := jsonv2.Unmarshal(state.Payload(), &wire); err != nil {
				t.Fatal(err)
			}
			wire["controls"].([]any)[0].(map[string]any)["result"].(map[string]any)["operation"] = operation
			altered := require(agent.ParseExecutionState(stateKind, require(jsonv2.Marshal(wire))))
			if _, err := definition.Restore(t.Context(), altered); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("control receipt restored a request operation %s: %v", operation, err)
			}
		}
	}
	for _, phase := range []phase{phaseReady, phaseStartingTurn, phaseApplying, phaseOpening, phaseWaiting} {
		if !phases[phase] {
			t.Fatalf("phase %d untested", phase)
		}
	}
	for _, sample := range cases {
		conformancetest.CheckRestoreCancellation(t, definition, sample.State)
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{Definition: definition, Input: input("initial"), RestoredCases: cases})
}

func TestCompletedStateLeavesOutputToTheEngine(t *testing.T) {
	definition := fixture(func(_ context.Context, turn Turn) (Decision, error) {
		if turn.Number == 1 {
			return Decision{Mode: ModeWait, State: turn.State, Tasks: []TaskRequest{request("work", "test.echo", "value")}}, nil
		}
		return finish(turn, "done"), nil
	}, echo())
	engine, process := run(t, definition, agent.NewMemoryTreeCommitter())
	completed(t, process)
	tree := require(engine.InspectTree(t.Context(), process.Relation().ProcessID()))
	root, _ := tree.Process(process.Relation().ProcessID())
	state := root.Snapshot.CommittedExecutionState()
	if string(state.Payload()) != `{"completed":true}` {
		t.Fatalf("completed state = %s, want only the completion marker", state.Payload())
	}
	if _, err := definition.Restore(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"completed":true,"output":"forged final answer"}`,
		`{"completed":true,"initial_state":"seed"}`,
		`{"completed":true,"turn":{"number":1,"state":"seed"}}`,
	} {
		altered := require(agent.ParseExecutionState(stateKind, json.RawMessage(payload)))
		if _, err := definition.Restore(t.Context(), altered); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("forged completed state %s accepted: %v", payload, err)
		}
	}
}

func TestRestoreStopsBetweenTasks(t *testing.T) {
	definition := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	state := executionState{Tasks: []Task{{Request: request("work", "test.echo", "x")}, {}}}
	ctx, cancel := conformancetest.CancelAfterCheck(t.Context(), 2)
	defer cancel()
	if _, err := state.validateTasks(ctx, definition); !errors.Is(err, context.Canceled) {
		t.Fatalf("task validation = %v, want cancellation before malformed second task", err)
	}
}
