package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

func TestRunWaitsForDescendantCleanup(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			name := "ephemeral/completed"
			if durable {
				name = "durable/completed"
			}
			if canceled {
				name += "/canceled"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					dispatcher := newBlockingChildDispatcher("cleanup")
					defer dispatcher.ReleaseAll()
					config := EngineConfig{}
					if durable {
						config.TreeDurability = &recordingTreeDurability{}
					}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					engine, root, returned := startRunScope(t, ctx, config, dispatcher)
					<-dispatcher.started
					waitForStatus(t, root, StatusPaused)
					want := StatusCompleted
					if canceled {
						want = StatusCanceled
						cancel()
					} else if err := root.Resume(t.Context()); err != nil {
						t.Fatal(err)
					}
					if result := mustAwait(t, root); result.Status() != want {
						t.Fatalf("root status = %s, want %s", result.Status(), want)
					}
					synctest.Wait()
					select {
					case result := <-returned:
						t.Fatalf("Run abandoned descendant cleanup: %+v", result)
					default:
					}
					dispatcher.ReleaseAll()
					result := <-returned
					if result.err != nil || result.result.Status() != want {
						t.Fatalf("Run = %+v, want status %s", result, want)
					}
					mustCloseEngine(t, engine)
				})
			})
		}
	}
}

func TestRunWaitsForDescendantAcknowledgmentAndReportsItsFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dispatcher := newBlockingChildDispatcher("cleanup")
		defer dispatcher.ReleaseAll()
		failure := errors.New("descendant checkpoint acknowledgment failed")
		durability := &blockingCancellationCheckpointDurability{
			recordingTreeDurability: &recordingTreeDurability{},
			entered:                 make(chan struct{}), release: make(chan struct{}), err: failure,
		}
		release := sync.OnceFunc(func() { close(durability.release) })
		defer release()
		engine, root, returned := startRunScope(t, t.Context(), EngineConfig{TreeDurability: durability}, dispatcher)
		<-dispatcher.started
		waitForStatus(t, root, StatusPaused)
		if err := root.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		acknowledged := mustAwait(t, root)
		if acknowledged.Status() != StatusCompleted {
			t.Fatalf("root status = %s", acknowledged.Status())
		}
		dispatcher.ReleaseAll()
		<-durability.entered
		synctest.Wait()
		select {
		case result := <-returned:
			t.Fatalf("Run preceded descendant acknowledgment: %+v", result)
		default:
		}
		release()
		result := <-returned
		runtimeErr, ok := errors.AsType[*RuntimeError](result.err)
		if !ok || !errors.Is(result.err, failure) || runtimeErr.ProcessID() != root.ID() || result.result.Valid() {
			t.Fatalf("Run discarded descendant failure or reported a result: %+v", result)
		}
		if retained := mustAwait(t, root); retained.Status() != acknowledged.Status() || retained.FinishedAt() != acknowledged.FinishedAt() {
			t.Fatal("Run failure changed the acknowledged root result")
		}
		mustCloseEngine(t, engine)
	})
}

type runCompletion struct {
	result Result
	err    error
}

func startRunScope(
	t *testing.T,
	ctx context.Context,
	config EngineConfig,
	dispatcher Dispatcher,
) (*Engine, *Process, <-chan runCompletion) {
	t.Helper()
	started := make(chan ProcessID, 1)
	config.EventListeners = append(config.EventListeners, EventListenerFunc(func(_ context.Context, event Event) {
		if _, hasParent := event.Relation().ParentID(); !hasParent && event.Name() == EventProcessStarted {
			started <- event.ProcessID()
		}
	}))
	engine, err := NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	deployment := newScopeJoinDeployment(t, ChildWaitBoundaryDrained, dispatcher)
	input, err := EncodeInput("scope")
	if err != nil {
		t.Fatal(err)
	}
	returned := make(chan runCompletion, 1)
	go func() {
		result, err := engine.Run(ctx, deployment, input)
		returned <- runCompletion{result: result, err: err}
	}()
	root, found := engine.Process(<-started)
	if !found {
		t.Fatal("Run did not publish its root Process")
	}
	return engine, root, returned
}
