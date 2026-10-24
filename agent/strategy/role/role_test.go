package role_test

import (
	"context"
	"errors"
	"testing"

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
