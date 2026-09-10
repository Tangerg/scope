package coordination_test

import (
	"context"
	"fmt"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/coordination"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

func ExampleFirstSuccess() {
	textSchema, err := agent.SchemaFor[string]()
	if err != nil {
		panic(err)
	}
	gate, err := coordination.NewInputGate(coordination.InputGateConfig{
		Name: "example.input", Description: "Receive one identified replacement instruction.",
		RequestSchema: textSchema, AnswerSchema: textSchema,
	})
	if err != nil {
		panic(err)
	}
	deadline, err := coordination.NewDeadline(coordination.DeadlineConfig{
		Name: "example.deadline", Description: "Wait for the coordination deadline.",
	})
	if err != nil {
		panic(err)
	}
	transform, err := workflow.Transform("answer", func(_ context.Context, request string) (string, error) {
		return "worker: " + request, nil
	})
	if err != nil {
		panic(err)
	}
	worker, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "example.worker", Description: "Prepare one deterministic answer.", Stages: []workflow.Stage{transform},
	})
	if err != nil {
		panic(err)
	}
	race, err := coordination.NewFirstSuccess(coordination.FirstSuccessConfig{
		Name: "example.coordination", Description: "Select the first completed work, input, or deadline.", MaxCandidates: 3,
		Accept: func(_ context.Context, _ agent.ChildOutcome) (bool, error) { return true, nil },
	})
	if err != nil {
		panic(err)
	}
	workerDeployment := exampleBinding(worker, nil)
	gateDeployment := exampleBinding(gate, nil)
	deadlineDeployment := exampleBinding(deadline, coordination.Timer{})
	rootDeployment := exampleBinding(race, nil)
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver{
		workerDeployment.DeploymentRef():   workerDeployment,
		gateDeployment.DeploymentRef():     gateDeployment,
		deadlineDeployment.DeploymentRef(): deadlineDeployment,
	}})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	defer func() {
		if closeErr := engine.Close(ctx); closeErr != nil {
			panic(closeErr)
		}
	}()
	requests := []agent.ChildSpec{
		exampleCandidate("worker", workerDeployment, "inspect deployment"),
		exampleCandidate("input", gateDeployment, "replacement instruction"),
		exampleCandidate("deadline", deadlineDeployment, time.Now().Add(time.Hour)),
	}
	input, err := agent.EncodeInput(requests)
	if err != nil {
		panic(err)
	}
	process, err := engine.Start(ctx, rootDeployment, input)
	if err != nil {
		panic(err)
	}
	result, err := process.Await(ctx)
	if err != nil {
		panic(err)
	}
	if joinErr := process.Join(ctx); joinErr != nil {
		panic(joinErr)
	}
	output, present := result.Output()
	if !present {
		panic("competition ended without an output")
	}
	report, err := output.Decode[coordination.FirstSuccessResult]()
	if err != nil || !report.Valid() || report.Winner == nil {
		panic("competition produced no successful candidate")
	}
	fmt.Println("winner:", report.Winner)
	for _, outcome := range report.Outcomes {
		if outcome.Key() == *report.Winner {
			value, _ := outcome.Result().Output()
			answer, decodeErr := value.Decode[string]()
			if decodeErr != nil {
				panic(decodeErr)
			}
			fmt.Println(answer)
		}
	}
	// Output:
	// winner: worker
	// worker: inspect deployment
}

func exampleBinding(definition agent.Definition, dispatcher agent.Dispatcher) agent.Deployment {
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("coordination-example-artifact")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("coordination-example-config/" + definition.Descriptor().Name())),
	})
	if err != nil {
		panic(err)
	}
	return deployment
}

func exampleCandidate[T any](key string, deployment agent.Deployment, value T) agent.ChildSpec {
	childKey, err := agent.ParseChildKey(key)
	if err != nil {
		panic(err)
	}
	input, err := agent.EncodeInput(value)
	if err != nil {
		panic(err)
	}
	budget := agent.Budget{Steps: 16, Effects: 8, Signals: 16}
	return agent.ChildSpec{Key: childKey, DeploymentRef: deployment.DeploymentRef(), Input: input, Budget: budget}
}
