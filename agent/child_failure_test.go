package agent_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolChildFailuresRetainRestorableParentState(t *testing.T) {
	for _, stage := range []string{"start", "completion"} {
		t.Run(stage, func(t *testing.T) {
			var calls atomic.Int32
			executable, err := tool.NewFunc(tool.FuncConfig{
				Name: "finish", Description: "Finish one child operation.",
			}, func(context.Context, struct{}) (string, error) {
				calls.Add(1)
				return "done", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tools := testToolSet(t, agent.ToolSetConfig{Tools: []tool.Tool{executable}})
			definition, err := agent.NewDefinition(agent.DefinitionConfig{
				Name: "interaction.child_failure", Description: "Preserve terminal child failures.",
				MaxModelCalls: run.NewQuota(1), Tools: tools, ToolBudget: run.Budget{Steps: run.NewQuota(1), Effects: run.NewQuota(2), Signals: run.NewQuota(4)},
			})
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := agent.NewDispatcher(definition, agent.DispatcherConfig{
				Model: &singleToolCallModel{call: chat.ToolCall{ID: "finish-call", Name: "finish", Arguments: `{}`}},
			})
			if err != nil {
				t.Fatal(err)
			}
			deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
				Definition: definition, Dispatcher: dispatcher,
				ImplementationDigest: run.ComputeDigest([]byte("child-failure")),
				ConfigurationDigest:  run.ComputeDigest([]byte("child-failure-config")),
			})
			if err != nil {
				t.Fatal(err)
			}
			config := run.Config{Store: run.NewMemoryStore()}
			wantCode, wantCalls := "engine.limit.steps", int32(1)
			wantKind, wantMessage := run.FailureKindExecution, "agent: resource limit exceeded"
			if stage == "start" {
				wantCode, wantCalls = "engine.child.admission.rejected", 0
				wantKind, wantMessage = run.FailureKindExternal, "agent: process admission rejected: child admission rejected"
				config.ProcessAdmitter = run.ProcessAdmitterFunc(func(_ context.Context, admission run.ProcessAdmission) error {
					if !admission.Relation().IsRoot() {
						return errors.New("child admission rejected")
					}
					return nil
				})
			}
			engine, err := run.New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			input, _ := run.EncodePayload(agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
			result, err := runtest.Observe(t.Context(), engine, deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if result.Termination().Status() != run.StatusFailed || !failed || failure.Code() != wantCode || failure.Kind() != wantKind || failure.Message() != wantMessage || calls.Load() != wantCalls {
				t.Fatalf("parent failure = %+v, calls = %d, want %s after %d calls", failure, calls.Load(), wantCode, wantCalls)
			}
			root, found := engine.Process(result.ProcessID())
			if !found {
				t.Fatal("root Process is missing")
			}
			captured := inspectProcessSnapshot(t, engine, root).CommittedExecutionState()
			if _, err := definition.Restore(t.Context(), captured); err != nil {
				t.Fatalf("terminal parent state cannot be restored: %v", err)
			}
		})
	}
}

func TestChildAdmissionFailurePolicyDistinguishesToolsAndDelegates(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		name := "tool"
		if delegated {
			name = "delegate"
		}
		t.Run(name, func(t *testing.T) {
			child := delegateWorkflow(t, "test.admission_worker", func(_ context.Context, input delegateRequest) (delegateResponse, error) {
				return delegateResponse(input), nil
			})
			config := agent.DefinitionConfig{Name: "test.admission_policy", Description: "Exercise child admission policy.", MaxModelCalls: run.NewQuota(2)}
			toolConfig := agent.ToolSetConfig{}
			if delegated {
				delegate, err := agent.NewDelegate(agent.DelegateConfig{Name: "work", Description: "Run delegated work.", Deployment: child})
				if err != nil {
					t.Fatal(err)
				}
				config.Delegates = []agent.Delegate{delegate}
			} else {
				executable, err := tool.NewFunc(tool.FuncConfig{Name: "work", Description: "Run ordinary work."}, func(context.Context, delegateRequest) (string, error) {
					t.Error("rejected child executed")
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				toolConfig.Tools = []tool.Tool{executable}
			}
			calls := 0
			model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				calls++
				if calls == 1 {
					return toolCallResponse(chat.ToolCall{ID: "work", Name: "work", Arguments: `{"value":"run"}`}), nil
				}
				parts := request.Messages[len(request.Messages)-1].Parts
				if len(parts) != 1 || parts[0].ToolResult == nil || !parts[0].ToolResult.IsError {
					return nil, errors.New("model did not receive a rejected delegate result")
				}
				return textResponse("handled rejection"), nil
			})
			deployment := configuredInteraction(t, config, agent.DispatcherConfig{Model: model}, toolConfig)
			engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(), ProcessAdmitter: run.ProcessAdmitterFunc(func(_ context.Context, admission run.ProcessAdmission) error {
				if !admission.Relation().IsRoot() {
					return errors.New("child admission rejected")
				}
				return nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(context.WithoutCancel(t.Context()))
			result, err := runtest.Observe(t.Context(), engine, deployment.Program, interactionInput(t, "run work"))
			if err != nil {
				t.Fatal(err)
			}
			if delegated {
				if result.Termination().Status() != run.StatusCompleted || calls != 2 {
					t.Fatalf("delegate status=%s calls=%d", result.Termination().Status(), calls)
				}
			} else {
				failure, failed := result.Termination().Failure()
				if !failed || failure.Kind() != run.FailureKindExternal || failure.Code() != "engine.child.admission.rejected" || calls != 1 {
					t.Fatalf("tool failure=%+v calls=%d", failure, calls)
				}
			}
		})
	}
}
