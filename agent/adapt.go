package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

const (
	stageBuildInput   = "build_input"
	stageRun          = "run"
	stageDecodeOutput = "decode_output"
)

var ErrInvalidAdapter = errors.New("agent: invalid adapter configuration")

// AdapterConfig binds one agent program to a business input and output through pure
// adapters. The bound child retains ownership of its model and tool loop.
type AdapterConfig[I, O any] struct {
	Name        string
	Description string

	Interaction run.Program[Input, Output]

	BuildInput func(context.Context, I) (Input, error)

	// DecodeOutput should reuse an authoritative decode rule, such as
	// [github.com/Tangerg/scope/core/chatclient.OutputFormat.Decode], so the role
	// does not grow a second, weaker structured-output rule.
	DecodeOutput func(context.Context, Output) (O, error)

	Budget run.Budget
	// Capabilities is the explicit attenuated grant to the Interaction child.
	// Its zero value grants no authority.
	Capabilities run.CapabilitySet

	ImplementationDigest run.Digest
	ConfigurationDigest  run.Digest
}

func (a AdapterConfig[I, O]) validate() error {
	switch {
	case a.BuildInput == nil:
		return fmt.Errorf("%w: BuildInput is required", ErrInvalidAdapter)
	case a.DecodeOutput == nil:
		return fmt.Errorf("%w: DecodeOutput is required", ErrInvalidAdapter)
	case !a.Interaction.Valid():
		return fmt.Errorf("%w: Interaction is required", ErrInvalidAdapter)
	}
	return nil
}

// Adapt composes the role into one ordinary [run.Program] with an I -> O
// contract, reusing the canonical workflow and interaction atoms.
func Adapt[I, O any](ctx context.Context, config AdapterConfig[I, O]) (run.Program[I, O], error) {
	if err := config.validate(); err != nil {
		return run.Program[I, O]{}, err
	}
	buildInput, buildErr := workflow.Transform(stageBuildInput, config.BuildInput)
	invoke, runErr := workflow.Call(workflow.CallConfig{
		ID: stageRun, Deployment: config.Interaction.Erase(), Budget: config.Budget, Capabilities: config.Capabilities,
	})
	decodeOutput, decodeErr := workflow.Transform(stageDecodeOutput, config.DecodeOutput)
	if err := errors.Join(buildErr, runErr, decodeErr); err != nil {
		return run.Program[I, O]{}, fmt.Errorf("%w: %w", ErrInvalidAdapter, err)
	}
	program, err := workflow.Compile[I, O](ctx, workflow.Config{Name: config.Name, Description: config.Description, Stages: []workflow.Stage{buildInput, invoke, decodeOutput}, BuildIdentity: config.ImplementationDigest, CallbackIdentity: config.ConfigurationDigest})
	if err != nil {
		return run.Program[I, O]{}, fmt.Errorf("%w: %w", ErrInvalidAdapter, err)
	}
	return program, nil
}
