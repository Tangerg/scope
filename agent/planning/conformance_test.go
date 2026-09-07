package planning_test

import (
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/planning"
)

func TestDefinitionConformance(t *testing.T) {
	condition := mustCondition(t, "conformance.ready", planning.True)
	action := mustAction(t, planning.ActionConfig{
		Name: "prepare", Description: "Prepare the observed world.", Effects: []planning.Condition{condition},
	})
	definition := newManagedDefinition(t, managedDeploymentConfig{
		name:     "planning.conformance",
		goal:     mustGoal(t, condition),
		bindings: []planning.ActionBinding{mustDispatcherBinding(t, action)},
	})
	input, err := agent.EncodeInput(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	world := newManagedWorld(t)
	dispatcher, err := planning.NewDispatcher(definition, planning.DispatcherConfig{
		Sensor: world, ActionExecutors: map[string]planning.ActionExecutor{"prepare": world.apply(action)},
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
	if output.Outcome != planning.OutcomeAchieved || len(output.Attempts) != 1 ||
		output.Attempts[0].Status != planning.AttemptSucceeded || world.observationCount() != 2 {
		t.Fatalf("output=%+v observations=%d", output, world.observationCount())
	}
}
