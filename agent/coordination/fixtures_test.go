package coordination_test

import (
	"context"
	"fmt"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/coordination"
)

func bind(t testing.TB, definition agent.Definition, dispatcher agent.Dispatcher) agent.Deployment {
	t.Helper()
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("coordination-test-artifact")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(t.Name() + "/" + definition.Descriptor().Name())),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func deadlineBinding(t testing.TB, dispatcher agent.Dispatcher) agent.Deployment {
	t.Helper()
	definition, err := coordination.NewDeadline(coordination.DeadlineConfig{
		Name: "test.deadline", Description: "Wait for the declared absolute instant.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return bind(t, definition, dispatcher)
}

func inputGate(t testing.TB) *coordination.InputGate {
	t.Helper()
	schema, err := agent.SchemaFor[string]()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := coordination.NewInputGate(coordination.InputGateConfig{
		Name: "test.input_gate", Description: "Wait for one identified answer.",
		RequestSchema: schema, AnswerSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func competition(t testing.TB, accept coordination.SuccessPredicate, maximum uint32) *coordination.FirstSuccess {
	t.Helper()
	definition, err := coordination.NewFirstSuccess(coordination.FirstSuccessConfig{
		Name: "test.first_success", Description: "Select the first acceptable observed result.",
		MaxCandidates: maximum, Accept: accept,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func candidate(t testing.TB, key string, deployment agent.Deployment, input agent.Input) agent.ChildSpec {
	t.Helper()
	childKey, err := agent.ParseChildKey(key)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := agent.NewBudget(agent.BudgetConfig{Steps: 16, Effects: 8, Signals: 16})
	if err != nil {
		t.Fatal(err)
	}
	return agent.ChildSpec{Key: childKey, DeploymentRef: deployment.DeploymentRef(), Input: input, Budget: budget}
}

func encodedInput[T any](t testing.TB, value T) agent.Input {
	t.Helper()
	input, err := agent.EncodeInput(value)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func inspect(t testing.TB, engine *agent.Engine, process *agent.Process) agent.ProcessInspection {
	t.Helper()
	tree, err := engine.InspectTree(context.Background(), process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	fact, present := tree.Process(process.ID())
	if !present {
		t.Fatal("process is missing from its tree")
	}
	return fact
}

func result(t testing.TB, process *agent.Process) agent.Result {
	t.Helper()
	value, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func completedOutput[T any](t testing.TB, process *agent.Process) T {
	t.Helper()
	value := result(t, process)
	output, present := value.Output()
	if value.Status() != agent.StatusCompleted || !present {
		t.Fatalf("process ended with %s: %+v", value.Status(), value.Termination())
	}
	decoded, err := output.Decode[T]()
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func closeEngine(t testing.TB, engine *agent.Engine) {
	t.Helper()
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type resolver map[agent.DeploymentRef]agent.Deployment

func (r resolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, present := r[reference]
	if !present {
		return agent.Deployment{}, fmt.Errorf("test deployment %s is unavailable", reference.Name())
	}
	return deployment, nil
}
