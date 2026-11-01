package agent_test

import (
	startrand "crypto/rand"

	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"encoding/json"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolEmptyJSONSurvivesExecution(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `{}`, `[]`, `false`, `0`} {
		for _, direct := range []bool{false, true} {
			t.Run(raw+"/direct="+map[bool]string{false: "false", true: "true"}[direct], func(t *testing.T) {
				executable, err := tool.NewFunc(tool.FuncConfig{Name: "empty", Description: "Return a JSON value."}, func(context.Context, struct{}) (json.RawMessage, error) { return json.RawMessage(raw), nil })
				if err != nil {
					t.Fatal(err)
				}
				var bound tool.Tool = executable
				if direct {
					bound = directTool{Tool: bound}
				}
				calls := 0
				model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
					calls++
					if calls == 1 {
						return toolBatchResponse(chat.ToolCall{ID: "call", Name: "empty", Arguments: `{}`}), nil
					}
					got := request.Messages[len(request.Messages)-1].Parts[0].ToolResult.Output.Details
					if string(got) != raw {
						t.Errorf("model tool details = %s, want %s", got, raw)
					}
					return textResponse("done"), nil
				})
				deployment := newDeployment(t, model, []tool.Tool{bound}, 2)
				engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
				if err != nil {
					t.Fatal(err)
				}
				defer engine.Close(context.WithoutCancel(t.Context()))
				result, err := runtest.Observe(t.Context(), engine, deployment.Program, interactionInput(t, "run"))
				if err != nil || result.Termination().Status() != run.StatusCompleted {
					t.Fatalf("result = %s, %v, %+v", result.Termination().Status(), err, result.Termination())
				}
				payload, _ := result.Termination().Output()
				output, err := payload.Decode[agent.Output]()
				if err != nil {
					t.Fatal(err)
				}
				if direct && (len(output.DirectToolResults) != 1 || string(output.DirectToolResults[0].Output.Details) != raw) {
					t.Fatalf("direct output = %+v", output)
				}
				tree, err := engine.CaptureTree(t.Context(), result.ProcessID())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := run.ParseTreeSnapshot(tree.JSON()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

type emptyAnswerTool struct{ received chan string }

func (*emptyAnswerTool) Definition() tool.Definition {
	return tool.Definition{Name: "answer", Description: "Wait for JSON.", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (e *emptyAnswerTool) Call(ctx context.Context, callInput tool.Invocation) (tool.Result, error) {
	continuation, resumed := callInput.Continuation()
	if !resumed {
		return waitingInput(json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`null`))
	}
	e.received <- string(continuation.Response())
	output, err := tool.NewJSONOutput(continuation.Response())
	return tool.Result{Output: output}, err
}

func TestEmptyToolAnswersSurviveWaitingRecovery(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `{}`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			executable := &emptyAnswerTool{received: make(chan string, 1)}
			model := &singleToolCallModel{call: chat.ToolCall{ID: "call", Name: "answer", Arguments: `{}`}}
			deployment := newDeployment(t, model, []tool.Tool{directTool{Tool: executable}}, 1)
			store := run.NewMemoryStore()
			engine, err := run.New(t.Context(), run.Config{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			root, err := engine.Start(t.Context(), deployment.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "answer")})
			if err != nil {
				t.Fatal(err)
			}
			tree, pending := captureToolInput(t, engine, root)
			parsed, err := run.ParseTreeSnapshot(tree.JSON())
			if err != nil {
				t.Fatal(err)
			}
			restoredEngine, err := run.New(t.Context(), run.Config{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := restoredEngine.Recover(t.Context(), deployment.Program, parsed)
			if err != nil {
				t.Fatal(err)
			}
			defer restoredEngine.Close(context.WithoutCancel(t.Context()))
			defer retireTestWriter(t, engine, root)
			id, _ := run.ParseSignalID("signal:empty-answer")
			response, err := pending.ResponseSignal(id, json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			owner := pendingToolProcess(t, restoredEngine, pending)
			if accepted, deliveryErr := owner.DeliverSignals(t.Context(), response); deliveryErr != nil || !accepted {
				t.Fatalf("delivery = %t, %v", accepted, deliveryErr)
			}
			result, err := restored.Await(t.Context())
			if err != nil || result.Termination().Status() != run.StatusCompleted {
				t.Fatalf("result = %s, %v", result.Termination().Status(), err)
			}
			if got := <-executable.received; got != raw {
				t.Fatalf("answer = %s, want %s", got, raw)
			}
		})
	}
}

func TestDelegateEmptyJSONPreservesArtifact(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[]`, `""`} {
		t.Run(raw, func(t *testing.T) {
			child := delegateWorkflow(t, "test.empty_delegate", func(context.Context, struct{}) (json.RawMessage, error) { return json.RawMessage(raw), nil })
			delegate, err := agent.NewDelegate(agent.DelegateConfig{Name: "empty", Description: "Return JSON.", Deployment: child, Budget: run.Budget{Steps: run.NewQuota(8), Effects: run.NewQuota(8), Signals: run.NewQuota(8)}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				calls++
				if calls == 1 {
					return toolBatchResponse(chat.ToolCall{ID: "call", Name: "empty", Arguments: `{}`}), nil
				}
				got := request.Messages[len(request.Messages)-1].Parts[0].ToolResult.Output.Details
				if string(got) != raw {
					t.Errorf("delegate details = %s, want %s", got, raw)
				}
				return textResponse("done"), nil
			})
			deployment := delegateInteraction(t, model, nil, []agent.Delegate{delegate})
			engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(context.WithoutCancel(t.Context()))
			result, err := runtest.Observe(t.Context(), engine, deployment.Program, interactionInput(t, "run"))
			if err != nil || result.Termination().Status() != run.StatusCompleted {
				t.Fatalf("result = %s, %v, %+v", result.Termination().Status(), err, result.Termination())
			}
			tree, err := engine.CaptureTree(t.Context(), result.ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := run.ParseTreeSnapshot(tree.JSON()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEmptyOutputSchemaReachesModel(t *testing.T) {
	format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "anything", Schema: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if request.Options.OutputFormat == nil || string(request.Options.OutputFormat.Schema) != `{}` {
			t.Errorf("format = %+v", request.Options.OutputFormat)
		}
		return textResponse("done"), nil
	})
	deployment := newDeployment(t, model, nil, 1)
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.WithoutCancel(t.Context()))
	input, err := run.EncodePayload(agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}, Options: chat.Options{OutputFormat: &format}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, deployment.Program, input)
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("result = %s, %v", result.Termination().Status(), err)
	}
}
