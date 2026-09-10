package interaction_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
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
			tools := testToolSet(t, interaction.ToolSetConfig{Tools: []tool.Tool{executable}})
			definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
				Name: "interaction.child_failure", Description: "Preserve terminal child failures.",
				MaxModelCalls: 1, Tools: tools, ToolBudget: agent.Budget{Steps: 1, Effects: 2, Signals: 4},
			})
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
				Client: &singleToolCallModel{call: chat.ToolCall{ID: "finish-call", Name: "finish", Arguments: `{}`}},
			})
			if err != nil {
				t.Fatal(err)
			}
			deployment, err := agent.NewDeployment(agent.DeploymentConfig{
				Definition: definition, Dispatcher: dispatcher,
				ImplementationDigest: agent.ComputeDigest([]byte("child-failure")),
				ConfigurationDigest:  agent.ComputeDigest([]byte("child-failure-config")),
			})
			if err != nil {
				t.Fatal(err)
			}
			child := tools.Deployment()
			config := agent.EngineConfig{DeploymentResolver: delegateResolver{child.DeploymentRef(): child}}
			wantCode, wantCalls := "interaction.tool.process_failed", int32(1)
			if stage == "start" {
				wantCode, wantCalls = "interaction.tool.start_failed", 0
				config.ProcessAdmitter = agent.ProcessAdmitterFunc(func(_ context.Context, admission agent.ProcessAdmission) error {
					if !admission.Relation().IsRoot() {
						return errors.New("child admission rejected")
					}
					return nil
				})
			}
			engine, err := agent.NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			input, _ := agent.EncodeInput(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
			result, err := engine.Run(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if result.Status() != agent.StatusFailed || !failed || failure.Code() != wantCode || calls.Load() != wantCalls {
				t.Fatalf("parent failure = %+v, calls = %d, want %s after %d calls", failure, calls.Load(), wantCode, wantCalls)
			}
			root, found := engine.Process(result.ProcessID())
			if !found {
				t.Fatal("root Process is missing")
			}
			captured := inspectProcessSnapshot(t, engine, root).CommittedExecutionState()
			if _, err := definition.Restore(captured); err != nil {
				t.Fatalf("terminal parent state cannot be restored: %v", err)
			}
		})
	}
}
