package agent_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

func TestDelegateAtModelLimit(t *testing.T) {
	child := delegateWorkflow(t, "fixture.worker", func(_ context.Context, in delegateRequest) (delegateResponse, error) {
		return delegateResponse(in), nil
	})
	budget := run.Budget{Steps: run.NewQuota(20), Effects: run.NewQuota(20), Signals: run.NewQuota(20)}
	delegate, err := agent.NewDelegate(agent.DelegateConfig{Name: "worker", Description: "Return one value.", Program: child, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "c1", Name: "worker", Arguments: `{"value":"evidence"}`}))
		return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}, nil
	})
	root := delegateInteractionWithValidator(t, model, nil, []agent.Delegate{delegate}, nil, 1)
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, root.Program, interactionInput(t, "delegate once"))
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
