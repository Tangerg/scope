package coordination_test

import (
	"context"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestCoordinationRejectsUnaddressedInputBeforeProtocolStep(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		deployment := deadlineBinding(t, coordination.DeadlineDispatcher{})
		conformancetest.Run(t, run.BindingConfig{Definition: deployment.Definition(), Dispatcher: coordination.DeadlineDispatcher{}, ImplementationDigest: run.ComputeDigest([]byte("timer")), ConfigurationDigest: run.ComputeDigest([]byte("timer"))}, run.Config{Store: run.NewMemoryStore()}, encodedInput(t, time.Unix(1, 0).UTC()))
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
		root := competition(t, func(context.Context, run.ChildKey, run.ChildOutcome) (bool, error) { return true, nil }, 1)
		conformancetest.Run(t, run.BindingConfig{Definition: root, ImplementationDigest: run.ComputeDigest([]byte("competition")), ConfigurationDigest: run.ComputeDigest([]byte("competition"))}, run.Config{Store: run.NewMemoryStore(), ProgramResolver: resolver{child.ProgramRef(): child}}, encodedInput(t, []run.ChildSpec{candidate(t, "winner", child, encodedInput(t, "done"))}))
	})
}
