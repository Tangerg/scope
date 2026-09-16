package agent_test

import (
	"fmt"

	"github.com/Tangerg/scope/agent"
)

func ExampleNewDescriptor() {
	schema, err := agent.SchemaFor[string]()
	if err != nil {
		panic(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name:         "example.echo",
		Description:  "Returns the supplied text.",
		InputSchema:  schema,
		OutputSchema: schema,
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(descriptor.Name(), descriptor.Valid())
	// Output:
	// example.echo true
}

func ExampleEncodePayload() {
	type request struct {
		Topic string `json:"topic"`
	}

	input, err := agent.EncodePayload(request{Topic: "agent runtimes"})
	if err != nil {
		panic(err)
	}
	decoded, err := input.Decode[request]()
	if err != nil {
		panic(err)
	}

	fmt.Println(decoded.Topic, input.Valid())
	// Output:
	// agent runtimes true
}
