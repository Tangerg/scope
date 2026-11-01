package trajectory_test

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/eval/trajectory"
)

// ExampleRecorder records one deterministic Agent run as a listener on the
// Runtime, then takes the drained root subtree as a portable Trajectory.
func ExampleRecorder() {
	inputSchema, err := jsonschema.For[fixtureInput]()
	if err != nil {
		panic(err)
	}
	outputSchema, err := jsonschema.For[fixtureOutput]()
	if err != nil {
		panic(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: "example.trajectory", Description: "Complete one deterministic trajectory.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		panic(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: fixtureDefinition{descriptor: descriptor}, Dispatcher: rejectingDispatcher{},
		ImplementationDigest: run.ComputeDigest([]byte("example-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte("example-configuration")),
	})
	if err != nil {
		panic(err)
	}

	recorder := &trajectory.Recorder{}
	engine, err := run.New(context.Background(), run.Config{
		Store:          run.NewMemoryStore(),
		EventListeners: []run.EventListener{recorder},
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = engine.Close(context.Background()) }()

	input, err := run.EncodePayload(fixtureInput{Value: "done"})
	if err != nil {
		panic(err)
	}
	result, err := runtest.Observe(context.Background(), engine, deployment, input)
	if err != nil {
		panic(err)
	}
	process, ok := engine.Process(result.ProcessID())
	if !ok {
		panic("root process is missing")
	}
	recorded, err := recorder.Take(context.Background(), process, &trajectory.Coverage{})
	if err != nil {
		panic(err)
	}

	fmt.Println(recorded.Termination().Status() == run.StatusCompleted)
	fmt.Println(len(recorded.Events()) > 0)
	// Output:
	// true
	// true
}
