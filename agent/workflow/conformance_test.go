package workflow_test

import (
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestDefinitionConformance(t *testing.T) {
	transform := mustTransform(t, "increment", func(input numberInput) (numberOutput, error) {
		return numberOutput{Value: input.Value + 1}, nil
	})
	child := mustDeployment(t, mustDefinition(t, "workflow.conformance.child", transform), "conformance-child")
	call, err := workflow.Call(workflow.CallConfig{
		ID: "increment_child", Deployment: child, Budget: mustBudget(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, "workflow.conformance", call)
	input, err := agent.EncodeInput(numberInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	result := conformancetest.Run(t, agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte("workflow-conformance")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("child-call")),
	}, agent.EngineConfig{
		DeploymentResolver: deploymentResolver{child.DeploymentRef(): child},
	}, input)
	output, present := result.Output()
	if !present || string(output.JSON()) != `{"value":8}` || result.Usage().PreparedEffects != 2 {
		t.Fatalf("child-call output=%s usage=%+v", output.JSON(), result.Usage())
	}
}
