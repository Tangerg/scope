package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

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
		go func() { closed <- engine.Close() }()
		synctest.Wait()
		go func() { closed <- engine.Close() }()
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
		if closeErr := engine.Close(); closeErr != nil {
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
		if err := engine.Close(); !errors.Is(err, ErrEngineHasActiveProcesses) {
			t.Errorf("Close during terminal publication = %v, want ErrEngineHasActiveProcesses", err)
		}
		if registered, found := engine.Process(process.ID()); !found || registered.ID() != process.ID() {
			t.Error("terminal Process disappeared during Close")
		}
		close(release)
		if result, awaitErr := process.Await(t.Context()); awaitErr != nil || result.Status() != StatusCompleted {
			t.Fatalf("Await = %s, %v", result.Status(), awaitErr)
		}
		if err := engine.Close(); err != nil {
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
						result <- engine.Close()
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
