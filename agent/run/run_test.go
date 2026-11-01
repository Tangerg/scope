package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

func TestRunWaitsForDescendantCleanup(t *testing.T) {
	for _, recording := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			name := "memory/completed"
			if recording {
				name = "recording/completed"
			}
			if canceled {
				name += "/canceled"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					dispatcher := newBlockingChildDispatcher("cleanup")
					defer dispatcher.ReleaseAll()
					config := Config{Store: NewMemoryStore()}
					if recording {
						config.Store = &recordingTreeCommitter{}
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
					if result := mustAwait(t, root); result.Termination().Status() != want {
						t.Fatalf("root status = %s, want %s", result.Termination().Status(), want)
					}
					synctest.Wait()
					select {
					case result := <-returned:
						t.Fatalf("Run abandoned descendant cleanup: %+v", result)
					default:
					}
					dispatcher.ReleaseAll()
					result := <-returned
					ended, logical := errors.AsType[*TerminationError](result.err)
					if (canceled && (!logical || ended.Termination.Status() != want || result.result.Valid())) || (!canceled && (result.err != nil || !result.result.Valid())) {
						t.Fatalf("Run = %+v, want status %s", result, want)
					}
					if _, found := engine.Process(root.ID()); found {
						t.Fatal("Run retained local registration after joining")
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
		committer := &blockingCancellationCheckpointDurability{
			recordingTreeCommitter: &recordingTreeCommitter{},
			entered:                make(chan struct{}), release: make(chan struct{}), err: failure,
		}
		release := sync.OnceFunc(func() { close(committer.release) })
		defer release()
		engine, root, returned := startRunScope(t, t.Context(), Config{Store: committer}, dispatcher)
		<-dispatcher.started
		waitForStatus(t, root, StatusPaused)
		if err := root.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		acknowledged := mustAwait(t, root)
		if acknowledged.Termination().Status() != StatusCompleted {
			t.Fatalf("root status = %s", acknowledged.Termination().Status())
		}
		dispatcher.ReleaseAll()
		<-committer.entered
		synctest.Wait()
		select {
		case result := <-returned:
			t.Fatalf("Run preceded descendant acknowledgment: %+v", result)
		default:
		}
		release()
		result := <-returned
		runtimeErr, ok := errors.AsType[*RuntimeError](result.err)
		if !ok || !errors.Is(result.err, failure) || runtimeErr.ProcessID() != root.Relation().ProcessID() || result.result.Valid() {
			t.Fatalf("Run discarded descendant failure or reported a result: %+v", result)
		}
		if retained := mustAwait(t, root); retained.Termination().Status() != acknowledged.Termination().Status() || retained.FinishedAt() != acknowledged.FinishedAt() {
			t.Fatal("Run failure changed the acknowledged root result")
		}
		mustCloseEngine(t, engine)
	})
}

type runCompletion struct {
	result Payload
	err    error
}

func startRunScope(
	t *testing.T,
	ctx context.Context,
	config Config,
	dispatcher Dispatcher,
) (*Runtime, *Handle[Payload], <-chan runCompletion) {
	t.Helper()
	started := make(chan ProcessID, 1)
	config.EventListeners = append(config.EventListeners, EventListenerFunc(func(_ context.Context, event Event) {
		if _, hasParent := event.Relation().ParentID(); !hasParent && event.Name() == EventProcessStarted {
			started <- event.Relation().ProcessID()
		}
	}))
	engine, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	deployment := newScopeJoinDeployment(t, ChildWaitBoundaryDrained, dispatcher)
	input, err := EncodePayload("scope")
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
