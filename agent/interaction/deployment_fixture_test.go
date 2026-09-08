package interaction_test

import (
	"context"
	"maps"
	"runtime"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
)

func (i interactionDeployment) resolveWith(children ...agent.Deployment) delegateResolver {
	resolver := maps.Clone(i.resolver)
	for _, child := range children {
		resolver[child.DeploymentRef()] = child
	}
	return resolver
}

func captureToolInput(t *testing.T, engine *agent.Engine, root *agent.Process) (agent.TreeSnapshot, interaction.PendingToolInput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		snapshot, err := engine.CaptureTree(ctx, root.ID())
		if err != nil {
			t.Fatal(err)
		}
		pending, err := interaction.PendingToolInputs(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 1 {
			return snapshot, pending[0]
		}
		if len(pending) > 1 || root.Status().Terminal() {
			t.Fatalf("Tool waits=%d root status=%s", len(pending), root.Status())
		}
		runtime.Gosched()
	}
	t.Fatal(ctx.Err())
	return agent.TreeSnapshot{}, interaction.PendingToolInput{}
}

func pendingToolProcess(t *testing.T, engine *agent.Engine, pending interaction.PendingToolInput) *agent.Process {
	t.Helper()
	process, found := engine.Process(pending.ProcessID())
	if !found {
		t.Fatal("pending Tool Process is missing")
	}
	return process
}

type interactionDeployment struct {
	agent.Deployment
	resolver delegateResolver
}

func configuredInteraction(t *testing.T, definitionConfig interaction.DefinitionConfig, dispatcherConfig interaction.DispatcherConfig, toolConfig interaction.ToolSetConfig) interactionDeployment {
	t.Helper()
	toolSet := testToolSet(t, toolConfig)
	if toolSet.Valid() {
		definitionConfig.Tools = toolSet
		definitionConfig.ToolBudget = agent.Budget{Steps: 32, Effects: 16, Signals: 32}
	}
	definition, err := interaction.NewDefinition(definitionConfig)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, dispatcherConfig)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("interaction-test-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("interaction-test-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return toolInteractionDeployment(deployment, toolSet)
}

func testToolSet(t *testing.T, config interaction.ToolSetConfig) interaction.ToolSet {
	t.Helper()
	if len(config.Tools)+len(config.DeferredTools) == 0 {
		return interaction.ToolSet{}
	}
	config.Name = "interaction.test.tools"
	config.Description = "Execute one test Tool call per child Process."
	config.ImplementationDigest = agent.ComputeDigest([]byte("test-tool-implementation"))
	config.ConfigurationDigest = agent.ComputeDigest([]byte("test-tool-configuration"))
	toolSet, err := interaction.NewToolSet(config)
	if err != nil {
		t.Fatal(err)
	}
	return toolSet
}

func toolInteractionDeployment(deployment agent.Deployment, toolSet interaction.ToolSet) interactionDeployment {
	fixture := interactionDeployment{Deployment: deployment, resolver: make(delegateResolver)}
	if toolSet.Valid() {
		child := toolSet.Deployment()
		fixture.resolver[child.DeploymentRef()] = child
	}
	return fixture
}
