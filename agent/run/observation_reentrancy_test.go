package run

import (
	"context"
	startrand "crypto/rand"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestListenerCallingItsOwnTreeIsRefused(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(context.Context, *Runtime, *Handle[Payload]) error
	}{
		{"InspectTree", func(ctx context.Context, engine *Runtime, process *Handle[Payload]) error {
			_, err := engine.InspectTree(ctx, process.Relation().ProcessID())
			return err
		}},
		{"CaptureTree", func(ctx context.Context, engine *Runtime, process *Handle[Payload]) error {
			_, err := engine.CaptureTree(ctx, process.Relation().ProcessID())
			return err
		}},
		{"ReleaseTree", func(ctx context.Context, engine *Runtime, process *Handle[Payload]) error {
			return engine.ReleaseTree(ctx, process.Relation().ProcessID())
		}},
		{"Pause", func(ctx context.Context, _ *Runtime, process *Handle[Payload]) error {
			return process.Pause(ctx, "listener")
		}},
		{"Resume", func(ctx context.Context, _ *Runtime, process *Handle[Payload]) error { return process.Resume(ctx) }},
		{"Kill", func(ctx context.Context, _ *Runtime, process *Handle[Payload]) error {
			return process.Kill(ctx, "listener")
		}},
		{"Cancel", func(ctx context.Context, _ *Runtime, process *Handle[Payload]) error {
			return process.Cancel(ctx, "listener")
		}},
		{"Await", func(ctx context.Context, _ *Runtime, process *Handle[Payload]) error {
			_, err := process.Await(ctx)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				result := make(chan error, 1)
				_, process := startListenerProcess(t, func(ctx context.Context, engine *Runtime, process *Handle[Payload]) {
					ctx, cancel := context.WithTimeout(ctx, time.Second)
					defer cancel()
					result <- test.call(ctx, engine, process)
				})
				if err := <-result; !errors.Is(err, ErrListenerReentrancy) {
					t.Errorf("%s = %v, want ErrListenerReentrancy", test.name, err)
				}
				if outcome := mustAwait(t, process); outcome.Termination().Status() != StatusCompleted {
					t.Errorf("refused call changed the Process outcome: %s", outcome.Termination().Status())
				}
			})
		})
	}
}

func TestListenerCaptureIsRefusedBeforeWaitingForTreeOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, proceed := make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		engine, process := startListenerProcess(t, func(ctx context.Context, engine *Runtime, process *Handle[Payload]) {
			close(entered)
			<-proceed
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_, err := engine.CaptureTree(ctx, process.Relation().ProcessID())
			result <- err
		})
		<-entered
		outer := make(chan error, 1)
		go func() {
			_, err := engine.CaptureTree(t.Context(), process.Relation().ProcessID())
			outer <- err
		}()
		synctest.Wait()
		close(proceed)
		if err := <-result; !errors.Is(err, ErrListenerReentrancy) {
			t.Errorf("nested CaptureTree = %v, want ErrListenerReentrancy", err)
		}
		if err := <-outer; err != nil {
			t.Fatal(err)
		}
	})
}

func TestListenerContextIsUsableAfterCallbackReturns(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "return", true: "panic"}[panics], func(t *testing.T) {
			contexts := make(chan context.Context, 1)
			engine, process := startListenerProcess(t, func(ctx context.Context, _ *Runtime, _ *Handle[Payload]) {
				contexts <- ctx
				if panics {
					panic("listener failure")
				}
			})
			ctx := <-contexts
			mustAwait(t, process)
			if err := engine.FlushObservations(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := engine.InspectTree(ctx, process.Relation().ProcessID()); err != nil {
				t.Fatalf("InspectTree after callback returned = %v", err)
			}
			wantPanics := uint64(0)
			if panics {
				wantPanics = 1
			}
			if got := engine.ObservationFailures().EventListenerPanics(); got != wantPanics {
				t.Fatalf("listener panics = %d, want %d", got, wantPanics)
			}
		})
	}
}

func TestListenerMayInspectAnotherTree(t *testing.T) {
	var other atomic.Pointer[Handle[Payload]]
	result := make(chan error, 1)
	var engine *Runtime
	listener := EventListenerFunc(func(ctx context.Context, event Event) {
		target := other.Load()
		if event.Name() != EventProcessStarted || target == nil || event.Relation().ProcessID() == target.Relation().ProcessID() {
			return
		}
		_, err := engine.InspectTree(ctx, target.Relation().ProcessID())
		result <- err
	})
	var err error
	engine, err = New(t.Context(), Config{Store: NewMemoryStore(), EventListeners: []EventListener{listener}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, err := EncodePayload(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	target, err := engine.Start(t.Context(), deployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	mustAwait(t, target)
	other.Store(target)
	observed, err := engine.Start(t.Context(), deployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	mustAwait(t, observed)
	if err := <-result; err != nil {
		t.Fatalf("InspectTree for another owner = %v", err)
	}
}

func TestListenerMayInspectAnotherEngineWithTheSameRoot(t *testing.T) {
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), &engineTestDispatcher{policy: ReplayPolicyNever})
	snapshot := singleProcessTreeSnapshot(t, preparedEngineTestSnapshot(t))
	other, err := New(t.Context(), Config{Store: newSnapshotTestCommitter(snapshot)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, other) })
	first, err := other.Recover(t.Context(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	mustAwait(t, first)
	result := make(chan error, 1)
	observed, err := New(t.Context(), Config{Store: newSnapshotTestCommitter(snapshot), EventListeners: []EventListener{
		EventListenerFunc(func(ctx context.Context, event Event) {
			if event.Name() == EventProcessRestored {
				_, inspectErr := other.InspectTree(ctx, event.Relation().RootID())
				result <- inspectErr
			}
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, observed) })
	second, err := observed.Recover(t.Context(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	mustAwait(t, second)
	if err := <-result; err != nil {
		t.Fatalf("InspectTree for an independent restoration = %v", err)
	}
}

func TestNestedListenerRetainsTheActiveOuterTree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var outer *Runtime
		var rootID ProcessID
		result := make(chan error, 1)
		done := make(chan struct{})
		inner, err := New(t.Context(), Config{Store: NewMemoryStore(), EventListeners: []EventListener{
			EventListenerFunc(func(ctx context.Context, event Event) {
				if event.Name() != EventProcessStarted {
					return
				}
				ctx, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				_, err := outer.InspectTree(ctx, rootID)
				result <- err
				close(done)
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mustCloseEngine(t, inner) })
		input, err := EncodePayload(childTestInput{Mode: "leaf"})
		if err != nil {
			t.Fatal(err)
		}
		deployment := newChildTestDeployment(t)
		outer, err = New(t.Context(), Config{Store: NewMemoryStore(), EventListeners: []EventListener{
			EventListenerFunc(func(ctx context.Context, event Event) {
				if event.Name() == EventProcessStarted {
					rootID = event.Relation().ProcessID()
					if _, runErr := inner.Run(ctx, deployment, input); runErr != nil {
						t.Error(runErr)
					}
					<-done
				}
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mustCloseEngine(t, outer) })
		if _, err := outer.Run(t.Context(), deployment, input); err != nil {
			t.Fatal(err)
		}
		if err := <-result; !errors.Is(err, ErrListenerReentrancy) {
			t.Fatalf("nested callback into the outer tree = %v", err)
		}
	})
}

func startListenerProcess(t *testing.T, callback func(context.Context, *Runtime, *Handle[Payload])) (*Runtime, *Handle[Payload]) {
	t.Helper()
	var engine *Runtime
	listener := EventListenerFunc(func(ctx context.Context, event Event) {
		if event.Name() != EventProcessStarted {
			return
		}
		process, found := engine.Process(event.Relation().ProcessID())
		if !found {
			t.Error("published Process is absent")
			return
		}
		callback(ctx, engine, process)
	})
	var err error
	engine, err = New(t.Context(), Config{Store: NewMemoryStore(), EventListeners: []EventListener{listener}})
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodePayload(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(t.Context(), newChildTestDeployment(t), StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if _, err := process.Await(ctx); err != nil {
			t.Error(err)
		}
		mustCloseEngine(t, engine)
	})
	return engine, process
}
