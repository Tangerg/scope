package trajectory_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

func ExampleCapture() {
	ctx := context.Background()
	runtime, err := run.New(ctx, run.Config{Store: run.NewMemoryStore(), Build: "example"})
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
	handle, err := runtime.Start(ctx, program, run.StartRequest[int]{ID: run.StartID{Scope: "example", Key: "double"}, Input: 21})
	if err != nil {
		panic(err)
	}
	if _, awaitErr := handle.Await(ctx); awaitErr != nil {
		panic(awaitErr)
	}
	actual, err := trajectory.Capture(ctx, handle, &trajectory.Coverage{})
	if err != nil {
		panic(err)
	}
	output, err := run.EncodePayload(42)
	if err != nil {
		panic(err)
	}
	report, err := (trajectory.Evaluator{}).Evaluate(ctx, trajectory.Sample{Actual: actual, Expected: trajectory.Expectation{Status: run.StatusCompleted, Output: output}})
	if err != nil {
		panic(err)
	}
	fmt.Println(report.Verdict() == eval.VerdictPass, actual.HistoryComplete())
	// Output: true true
}
