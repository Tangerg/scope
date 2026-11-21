package agent_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	agentotel "github.com/Tangerg/scope/otel/agent"
)

func ExampleObserver_Consume() {
	ctx := context.Background()
	observer, err := agentotel.NewObserver(agentotel.ObserverConfig{})
	if err != nil {
		panic(err)
	}
	defer observer.Close(ctx)
	runtime, err := run.New(ctx, run.Config{Store: run.NewMemoryStore(), Build: "telemetry-example"})
	if err != nil {
		panic(err)
	}
	defer runtime.Close(ctx)
	stage, err := workflow.Transform("double", func(value int) (int, error) { return 2 * value, nil })
	if err != nil {
		panic(err)
	}
	program, err := workflow.Compile[int, int](runtime.Compiler(), workflow.Config{Name: "example.double", Description: "Double an integer", Revision: "double", Stages: []workflow.Stage{stage}})
	if err != nil {
		panic(err)
	}
	handle, err := runtime.Start(ctx, program, run.StartRequest[int]{ID: run.StartID{Scope: "example", Key: "telemetry"}, Input: 21})
	if err != nil {
		panic(err)
	}
	if _, awaitErr := handle.Await(ctx); awaitErr != nil {
		panic(awaitErr)
	}
	cursor, err := observer.Consume(ctx, handle, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println(cursor > 0)
	// Output: true
}

func ExampleMiddleware() {
	ctx := context.Background()
	middleware, err := agentotel.NewMiddleware(agentotel.MiddlewareConfig{})
	if err != nil {
		panic(err)
	}
	// The Store and every dispatch of every Program the Runtime runs are
	// instrumented from the outside; Programs need no change.
	runtime, err := run.New(ctx, run.Config{
		Build: "telemetry-example", Store: middleware.Store(run.NewMemoryStore()),
		DispatchMiddlewares: []run.DispatchMiddleware{middleware.Dispatch},
	})
	if err != nil {
		panic(err)
	}
	defer runtime.Close(ctx)
	stage, err := workflow.Transform("double", func(value int) (int, error) { return 2 * value, nil })
	if err != nil {
		panic(err)
	}
	program, err := workflow.Compile[int, int](runtime.Compiler(), workflow.Config{Name: "example.double", Description: "Double an integer", Revision: "double", Stages: []workflow.Stage{stage}})
	if err != nil {
		panic(err)
	}
	output, err := runtime.Run(ctx, program, 21)
	if err != nil {
		panic(err)
	}
	fmt.Println(output)
	// Output: 42
}
