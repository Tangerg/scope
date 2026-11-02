package trajectory_test

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestTreeUsageIncludesReapedChildren(t *testing.T) {
	runtime := newRuntime(t, run.NewMemoryStore())
	stage, err := workflow.Transform("identity", func(_ context.Context, value string) (string, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	child, err := workflow.Compile[string, string](runtime.Compiler(), workflow.Config{Name: "test.child", Description: "Return the input", Revision: "identity", Stages: []workflow.Stage{stage}})
	if err != nil {
		t.Fatal(err)
	}
	call, err := workflow.Call(workflow.CallConfig{ID: "child", Program: child.Erase()})
	if err != nil {
		t.Fatal(err)
	}
	program, err := workflow.Compile[string, string](runtime.Compiler(), workflow.Config{Name: "test.parent", Description: "Wait for a child", Stages: []workflow.Stage{call}})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "trajectory", Key: "reaped"}, Input: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	inspection, err := handle.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Processes) != 1 {
		t.Fatalf("child was not reaped: %d", len(inspection.Processes))
	}
	actual, err := trajectory.Capture(t.Context(), handle, &trajectory.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	log, err := actual.Log()
	if err != nil {
		t.Fatal(err)
	}
	var childSteps uint64
	for _, entry := range log.Entries {
		if step, ok := entry.Record.(run.Stepped); ok && step.Process != handle.ID() {
			childSteps++
		}
	}
	root := actual.RootUsage()
	total, err := actual.TreeUsage()
	if err != nil {
		t.Fatal(err)
	}
	if childSteps == 0 || total.CommittedSteps != root.CommittedSteps+childSteps {
		t.Fatalf("reap erased historical usage: root=%+v total=%+v child=%d", root, total, childSteps)
	}
}
