package agent_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
)

func captureToolInput(t *testing.T, engine *run.Runtime, root *run.Handle[run.Payload]) (run.TreeSnapshot, agent.PendingToolInput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		snapshot, err := engine.CaptureTree(ctx, root.Relation().ProcessID())
		if err != nil {
			t.Fatal(err)
		}
		pending, err := agent.PendingToolInputs(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 1 {
			// A child can expose its input before the parent finishes entering
			// its wait. Recovery fixtures need a cut that cannot advance alone.
			parked := true
			for _, process := range snapshot.ProcessSnapshots() {
				status := process.Status()
				if !status.Terminal() && status != run.StatusWaiting && status != run.StatusPaused {
					parked = false
					break
				}
			}
			if parked {
				return snapshot, pending[0]
			}
		}
		if len(pending) > 1 || inspectProcessSnapshot(t, engine, root).Status().Terminal() {
			t.Fatalf("Tool waits=%d root status=%s", len(pending), inspectProcessSnapshot(t, engine, root).Status())
		}
		runtime.Gosched()
	}
	t.Fatal(ctx.Err())
	return run.TreeSnapshot{}, agent.PendingToolInput{}
}

func pendingToolProcess(t *testing.T, engine *run.Runtime, pending agent.PendingToolInput) *run.Handle[run.Payload] {
	t.Helper()
	process, found := engine.Process(pending.ProcessID())
	if !found {
		t.Fatal("pending Tool Process is missing")
	}
	return process
}

type interactionDeployment struct {
	run.Program[run.Payload, run.Payload]
	// tools is the Tool child binding of the Interaction, when configured.
	tools run.Program[run.Payload, run.Payload]
}

func configuredInteraction(t *testing.T, definitionConfig agent.DefinitionConfig, dispatcherConfig agent.DispatcherConfig, toolConfig agent.ToolSetConfig) interactionDeployment {
	t.Helper()
	definitionConfig.PrepareContext = dispatcherConfig.ContextProcessor != nil
	toolSet := testToolSet(t, toolConfig)
	if toolSet.Configured() {
		definitionConfig.Tools = toolSet
	}
	definition, err := agent.NewDefinition(definitionConfig)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := agent.NewDispatcher(definition, dispatcherConfig)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("interaction-test-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte("interaction-test-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return toolInteractionDeployment(deployment, toolSet)
}

func testToolSet(t *testing.T, config agent.ToolSetConfig) agent.ToolSet {
	t.Helper()
	if len(config.Tools)+len(config.DeferredTools) == 0 {
		return agent.ToolSet{}
	}
	config.Name = "interaction.test.tools"
	config.Description = "Execute one test Tool call per child Process."
	config.ImplementationDigest = run.ComputeDigest([]byte("test-tool-implementation"))
	config.ConfigurationDigest = run.ComputeDigest([]byte("test-tool-configuration"))
	toolSet, err := agent.NewToolSet(config)
	if err != nil {
		t.Fatal(err)
	}
	return toolSet
}

func toolInteractionDeployment(deployment run.Program[run.Payload, run.Payload], toolSet agent.ToolSet) interactionDeployment {
	return interactionDeployment{Program: deployment, tools: toolSet.Program()}
}

func inspectProcessSnapshot(t *testing.T, engine *run.Runtime, process *run.Handle[run.Payload]) run.ProcessSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	inspection, err := engine.InspectTree(ctx, process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	report, found := inspection.Process(process.Relation().ProcessID())
	if !found {
		t.Fatal("Process is missing from the inspected tree")
	}
	return report.Snapshot
}
