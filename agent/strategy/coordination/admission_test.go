package coordination_test

import (
	"context"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/strategy/coordination"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

func TestCoordinationRejectsUnaddressedInputBeforeProtocolStep(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		deployment := deadlineBinding(t, coordination.Timer{})
		conformancetest.Run(t, agent.DeploymentConfig{Definition: deployment.Definition(), Dispatcher: coordination.Timer{}, ImplementationDigest: agent.ComputeDigest([]byte("timer")), ConfigurationDigest: agent.ComputeDigest([]byte("timer"))}, agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}, encodedInput(t, time.Unix(1, 0).UTC()))
	})
	t.Run("first success", func(t *testing.T) {
		stage, err := workflow.Transform("echo", func(_ context.Context, input string) (string, error) { return input, nil })
		if err != nil {
			t.Fatal(err)
		}
		definition, err := workflow.NewDefinition(workflow.DefinitionConfig{Name: "test.echo", Description: "Return the input.", Stages: []workflow.Stage{stage}})
		if err != nil {
			t.Fatal(err)
		}
		child := bind(t, definition, nil)
		root := competition(t, func(context.Context, agent.ChildOutcome) (bool, error) { return true, nil }, 1)
		conformancetest.Run(t, agent.DeploymentConfig{Definition: root, ImplementationDigest: agent.ComputeDigest([]byte("competition")), ConfigurationDigest: agent.ComputeDigest([]byte("competition"))}, agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), DeploymentResolver: resolver{child.DeploymentRef(): child}}, encodedInput(t, []agent.ChildSpec{candidate(t, "winner", child, encodedInput(t, "done"))}))
	})
}
