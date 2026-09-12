package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolInputAnswerQueuedBeforeWaitAdoptionSurvivesPauseAndRestore(t *testing.T) {
	for _, restore := range []bool{false, true} {
		name := "resume"
		if restore {
			name = "restore"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var initialCalls, resumedCalls atomic.Int32
				executable, err := tool.NewFunc(tool.FuncConfig{
					Name: "confirm", Description: "Confirm the pending Tool operation.",
				}, func(ctx context.Context, _ struct{}) (string, error) {
					continuation, resumed := ToolInputContinuationFromContext(ctx)
					if !resumed {
						initialCalls.Add(1)
						return "", RequireToolInput([]byte(`"confirm"`), []byte(`{"type":"boolean"}`), []byte(`{"pending":true}`))
					}
					resumedCalls.Add(1)
					if string(continuation.Response()) != "true" || string(continuation.State()) != `{"pending":true}` {
						return "", errors.New("Tool continuation changed its answer or state")
					}
					return "confirmed", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				tools, err := NewToolSet(ToolSetConfig{
					Name: "interaction.early_input", Description: "Preserve early Tool answers.", Tools: []tool.Tool{executable},
					ImplementationDigest: agent.ComputeDigest([]byte("early-input-tool")),
					ConfigurationDigest:  agent.ComputeDigest([]byte("early-input-config")),
				})
				if err != nil {
					t.Fatal(err)
				}
				definition := &heldToolOpeningDefinition{Definition: tools.Deployment().Definition(), entered: make(chan struct{})}
				deployment, err := agent.NewDeployment(agent.DeploymentConfig{
					Definition: definition, Dispatcher: tools.dispatcher,
					ImplementationDigest: agent.ComputeDigest([]byte("held-tool-opening")),
					ConfigurationDigest:  agent.ComputeDigest([]byte("early-input-config")),
				})
				if err != nil {
					t.Fatal(err)
				}
				engine, err := agent.NewEngine(agent.EngineConfig{})
				if err != nil {
					t.Fatal(err)
				}
				input, err := agent.EncodeInput(toolCall{ModelCallSequence: 1, Call: chat.ToolCall{ID: "call", Name: "confirm", Arguments: `{}`}})
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(t.Context(), deployment, input)
				if err != nil {
					t.Fatal(err)
				}
				<-definition.entered
				inspection, err := engine.InspectTree(t.Context(), process.ID())
				if err != nil {
					t.Fatal(err)
				}
				receipts := inspection.Processes[0].Snapshot.SignalReceipts()
				waitID, addressed := receipts[len(receipts)-1].WaitID()
				if !addressed || receipts[len(receipts)-1].Consumed() {
					t.Fatal("Tool opening Signal is not pending")
				}
				answerID, err := agent.ParseSignalID("signal:early-tool-answer")
				if err != nil {
					t.Fatal(err)
				}
				answer, err := NewToolInputResponseSignal(answerID, waitID, json.RawMessage("true"))
				if err != nil {
					t.Fatal(err)
				}
				if accepted, deliveryErr := process.DeliverSignals(t.Context(), answer); deliveryErr != nil || !accepted {
					t.Fatalf("early answer accepted=%t error=%v", accepted, deliveryErr)
				}
				// Discarding the active Step window makes the next attempt see
				// both the unconsumed opening and the already admitted answer.
				if pauseErr := process.Pause(t.Context(), "pause before adopting the wait"); pauseErr != nil {
					t.Fatal(pauseErr)
				}
				synctest.Wait()
				capture, err := engine.CaptureTree(t.Context(), process.ID())
				if err != nil || capture.ProcessSnapshots()[0].Status() != agent.StatusPaused {
					t.Fatalf("Tool did not pause at the committed boundary: %v", err)
				}
				if restore {
					if killErr := process.Kill(t.Context(), "replace paused Tool"); killErr != nil {
						t.Fatal(killErr)
					}
					if joinErr := process.Join(t.Context()); joinErr != nil {
						t.Fatal(joinErr)
					}
					if releaseErr := engine.ReleaseTree(t.Context(), process.ID()); releaseErr != nil {
						t.Fatal(releaseErr)
					}
					process, err = engine.RestoreTree(t.Context(), deployment, capture)
					if err != nil {
						t.Fatal(err)
					}
				}
				if resumeErr := process.Resume(t.Context()); resumeErr != nil {
					t.Fatal(resumeErr)
				}
				result, err := process.Await(t.Context())
				if err != nil || result.Status() != agent.StatusCompleted {
					t.Fatalf("queued opening and answer failed: status=%s termination=%+v error=%v", result.Status(), result.Termination(), err)
				}
				output, present := result.Output()
				decoded, err := output.Decode[toolCallResult]()
				if err != nil || !present {
					t.Fatalf("Tool output is missing: %v", err)
				}
				text, isText := decoded.Result.Output.Text()
				if !isText || text != "confirmed" || decoded.Result.IsError || initialCalls.Load() != 1 || resumedCalls.Load() != 1 {
					t.Fatalf("Tool output=%+v initial/resumed calls=%d/%d", decoded.Result, initialCalls.Load(), resumedCalls.Load())
				}
				if result.Usage() != (agent.Usage{CommittedSteps: 5, PreparedEffects: 3, AcceptedSignals: 4}) {
					t.Fatalf("Tool continuation changed resource usage: %+v", result.Usage())
				}
				inspection, err = engine.InspectTree(t.Context(), process.ID())
				if err != nil {
					t.Fatal(err)
				}
				for _, receipt := range inspection.Processes[0].Snapshot.SignalReceipts() {
					if !receipt.Consumed() {
						t.Fatalf("Tool left accepted Signal %s unconsumed", receipt.ID())
					}
				}
				if err := process.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := engine.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

type heldToolOpeningDefinition struct {
	agent.Definition
	entered chan struct{}
	held    atomic.Bool
}

func (h *heldToolOpeningDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := h.Definition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &heldToolOpeningExecution{Execution: execution, definition: h}, nil
}

type heldToolOpeningExecution struct {
	agent.Execution
	definition *heldToolOpeningDefinition
}

func (h *heldToolOpeningExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if h.Execution.(*toolExecution).state.Phase == toolAwaitingWaitOpen && h.definition.held.CompareAndSwap(false, true) {
		close(h.definition.entered)
		<-ctx.Done()
		return agent.Transition{}, ctx.Err()
	}
	return h.Execution.Step(ctx, signals)
}
