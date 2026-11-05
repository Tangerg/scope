package team

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestTeamRuntimeReceiptsControlAfterTaskExit(t *testing.T) {
	for _, operation := range []string{"signal", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			runtime := conformancetest.Runtime(t, run.Config{})
			worker := require(coordination.Compile[string, run.Signal](runtime.Compiler(), coordination.Config{
				Name: "test.worker", Description: "Complete after the test answers.",
				Policy: coordination.InputGate{RequestSchema: require(jsonschema.For[string]()), AnswerSchema: require(jsonschema.For[string]())},
			})).Erase()
			gate := require(coordination.Compile[Turn, run.Signal](runtime.Compiler(), coordination.Config{
				Name: "test.coordinator.input", Description: "Select the next team decision.",
				Policy: coordination.InputGate{RequestSchema: require(jsonschema.For[Turn]()), AnswerSchema: require(jsonschema.For[Decision]())},
			})).Erase()
			coordinator := require(workflow.Compile[Turn, Decision](runtime.Compiler(), workflow.Config{
				Name: "test.coordinator", Description: "Apply the answered decision.", Revision: "test",
				Stages: []workflow.Stage{
					require(workflow.Call(workflow.CallConfig{ID: "ask", Child: run.ChildBinding{Program: gate}})),
					require(workflow.Transform("decision", func(_ context.Context, answer run.Signal) (Decision, error) { return answer.Payload.Decode[Decision]() })),
				},
			})).Erase()
			handle := require(runtime.Start(t.Context(), compileTeam(t, runtime, coordinator, worker), run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: operation}, Input: "initial"}))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			next, stop := iter.Pull2(handle.Observe(ctx, 0))
			defer stop()
			waits := make(map[run.ProcessID]run.Wait)
			reaped := make(map[run.ProcessID]bool)
			observeUntil := func(done func() bool) {
				t.Helper()
				for !done() {
					entry, err, open := next()
					if err != nil || !open {
						t.Fatalf("team stopped before the expected fact: open=%v error=%v", open, err)
					}
					switch record := entry.Record.(type) {
					case run.Stepped:
						for _, wait := range record.Transition.Waits() {
							waits[record.Process] = wait
						}
					case run.Reaped:
						reaped[record.Process] = true
					}
				}
			}
			awaitWait := func(parent run.ProcessID) (run.ProcessID, run.Wait) {
				t.Helper()
				var process run.ProcessID
				var question run.Wait
				observeUntil(func() bool {
					for id, wait := range waits {
						if parent.Contains(id) {
							process, question = id, wait
							return true
						}
					}
					return false
				})
				return process, question
			}
			answer := func(process run.ProcessID, question run.Wait, value any) {
				t.Helper()
				child, active := runtime.Process(process)
				if !active {
					t.Fatal("question process is not active")
				}
				if err := child.Answer(ctx, question.Key, value); err != nil {
					t.Fatal(err)
				}
			}
			first, question := awaitWait(require(handle.ID().Child(require(turnKey(1)))))
			answer(first, question, Decision{Mode: ModeContinue, State: input("working"), Tasks: []TaskRequest{request("task", "test.worker", "value")}})
			taskID := require(handle.ID().Child(require(run.ParseChildKey("task"))))
			workerID, workerQuestion := awaitWait(taskID)
			second, question := awaitWait(require(handle.ID().Child(require(turnKey(2)))))
			visible := require(question.Question.Decode[Turn]())
			if len(visible.Tasks) != 1 || visible.Tasks[0].Process == nil || *visible.Tasks[0].Process != taskID {
				t.Fatal("coordinator did not observe the admitted task")
			}
			answer(workerID, workerQuestion, "finished")
			observeUntil(func() bool { return reaped[taskID] })
			control := Control{Task: visible.Tasks[0].Request.Key}
			if operation == "signal" {
				control.Signal = &run.Signal{ID: require(run.ParseSignalID("follow-up")), Payload: input("continue")}
			} else {
				control.CancelReason = new("no longer needed")
			}
			answer(second, question, Decision{Mode: ModeContinue, State: visible.State, Controls: []Control{control}})
			third, question := awaitWait(require(handle.ID().Child(require(turnKey(3)))))
			visible = require(question.Question.Decode[Turn]())
			if len(visible.Controls) != 1 || visible.Controls[0].Result == nil || visible.Controls[0].Result.Status() != run.SettlementStatusFailed || visible.Tasks[0].Outcome == nil || visible.Tasks[0].Process != nil {
				t.Fatalf("task exit or control rejection was lost: %+v", visible)
			}
			answer(third, question, Decision{State: visible.State, Output: input("done")})
			if output := require(require(handle.Await(ctx)).Output()); output != "done" {
				t.Fatal(output)
			}
		})
	}
}
