package team

import (
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

func TestTeamRestoreAdmitsOneOwnerPerPendingOperation(t *testing.T) {
	runtime, config := validationPrograms(t)
	config.MaxTasks, config.MaxTurns = run.NewQuota(3), run.NewQuota(3)
	config.MaxConcurrentTasks, config.MaxControlsPerTurn = 3, 2
	definition := require(Compile[string, string](runtime.Compiler(), config)).Definition()
	worker := config.Workers[0].Program.Descriptor().Name()
	root := testTurn(2).Process
	newState := func() executionState {
		historical := request("historical", worker, "old")
		control := Control{Task: historical.Key, CancelReason: new("stop task")}
		return executionState{
			WorkingState: input("working"), TurnNumber: 2, WaitForTask: true,
			Tasks: []taskState{
				{Request: historical, Invocation: child.Invocation{Process: new(require(root.Child(historical.Key)))}},
				{Request: request("first", worker, "a"), Invocation: child.Invocation{Request: new(testTurn(2).EffectID(0))}},
				{Request: request("second", worker, "b"), Invocation: child.Invocation{Request: new(testTurn(2).EffectID(1))}},
			},
			Controls: []controlState{
				{Control: control, Effect: new(testTurn(2).EffectID(2))},
				{Control: control, Effect: new(testTurn(2).EffectID(3))},
			},
		}
	}
	coordinating := func(state *executionState) {
		for index := range state.Tasks {
			state.Tasks[index].Invocation = child.Invocation{Process: new(require(root.Child(state.Tasks[index].Request.Key)))}
		}
		state.Controls = nil
		state.WaitForTask = false
		state.TurnNumber = 3
		state.Coordinator = &child.Invocation{Request: new(testTurn(3).EffectID(0))}
	}
	for _, row := range []struct {
		name   string
		change func(*executionState)
	}{
		{"pending tasks and distinct controls", func(*executionState) {}},
		{"partially accepted tasks", func(s *executionState) {
			s.Tasks[1].Invocation = child.Invocation{Process: new(require(root.Child(s.Tasks[1].Request.Key)))}
		}},
		{"partially settled controls", func(s *executionState) {
			s.Controls[0].Effect = nil
			s.Controls[0].Result = new(require(run.NewSettlement(run.SettlementStatusSucceeded, require(run.ParsePayload([]byte(`null`))))))
		}},
		{"task completed before control receipt", func(s *executionState) {
			s.Tasks[0].Invocation = child.Invocation{Termination: new(completed("done"))}
		}},
		{"coordinator request with running tasks", coordinating},
		{"coordinator process with running tasks", func(s *executionState) {
			coordinating(s)
			s.Coordinator = &child.Invocation{Process: new(require(root.Child(require(turnKey(s.TurnNumber)))))}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			state := newState()
			row.change(&state)
			if _, err := definition.Restore(t.Context(), require(run.EncodeExecutionState(stateKind, state))); err != nil {
				t.Fatalf("valid obligations rejected: %v", err)
			}
		})
	}
	foreign := require(run.ParseProcessID("other-team"))
	for _, row := range []struct {
		name   string
		change func(*executionState)
	}{
		{"task aliases task", func(s *executionState) { s.Tasks[2].Invocation.Request = s.Tasks[1].Invocation.Request }},
		{"control aliases task", func(s *executionState) { s.Controls[0].Effect = s.Tasks[1].Invocation.Request }},
		{"control aliases control", func(s *executionState) { s.Controls[1].Effect = s.Controls[0].Effect }},
		{"coordinator aliases task", func(s *executionState) {
			s.WaitForTask = false
			s.Coordinator = &child.Invocation{Request: s.Tasks[1].Invocation.Request}
		}},
		{"coordinator aliases control", func(s *executionState) {
			coordinating(s)
			s.Controls = []controlState{{Control: Control{Task: s.Tasks[0].Request.Key, CancelReason: new("stop task")}, Effect: s.Coordinator.Request}}
		}},
		{"task has another parent", func(s *executionState) {
			s.Tasks[0].Invocation.Process = new(require(foreign.Child(s.Tasks[0].Request.Key)))
		}},
		{"request has another parent", func(s *executionState) { s.Tasks[2].Invocation.Request.Process = foreign }},
		{"control has another parent", func(s *executionState) { s.Controls[1].Effect.Process = foreign }},
		{"coordinator has another parent", func(s *executionState) {
			coordinating(s)
			s.Coordinator.Request.Process = foreign
		}},
		{"request from another step", func(s *executionState) { s.Tasks[2].Invocation.Request.Step++ }},
		{"task ordinal changes batch origin", func(s *executionState) { s.Tasks[2].Invocation.Request.Index++ }},
		{"task ordinal exceeds task position", func(s *executionState) { s.Tasks[1].Invocation.Request.Index = 2 }},
		{"control ordinal changes batch origin", func(s *executionState) { s.Controls[1].Effect.Index++ }},
		{"control ordinal precedes control position", func(s *executionState) { s.Controls[1].Effect.Index = 0 }},
		{"control batch exceeds task count", func(s *executionState) { s.Controls[0].Effect.Index = 4 }},
		{"coordinator ordinal", func(s *executionState) {
			coordinating(s)
			s.Coordinator.Request.Index = 1
		}},
		{"control targets unadmitted task", func(s *executionState) { s.Controls[0].Control.Task = s.Tasks[1].Request.Key }},
		{"control targets rejected task", func(s *executionState) {
			s.Tasks[0].Invocation = child.Invocation{Rejection: new(require(run.NewFailure(run.FailureKindContract, "test.child.rejected", "admission refused")))}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			state := newState()
			row.change(&state)
			if _, err := definition.Restore(t.Context(), require(run.EncodeExecutionState(stateKind, state))); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("contradictory obligations admitted: %v", err)
			}
		})
	}
}

func TestTeamRecoveryRejectsAliasedObligationsBeforeActivation(t *testing.T) {
	builder, config := validationPrograms(t)
	config.MaxConcurrentTasks = 2
	program := require(Compile[string, string](builder.Compiler(), config))
	root := testTurn(2).Process
	shared := testTurn(2).EffectID(0)
	worker := config.Workers[0].Program.Descriptor().Name()
	state := executionState{
		WorkingState: input("working"), TurnNumber: 1, WaitForTask: true,
		Tasks: []taskState{
			{Request: request("first", worker, "a"), Invocation: child.Invocation{Request: &shared}},
			{Request: request("second", worker, "b"), Invocation: child.Invocation{Request: &shared}},
		},
	}
	failure := require(run.NewFailure(run.FailureKindContract, "test.start.rejected", "one start was rejected"))
	completion := run.Completion{Effect: shared, Outcome: require(run.NewFailedSettlement(failure, run.Payload{}))}
	started := run.Started{Process: root, Program: program.ProgramRef(), Input: input("working"), Limits: run.DefaultTreeLimits(), At: time.Now()}
	checkpoint := run.Checkpoint{StartedProcesses: 1, Processes: []run.ProcessCheckpoint{{
		Started: started, State: require(run.EncodeExecutionState(stateKind, state)), Inbox: run.Inbox{completion},
		Usage: run.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedMessages: 1},
	}}}
	store := run.NewMemoryStore()
	if err := store.Append(t.Context(), run.Batch{Root: root, Records: []run.Record{started, run.Activated{Epoch: 1}, checkpoint}}); err != nil {
		t.Fatal(err)
	}
	before, found, err := store.Load(t.Context(), root)
	if err != nil || !found || before.Validate() != nil {
		t.Fatalf("invalid checkpoint envelope: found=%v load=%v validate=%v", found, err, before.Validate())
	}
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	if _, recoverErr := runtime.Recover(t.Context(), program, root); !errors.Is(recoverErr, ErrInvalidExecutionState) {
		t.Fatalf("recovery activated aliased obligations: %v", recoverErr)
	}
	after, _, err := store.Load(t.Context(), root)
	if err != nil || after.Head() != before.Head() || after.Epoch() != before.Epoch() {
		t.Fatalf("rejected recovery changed the log: head=%d epoch=%d err=%v", after.Head(), after.Epoch(), err)
	}
}
