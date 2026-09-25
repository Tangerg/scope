package interaction

import (
	"fmt"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// DelegateConfig exposes one exact child Deployment as a model-selectable
// Interaction capability. Name and Description are written for the model;
// lifecycle identity and resource authority remain frozen Framework values.
type DelegateConfig struct {
	// Name is the provider-compatible model Tool name.
	Name string

	// Description tells the model when and why to delegate this work.
	Description string

	// Deployment is the exact child behavior binding. The Delegate retains only
	// its immutable reference and Descriptor schemas.
	Deployment agent.Deployment

	// Budget is permanently allocated from the parent for each invocation.
	Budget agent.Budget

	// Capabilities is the attenuated authority set granted to each child.
	Capabilities agent.CapabilitySet
}

// Delegate is an immutable, model-visible binding to one exact managed child
// Deployment. It is a composition value owned by Interaction, not an
// executable Tool or a second Process-start entry point.
type Delegate struct {
	definition    chat.ToolDefinition
	deploymentRef agent.DeploymentRef
	inputSchema   agent.Schema
	outputSchema  agent.Schema
	budget        agent.Budget
	capabilities  agent.CapabilitySet
}

// NewDelegate exposes exactly one child Deployment to the model as a tool. The
// target is fixed at construction so a model cannot choose which agent to
// invoke: routing is a host decision, and a model-selected Deployment would be
// an unbounded authority grant.
func NewDelegate(config DelegateConfig) (Delegate, error) {
	if !config.Deployment.Valid() || !config.Capabilities.Valid() ||
		!agent.ValidDescription(config.Description) {
		return Delegate{}, ErrInvalidDelegate
	}
	descriptor := config.Deployment.Descriptor()
	definition := chat.ToolDefinition{
		Name: config.Name, Description: config.Description,
		InputSchema: descriptor.InputSchema().JSON(),
	}
	if err := definition.Validate(); err != nil {
		return Delegate{}, fmt.Errorf("%w: model contract: %w", ErrInvalidDelegate, err)
	}
	return Delegate{
		definition: definition, deploymentRef: config.Deployment.DeploymentRef(),
		inputSchema: descriptor.InputSchema(), outputSchema: descriptor.OutputSchema(), budget: config.Budget,
		capabilities: config.Capabilities,
	}, nil
}

func (d Delegate) Valid() bool {
	return d.definition.Validate() == nil && agent.ValidDescription(d.definition.Description) &&
		d.deploymentRef.Valid() && d.inputSchema.Valid() && d.outputSchema.Valid() &&
		d.capabilities.Valid()
}

func (d Delegate) validateInput(input agent.Payload) error {
	if !d.Valid() {
		return ErrInvalidDelegate
	}
	return d.inputSchema.Validate(input.JSON())
}

func delegateToolResult(call chat.ToolCall, result agent.Result) (chat.ToolResult, error) {
	if result.Status() != agent.StatusCompleted {
		termination := result.Termination()
		diagnostic := "child ended with " + result.Status().String() + " (" + termination.Cause().String() + ")"
		if termination.Reason() != "" {
			diagnostic += ": " + termination.Reason()
		}
		return delegateErrorResult(call, diagnostic), nil
	}
	output, present := result.Output()
	if !present {
		return chat.ToolResult{}, ErrInvalidExecutionState
	}
	toolOutput, err := chat.NewJSONToolOutput(output.JSON())
	if err != nil {
		return chat.ToolResult{}, fmt.Errorf("%w: encode Delegate Tool output: %w", ErrInvalidExecutionState, err)
	}
	return chat.ToolResult{ID: call.ID, Name: call.Name, Output: toolOutput}, nil
}

func delegateErrorResult(call chat.ToolCall, diagnostic string) chat.ToolResult {
	return chat.ToolResult{
		ID: call.ID, Name: call.Name,
		Output: chat.NewTextToolOutput("error: delegated worker " + agent.NormalizeDiagnostic(diagnostic)), IsError: true,
	}
}

func (d Delegate) prepareInput(call chat.ToolCall) (agent.Payload, error) {
	arguments := strings.TrimSpace(call.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	input, err := agent.ParsePayload([]byte(arguments))
	if err != nil {
		return agent.Payload{}, fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if err := d.validateInput(input); err != nil {
		return agent.Payload{}, fmt.Errorf("arguments violate the delegated worker input contract: %w", err)
	}
	return input, nil
}
