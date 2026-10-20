package trajectory_test

import (
	"context"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/eval/trajectory"
)

// ExampleRecorder records one deterministic Agent run as a listener on the
// Engine, then takes the drained root subtree as a portable Trajectory.
func ExampleRecorder() {
	inputSchema, err := jsonschema.For[fixtureInput]()
	if err != nil {
		panic(err)
	}
	outputSchema, err := jsonschema.For[fixtureOutput]()
	if err != nil {
		panic(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "example.trajectory", Description: "Complete one deterministic trajectory.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		panic(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: fixtureDefinition{descriptor: descriptor}, Dispatcher: rejectingDispatcher{},
		ImplementationDigest: agent.ComputeDigest([]byte("example-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-configuration")),
	})
	if err != nil {
		panic(err)
	}

	recorder := &trajectory.Recorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeCommitter:  agent.NewMemoryTreeCommitter(),
		EventListeners: []agent.EventListener{recorder},
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = engine.Close(context.Background()) }()

	input, err := agent.EncodePayload(fixtureInput{Value: "done"})
	if err != nil {
		panic(err)
	}
	result, err := engine.Run(context.Background(), deployment, input)
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

	fmt.Println(recorded.Termination().Status() == agent.StatusCompleted)
	fmt.Println(len(recorded.Events()) > 0)
	// Output:
	// true
	// true
}
