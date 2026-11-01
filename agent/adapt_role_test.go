package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"

	"github.com/Tangerg/scope/agent/run"
)

func TestRoleConfigGuards(t *testing.T) {
	deployment := greetDeployment()
	build := func(context.Context, greetRequest) (agent.Input, error) { return agent.Input{}, nil }
	decode := func(context.Context, agent.Output) (greeting, error) { return greeting{}, nil }

	if _, err := agent.Adapt(t.Context(), agent.AdapterConfig[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", Interaction: deployment, DecodeOutput: decode,
	}); !errors.Is(err, agent.ErrInvalidAdapter) {
		t.Fatalf("missing BuildInput error = %v, want ErrInvalidConfig", err)
	}
	if _, err := agent.Adapt(t.Context(), agent.AdapterConfig[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", Interaction: deployment, BuildInput: build,
	}); !errors.Is(err, agent.ErrInvalidAdapter) {
		t.Fatalf("missing DecodeOutput error = %v, want ErrInvalidConfig", err)
	}
	if _, err := agent.Adapt(t.Context(), agent.AdapterConfig[greetRequest, greeting]{
		Name: "role.test.guard", Description: "Guard construction.", BuildInput: build, DecodeOutput: decode,
	}); !errors.Is(err, agent.ErrInvalidAdapter) {
		t.Fatalf("missing Interaction error = %v, want ErrInvalidConfig", err)
	}
}

func TestRoleRejectsUnrepresentableContracts(t *testing.T) {
	deployment := greetDeployment()
	_, err := agent.Adapt(t.Context(), agent.AdapterConfig[chan string, greeting]{
		Name: "role.invalid_input", Description: "Reject an input without a JSON contract.", Interaction: deployment,
		BuildInput:   func(context.Context, chan string) (agent.Input, error) { return agent.Input{}, nil },
		DecodeOutput: func(context.Context, agent.Output) (greeting, error) { return greeting{}, nil },
	})
	if !errors.Is(err, agent.ErrInvalidAdapter) {
		t.Fatalf("unrepresentable input error = %v", err)
	}
	_, err = agent.Adapt(t.Context(), agent.AdapterConfig[greetRequest, chan string]{
		Name: "role.invalid_output", Description: "Reject an output without a JSON contract.", Interaction: deployment,
		BuildInput:   func(context.Context, greetRequest) (agent.Input, error) { return agent.Input{}, nil },
		DecodeOutput: func(context.Context, agent.Output) (chan string, error) { return nil, nil },
	})
	if !errors.Is(err, agent.ErrInvalidAdapter) {
		t.Fatalf("unrepresentable output error = %v", err)
	}
}

func TestRoleRequiresAValidDescriptorAndExactIdentity(t *testing.T) {
	config := agent.AdapterConfig[greetRequest, greeting]{
		Name: "invalid name", Description: "Require a qualified role name.", Interaction: greetDeployment(),
		BuildInput:   func(context.Context, greetRequest) (agent.Input, error) { return agent.Input{}, nil },
		DecodeOutput: func(context.Context, agent.Output) (greeting, error) { return greeting{}, nil },
	}
	if deployment, err := agent.Adapt(t.Context(), config); !errors.Is(err, agent.ErrInvalidAdapter) || deployment.Valid() {
		t.Fatalf("invalid descriptor returned deployment=%v error=%v", deployment.Valid(), err)
	}
	config.Name = "role.missing_identity"
	if deployment, err := agent.Adapt(t.Context(), config); !errors.Is(err, run.ErrInvalidProgram) || deployment.Valid() {
		t.Fatalf("missing binding identity returned deployment=%v error=%v", deployment.Valid(), err)
	}
}
