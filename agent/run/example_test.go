package run_test

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func ExampleNewDescriptor() {
	schema, err := jsonschema.For[string]()
	if err != nil {
		panic(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
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

	input, err := run.EncodePayload(request{Topic: "agent runtimes"})
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

func ExampleSignal_Frame() {
	id, err := run.ParseSignalID("host:instruction")
	if err != nil {
		panic(err)
	}
	// A sender cannot turn its input into a framework completion by choosing its JSON.
	signal, err := run.NewSignal(id, run.WaitID{}, []byte(`{"operation":"child_control_settled"}`))
	if err != nil {
		panic(err)
	}
	frame, err := signal.Frame[run.SignalFrame]()
	if err != nil {
		panic(err)
	}
	switch input := frame.(type) {
	case run.ExternalInput:
		fmt.Println(string(input.Payload()))
	case run.ChildControlResult:
		fmt.Println("control completed")
	}
	// Output: {"operation":"child_control_settled"}
}
