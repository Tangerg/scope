package main

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"testing"

	agentrun "github.com/Tangerg/scope/agent/run"
)

func TestCanceledExampleJoinsEveryOwnedChildBeforeClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	admittedChild := false
	runtime, err := agentrun.New(t.Context(), agentrun.Config{Build: "example-cancellation",
		Store: agentrun.NewMemoryStore(),
		ProcessAdmitter: agentrun.ProcessAdmitterFunc(func(_ context.Context, admission agentrun.ProcessAdmission) error {
			if _, child := admission.ProcessID().Parent(); child {
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
		if closeErr := runtime.Close(context.WithoutCancel(ctx)); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	root, err := newWorkflowPatterns(runtime.Compiler())
	if err != nil {
		t.Fatal(err)
	}
	input, err := root.Descriptor().EncodeInput(patternRequest{Text: "cancel child work", Urgent: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(ctx, runtime, root, input)
	if err != nil || !admittedChild || result.Termination().Status() != agentrun.StatusCanceled {
		t.Fatalf("cancellation result = %s, child admission = %t, error = %v", result.Termination().Status(), admittedChild, err)
	}
}
