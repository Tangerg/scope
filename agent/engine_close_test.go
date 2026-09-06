package agent

import (
	"context"
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

func TestEngineCloseAllowsRegistryReadsDuringTerminalPublication(t *testing.T) {
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
		closed := make(chan error, 1)
		go func() { closed <- engine.Close() }()
		synctest.Wait()
		// A terminal listener may inspect the registry before it returns.
		// Close cannot hold that registry while waiting for the publication.
		if registered, found := engine.Process(process.ID()); !found || registered.ID() != process.ID() {
			t.Error("terminal Process disappeared during Close")
		}
		if len(closed) != 0 {
			t.Error("Close returned before terminal publication completed")
		}
		close(release)
		if closeErr := <-closed; closeErr != nil {
			t.Fatalf("Close = %v", closeErr)
		}
		if result, awaitErr := process.Await(t.Context()); awaitErr != nil || result.Status() != StatusCompleted {
			t.Fatalf("Await = %s, %v", result.Status(), awaitErr)
		}
	})
}
