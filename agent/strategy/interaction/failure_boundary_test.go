package interaction_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
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
			configured := configuredInteraction(t, interaction.DefinitionConfig{Name: "test.invalid_model", Description: "Classify invalid provider output."}, interaction.DispatcherConfig{Model: model}, interaction.ToolSetConfig{})
			engine, err := agent.NewEngine(agent.EngineConfig{})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(context.WithoutCancel(t.Context()))
			input, err := agent.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Run(t.Context(), configured.Deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != agent.FailureKindExternal || failure.Code() != "interaction.model.invalid_response" || calls != 1 {
				t.Fatalf("failure=%+v model calls=%d", failure, calls)
			}
		})
	}
}

func TestDefinitionRejectsFiniteZeroModelCalls(t *testing.T) {
	_, err := interaction.NewDefinition(interaction.DefinitionConfig{Name: "test.zero", Description: "Reject a zero model allowance.", MaxModelCalls: agent.NewQuota(0)})
	if !errors.Is(err, interaction.ErrInvalidDefinitionConfig) {
		t.Fatal(err)
	}
}
