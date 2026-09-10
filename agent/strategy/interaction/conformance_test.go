package interaction_test

import (
	"context"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestDefinitionConformance(t *testing.T) {
	input, err := agent.EncodeInput(interaction.Input{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	add, err := tool.NewFunc(tool.FuncConfig{
		Name: "add", Description: "Add two integers.",
	}, func(_ context.Context, input struct {
		Left  int `json:"left"`
		Right int `json:"right"`
	}) (int, error) {
		return input.Left + input.Right, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		source     interaction.CompletionSource
		executable tool.Tool
		modelCalls int
		effects    uint64
	}{
		{interaction.CompletionSourceModelResponse, add, 2, 4},
		{interaction.CompletionSourceDirectToolResults, directTool{Tool: add}, 1, 3},
	} {
		t.Run(string(test.source), func(t *testing.T) {
			toolSet := testToolSet(t, interaction.ToolSetConfig{Tools: []tool.Tool{test.executable}})
			definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
				Name:          "interaction.conformance",
				Description:   "Verify the Interaction Definition and Execution contract.",
				MaxModelCalls: 2, Tools: toolSet, ToolBudget: agent.Budget{Steps: 8, Effects: 4, Signals: 8},
			})
			if err != nil {
				t.Fatal(err)
			}
			model := &scriptedModel{}
			client, err := chatclient.New(model, chatclient.Config{})
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
				Model: client,
			})
			if err != nil {
				t.Fatal(err)
			}
			result := conformancetest.Run(t, agent.DeploymentConfig{
				Definition: definition, Dispatcher: dispatcher,
				ImplementationDigest: agent.ComputeDigest([]byte("interaction-conformance")),
				ConfigurationDigest:  agent.ComputeDigest([]byte(test.source)),
			}, agent.EngineConfig{DeploymentResolver: delegateResolver{toolSet.Deployment().DeploymentRef(): toolSet.Deployment()}}, input)
			if result.Usage().PreparedEffects != test.effects || model.Calls() != test.modelCalls {
				t.Fatalf("tool-loop usage=%+v model calls=%d, want effects=%d calls=%d", result.Usage(), model.Calls(), test.effects, test.modelCalls)
			}
			erased, present := result.Output()
			output, err := erased.Decode[interaction.Output]()
			if !present || err != nil || output.Source != test.source {
				t.Fatalf("output=%+v present=%t error=%v, want source=%s", output, present, err, test.source)
			}
		})
	}
}
