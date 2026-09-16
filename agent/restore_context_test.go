package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

type cancellationRestoreDefinition struct {
	Definition
	entered chan context.Context
}

func (c *cancellationRestoreDefinition) Restore(ctx context.Context, _ ExecutionState) (Execution, error) {
	c.entered <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestInitializationRestoreHonorsCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		definition := &cancellationRestoreDefinition{
			Definition: newEngineTestDefinition(t, "restore.cancel", "wait"),
			entered:    make(chan context.Context, 1),
		}
		engine, err := NewEngine(EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		defer mustCloseEngine(t, engine)
		type hostKey struct{}
		ctx, cancel := context.WithCancel(context.WithValue(t.Context(), hostKey{}, "unrecorded"))
		defer cancel()
		input, err := EncodeInput(engineTestInput{Value: "cancel"})
		if err != nil {
			t.Fatal(err)
		}
		deployment := engineTestDeployment(t, definition, nil)
		completed := make(chan error, 1)
		go func() {
			_, startErr := engine.Start(ctx, deployment, input)
			completed <- startErr
		}()
		var restoreCtx context.Context
		select {
		case restoreCtx = <-definition.entered:
		case err := <-completed:
			t.Fatalf("initialization finished before Restore: %v", err)
		}
		if restoreCtx.Value(hostKey{}) != nil {
			t.Error("Restore received unrecorded Host context values")
		}
		cancel()
		if err := <-completed; !errors.Is(err, context.Canceled) {
			t.Fatalf("initialization error = %v, want cancellation", err)
		}
		if len(engine.startReservations) != 0 || len(engine.processes) != 0 {
			t.Fatal("canceled restoration retained initialization state")
		}
	})
}
