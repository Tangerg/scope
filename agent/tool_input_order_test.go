package agent

import (
	startrand "crypto/rand"

	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/run"
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
				executable, err := newContinuingTool(tool.FuncConfig{
					Name: "confirm", Description: "Confirm the pending Tool operation.",
				}, func(ctx context.Context, callInput tool.Invocation) (tool.Result, error) {
					invocation, present := ToolInvocationFromContext(ctx)
					if !present {
						return tool.Result{}, errors.New("root Tool invocation lost physical attribution")
					}
					if reference, attributed := invocation.Reference(); attributed || reference.Valid() {
						return tool.Result{}, errors.New("root Tool invocation invented an Interaction parent")
					}
					continuation, resumed := callInput.Continuation()
					if !resumed {
						initialCalls.Add(1)
						return waitingInput([]byte(`"confirm"`), []byte(`{"type":"boolean"}`), []byte(`{"pending":true}`))
					}
					resumedCalls.Add(1)
					if string(continuation.Response()) != "true" || string(continuation.State()) != `{"pending":true}` {
						return tool.Result{}, errors.New("Tool continuation changed its answer or state")
					}
					return tool.Result{Output: tool.NewTextOutput("confirmed")}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				tools, err := NewToolSet(ToolSetConfig{
					Name: "interaction.early_input", Description: "Preserve early Tool answers.", Tools: []tool.Tool{executable},
					ImplementationDigest: run.ComputeDigest([]byte("early-input-tool")),
					ConfigurationDigest:  run.ComputeDigest([]byte("early-input-config")),
				})
				if err != nil {
					t.Fatal(err)
				}
				definition := &heldToolOpeningDefinition{Definition: tools.Program().Definition(), entered: make(chan struct{})}
				deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
					Definition: definition, Dispatcher: tools.dispatcher,
					ImplementationDigest: run.ComputeDigest([]byte("held-tool-opening")),
					ConfigurationDigest:  run.ComputeDigest([]byte("early-input-config")),
				})
				if err != nil {
					t.Fatal(err)
				}
				store := run.NewMemoryStore()
				engine, err := run.New(t.Context(), run.Config{Store: store})
				if err != nil {
					t.Fatal(err)
				}
				input, err := run.EncodePayload(toolCall{Call: chat.ToolCall{ID: "call", Name: "confirm", Arguments: `{}`}})
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
				if err != nil {
					t.Fatal(err)
				}
				<-definition.entered
				opening, err := engine.CaptureTree(t.Context(), process.Relation().ProcessID())
				if err != nil {
					t.Fatal(err)
				}
				receipts := opening.ProcessSnapshots()[0].SignalReceipts()
				waitID, addressed := receipts[len(receipts)-1].WaitID()
				if !addressed || receipts[len(receipts)-1].Consumed() {
					t.Fatal("Tool opening Signal is not pending")
				}
				answerID, err := run.ParseSignalID("signal:early-tool-answer")
				if err != nil {
					t.Fatal(err)
				}
				answer, err := run.NewSignalRequest(answerID, waitID, json.RawMessage("true"))
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
				capture, err := engine.CaptureTree(t.Context(), process.Relation().ProcessID())
				if err != nil || capture.ProcessSnapshots()[0].Status() != run.StatusPaused {
					t.Fatalf("Tool did not pause at the committed boundary: %v", err)
				}
				if restore {
					oldEngine, oldProcess := engine, process
					engine, err = run.New(t.Context(), run.Config{Store: store})
					if err != nil {
						t.Fatal(err)
					}
					process, err = engine.Recover(t.Context(), deployment, capture)
					if err != nil {
						t.Fatal(err)
					}
					_ = oldProcess.Kill(t.Context(), "release retired writer")
					if joinErr := oldProcess.Join(t.Context()); !errors.Is(joinErr, run.ErrTreeIncarnationConflict) {
						t.Fatalf("retired writer: %v", joinErr)
					}
					if closeErr := oldEngine.Close(t.Context()); closeErr != nil {
						t.Fatal(closeErr)
					}
				}

				if resumeErr := process.Resume(t.Context()); resumeErr != nil {
					t.Fatal(resumeErr)
				}
				result, err := process.Await(t.Context())
				if err != nil || result.Termination().Status() != run.StatusCompleted {
					t.Fatalf("queued opening and answer failed: status=%s termination=%+v error=%v", result.Termination().Status(), result.Termination(), err)
				}
				output, present := result.Termination().Output()
				decoded, err := output.Decode[toolCallResult]()
				if err != nil || !present {
					t.Fatalf("Tool output is missing: %v", err)
				}
				text, isText := decoded.Output.Text()
				if !isText || text != "confirmed" || decoded.Disposition != ResultSucceeded || initialCalls.Load() != 1 || resumedCalls.Load() != 1 {
					t.Fatalf("Tool output=%+v initial/resumed calls=%d/%d", decoded, initialCalls.Load(), resumedCalls.Load())
				}
				if result.Usage() != (run.Usage{CommittedSteps: 5, PreparedEffects: 3, AcceptedSignals: 4}) {
					t.Fatalf("Tool continuation changed resource usage: %+v", result.Usage())
				}
				inspection, err := engine.InspectTree(t.Context(), process.Relation().ProcessID())
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
	run.Definition
	entered chan struct{}
	held    atomic.Bool
}

func (h *heldToolOpeningDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	execution, err := h.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &heldToolOpeningExecution{Execution: execution, definition: h}, nil
}

type heldToolOpeningExecution struct {
	run.Execution
	definition *heldToolOpeningDefinition
}

func (h *heldToolOpeningExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if h.Execution.(*toolExecution).state.phase() == toolAwaitingWaitOpen && h.definition.held.CompareAndSwap(false, true) {
		close(h.definition.entered)
		<-ctx.Done()
		return run.Transition{}, ctx.Err()
	}
	return h.Execution.Step(ctx, signals)
}
