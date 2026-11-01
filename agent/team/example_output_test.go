package team_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/team"
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
			definition := exampleValue(agent.NewDefinition(agent.DefinitionConfig{
				Name: "test.incomplete_coordinator", Description: "Return parseable JSON without a complete decision.", MaxModelCalls: run.NewQuota(1),
			}))
			client := exampleValue(chatclient.New(model, chatclient.Config{}))
			dispatcher := exampleValue(agent.NewDispatcher(definition, agent.DispatcherConfig{Model: client}))
			coordinator := exampleCoordinator(exampleValue(run.Bind[agent.Input, agent.Output](run.BindingConfig{Definition: definition, Dispatcher: dispatcher, ImplementationDigest: run.ComputeDigest([]byte("example coordinator build")), ConfigurationDigest: run.ComputeDigest([]byte("coordinator output settings"))})), run.Budget{})
			engine := exampleValue(run.New(t.Context(), run.Config{Store: run.NewMemoryStore()}))
			t.Cleanup(func() {
				if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})
			input := exampleValue(run.EncodePayload(team.Turn{Number: 1, State: exampleValue(run.EncodePayload("initial"))}))
			result, err := runtest.Observe(t.Context(), engine, coordinator, input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if result.Termination().Status() != run.StatusFailed || !failed || failure.Code() != "execution.step.failed" {
				t.Fatalf("incomplete decision was adopted: %+v", result.Termination())
			}
			if _, present := result.Termination().Output(); present {
				t.Fatal("incomplete decision escaped as coordinator output")
			}
		})
	}
}
