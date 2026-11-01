package agent_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"testing"

	"github.com/Tangerg/scope/agent"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

type capabilityDispatcher struct {
	run.Dispatcher
	required run.CapabilitySet
}

func (c capabilityDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	policy := c.Dispatcher.Policy(effect)
	policy.RequiredCapabilities = c.required
	return policy
}

func TestRoleGrantsOnlyExplicitlyAllocatedCapabilities(t *testing.T) {
	grants := exampleValue(run.NewCapabilitySet(exampleValue(run.ParseCapability("model.call"))))
	plain := greetDeployment()
	dispatcher := exampleValue(agent.NewDispatcher(plain.Definition().(*agent.Definition), agent.DispatcherConfig{Model: greetModel{}}))
	secured := exampleValue(run.Bind[agent.Input, agent.Output](run.BindingConfig{
		Definition: plain.Definition(), Dispatcher: capabilityDispatcher{Dispatcher: dispatcher, required: grants},
		ImplementationDigest: run.ComputeDigest([]byte("secured-greet")),
		ConfigurationDigest:  run.ComputeDigest([]byte("secured-greet-config")),
	}))
	build := func(_ context.Context, _ greetRequest) (agent.Input, error) {
		return agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(`{"name":"ada"}`))}}, nil
	}
	format := chatclient.JSON[greeting]()
	for _, test := range []struct {
		name          string
		parent, child run.CapabilitySet
		failure       string
	}{
		{name: "explicit grant", parent: grants, child: grants},
		{name: "no implicit inheritance", parent: grants, failure: "engine.capability.denied"},
		{name: "no parent escalation", child: grants, failure: "engine.child.capability_escalation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			deployment := exampleValue(agent.Adapt(t.Context(), agent.AdapterConfig[greetRequest, greeting]{
				Name: "role.capabilities", Description: "Compose an explicitly authorized Interaction.",
				Interaction: secured, Capabilities: test.child, BuildInput: build,
				DecodeOutput: func(_ context.Context, output agent.Output) (greeting, error) {
					return format.Decode(output.ModelResponse)
				},
				ImplementationDigest: run.ComputeDigest([]byte("capability-role")),
				ConfigurationDigest:  run.ComputeDigest([]byte("capability-role-config")),
			}))
			engine := exampleValue(run.New(t.Context(), run.Config{Store: run.NewMemoryStore(), Capabilities: test.parent}))
			t.Cleanup(func() {
				if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})
			result := exampleValue(runtest.Observe(t.Context(), engine, deployment.Erase(), exampleValue(run.EncodePayload(greetRequest{Name: "ada"}))))
			if test.failure != "" {
				failure, ok := result.Termination().Failure()
				if !ok || failure.Kind() != run.FailureKindContract || failure.Code() != test.failure {
					t.Fatalf("denied role=%v, want %s", result.Termination(), test.failure)
				}
				return
			}
			if result.Termination().Status() != run.StatusCompleted {
				t.Fatalf("authorized role=%v", result.Termination())
			}
			output, ok := result.Termination().Output()
			if !ok || exampleValue(output.Decode[greeting]()).Message != "hi ada" {
				t.Fatal("authorized Interaction output was lost")
			}
		})
	}
}
