package role

import (
	"context"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

const (
	stageBuildInput   = "build_input"
	stageRun          = "run"
	stageDecodeOutput = "decode_output"
)

var ErrInvalidConfig = errors.New("role: invalid config")

// Config binds one interaction to a business input and output through pure
// adapters; the interaction, not role, owns the model-and-tool loop.
type Config[I, O any] struct {
	Name        string
	Description string

	Interaction agent.Deployment

	BuildInput func(context.Context, I) (interaction.Input, error)

	// DecodeOutput should reuse an authoritative decode rule, such as
	// [github.com/Tangerg/scope/core/chatclient.OutputFormat.Decode], so the role
	// does not grow a second, weaker structured-output rule.
	DecodeOutput func(context.Context, interaction.Output) (O, error)

	Budget agent.Budget

	ImplementationDigest agent.Digest
	ConfigurationDigest  agent.Digest
}

func (c Config[I, O]) validate() error {
	switch {
	case c.BuildInput == nil:
		return fmt.Errorf("%w: BuildInput is required", ErrInvalidConfig)
	case c.DecodeOutput == nil:
		return fmt.Errorf("%w: DecodeOutput is required", ErrInvalidConfig)
	case !c.Interaction.Valid():
		return fmt.Errorf("%w: Interaction is required", ErrInvalidConfig)
	}
	return nil
}

// New composes the role into one ordinary [agent.Deployment] with an I -> O
// contract, reusing the canonical workflow and interaction atoms.
func New[I, O any](config Config[I, O]) (agent.Deployment, error) {
	if err := config.validate(); err != nil {
		return agent.Deployment{}, err
	}
	buildInput, buildErr := workflow.Transform(stageBuildInput, config.BuildInput)
	run, runErr := workflow.Call(workflow.CallConfig{ID: stageRun, Deployment: config.Interaction, Budget: config.Budget})
	decodeOutput, decodeErr := workflow.Transform(stageDecodeOutput, config.DecodeOutput)
	if err := errors.Join(buildErr, runErr, decodeErr); err != nil {
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
