package main

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"testing"

	agentrun "github.com/Tangerg/scope/agent/run"
)

func TestCanceledExampleJoinsEveryOwnedChildBeforeClosing(t *testing.T) {
	root, err := newWorkflowPatterns()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	admittedChild := false
	engine, err := agentrun.New(t.Context(), agentrun.Config{
		Store: agentrun.NewMemoryStore(),
		ProcessAdmitter: agentrun.ProcessAdmitterFunc(func(_ context.Context, admission agentrun.ProcessAdmission) error {
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
	input, err := root.Descriptor().EncodeInput(patternRequest{Text: "cancel child work", Urgent: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(ctx, engine, root, input)
	if err != nil || !admittedChild || result.Termination().Status() != agentrun.StatusCanceled {
		t.Fatalf("cancellation result = %s, child admission = %t, error = %v", result.Termination().Status(), admittedChild, err)
	}
}
