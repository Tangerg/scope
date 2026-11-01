package agent_test

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestDefinitionConformance(t *testing.T) {
	input, err := run.EncodePayload(agent.Input{
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
		name       string
		direct     bool
		executable tool.Tool
		modelCalls int
		effects    uint64
	}{
		{"model_response", false, add, 2, 4},
		{"direct_tool_results", true, directTool{Tool: add}, 1, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			toolSet := testToolSet(t, agent.ToolSetConfig{Tools: []tool.Tool{test.executable}})
			definition, err := agent.NewDefinition(agent.DefinitionConfig{
				Name:          "interaction.conformance",
				Description:   "Verify the Interaction Definition and Execution contract.",
				MaxModelCalls: run.NewQuota(2), Tools: toolSet, ToolBudget: run.Budget{Steps: run.NewQuota(8), Effects: run.NewQuota(4), Signals: run.NewQuota(8)},
			})
			if err != nil {
				t.Fatal(err)
			}
			model := &scriptedModel{}
			client, err := chatclient.New(model, chatclient.Config{})
			if err != nil {
				t.Fatal(err)
			}
			dispatcher, err := agent.NewDispatcher(definition, agent.DispatcherConfig{
				Model: client,
			})
			if err != nil {
				t.Fatal(err)
			}
			result := conformancetest.Run(t, run.BindingConfig{
				Definition: definition, Dispatcher: dispatcher,
				ImplementationDigest: run.ComputeDigest([]byte("interaction-conformance")),
				ConfigurationDigest:  run.ComputeDigest([]byte(test.name)),
			}, run.Config{Store: run.NewMemoryStore()}, input)
			if result.Usage().PreparedEffects != test.effects || model.Calls() != test.modelCalls {
				t.Fatalf("tool-loop usage=%+v model calls=%d, want effects=%d calls=%d", result.Usage(), model.Calls(), test.effects, test.modelCalls)
			}
			erased, present := result.Termination().Output()
			output, err := erased.Decode[agent.Output]()
			if !present || err != nil || (len(output.DirectToolResults) != 0) != test.direct {
				t.Fatalf("output=%+v present=%t error=%v, want direct=%t", output, present, err, test.direct)
			}
		})
	}
}
