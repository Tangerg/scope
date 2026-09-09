// Command autonomous demonstrates an Interaction in which the model chooses a
// Tool from environment feedback and decides when to stop.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

const (
	calculatorToolRequestCall = 1
	calculatorFinalAnswerCall = 2
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	client, err := chatclient.New(&calculatorModel{}, chatclient.Config{})
	if err != nil {
		return err
	}
	toolSet, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "example.autonomous_tools", Description: "Execute each requested Tool through its own child Process.", Tools: []tool.Tool{additionTool{}},
		ImplementationDigest: agent.ComputeDigest([]byte("example.autonomous_tools.implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example.autonomous_tools.configuration")),
	})
	if err != nil {
		return err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name:          "example.autonomous_calculator",
		Description:   "Use available Tools until the requested calculation is complete.",
		MaxModelCalls: calculatorFinalAnswerCall,
		Tools:         toolSet, ToolBudget: agent.Budget{Steps: 8, Effects: 4, Signals: 8},
	})
	if err != nil {
		return err
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Client: client,
	})
	if err != nil {
		return err
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		Dispatcher:           dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("example-autonomous-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-autonomous-configuration")),
	})
	if err != nil {
		return err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deploymentResolver{toolSet.Deployment().DeploymentRef(): toolSet.Deployment()}})
	if err != nil {
		return err
	}
	defer engine.Close()
	input, err := agent.EncodeInput(interaction.Input{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("What is 20 + 22?")),
	}})
	if err != nil {
		return err
	}
	result, err := engine.Run(ctx, deployment, input)
	if err != nil {
		return err
	}
	erased, ok := result.Output()
	if !ok {
		return fmt.Errorf("autonomous Process ended with %s", result.Status())
	}
	decoded, err := erased.Decode[interaction.Output]()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, decoded.ModelResponse.Text())
	return err
}

type additionTool struct{}

func (additionTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name:        "add",
		Description: "Add two numbers and return their sum.",
		InputSchema: json.RawMessage(`{
            "type":"object",
            "properties":{"left":{"type":"number"},"right":{"type":"number"}},
            "required":["left","right"],
            "additionalProperties":false
        }`),
	}
}

func (additionTool) Call(_ context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
	var input struct {
		Left  float64 `json:"left"`
		Right float64 `json:"right"`
	}
	if err := json.Unmarshal(invocation.Arguments(), &input); err != nil {
		return chat.ToolOutput{}, err
	}
	return chat.NewTextToolOutput(fmt.Sprintf("%g", input.Left+input.Right)), nil
}

type calculatorModel struct{ calls int }

func (c *calculatorModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	c.calls++
	switch c.calls {
	case calculatorToolRequestCall:
		message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
			ID: "calculation_1", Name: "add", Arguments: `{"left":20,"right":22}`,
		}))
		return &chat.Response{Output: &chat.Output{
			Message: &message, FinishReason: chat.FinishReasonToolCalls,
		}}, nil
	case calculatorFinalAnswerCall:
		last := request.Messages[len(request.Messages)-1]
		if last.Role != chat.RoleTool || len(last.Parts) != 1 || last.Parts[0].ToolResult == nil {
			return nil, errors.New("model did not receive the addition result")
		}
		result, ok := last.Parts[0].ToolResult.Output.Text()
		if !ok || result != "42" {
			return nil, errors.New("model did not receive the addition result")
		}
		message := chat.NewAssistantMessage(chat.NewTextPart("20 + 22 = 42"))
		return &chat.Response{Output: &chat.Output{
			Message: &message, FinishReason: chat.FinishReasonStop,
		}}, nil
	default:
		return nil, errors.New("interaction did not stop after receiving the Tool result")
	}
}

type deploymentResolver map[agent.DeploymentRef]agent.Deployment

func (d deploymentResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, found := d[reference]
	if !found {
		return agent.Deployment{}, fmt.Errorf("deployment %s is not bound", reference.Name())
	}
	return deployment, nil
}
