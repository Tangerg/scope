package team

import (
	"context"
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

func TestTeamControlsTaskThatExitedDuringCoordinatorTurn(t *testing.T) {
	for _, operation := range []string{"signal", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			runtime := conformancetest.Runtime(t, run.Config{})
			worker := transformed(t, runtime, "test.worker", func(_ context.Context, value string) (string, error) { return value, nil })
			coordinator := transformed(t, runtime, "test.coordinator", func(_ context.Context, turn Turn) (Decision, error) {
				return Decision{State: turn.State, Output: input("unused")}, nil
			})
			definition := compileTeam(t, runtime, coordinator, worker).Definition()
			execution := require(definition.Start(input("initial")))
			first := require(execution.Step(t.Context(), testTurn(1)))
			coordinatorID := require(testTurn(1).Process.Child(childStart(t, first.Effects()[0]).Key))
			firstDecision := Decision{Mode: ModeContinue, State: input("working"), Tasks: []TaskRequest{request("task", "test.worker", "value")}}
			second := testTurn(2,
				run.Completion{Effect: testTurn(1).EffectID(0), Outcome: require(run.NewSettlement(run.SettlementStatusSucceeded, input(coordinatorID).JSON()))},
				run.Exit{Child: coordinatorID, Termination: completed(firstDecision)},
			)
			started := require(execution.Step(t.Context(), second))
			taskSpec := childStart(t, started.Effects()[0])
			taskID := require(testTurn(1).Process.Child(taskSpec.Key))
			third := testTurn(3, run.Completion{Effect: second.EffectID(0), Outcome: require(run.NewSettlement(run.SettlementStatusSucceeded, input(taskID).JSON()))})
			next := require(execution.Step(t.Context(), third))
			nextSpec := childStart(t, next.Effects()[0])
			visible := require(nextSpec.Input.Decode[Turn]())
			if len(visible.Tasks) != 1 || visible.Tasks[0].Process == nil || *visible.Tasks[0].Process != taskID {
				t.Fatal("coordinator was not offered the admitted task")
			}
			nextCoordinator := require(testTurn(1).Process.Child(nextSpec.Key))
			control := Control{Task: taskSpec.Key}
			var expected run.Effect
			if operation == "signal" {
				signal := run.Signal{ID: require(run.ParseSignalID("follow-up")), Payload: input("continue")}
				control.Signal = &signal
				expected = require(run.NewChildSignalEffect(taskID, signal))
			} else {
				control.CancelReason = new("no longer needed")
				expected = require(run.NewChildCancelEffect(taskID, *control.CancelReason))
			}
			decision := Decision{Mode: ModeContinue, State: input("working"), Controls: []Control{control}}
			fourth := testTurn(4,
				run.Completion{Effect: third.EffectID(0), Outcome: require(run.NewSettlement(run.SettlementStatusSucceeded, input(nextCoordinator).JSON()))},
				run.Exit{Child: taskID, Termination: completed("finished")},
				run.Exit{Child: nextCoordinator, Termination: completed(decision)},
			)
			state := require(execution.Snapshot())
			transition, err := execution.Step(t.Context(), fourth)
			if err != nil {
				t.Fatalf("task exit invalidated a control based on the coordinator's admitted-task view: %v", err)
			}
			actual := require(jsonv2.Marshal(transition.Effects()))
			want := require(jsonv2.Marshal([]run.Effect{expected}))
			if transition.Kind() != run.TransitionKindYield || string(actual) != string(want) {
				t.Fatalf("control lost its runtime-owned destination: %s", actual)
			}
			restored := require(definition.Restore(t.Context(), require(execution.Snapshot())))
			failure := require(run.NewFailure(run.FailureKindContract, "test.task.finished", "task already exited"))
			receipt := require(run.NewFailedSettlement(failure, run.Payload{}))
			resumed := require(restored.Step(t.Context(), testTurn(5, run.Completion{Effect: fourth.EffectID(0), Outcome: receipt})))
			observed := require(childStart(t, resumed.Effects()[0]).Input.Decode[Turn]())
			if len(observed.Controls) != 1 || observed.Controls[0].Result == nil || observed.Controls[0].Result.Status() != run.SettlementStatusFailed || observed.Tasks[0].Outcome == nil || observed.Tasks[0].Process != nil {
				t.Fatalf("next coordinator turn lost the exited task or the control rejection: %+v", observed)
			}
			runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{
				Definition: definition, Input: input("initial"), InitialTurn: testTurn(1),
				RestoredCases: []runtest.ExecutionConformanceCase{{Name: "task exit before control", State: state, Turn: fourth}},
			})
		})
	}
}
