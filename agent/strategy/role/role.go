// Package role is a convenience facade that presents an interaction as a
// business-typed, composable Deployment. The repeated assembly — adapt a business
// input into an [interaction.Input], run the interaction, project its
// [interaction.Output] into a business result — otherwise reappears at every call
// site. New composes the canonical workflow and interaction atoms into one plain
// [agent.Deployment]; it introduces no new execution, working context, or
// persistence, and the produced Deployment stays composable like any other.
//
// role is the sanctioned composition tier: unlike the strategies, which never
// import one another, role may depend on the specific strategies it adapts.
package role

import (
	"context"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

// ErrInvalidConfig reports a role Config that cannot form a Deployment.
var ErrInvalidConfig = errors.New("role: invalid config")

// Config binds one interaction to a business input and output through pure
// adapters. BuildInput and DecodeOutput are the type-adapter seam; the
// interaction, not this facade, owns the model-and-tool loop.
type Config[I, O any] struct {
	Name        string
	Description string

	// Interaction is the model-and-tool loop the role runs as its exact child.
	Interaction agent.Deployment

	// BuildInput adapts the business input into the interaction request.
	BuildInput func(context.Context, I) (interaction.Input, error)

	// DecodeOutput projects the settled interaction result into the business
	// output. It should reuse an authoritative decode rule, such as
	// [github.com/Tangerg/scope/core/chatclient.OutputFormat.Decode], rather than
	// a private one.
	DecodeOutput func(context.Context, interaction.Output) (O, error)

	// Budget is permanently allocated to the interaction child when it starts.
	Budget agent.Budget

	ImplementationDigest agent.Digest
	ConfigurationDigest  agent.Digest
}

// New composes BuildInput, the interaction child, and DecodeOutput into one
// ordinary workflow-backed Deployment presenting the I -> O contract.
func New[I, O any](config Config[I, O]) (agent.Deployment, error) {
	if config.BuildInput == nil {
		return agent.Deployment{}, fmt.Errorf("%w: BuildInput is required", ErrInvalidConfig)
	}
	if config.DecodeOutput == nil {
		return agent.Deployment{}, fmt.Errorf("%w: DecodeOutput is required", ErrInvalidConfig)
	}
	buildInput, err := workflow.Transform("build_input", config.BuildInput)
	if err != nil {
		return agent.Deployment{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	run, err := workflow.Call(workflow.CallConfig{ID: "run", Deployment: config.Interaction, Budget: config.Budget})
	if err != nil {
		return agent.Deployment{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	decodeOutput, err := workflow.Transform("decode_output", config.DecodeOutput)
	if err != nil {
		return agent.Deployment{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: config.Name, Description: config.Description,
		Stages: []workflow.Stage{buildInput, run, decodeOutput},
	})
	if err != nil {
		return agent.Deployment{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: config.ImplementationDigest,
		ConfigurationDigest:  config.ConfigurationDigest,
	})
}
