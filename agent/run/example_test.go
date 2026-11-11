package run_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func ExampleBind() {
	ctx := context.Background()
	runtime := exampleValue(run.New(ctx, run.Config{Build: "example", Store: run.NewMemoryStore()}))
	defer func() {
		if err := runtime.Close(ctx); err != nil {
			panic(err)
		}
	}()
	schema := exampleValue(jsonschema.For[string]())
	descriptor := exampleValue(run.NewDescriptor(run.DescriptorConfig{
		Name: "example.echo", Description: "Return the admitted input.", InputSchema: schema, OutputSchema: schema,
	}))
	program := exampleValue(run.Bind[string, string](runtime.Compiler(), run.BindingConfig{
		Definition: echoDefinition{descriptor: descriptor},
	}))
	output := exampleValue(runtime.Run(ctx, program, "hello"))
	fmt.Println(output)
	// Output: hello
}

func exampleValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

const echoStateKind = "example.echo"

type echoDefinition struct{ descriptor run.Descriptor }

func (e echoDefinition) Descriptor() run.Descriptor                           { return e.descriptor }
func (echoDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (echoDefinition) Start(input run.Payload) (run.Execution, error) {
	return echoExecution{input: input}, nil
}

func (echoDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	input, err := state.Decode[string](echoStateKind)
	if err != nil {
		return nil, err
	}
	payload, err := run.EncodePayload(input)
	if err != nil {
		return nil, err
	}
	return echoExecution{input: payload}, nil
}

type echoExecution struct{ input run.Payload }

func (e echoExecution) Snapshot() (run.ExecutionState, error) {
	return run.NewExecutionState(echoStateKind, e.input)
}

func (e echoExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	return run.Complete(uint32(len(turn.Messages)), e.input)
}
