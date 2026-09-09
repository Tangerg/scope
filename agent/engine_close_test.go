package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestEngineCloseCancellationLeavesOwnedShutdownJoinable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		delivered := 0
		var engine *Engine
		var err error
		engine, err = NewEngine(EngineConfig{
			DeltaBufferCapacity: 2,
			DeltaListeners: []DeltaListener{DeltaListenerFunc(func(_ context.Context, delta Delta) {
				delivered++
				if delivered == 1 {
					close(entered)
					<-release
				}
				if _, found := engine.Process(delta.ProcessID()); !found {
					t.Error("shutdown removed a Process before observer delivery")
				}
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), &engineTestDispatcher{policy: ReplayPolicyNever, deltas: 2})
		input, err := EncodeInput(engineTestInput{Value: "stream"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := engine.Run(t.Context(), deployment, input)
		if err != nil || result.Status() != StatusCompleted {
			t.Fatalf("Run = %s, %v", result.Status(), err)
		}
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := engine.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("first Close = %v, want context deadline exceeded", err)
		}
		if _, err := engine.Start(t.Context(), deployment, input); !errors.Is(err, ErrEngineClosed) {
			t.Errorf("Start after canceled Close = %v, want ErrEngineClosed", err)
		}
		if err := engine.FlushDeltas(t.Context()); !errors.Is(err, ErrEngineClosed) {
			t.Errorf("FlushDeltas after canceled Close = %v, want ErrEngineClosed", err)
		}
		closed := make(chan error, 1)
		go func() { closed <- engine.Close(t.Context()) }()
		synctest.Wait()
		if len(closed) != 0 {
			t.Error("later Close abandoned accepted Delta delivery")
		}
		close(release)
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		if delivered != 2 {
			t.Errorf("delivered Deltas = %d, want 2", delivered)
		}
	})
}

func TestEngineCloseRejectsAlreadyCanceledContextBeforeClosingAdmission(t *testing.T) {
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if closeErr := engine.Close(ctx); !errors.Is(closeErr, context.Canceled) {
		t.Errorf("Close = %v, want context canceled", closeErr)
	}
	input, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Run(t.Context(), newChildTestDeployment(t), input); err != nil {
		t.Fatalf("canceled Close prevented a later Run: %v", err)
	}
}

func TestEngineFlushDeltasRejectsCanceledAndClosedCallsWithoutListeners(t *testing.T) {
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := engine.FlushDeltas(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled FlushDeltas = %v, want context canceled", err)
	}
	mustCloseEngine(t, engine)
	if err := engine.FlushDeltas(t.Context()); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("FlushDeltas after Close = %v, want ErrEngineClosed", err)
	}
}

func TestConcurrentEngineCloseWaitsForObserverCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		definition := newEngineTestDefinition(t, "engine.effect", "effect")
		deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
		listener := &blockingDeltaListener{entered: make(chan struct{}), release: make(chan struct{})}
		engine, err := NewEngine(EngineConfig{DeltaListeners: []DeltaListener{listener}})
		if err != nil {
			t.Fatal(err)
		}
		input, err := EncodeInput(engineTestInput{Value: "close"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := engine.Run(t.Context(), deployment, input)
		if err != nil || result.Status() != StatusCompleted {
			t.Fatalf("Run = %s, %v", result.Status(), err)
		}
		<-listener.entered
		closed := make(chan error, 2)
		go func() { closed <- engine.Close(context.WithoutCancel(t.Context())) }()
		synctest.Wait()
		go func() { closed <- engine.Close(context.WithoutCancel(t.Context())) }()
		synctest.Wait()
		if len(closed) != 0 {
			t.Error("Close returned while an accepted observer callback was still running")
		}
		close(listener.release)
		for range 2 {
			if closeErr := <-closed; closeErr != nil {
				t.Errorf("Close = %v", closeErr)
			}
		}
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Fatalf("repeated Close = %v", closeErr)
		}
	})
}

func TestEngineCloseRejectsIncompleteTerminalPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		engine, err := NewEngine(EngineConfig{EventListeners: []EventListener{
			EventListenerFunc(func(_ context.Context, event Event) {
				if event.Name() == EventProcessFinished {
					close(entered)
					<-release
				}
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		input, err := EncodeInput(childTestInput{Mode: "leaf"})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
		if err != nil {
			t.Fatal(err)
		}
		<-entered
		if err := engine.Close(context.WithoutCancel(t.Context())); !errors.Is(err, ErrEngineHasActiveProcesses) {
			t.Errorf("Close during terminal publication = %v, want ErrEngineHasActiveProcesses", err)
		}
		if registered, found := engine.Process(process.ID()); !found || registered.ID() != process.ID() {
			t.Error("terminal Process disappeared during Close")
		}
		close(release)
		if result, awaitErr := process.Await(t.Context()); awaitErr != nil || result.Status() != StatusCompleted {
			t.Fatalf("Await = %s, %v", result.Status(), awaitErr)
		}
		if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTerminalListenerCannotCloseItsOwnEngine(t *testing.T) {
	for _, durable := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var engine *Engine
			result := make(chan error, 1)
			config := EngineConfig{EventListeners: []EventListener{
				EventListenerFunc(func(_ context.Context, event Event) {
					if event.Name() == EventProcessFinished {
						result <- engine.Close(context.WithoutCancel(t.Context()))
					}
				}),
			}}
			if durable {
				config.TreeDurability = &recordingTreeDurability{}
			}
			var err error
			engine, err = NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			input, err := EncodeInput(childTestInput{Mode: "leaf"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Run(t.Context(), newChildTestDeployment(t), input); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, ErrEngineHasActiveProcesses) {
				t.Fatalf("listener Close with durable=%t: %v, want ErrEngineHasActiveProcesses", durable, err)
			}
			mustCloseEngine(t, engine)
		})
	}
}
