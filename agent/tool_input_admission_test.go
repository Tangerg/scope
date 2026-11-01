package agent_test

import (
	startrand "crypto/rand"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolInputAdmissionPreservesWaitAfterOversizedAnswer(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprintf("restored=%t", restored), func(t *testing.T) {
			resumed := make(chan tool.Continuation, 1)
			executable, err := newContinuingTool(tool.FuncConfig{Name: "ask_text", Description: "Ask for bounded text."}, func(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
				continuation, ok := invocation.Continuation()
				if !ok {
					return waitingInput(json.RawMessage(`"answer"`), json.RawMessage(`{"type":"string"}`), json.RawMessage(`null`))
				}
				resumed <- continuation
				return tool.Result{Output: tool.NewTextOutput("done")}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				if request.Messages[len(request.Messages)-1].Role == chat.RoleTool {
					return textResponse("done"), nil
				}
				return toolCallResponse(chat.ToolCall{ID: "ask", Name: "ask_text", Arguments: `{}`}), nil
			})
			deployment := newDeployment(t, model, []tool.Tool{executable}, 2)
			store := run.NewMemoryStore()
			engine, err := run.New(t.Context(), run.Config{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			root, err := engine.Start(ctx, deployment.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "ask")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
				defer stop()
				if killErr := root.Kill(cleanup, "test complete"); killErr != nil && !errors.Is(killErr, run.ErrProcessFinished) {
					t.Error(killErr)
				}
				if joinErr := root.Join(cleanup); joinErr != nil {
					t.Error(joinErr)
				}
				if closeErr := engine.Close(cleanup); closeErr != nil {
					t.Error(closeErr)
				}
			})
			tree, pending := captureToolInput(t, engine, root)
			if restored {
				tree, err = run.ParseTreeSnapshot(tree.JSON())
				if err != nil {
					t.Fatal(err)
				}
				previousEngine, previousRoot := engine, root
				engine, err = run.New(t.Context(), run.Config{Store: store})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { retireTestWriter(t, previousEngine, previousRoot) })
				root, err = engine.Recover(ctx, deployment.Program, tree)
				if err != nil {
					t.Fatal(err)
				}
			}
			child := pendingToolProcess(t, engine, pending)
			id, err := run.ParseSignalID("signal:bounded-answer")
			if err != nil {
				t.Fatal(err)
			}
			before := inspectProcessSnapshot(t, engine, child)
			for _, response := range []string{
				`"` + strings.Repeat("x", tool.MaxInputBytes-1) + `"`,
				`"` + strings.Repeat("<", tool.MaxInputBytes/6+1) + `"`,
			} {
				if _, responseErr := pending.ResponseSignal(id, json.RawMessage(response)); !errors.Is(responseErr, tool.ErrInvalidInputRequest) {
					t.Fatalf("facade accepted oversized answer: %v", responseErr)
				}
				answer, answerErr := run.NewSignalRequest(id, pending.WaitID(), json.RawMessage(response))
				if answerErr != nil {
					t.Fatal(answerErr)
				}
				if accepted, deliveryErr := child.DeliverSignals(ctx, answer); accepted || !errors.Is(deliveryErr, run.ErrSignalRejected) || !errors.Is(deliveryErr, run.ErrPayloadTooLarge) {
					t.Fatalf("oversized answer admitted=%t error=%v", accepted, deliveryErr)
				}
				after := inspectProcessSnapshot(t, engine, child)
				waitID, waiting := after.WaitID()
				if !waiting || waitID != pending.WaitID() || after.Status() != run.StatusWaiting || after.Usage() != before.Usage() {
					t.Fatalf("rejection changed wait or usage: before=%+v after=%+v", before.Usage(), after.Usage())
				}
			}
			response := json.RawMessage(`"` + strings.Repeat("x", tool.MaxInputBytes-2) + `"`)
			answer, err := run.NewSignalRequest(id, pending.WaitID(), response)
			if err != nil {
				t.Fatal(err)
			}
			if accepted, deliveryErr := child.DeliverSignals(ctx, answer); !accepted || deliveryErr != nil {
				t.Fatalf("exact-bound retry admitted=%t error=%v", accepted, deliveryErr)
			}
			result, err := root.Await(ctx)
			if err != nil || result.Termination().Status() != run.StatusCompleted {
				t.Fatalf("resumed Interaction=%v error=%v", result.Termination(), err)
			}
			if continuation := <-resumed; string(continuation.Response()) != string(response) {
				t.Fatal("continuation did not receive the exact admitted answer")
			}
		})
	}
}
