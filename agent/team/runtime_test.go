package team

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/agent/workflow"
)

func require[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
func input(value any) run.Payload { return require(run.EncodePayload(value)) }
func request(key, worker string, value any) TaskRequest {
	return TaskRequest{Key: require(run.ParseChildKey(key)), Worker: worker, Input: input(value)}
}
func transformed[I, O any](t *testing.T, runtime *run.Runtime, name string, transform func(context.Context, I) (O, error)) run.Program[run.Payload, run.Payload] {
	t.Helper()
	stage := require(workflow.Transform("transform", transform))
	return require(workflow.Compile[I, O](runtime.Compiler(), workflow.Config{Name: name, Description: "Execute the configured transformation.", Revision: "test", Stages: []workflow.Stage{stage}})).Erase()
}
func compileTeam(t *testing.T, runtime *run.Runtime, coordinator run.Program[run.Payload, run.Payload], workers ...run.Program[run.Payload, run.Payload]) run.Program[string, string] {
	t.Helper()
	config := Config{Name: "test.team", Description: "Coordinate declared workers.", Coordinator: run.ChildBinding{Program: coordinator}, MaxTurns: run.NewQuota(8), MaxTasks: run.NewQuota(8), MaxConcurrentTasks: 4, MaxControlsPerTurn: 4}
	for _, worker := range workers {
		config.Workers = append(config.Workers, run.ChildBinding{Program: worker})
	}
	program, err := Compile[string, string](runtime.Compiler(), config)
	if err != nil {
		t.Fatal(err)
	}
	return program
}
func startTeam(t *testing.T, runtime *run.Runtime, program run.Program[string, string]) *run.Handle[string] {
	t.Helper()
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: t.Name()}, Input: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	return handle
}
func awaitTeam(t *testing.T, handle *run.Handle[string]) run.Result[string] {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := handle.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func completed(value any) run.Termination {
	data := require(jsonv2.Marshal(struct {
		Output run.Payload `json:"output"`
	}{input(value)}))
	var termination run.Termination
	if err := jsonv2.Unmarshal(data, &termination); err != nil {
		panic(err)
	}
	return termination
}
func testTurn(step uint64, messages ...run.Message) run.Turn {
	return run.Turn{Process: require(run.ParseProcessID("team-test")), Step: step, Messages: messages}
}

func TestTeamTransfersWorkingMemoryAndDrainedResults(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	worker := transformed(t, runtime, "test.echo", func(_ context.Context, value string) (string, error) { return "echo:" + value, nil })
	coordinator := transformed(t, runtime, "test.coordinator", func(_ context.Context, turn Turn) (Decision, error) {
		if turn.Number == 1 {
			return Decision{Mode: ModeWait, State: input("working"), Tasks: []TaskRequest{request("a", "test.echo", "one"), request("b", "test.echo", "two")}}, nil
		}
		if require(turn.State.Decode[string]()) != "working" || len(turn.Workers) != 1 {
			return Decision{}, errors.New("working memory or worker manifest changed")
		}
		for _, task := range turn.Tasks {
			if task.Outcome == nil {
				return Decision{Mode: ModeWait, State: turn.State}, nil
			}
			if task.Process != nil || task.Rejection != nil {
				return Decision{}, errors.New("task retains multiple lifecycle variants")
			}
		}
		if len(turn.Tasks) != 2 {
			return Decision{}, errors.New("task history lost")
		}
		outputA, _ := turn.Tasks[0].Outcome.Output()
		outputB, _ := turn.Tasks[1].Outcome.Output()
		return Decision{State: turn.State, Output: input(require(outputA.Decode[string]()) + "," + require(outputB.Decode[string]()))}, nil
	})
	program := compileTeam(t, runtime, coordinator, worker)
	handle := startTeam(t, runtime, program)
	result := awaitTeam(t, handle)
	if output := require(result.Output()); output != "echo:one,echo:two" {
		t.Fatal(output)
	}
	log, _, err := store.Load(t.Context(), handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	inspection := require(log.Inspect())
	if len(inspection.Processes) != 1 {
		t.Fatal("drained team children remain active")
	}
}

func TestTeamControlsWaitForCancellationReceiptAndExit(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	deadline := time.Now().Add(time.Hour)
	worker := require(coordination.Compile[time.Time, time.Time](runtime.Compiler(), coordination.Config{Name: "test.slow", Description: "Wait for later.", Policy: coordination.Deadline{}})).Erase()
	coordinator := transformed(t, runtime, "test.coordinator", func(_ context.Context, turn Turn) (Decision, error) {
		switch turn.Number {
		case 1:
			return Decision{Mode: ModeContinue, State: turn.State, Tasks: []TaskRequest{request("slow", "test.slow", deadline)}}, nil
		case 2:
			if len(turn.Tasks) != 1 || turn.Tasks[0].Process == nil {
				return Decision{}, errors.New("task was not admitted")
			}
			reason := "no longer needed"
			return Decision{Mode: ModeWait, State: turn.State, Controls: []Control{{Task: turn.Tasks[0].Request.Key, CancelReason: &reason}}}, nil
		case 3:
			if len(turn.Controls) != 1 || turn.Controls[0].Result == nil || turn.Controls[0].Result.Status() != run.SettlementStatusSucceeded || turn.Tasks[0].Outcome == nil || turn.Tasks[0].Outcome.Status() != run.StatusCanceled {
				return Decision{}, errors.New("coordinator resumed before cancellation drain")
			}
			return Decision{State: turn.State, Output: input("drained")}, nil
		default:
			return Decision{}, errors.New("unexpected coordinator turn")
		}
	})
	program := compileTeam(t, runtime, coordinator, worker)
	result := awaitTeam(t, startTeam(t, runtime, program))
	if value := require(result.Output()); value != "drained" {
		t.Fatal(value)
	}
}

func TestTeamValidatesWholeDecisionBeforeStartingAnyTask(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	worker := transformed(t, runtime, "test.worker", func(_ context.Context, value string) (string, error) {
		t.Error("invalid batch executed a prefix")
		return value, nil
	})
	coordinator := transformed(t, runtime, "test.coordinator", func(_ context.Context, turn Turn) (Decision, error) {
		return Decision{Mode: ModeWait, State: turn.State, Tasks: []TaskRequest{request("duplicate", "test.worker", "a"), request("duplicate", "test.worker", "b")}}, nil
	})
	handle := startTeam(t, runtime, compileTeam(t, runtime, coordinator, worker))
	result := awaitTeam(t, handle)
	failure, failed := result.Termination().Failure()
	if !failed || failure.Kind() != run.FailureKindContract {
		t.Fatalf("termination=%+v", result.Termination())
	}
	log, _, err := store.Load(t.Context(), handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range log.Entries {
		if started, ok := entry.Record.(run.Started); ok && started.Program == worker.ProgramRef() {
			t.Fatal("invalid batch emitted worker start")
		}
	}
}

func TestTaskExitDuringCoordinatorTurnRemainsVisibleToNextTurn(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	worker := transformed(t, runtime, "test.worker", func(_ context.Context, value string) (string, error) { return value, nil })
	coordinator := transformed(t, runtime, "test.coordinator", func(_ context.Context, turn Turn) (Decision, error) {
		return Decision{State: turn.State, Output: input("unused")}, nil
	})
	program := compileTeam(t, runtime, coordinator, worker)
	definition := program.Definition()
	execution := require(definition.Start(input("initial"))).(*execution)
	first := require(execution.Step(t.Context(), testTurn(1)))
	spec := require(run.ParseChildStartEffect(first.Effects()[0]))
	coordinatorID := require(testTurn(1).Process.Child(spec.Key))
	second := testTurn(2, run.Completion{Effect: testTurn(1).EffectID(0), Outcome: require(run.NewSettlement(run.SettlementStatusSucceeded, input(coordinatorID).JSON()))}, run.Exit{Child: coordinatorID, Termination: completed(Decision{Mode: ModeContinue, State: input("working"), Tasks: []TaskRequest{request("task", "test.worker", "value")}})})
	started := require(execution.Step(t.Context(), second))
	taskSpec := require(run.ParseChildStartEffect(started.Effects()[0]))
	taskID := require(testTurn(1).Process.Child(taskSpec.Key))
	third := testTurn(3, run.Completion{Effect: second.EffectID(0), Outcome: require(run.NewSettlement(run.SettlementStatusSucceeded, input(taskID).JSON()))})
	next := require(execution.Step(t.Context(), third))
	nextSpec := require(run.ParseChildStartEffect(next.Effects()[0]))
	nextCoordinator := require(testTurn(1).Process.Child(nextSpec.Key))
	fourth := testTurn(4, run.Completion{Effect: third.EffectID(0), Outcome: require(run.NewSettlement(run.SettlementStatusSucceeded, input(nextCoordinator).JSON()))}, run.Exit{Child: taskID, Termination: completed("finished")}, run.Exit{Child: nextCoordinator, Termination: completed(Decision{Mode: ModeWait, State: input("working")})})
	state := require(execution.Snapshot())
	transition := require(execution.Step(t.Context(), fourth))
	if len(transition.Effects()) != 1 {
		t.Fatal("already arrived outcome left team waiting without wake source")
	}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{Definition: definition, Input: input("initial"), InitialTurn: testTurn(1), RestoredCases: []runtest.ExecutionConformanceCase{{Name: "task exit during turn", State: state, Turn: fourth}}})
}

func TestTeamRejectsContradictoryRestoredOwnership(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	coordinator := transformed(t, runtime, "test.coordinator", func(_ context.Context, turn Turn) (Decision, error) {
		return Decision{State: turn.State, Output: input("done")}, nil
	})
	worker := transformed(t, runtime, "test.worker", func(_ context.Context, value string) (string, error) { return value, nil })
	compiled := compileTeam(t, runtime, coordinator, worker).Definition()
	for _, raw := range []string{`{"completed":true,"working_state":"duplicate"}`, `{"working_state":"initial","turn_number":0,"coordinator":{}}`, `{"working_state":"initial","turn_number":0,"unknown":true}`} {
		state := require(run.ParseExecutionState(stateKind, []byte(raw)))
		if _, err := compiled.Restore(t.Context(), state); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, kind := range []reflect.Type{reflect.TypeFor[definition](), reflect.TypeFor[execution]()} {
		conformancetest.AssertNoProcessAuthority(t, kind)
	}
}
