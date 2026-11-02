package workflow_test

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestDefinitionConformance(t *testing.T) {
	transform := mustTransform(t, "increment", func(_ context.Context, input numberInput) (numberOutput, error) {
		return numberOutput{Value: input.Value + 1}, nil
	})
	child := mustDeployment(t, mustDefinition(t, "workflow.conformance.child", transform), "conformance-child")
	call, err := workflow.Call(workflow.CallConfig{
		ID: "increment_child", Program: child, Budget: mustBudget(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, "workflow.conformance", call)
	input, err := run.EncodePayload(numberInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	result := conformancetest.Run(t, run.BindingConfig{
		Definition:           definition,
		ImplementationDigest: run.ComputeDigest([]byte("workflow-conformance")),
		ConfigurationDigest:  run.ComputeDigest([]byte("child-call")),
	}, run.Config{Store: run.NewMemoryStore()}, input)
	output, present := result.Termination().Output()
	if !present || string(output.JSON()) != `{"value":8}` || result.Usage().PreparedEffects != 2 {
		t.Fatalf("child-call output=%s usage=%+v", output.JSON(), result.Usage())
	}
}
