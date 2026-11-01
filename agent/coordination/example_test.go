package coordination_test

import (
	startrand "crypto/rand"

	"context"
	"fmt"
	"time"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/jsonschema"
)

func ExampleFirstSuccess() {
	textSchema, err := jsonschema.For[string]()
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
		Accept: func(_ context.Context, _ run.ChildKey, _ run.ChildOutcome) (bool, error) { return true, nil },
	})
	if err != nil {
		panic(err)
	}
	workerDeployment := exampleBinding(worker, nil)
	gateDeployment := exampleBinding(gate, nil)
	deadlineDeployment := exampleBinding(deadline, coordination.DeadlineDispatcher{})
	rootDeployment := exampleBinding(race, nil)
	engine, err := run.New(context.Background(), run.Config{Store: run.NewMemoryStore(), ProgramResolver: resolver{
		workerDeployment.ProgramRef():   workerDeployment,
		gateDeployment.ProgramRef():     gateDeployment,
		deadlineDeployment.ProgramRef(): deadlineDeployment,
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
	requests := []run.ChildSpec{
		exampleCandidate("worker", workerDeployment, "inspect deployment"),
		exampleCandidate("input", gateDeployment, "replacement instruction"),
		exampleCandidate("deadline", deadlineDeployment, time.Now().Add(time.Hour)),
	}
	input, err := run.EncodePayload(requests)
	if err != nil {
		panic(err)
	}
	process, err := engine.Start(ctx, rootDeployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
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
	output, present := result.Termination().Output()
	if !present {
		panic("competition ended without an output")
	}
	report, err := output.Decode[coordination.FirstSuccessResult]()
	if err != nil || !report.Valid() || report.Winner == nil {
		panic("competition produced no successful candidate")
	}
	// Winner indexes the request, and Candidates answers it in the same order.
	fmt.Println("winner:", requests[*report.Winner].Key)
	value, _ := report.Candidates[*report.Winner].Outcome.Result().Termination().Output()
	answer, decodeErr := value.Decode[string]()
	if decodeErr != nil {
		panic(decodeErr)
	}
	fmt.Println(answer)
	// Output:
	// winner: worker
	// worker: inspect deployment
}

func exampleBinding(definition run.Definition, dispatcher run.Dispatcher) run.Program[run.Payload, run.Payload] {
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("coordination-example-artifact")),
		ConfigurationDigest:  run.ComputeDigest([]byte("coordination-example-config/" + definition.Descriptor().Name())),
	})
	if err != nil {
		panic(err)
	}
	return deployment
}

func exampleCandidate[T any](key string, deployment run.Program[run.Payload, run.Payload], value T) run.ChildSpec {
	childKey, err := run.ParseChildKey(key)
	if err != nil {
		panic(err)
	}
	input, err := run.EncodePayload(value)
	if err != nil {
		panic(err)
	}
	budget := run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(8), Signals: run.NewQuota(16)}
	return run.ChildSpec{Key: childKey, ProgramRef: deployment.ProgramRef(), Input: input, Budget: budget}
}
