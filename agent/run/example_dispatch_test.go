package run_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func ExampleDispatchMiddleware() {
	ctx := context.Background()
	// A Runtime-wide middleware sees every dispatcher Effect of every Program.
	logDispatch := func(next run.Dispatcher) run.Dispatcher {
		return run.DispatcherFunc(func(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
			fmt.Println("dispatching", request.Effect().Payload().JSON(), "under", request.Effect().Policy().Replay)
			return next.Dispatch(ctx, request, emit)
		})
	}
	runtime := exampleValue(run.New(ctx, run.Config{
		Build: "example", Store: run.NewMemoryStore(),
		DispatchMiddlewares: []run.DispatchMiddleware{logDispatch},
	}))
	defer func() {
		if err := runtime.Close(ctx); err != nil {
			panic(err)
		}
	}()
	schema := exampleValue(jsonschema.For[string]())
	descriptor := exampleValue(run.NewDescriptor(run.DescriptorConfig{
		Name: "example.shout", Description: "Upper-case the input through an external call.", InputSchema: schema, OutputSchema: schema,
	}))
	shout := run.DispatcherFunc(func(_ context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
		text, err := request.Effect().Payload().Decode[string]()
		if err != nil {
			return run.Settlement{}, err
		}
		return run.NewSettlement(run.SettlementStatusSucceeded, exampleValue(run.EncodePayload(strings.ToUpper(text))))
	})
	program := exampleValue(run.Bind[string, string](runtime.Compiler(), run.BindingConfig{
		Definition: callDefinition{descriptor: descriptor}, Dispatcher: shout,
	}))
	fmt.Println(exampleValue(runtime.Run(ctx, program, "hello")))
	// Output:
	// dispatching "hello" under never
	// HELLO
}

const callStateKind = "example.call"

// callDefinition declares its input as one external call and completes with
// the call's result.
type callDefinition struct{ descriptor run.Descriptor }

func (c callDefinition) Descriptor() run.Descriptor                           { return c.descriptor }
func (callDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (callDefinition) Start(input run.Payload) (run.Execution, error) {
	return &callExecution{state: callState{Input: input}}, nil
}

func (callDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[callState](callStateKind)
	if err != nil {
		return nil, err
	}
	return &callExecution{state: value}, nil
}

type callState struct {
	Input  run.Payload `json:"input"`
	Called bool        `json:"called"`
}

type callExecution struct{ state callState }

func (c *callExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(callStateKind, c.state)
}

func (c *callExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	if !c.state.Called {
		// The call is not idempotent, so recovery must not repeat it.
		effect, err := run.NewDispatcherEffect(c.state.Input, run.EffectPolicy{Replay: run.ReplayPolicyNever})
		if err != nil {
			return run.Transition{}, err
		}
		c.state.Called = true
		return run.Yield(0, []run.Effect{effect})
	}
	if len(turn.Messages) != 1 {
		return run.Transition{}, run.ErrInvalidMessage
	}
	completion, ok := turn.Messages[0].(run.Completion)
	if !ok {
		return run.Transition{}, run.ErrInvalidMessage
	}
	return run.Complete(1, completion.Outcome.Payload())
}
