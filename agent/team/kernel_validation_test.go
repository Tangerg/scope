package team

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

func validationPrograms(t *testing.T) (*run.Runtime, Config) {
	t.Helper()
	runtime := conformancetest.Runtime(t, run.Config{})
	coordinator := transformed(t, runtime, "test.validation.coordinator", func(_ context.Context, turn Turn) (Decision, error) {
		return Decision{State: turn.State, Output: input("done")}, nil
	})
	worker := transformed(t, runtime, "test.validation.worker", func(_ context.Context, value string) (string, error) { return value, nil })
	return runtime, Config{
		Name: "test.validation.team", Description: "Exercise team ownership and bounds.",
		Coordinator: run.ChildBinding{Program: coordinator}, Workers: []run.ChildBinding{{Program: worker}},
		MaxTurns: run.NewQuota(2), MaxTasks: run.NewQuota(2), MaxConcurrentTasks: 1, MaxControlsPerTurn: 1,
	}
}

func TestTeamDeclarationRejectsAmbiguousAuthority(t *testing.T) {
	runtime, base := validationPrograms(t)
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"no initial turn", func(c *Config) { c.MaxTurns = run.NewQuota(0) }},
		{"missing coordinator", func(c *Config) { c.Coordinator = run.ChildBinding{} }},
		{"coordinator contract", func(c *Config) { c.Coordinator = c.Workers[0] }},
		{"missing workers", func(c *Config) { c.Workers = nil }},
		{"invalid worker", func(c *Config) { c.Workers = []run.ChildBinding{{}} }},
		{"duplicate worker name", func(c *Config) { c.Workers = append(c.Workers, c.Workers[0]) }},
		{"no concurrent tasks", func(c *Config) { c.MaxConcurrentTasks = 0 }},
		{"no control bound", func(c *Config) { c.MaxControlsPerTurn = 0 }},
		{"invalid program name", func(c *Config) { c.Name = "invalid name" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.change(&config)
			if _, err := Compile[string, string](runtime.Compiler(), config); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("accepted invalid authority declaration: %v", err)
			}
		})
	}
}

func TestTeamRestoreRejectsContradictoryProgress(t *testing.T) {
	runtime, config := validationPrograms(t)
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition()
	root := testTurn(1).Process
	taskKey := require(run.ParseChildKey("task"))
	taskProcess := require(root.Child(taskKey))
	controlEffect := testTurn(2).EffectID(0)
	controlOutcome := require(run.NewSettlement(run.SettlementStatusSucceeded, []byte(`null`)))
	reason := "stop task"
	newState := func() executionState {
		return executionState{WorkingState: input("working"), TurnNumber: 1,
			Tasks:    []taskState{{Request: request("task", config.Workers[0].Program.Descriptor().Name(), "work"), Invocation: child.Invocation{Process: &taskProcess}}},
			Controls: []controlState{{Control: Control{Task: taskKey, CancelReason: &reason}, Effect: &controlEffect}},
		}
	}
	if _, err := definition.Restore(t.Context(), require(run.EncodeExecutionState(stateKind, newState()))); err != nil {
		t.Fatalf("valid progress rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*executionState)
	}{
		{"working state schema", func(s *executionState) { s.WorkingState = input(42) }},
		{"turn bound", func(s *executionState) { s.TurnNumber = 3 }},
		{"concurrent task bound", func(s *executionState) {
			s.Tasks = append(s.Tasks, taskState{Request: request("second", config.Workers[0].Program.Descriptor().Name(), "work"), Invocation: child.Invocation{Request: new(testTurn(1).EffectID(1))}})
		}},
		{"control bound", func(s *executionState) { s.Controls = append(s.Controls, s.Controls[0]) }},
		{"completed coordinator", func(s *executionState) {
			s.Coordinator = &child.Invocation{Termination: new(completed(Decision{State: input("working"), Output: input("done")}))}
		}},
		{"coordinator with wait", func(s *executionState) {
			s.Coordinator = &child.Invocation{Request: new(testTurn(1).EffectID(0))}
			s.WaitForTask = true
		}},
		{"coordinator turn identity", func(s *executionState) {
			s.Coordinator = &child.Invocation{Process: new(require(root.Child(require(turnKey(2)))))}
		}},
		{"reserved task key", func(s *executionState) { s.Tasks[0].Request.Key = require(turnKey(1)) }},
		{"unknown worker", func(s *executionState) { s.Tasks[0].Request.Worker = "test.unknown.worker" }},
		{"worker input schema", func(s *executionState) { s.Tasks[0].Request.Input = input(42) }},
		{"missing task owner", func(s *executionState) { s.Tasks[0].Invocation = child.Invocation{} }},
		{"two task owners", func(s *executionState) { s.Tasks[0].Invocation.Request = new(testTurn(1).EffectID(0)) }},
		{"different task identity", func(s *executionState) {
			s.Tasks[0].Invocation.Process = new(require(root.Child(require(run.ParseChildKey("other")))))
		}},
		{"worker output schema", func(s *executionState) { s.Tasks[0].Invocation = child.Invocation{Termination: new(completed(42))} }},
		{"duplicate completed task", func(s *executionState) {
			s.Tasks[0].Invocation = child.Invocation{Termination: new(completed("done"))}
			s.Tasks = append(s.Tasks, s.Tasks[0])
		}},
		{"control unknown task", func(s *executionState) { s.Controls[0].Control.Task = require(run.ParseChildKey("other")) }},
		{"control no operation", func(s *executionState) { s.Controls[0].Control.CancelReason = nil }},
		{"control two operations", func(s *executionState) {
			s.Controls[0].Control.Signal = &run.Signal{ID: require(run.ParseSignalID("signal")), Payload: input("answer")}
		}},
		{"control blank reason", func(s *executionState) { s.Controls[0].Control.CancelReason = new("") }},
		{"control untrimmed reason", func(s *executionState) { s.Controls[0].Control.CancelReason = new(" stop ") }},
		{"control no result owner", func(s *executionState) { s.Controls[0].Effect = nil }},
		{"control invalid effect identity", func(s *executionState) { s.Controls[0].Effect = new(run.EffectID{}) }},
		{"control two result owners", func(s *executionState) { s.Controls[0].Result = &controlOutcome }},
		{"control uncertain result", func(s *executionState) {
			s.Controls[0].Effect = nil
			s.Controls[0].Result = new(require(run.NewSettlement(run.SettlementStatusUnknown, []byte(`null`))))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newState()
			test.change(&state)
			if _, err := definition.Restore(t.Context(), require(run.EncodeExecutionState(stateKind, state))); err == nil {
				t.Fatal("restored progress that disagreed with the bound team contract")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := definition.Restore(ctx, require(run.EncodeExecutionState(stateKind, newState()))); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restoration: %v", err)
	}
}

func TestTeamValidatesDecisionAuthorityBeforeEffects(t *testing.T) {
	runtime, config := validationPrograms(t)
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition().(*definition)
	workerName := config.Workers[0].Program.Descriptor().Name()
	taskKey := require(run.ParseChildKey("task"))
	taskProcess := require(testTurn(1).Process.Child(taskKey))
	signal := run.Signal{ID: require(run.ParseSignalID("answer")), Payload: input("ready")}
	for _, test := range []struct {
		name     string
		state    executionState
		decision Decision
	}{
		{"unselected mode", executionState{}, Decision{State: input("working")}},
		{"unknown mode", executionState{}, Decision{Mode: Mode("other"), State: input("working")}},
		{"output with mode", executionState{}, Decision{Mode: ModeContinue, State: input("working"), Output: input("done")}},
		{"output with tasks", executionState{}, Decision{State: input("working"), Output: input("done"), Tasks: []TaskRequest{request("task", workerName, "work")}}},
		{"output schema", executionState{}, Decision{State: input("working"), Output: input(42)}},
		{"state schema", executionState{}, Decision{Mode: ModeContinue, State: input(42)}},
		{"reserved key", executionState{}, Decision{Mode: ModeWait, State: input("working"), Tasks: []TaskRequest{request(turnPrefix+"1", workerName, "work")}}},
		{"worker name", executionState{}, Decision{Mode: ModeWait, State: input("working"), Tasks: []TaskRequest{request("task", "test.unknown", "work")}}},
		{"worker input", executionState{}, Decision{Mode: ModeWait, State: input("working"), Tasks: []TaskRequest{request("task", workerName, 42)}}},
		{"concurrent task bound", executionState{}, Decision{Mode: ModeWait, State: input("working"), Tasks: []TaskRequest{request("a", workerName, "work"), request("b", workerName, "work")}}},
		{"cumulative task bound", executionState{Tasks: []taskState{{Invocation: child.Invocation{Termination: new(completed("a"))}}, {Invocation: child.Invocation{Termination: new(completed("b"))}}}}, Decision{Mode: ModeWait, State: input("working"), Tasks: []TaskRequest{request("c", workerName, "work")}}},
		{"reused key", executionState{Tasks: []taskState{{Request: request("task", workerName, "before"), Invocation: child.Invocation{Termination: new(completed("done"))}}}}, Decision{Mode: ModeWait, State: input("working"), Tasks: []TaskRequest{request("task", workerName, "again")}}},
		{"control absent task", executionState{}, Decision{Mode: ModeContinue, State: input("working"), Controls: []Control{{Task: taskKey, Signal: &signal}}}},
		{"control pending task", executionState{Tasks: []taskState{{Request: request("task", workerName, "work"), Invocation: child.Invocation{Request: new(testTurn(1).EffectID(0))}}}}, Decision{Mode: ModeContinue, State: input("working"), Controls: []Control{{Task: taskKey, Signal: &signal}}}},
		{"control invalid signal", executionState{Tasks: []taskState{{Request: request("task", workerName, "work"), Invocation: child.Invocation{Process: &taskProcess}}}}, Decision{Mode: ModeContinue, State: input("working"), Controls: []Control{{Task: taskKey, Signal: new(run.Signal{})}}}},
		{"control bound", executionState{Tasks: []taskState{{Request: request("task", workerName, "work"), Invocation: child.Invocation{Process: &taskProcess}}}}, Decision{Mode: ModeContinue, State: input("working"), Controls: []Control{{Task: taskKey, Signal: &signal}, {Task: taskKey, Signal: &signal}}}},
		{"wait without wake source", executionState{}, Decision{Mode: ModeWait, State: input("working")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := &execution{definition: definition, state: test.state}
			transition, err := execution.applyDecision(t.Context(), testTurn(2), test.decision, 0)
			if err == nil || transition.Valid() {
				t.Fatalf("invalid decision declared effects: %+v, %v", transition, err)
			}
		})
	}
}

func TestTeamSignalControlKeepsReceiptSeparateFromTaskExit(t *testing.T) {
	runtime, config := validationPrograms(t)
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition().(*definition)
	key := require(run.ParseChildKey("task"))
	process := require(testTurn(1).Process.Child(key))
	execution := &execution{definition: definition, state: executionState{WorkingState: input("working"), TurnNumber: 1,
		Tasks: []taskState{{Request: request("task", config.Workers[0].Program.Descriptor().Name(), "work"), Invocation: child.Invocation{Process: &process}}}}}
	signal := run.Signal{ID: require(run.ParseSignalID("answer")), Payload: input("ready")}
	decision := Decision{Mode: ModeWait, State: input("working"), Controls: []Control{{Task: key, Signal: &signal}}}
	transition := require(execution.applyDecision(t.Context(), testTurn(2), decision, 0))
	actual := require(jsonv2.Marshal(transition.Effects()))
	expected := require(jsonv2.Marshal([]run.Effect{require(run.NewChildSignalEffect(process, signal))}))
	if string(actual) != string(expected) {
		t.Fatalf("control changed its recipient or signal: %s", actual)
	}
	restored := require(definition.Restore(t.Context(), require(execution.Snapshot())))
	receipt := run.Completion{Effect: testTurn(2).EffectID(0), Outcome: require(run.NewSettlement(run.SettlementStatusSucceeded, []byte(`null`)))}
	waiting := require(restored.Step(t.Context(), testTurn(3, receipt)))
	if waiting.Kind() != run.TransitionKindYield || len(waiting.Effects()) != 0 {
		t.Fatal("control receipt was mistaken for drained task completion")
	}
	next := require(restored.Step(t.Context(), testTurn(4, run.Exit{Child: process, Termination: completed("finished")})))
	turnSpec := childStart(t, next.Effects()[0])
	observed := require(turnSpec.Input.Decode[Turn]())
	if len(observed.Controls) != 1 || observed.Controls[0].Result == nil || observed.Controls[0].Result.Status() != run.SettlementStatusSucceeded || observed.Tasks[0].Outcome == nil || observed.Tasks[0].Process != nil {
		t.Fatalf("next coordinator turn lost one of its independent outcomes: %+v", observed)
	}
}

func TestTeamTaskExitCannotStandInForAnOutstandingControlReceipt(t *testing.T) {
	runtime, config := validationPrograms(t)
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition().(*definition)
	key := require(run.ParseChildKey("task"))
	process := require(testTurn(1).Process.Child(key))
	execution := &execution{definition: definition, state: executionState{WorkingState: input("working"), TurnNumber: 1,
		Tasks: []taskState{{Request: request("task", config.Workers[0].Program.Descriptor().Name(), "work"), Invocation: child.Invocation{Process: &process}}}}}
	signal := run.Signal{ID: require(run.ParseSignalID("answer")), Payload: input("ready")}
	require(execution.applyDecision(t.Context(), testTurn(2), Decision{Mode: ModeWait, State: input("working"), Controls: []Control{{Task: key, Signal: &signal}}}, 0))
	waiting := require(execution.Step(t.Context(), testTurn(3, run.Exit{Child: process, Termination: completed("already finished")})))
	if waiting.Kind() != run.TransitionKindYield || len(waiting.Effects()) != 0 {
		t.Fatal("task completion started another coordinator before the control outcome existed")
	}
	restored := require(definition.Restore(t.Context(), require(execution.Snapshot())))
	receipt := run.Completion{Effect: testTurn(2).EffectID(0), Outcome: require(run.NewFailedSettlement(require(run.NewFailure(run.FailureKindContract, "test.task.finished", "task was already finished")), run.Payload{}))}
	next := require(restored.Step(t.Context(), testTurn(4, receipt)))
	turnSpec := childStart(t, next.Effects()[0])
	observed := require(turnSpec.Input.Decode[Turn]())
	if len(observed.Controls) != 1 || observed.Controls[0].Result == nil || observed.Controls[0].Result.Status() != run.SettlementStatusFailed || len(observed.Tasks) != 1 || observed.Tasks[0].Outcome == nil {
		t.Fatalf("recovery lost the independently committed exit or control failure: %+v", observed)
	}
}

func TestTeamModeDiagnosticsUseOnlyTheDeclaredVocabulary(t *testing.T) {
	for _, test := range []struct {
		mode Mode
		name string
	}{
		{ModeUndecided, "undecided"}, {ModeContinue, "continue"}, {ModeWait, "wait"},
	} {
		if !test.mode.Valid() || test.mode.String() != test.name {
			t.Fatalf("mode diagnostic disagreed with its declaration: %q", test.mode)
		}
	}
	if unknown := Mode("unknown"); unknown.Valid() || unknown.String() != "invalid" {
		t.Fatalf("unknown mode advertised a supported operation: %q", unknown)
	}
}

func TestTeamRejectsUnportableContractsAndUnownedMessages(t *testing.T) {
	runtime, config := validationPrograms(t)
	if _, err := Compile[chan int, string](runtime.Compiler(), config); err == nil {
		t.Fatal("nonportable working state was admitted")
	}
	if _, err := Compile[string, chan int](runtime.Compiler(), config); err == nil {
		t.Fatal("nonportable output was admitted")
	}
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition()
	execution := require(definition.Start(input("working")))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if transition, err := execution.Step(ctx, testTurn(1)); !errors.Is(err, context.Canceled) || transition.Valid() {
		t.Fatalf("canceled team step manufactured a coordinator start: %+v %v", transition, err)
	}
	signal := run.Signal{ID: require(run.ParseSignalID("unsolicited")), Payload: input("unowned")}
	if transition, err := execution.Step(t.Context(), testTurn(1, signal)); !errors.Is(err, ErrInvalidProtocol) || transition.Valid() {
		t.Fatalf("unowned input advanced the coordinator: %+v %v", transition, err)
	}
}

func TestTeamCoordinatorFailureRetainsItsClassification(t *testing.T) {
	runtime, config := validationPrograms(t)
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition()
	failure := require(run.NewFailure(run.FailureKindExternal, "test.coordinator.failed", "coordinator refused"))
	failedWire := require(jsonv2.Marshal(struct {
		Failure run.Failure `json:"failure"`
	}{failure}))
	var failed, canceled run.Termination
	if err := jsonv2.Unmarshal(failedWire, &failed); err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal([]byte(`{"cause":"parent_cancellation","reason":"stop coordinator"}`), &canceled); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		termination *run.Termination
		want        string
	}{
		{"start rejection", nil, failure.Code()},
		{"failed child", &failed, failure.Code()},
		{"canceled child", &canceled, failureCodeCoordinatorNotCompleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := require(definition.Start(input("initial")))
			started := require(execution.Step(t.Context(), testTurn(1)))
			completion := run.Completion{Effect: testTurn(1).EffectID(0), Outcome: require(run.NewFailedSettlement(failure, run.Payload{}))}
			messages := []run.Message{completion}
			if test.termination != nil {
				spec := childStart(t, started.Effects()[0])
				process := require(testTurn(1).Process.Child(spec.Key))
				completion.Outcome = require(run.NewSettlement(run.SettlementStatusSucceeded, input(process).JSON()))
				messages = []run.Message{completion, run.Exit{Child: process, Termination: *test.termination}}
			}
			transition := require(execution.Step(t.Context(), testTurn(2, messages...)))
			observed, ok := transition.Failure()
			if !ok || observed.Code() != test.want || observed.Kind() != run.FailureKindExternal {
				t.Fatalf("coordinator classification changed: %+v", observed)
			}
		})
	}
}

func TestTeamRestoredBoundsDoNotAdmitAnotherTurn(t *testing.T) {
	runtime, config := validationPrograms(t)
	for _, test := range []struct {
		name   string
		limit  run.Quota
		number uint64
		want   error
	}{
		{"configured turns", run.NewQuota(2), 2, ErrTurnLimit},
		{"identity counter", run.Quota{}, math.MaxUint64, run.ErrCounterExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			config.MaxTurns = test.limit
			definition := require(Compile[string, string](runtime.Compiler(), config)).Definition()
			state := require(run.EncodeExecutionState(stateKind, executionState{WorkingState: input("working"), TurnNumber: test.number}))
			execution := require(definition.Restore(t.Context(), state))
			if transition, err := execution.Step(t.Context(), testTurn(1)); !errors.Is(err, test.want) || transition.Valid() {
				t.Fatalf("exhausted bound admitted another coordinator: %+v, %v", transition, err)
			}
		})
	}
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition()
	if _, err := definition.Start(input(42)); err == nil {
		t.Fatal("team accepted working state outside its schema")
	}
}
