package interaction_test

import (
	"context"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func TestDelegateAtModelLimit(t *testing.T) {
	child := delegateWorkflow(t, "fixture.worker", func(_ context.Context, in delegateRequest) (delegateResponse, error) {
		return delegateResponse(in), nil
	})
	budget := agent.Budget{Steps: 20, Effects: 20, Signals: 20}
	delegate, err := interaction.NewDelegate(interaction.DelegateConfig{Name: "worker", Description: "Return one value.", Deployment: child, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "c1", Name: "worker", Arguments: `{"value":"evidence"}`}))
		return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}, nil
	})
	root := delegateInteractionWithValidator(t, model, nil, []interaction.Delegate{delegate}, nil, 1)
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: delegateResolver{child.DeploymentRef(): child}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), root.Deployment, interactionInput(t, "delegate once"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	failure, _ := result.Termination().Failure()
	if failure.Code() != "interaction.limit.model_calls" {
		t.Fatalf("failure=%s kind=%s message=%s", failure.Code(), failure.Kind(), failure.Message())
	}
}
