package role_test

import (
	"context"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/role"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

type capabilityDispatcher struct {
	agent.Dispatcher
	required agent.CapabilitySet
}

func (c capabilityDispatcher) Policy(effect agent.Effect) agent.EffectPolicy {
	policy := c.Dispatcher.Policy(effect)
	policy.RequiredCapabilities = c.required
	return policy
}

func TestRoleGrantsOnlyExplicitlyAllocatedCapabilities(t *testing.T) {
	grants := exampleValue(agent.NewCapabilitySet(exampleValue(agent.ParseCapability("model.call"))))
	plain := greetDeployment()
	dispatcher := exampleValue(interaction.NewDispatcher(plain.Definition().(*interaction.Definition), interaction.DispatcherConfig{Model: greetModel{}}))
	secured := exampleValue(agent.NewDeployment(agent.DeploymentConfig{
		Definition: plain.Definition(), Dispatcher: capabilityDispatcher{Dispatcher: dispatcher, required: grants},
		ImplementationDigest: agent.ComputeDigest([]byte("secured-greet")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("secured-greet-config")),
	}))
	build := func(_ context.Context, _ greetRequest) (interaction.Input, error) {
		return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(`{"name":"ada"}`))}}, nil
	}
	format := chatclient.JSON[greeting]()
	for _, test := range []struct {
		name          string
		parent, child agent.CapabilitySet
		failure       string
	}{
		{name: "explicit grant", parent: grants, child: grants},
		{name: "no implicit inheritance", parent: grants, failure: "engine.capability.denied"},
		{name: "no parent escalation", child: grants, failure: "engine.child.capability_escalation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			deployment := exampleValue(role.New(role.Config[greetRequest, greeting]{
				Name: "role.capabilities", Description: "Compose an explicitly authorized Interaction.",
				Interaction: secured, Capabilities: test.child, BuildInput: build,
				DecodeOutput: func(_ context.Context, output interaction.Output) (greeting, error) {
					return format.Decode(output.ModelResponse)
				},
				ImplementationDigest: agent.ComputeDigest([]byte("capability-role")),
				ConfigurationDigest:  agent.ComputeDigest([]byte("capability-role-config")),
			}))
			engine := exampleValue(agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), Capabilities: test.parent}))
			t.Cleanup(func() {
				if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})
			result := exampleValue(engine.Run(t.Context(), deployment, exampleValue(agent.EncodePayload(greetRequest{Name: "ada"}))))
			if test.failure != "" {
				failure, ok := result.Termination().Failure()
				if !ok || failure.Kind() != agent.FailureKindContract || failure.Code() != test.failure {
					t.Fatalf("denied role=%v, want %s", result.Termination(), test.failure)
				}
				return
			}
			if result.Termination().Status() != agent.StatusCompleted {
				t.Fatalf("authorized role=%v", result.Termination())
			}
			output, ok := result.Termination().Output()
			if !ok || exampleValue(output.Decode[greeting]()).Message != "hi ada" {
				t.Fatal("authorized Interaction output was lost")
			}
		})
	}
}
