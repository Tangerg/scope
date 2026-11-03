package main

import (
	"context"
	"testing"

	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

func TestCanceledExampleJoinsPendingChildAdmissionBeforeClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	joined := make(chan struct{})
	runtime, err := agentrun.New(t.Context(), agentrun.Config{Build: "example-cancellation",
		Store: agentrun.NewMemoryStore(),
		ProcessAdmitter: agentrun.ProcessAdmitterFunc(func(admissionContext context.Context, admission agentrun.ProcessAdmission) error {
			if _, child := admission.ProcessID().Parent(); child {
				defer close(joined)
				cancel()
				// Cancellation must reach the owned admission before it can finish.
				<-admissionContext.Done()
				return admissionContext.Err()
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
	root, err := newManagedWorkflow(runtime.Compiler())
	if err != nil {
		t.Fatal(err)
	}
	input, err := root.Descriptor().EncodeInput(request{Text: "cancel child work"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(ctx, runtime, root.Erase(), input)
	if err != nil || result.Termination().Status() != agentrun.StatusCanceled {
		t.Fatalf("cancellation result = %s, error = %v", result.Termination().Status(), err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("cancellation returned before child admission joined")
	}
}
