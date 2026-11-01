package agent_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

func TestInvalidModelDataHasAnExternalFailureCode(t *testing.T) {
	duplicate := chat.ToolCall{ID: "duplicate", Name: "unused", Arguments: `{}`}
	message := chat.NewAssistantMessage(chat.NewToolCallPart(duplicate), chat.NewToolCallPart(duplicate))
	for name, response := range map[string]*chat.Response{
		"duplicate call ID": {Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}},
		"missing message":   {Output: &chat.Output{FinishReason: chat.FinishReasonStop}},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { calls++; return response, nil })
			configured := configuredInteraction(t, agent.DefinitionConfig{Name: "test.invalid_model", Description: "Classify invalid provider output."}, agent.DispatcherConfig{Model: model}, agent.ToolSetConfig{})
			engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(context.WithoutCancel(t.Context()))
			input, err := run.EncodePayload(agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runtest.Observe(t.Context(), engine, configured.Program, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != run.FailureKindExternal || failure.Code() != "interaction.model.invalid_response" || calls != 1 {
				t.Fatalf("failure=%+v model calls=%d", failure, calls)
			}
		})
	}
}

func TestDefinitionRejectsFiniteZeroModelCalls(t *testing.T) {
	_, err := agent.NewDefinition(agent.DefinitionConfig{Name: "test.zero", Description: "Reject a zero model allowance.", MaxModelCalls: run.NewQuota(0)})
	if !errors.Is(err, agent.ErrInvalidDefinitionConfig) {
		t.Fatal(err)
	}
}
