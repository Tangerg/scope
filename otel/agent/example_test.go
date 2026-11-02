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
	store, err := observer.WrapStore(run.NewMemoryStore())
	if err != nil {
		panic(err)
	}
	runtime, err := run.New(ctx, run.Config{Store: store, Build: "telemetry-example"})
	if err != nil {
		panic(err)
	}
	defer runtime.Close(ctx)
	stage, err := workflow.Transform("double", func(_ context.Context, value int) (int, error) { return 2 * value, nil })
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
