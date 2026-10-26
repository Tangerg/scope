package collaboration_test

import (
	"context"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/collaboration"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

func TestCoordinatorExampleRejectsIncompleteDecision(t *testing.T) {
	for _, test := range []struct {
		name    string
		finish  chat.FinishReason
		refusal bool
	}{
		{name: "truncated", finish: chat.FinishReasonLength},
		{name: "refused", finish: chat.FinishReasonRefusal, refusal: true},
		{name: "stopped with refusal", finish: chat.FinishReasonStop, refusal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				parts := []chat.Part{chat.NewTextPart(`{"mode":"continue","state":"next"}`)}
				if test.refusal {
					parts = append(parts, chat.NewRefusalPart("decision declined"))
				}
				message := chat.NewAssistantMessage(parts...)
				return chat.NewResponse(&chat.Output{Message: &message, FinishReason: test.finish}, nil)
			})
			definition := exampleValue(interaction.NewDefinition(interaction.DefinitionConfig{
				Name: "test.incomplete_coordinator", Description: "Return parseable JSON without a complete decision.", MaxModelCalls: agent.NewQuota(1),
			}))
			client := exampleValue(chatclient.New(model, chatclient.Config{}))
			dispatcher := exampleValue(interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: client}))
			coordinator := exampleCoordinator(exampleBinding(definition, dispatcher), agent.Budget{})
			engine := exampleValue(agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}))
			t.Cleanup(func() {
				if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})
			input := exampleValue(agent.EncodePayload(collaboration.Turn{Number: 1, State: exampleValue(agent.EncodePayload("initial"))}))
			result, err := engine.Run(t.Context(), coordinator, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if result.Termination().Status() != agent.StatusFailed || !failed || failure.Code() != "execution.step.failed" {
				t.Fatalf("incomplete decision was adopted: %+v", result.Termination())
			}
			if _, present := result.Termination().Output(); present {
				t.Fatal("incomplete decision escaped as coordinator output")
			}
		})
	}
}
