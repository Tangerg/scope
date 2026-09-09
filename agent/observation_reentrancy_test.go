package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestListenerCallingItsOwnTreeIsRefused(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(context.Context, *Engine, *Process) error
	}{
		{"InspectTree", func(ctx context.Context, engine *Engine, process *Process) error {
			_, err := engine.InspectTree(ctx, process.ID())
			return err
		}},
		{"CaptureTree", func(ctx context.Context, engine *Engine, process *Process) error {
			_, err := engine.CaptureTree(ctx, process.ID())
			return err
		}},
		{"ReleaseTree", func(ctx context.Context, engine *Engine, process *Process) error {
			return engine.ReleaseTree(ctx, process.ID())
		}},
		{"Pause", func(ctx context.Context, _ *Engine, process *Process) error { return process.Pause(ctx, "listener") }},
		{"Resume", func(ctx context.Context, _ *Engine, process *Process) error { return process.Resume(ctx) }},
		{"Kill", func(ctx context.Context, _ *Engine, process *Process) error { return process.Kill(ctx, "listener") }},
		{"RequestCancellation", func(ctx context.Context, _ *Engine, process *Process) error {
			return process.RequestCancellation(ctx, "listener")
		}},
		{"Await", func(ctx context.Context, _ *Engine, process *Process) error {
			_, err := process.Await(ctx)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				result := make(chan error, 1)
				_, process := startListenerProcess(t, func(ctx context.Context, engine *Engine, process *Process) {
					ctx, cancel := context.WithTimeout(ctx, time.Second)
					defer cancel()
					result <- test.call(ctx, engine, process)
				})
				if err := <-result; !errors.Is(err, ErrListenerReentrancy) {
					t.Errorf("%s = %v, want ErrListenerReentrancy", test.name, err)
				}
				if outcome := mustAwait(t, process); outcome.Status() != StatusCompleted {
					t.Errorf("refused call changed the Process outcome: %s", outcome.Status())
				}
			})
		})
	}
}

func TestListenerCaptureIsRefusedBeforeWaitingForTreeOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, proceed := make(chan struct{}), make(chan struct{})
		result := make(chan error, 1)
		engine, process := startListenerProcess(t, func(ctx context.Context, engine *Engine, process *Process) {
			close(entered)
			<-proceed
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_, err := engine.CaptureTree(ctx, process.ID())
			result <- err
		})
		<-entered
		outer := make(chan error, 1)
		go func() {
			_, err := engine.CaptureTree(t.Context(), process.ID())
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
			engine, process := startListenerProcess(t, func(ctx context.Context, _ *Engine, _ *Process) {
				contexts <- ctx
				if panics {
					panic("listener failure")
				}
			})
			ctx := <-contexts
			mustAwait(t, process)
			if _, err := engine.InspectTree(ctx, process.ID()); err != nil {
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
	var other atomic.Pointer[Process]
	result := make(chan error, 1)
	var engine *Engine
	listener := EventListenerFunc(func(ctx context.Context, event Event) {
		target := other.Load()
		if event.Name() != EventProcessStarted || target == nil || event.ProcessID() == target.ID() {
			return
		}
		_, err := engine.InspectTree(ctx, target.ID())
		result <- err
	})
	var err error
	engine, err = NewEngine(EngineConfig{EventListeners: []EventListener{listener}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	target, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	mustAwait(t, target)
	other.Store(target)
	observed, err := engine.Start(t.Context(), deployment, input)
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
	other, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, other) })
	first, err := other.RestoreTree(t.Context(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	mustAwait(t, first)
	result := make(chan error, 1)
	observed, err := NewEngine(EngineConfig{EventListeners: []EventListener{
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
	second, err := observed.RestoreTree(t.Context(), deployment, snapshot)
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
		var outer *Engine
		var rootID ProcessID
		result := make(chan error, 1)
		inner, err := NewEngine(EngineConfig{EventListeners: []EventListener{
			EventListenerFunc(func(ctx context.Context, event Event) {
				if event.Name() != EventProcessStarted {
					return
				}
				ctx, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				_, err := outer.InspectTree(ctx, rootID)
				result <- err
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mustCloseEngine(t, inner) })
		input, err := EncodeInput(childTestInput{Mode: "leaf"})
		if err != nil {
			t.Fatal(err)
		}
		deployment := newChildTestDeployment(t)
		outer, err = NewEngine(EngineConfig{EventListeners: []EventListener{
			EventListenerFunc(func(ctx context.Context, event Event) {
				if event.Name() == EventProcessStarted {
					rootID = event.ProcessID()
					if _, runErr := inner.Run(ctx, deployment, input); runErr != nil {
						t.Error(runErr)
					}
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

func startListenerProcess(t *testing.T, callback func(context.Context, *Engine, *Process)) (*Engine, *Process) {
	t.Helper()
	var engine *Engine
	listener := EventListenerFunc(func(ctx context.Context, event Event) {
		if event.Name() != EventProcessStarted {
			return
		}
		process, found := engine.Process(event.ProcessID())
		if !found {
			t.Error("published Process is absent")
			return
		}
		callback(ctx, engine, process)
	})
	var err error
	engine, err = NewEngine(EngineConfig{EventListeners: []EventListener{listener}})
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
