package role_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/role"
)

func TestRoleConfigGuards(t *testing.T) {
	deployment := greetDeployment()
	build := func(context.Context, greetRequest) (interaction.Input, error) { return interaction.Input{}, nil }
	decode := func(context.Context, interaction.Output) (greeting, error) { return greeting{}, nil }

	if _, err := role.New(role.Config[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", Interaction: deployment, DecodeOutput: decode,
	}); !errors.Is(err, role.ErrInvalidConfig) {
		t.Fatalf("missing BuildInput error = %v, want ErrInvalidConfig", err)
	}
	if _, err := role.New(role.Config[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", Interaction: deployment, BuildInput: build,
	}); !errors.Is(err, role.ErrInvalidConfig) {
		t.Fatalf("missing DecodeOutput error = %v, want ErrInvalidConfig", err)
	}
	if _, err := role.New(role.Config[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", BuildInput: build, DecodeOutput: decode,
	}); !errors.Is(err, role.ErrInvalidConfig) {
		t.Fatalf("missing Interaction error = %v, want ErrInvalidConfig", err)
	}
}

func TestRoleRejectsUnrepresentableContracts(t *testing.T) {
	deployment := greetDeployment()
	_, err := role.New(role.Config[chan string, greeting]{
		Name: "role.invalid_input", Description: "Reject an input without a JSON contract.", Interaction: deployment,
		BuildInput:   func(context.Context, chan string) (interaction.Input, error) { return interaction.Input{}, nil },
		DecodeOutput: func(context.Context, interaction.Output) (greeting, error) { return greeting{}, nil },
	})
	if !errors.Is(err, role.ErrInvalidConfig) {
		t.Fatalf("unrepresentable input error = %v", err)
	}
	_, err = role.New(role.Config[greetRequest, chan string]{
		Name: "role.invalid_output", Description: "Reject an output without a JSON contract.", Interaction: deployment,
		BuildInput:   func(context.Context, greetRequest) (interaction.Input, error) { return interaction.Input{}, nil },
		DecodeOutput: func(context.Context, interaction.Output) (chan string, error) { return nil, nil },
	})
	if !errors.Is(err, role.ErrInvalidConfig) {
		t.Fatalf("unrepresentable output error = %v", err)
	}
}

func TestRoleRequiresAValidDescriptorAndExactIdentity(t *testing.T) {
	config := role.Config[greetRequest, greeting]{
		Name: "invalid name", Description: "Require a qualified role name.", Interaction: greetDeployment(),
		BuildInput:   func(context.Context, greetRequest) (interaction.Input, error) { return interaction.Input{}, nil },
		DecodeOutput: func(context.Context, interaction.Output) (greeting, error) { return greeting{}, nil },
	}
	if deployment, err := role.New(config); !errors.Is(err, role.ErrInvalidConfig) || deployment.Valid() {
		t.Fatalf("invalid descriptor returned deployment=%v error=%v", deployment.Valid(), err)
	}
	config.Name = "role.missing_identity"
	if deployment, err := role.New(config); !errors.Is(err, agent.ErrInvalidDeployment) || deployment.Valid() {
		t.Fatalf("missing binding identity returned deployment=%v error=%v", deployment.Valid(), err)
	}
}
