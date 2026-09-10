package planning_test

import (
	"context"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/strategy/planning"
)

func TestDefinitionConformance(t *testing.T) {
	condition := mustCondition(t, "conformance.ready", planning.True)
	refused := mustAction(t, planning.ActionConfig{
		Name: "refused", Description: "Attempt the cheapest route.",
		Effects: []planning.Condition{condition}, Cost: planning.FixedCost(1),
	})
	unconfirmed := mustAction(t, planning.ActionConfig{
		Name: "unconfirmed", Description: "Attempt a route whose prediction may not hold.",
		Effects: []planning.Condition{condition}, Cost: planning.FixedCost(2),
	})
	successful := mustAction(t, planning.ActionConfig{
		Name: "successful", Description: "Prepare the observed world.",
		Effects: []planning.Condition{condition}, Cost: planning.FixedCost(3),
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		name: "planning.conformance",
		goal: mustGoal(t, condition),
		bindings: []planning.ActionBinding{
			mustDispatcherBinding(t, refused), mustDispatcherBinding(t, unconfirmed), mustDispatcherBinding(t, successful),
		},
	})
	input, err := agent.EncodeInput(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	world := newManagedWorld(t)
	failure, err := planning.ActionFailed("route refused")
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := planning.NewDispatcher(definition, planning.DispatcherConfig{
		Sensor: world,
		ActionExecutors: map[string]planning.ActionExecutor{
			"refused": planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
				return failure, nil
			}),
			"unconfirmed": planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
				return planning.ActionSucceeded(), nil
			}),
			"successful": world.apply(successful),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := conformancetest.Run(t, agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("planning-conformance")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("action-reobservation")),
	}, agent.EngineConfig{}, input)
	output := managedOutput(t, result)
	wantAttempts := []planning.Attempt{
		{ActionName: "refused", Status: planning.AttemptFailed, Diagnostic: "route refused"},
		{
			ActionName: "unconfirmed", Status: planning.AttemptUnconfirmed,
			Diagnostic: "Reobservation did not establish the Action's predicted effects",
		},
		{ActionName: "successful", Status: planning.AttemptSucceeded},
	}
	if output.Outcome != planning.OutcomeAchieved || output.PlanningPasses != 3 ||
		!slices.Equal(output.Attempts, wantAttempts) || world.observationCount() != 4 {
		t.Fatalf("output=%+v observations=%d", output, world.observationCount())
	}
}
