package workflow

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

func fanoutRestoreFixture(t *testing.T, kind StageKind) (Stage, run.Payload) {
	t.Helper()
	leaf := mustProgram(t, mustDefinition(t, "test.restore.integer", mustTransform(t, "identity", func(_ context.Context, value int) (int, error) {
		return value, nil
	})), "integer")
	binding := run.ChildBinding{Program: leaf}
	if kind == StageKindMap {
		stage, err := Map(MapConfig[int, int]{ID: "map", Child: binding, MaxItems: 3, WindowSize: 2})
		if err != nil {
			t.Fatal(err)
		}
		return stage, mustPayload(t, []int{1, 2, 3})
	}
	stage, err := Fork(ForkConfig[int, int, int]{
		ID: "fork", WindowSize: 2,
		Branches: []ForkBranch{{ID: "first", Child: binding}, {ID: "second", Child: binding}, {ID: "third", Child: binding}},
		Reduce:   func(_ context.Context, input int, _ []int) (int, error) { return input, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return stage, mustPayload(t, 1)
}

func fanoutTermination(t *testing.T, wire string) run.Termination {
	t.Helper()
	var termination run.Termination
	if err := jsonv2.Unmarshal([]byte(wire), &termination); err != nil {
		t.Fatal(err)
	}
	return termination
}

func TestFanoutRestoreAdmitsSuccessAndFailureVariants(t *testing.T) {
	failure, err := run.NewFailure(run.FailureKindExternal, "test.child.refused", "child refused")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []StageKind{StageKindMap, StageKindFork} {
		t.Run(string(kind), func(t *testing.T) {
			stage, input := fanoutRestoreFixture(t, kind)
			definition := mustDefinition(t, "test.restore.fanout", stage)
			for _, row := range []struct {
				name       string
				invocation child.Invocation
			}{
				{"completed", child.Invocation{Termination: new(fanoutTermination(t, `{"output":1}`))}},
				{"failed", child.Invocation{Termination: new(fanoutTermination(t, `{"failure":{"kind":"external","code":"test.child.failed","message":"child failed"}}`))}},
				{"canceled", child.Invocation{Termination: new(fanoutTermination(t, `{"cause":"parent_cancellation","reason":"parent stopped"}`))}},
				{"rejected", child.Invocation{Rejection: &failure}},
			} {
				t.Run(row.name, func(t *testing.T) {
					state := executionState{CurrentValue: input.JSON(), ActiveFanoutWindow: []child.Invocation{row.invocation, {Request: new(testTurn(2).EffectID(1))}}}
					snapshot, err := state.snapshot()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := definition.Restore(t.Context(), snapshot); err != nil {
						t.Fatalf("valid fan-out outcome rejected: %v", err)
					}
				})
			}
		})
	}
}

func TestFanoutRecoveryRejectsInvalidOutputsBeforeActivation(t *testing.T) {
	for _, kind := range []StageKind{StageKindMap, StageKindFork} {
		t.Run(string(kind), func(t *testing.T) {
			stage, input := fanoutRestoreFixture(t, kind)
			definition := mustDefinition(t, "test.restore.fanout", stage)
			program := mustProgram(t, definition, "fanout")
			for _, row := range []struct {
				name  string
				state executionState
			}{
				{"active", executionState{CurrentValue: input.JSON(), ActiveFanoutWindow: []child.Invocation{
					{Termination: new(fanoutTermination(t, `{"output":"wrong schema"}`))},
					{Termination: new(fanoutTermination(t, `{"output":2}`))},
				}}},
				{"archived", executionState{CurrentValue: input.JSON(), CompletedFanoutOutputs: []json.RawMessage{json.RawMessage(`"wrong schema"`), json.RawMessage(`2`)},
					ActiveFanoutWindow: []child.Invocation{{Request: new(testTurn(2).EffectID(0))}}}},
			} {
				t.Run(row.name, func(t *testing.T) {
					snapshot, err := row.state.snapshot()
					if err != nil {
						t.Fatal(err)
					}
					if _, restoreErr := definition.Restore(t.Context(), snapshot); !errors.Is(restoreErr, ErrInvalidExecutionState) {
						t.Fatalf("invalid fan-out output admitted: %v", restoreErr)
					}
					root := testTurn(2).Process
					started := run.Started{Process: root, Program: program.ProgramRef(), Input: input, Limits: run.DefaultTreeLimits(), At: time.Now()}
					checkpoint := run.Checkpoint{StartedProcesses: 1, Processes: []run.ProcessCheckpoint{{Started: started, State: snapshot, Usage: run.Usage{CommittedSteps: 2, PreparedEffects: 2}}}}
					store := run.NewMemoryStore()
					if appendErr := store.Append(t.Context(), run.Batch{Root: root, Records: []run.Record{started, run.Activated{Epoch: 1}, checkpoint}}); appendErr != nil {
						t.Fatal(appendErr)
					}
					before, found, err := store.Load(t.Context(), root)
					if err != nil || !found || before.Validate() != nil {
						t.Fatalf("invalid checkpoint envelope: found=%v load=%v validate=%v", found, err, before.Validate())
					}
					runtime := conformancetest.Runtime(t, run.Config{Store: store})
					if _, recoverErr := runtime.Recover(t.Context(), program, root); !errors.Is(recoverErr, ErrInvalidExecutionState) {
						t.Fatalf("recovery activated invalid fan-out progress: %v", recoverErr)
					}
					after, _, err := store.Load(t.Context(), root)
					if err != nil || after.Head() != before.Head() || after.Epoch() != before.Epoch() {
						t.Fatalf("rejected recovery changed the log: head=%d epoch=%d err=%v", after.Head(), after.Epoch(), err)
					}
				})
			}
		})
	}
}
