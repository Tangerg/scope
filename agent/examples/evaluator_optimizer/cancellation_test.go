package main

import (
	"context"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func TestCanceledExampleJoinsEveryOwnedChildBeforeClosing(t *testing.T) {
	root, err := newEvaluatorOptimizer([]float64{0.5, 0.95}, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	admittedChild := false
	engine, err := agent.NewEngine(agent.EngineConfig{
		TreeCommitter: agent.NewMemoryTreeCommitter(),
		ProcessAdmitter: agent.ProcessAdmitterFunc(func(_ context.Context, admission agent.ProcessAdmission) error {
			if _, child := admission.Relation().ParentID(); child {
				admittedChild = true
				cancel()
			}
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := engine.Close(context.WithoutCancel(ctx)); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	input, err := root.Descriptor().EncodeInput(optimizationRequest{Objective: "cancel child work"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(ctx, root, input)
	if err != nil || !admittedChild || result.Termination().Status() != agent.StatusCanceled {
		t.Fatalf("cancellation result = %s, child admission = %t, error = %v", result.Termination().Status(), admittedChild, err)
	}
}
