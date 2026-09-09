package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// reentrantEventListener calls back into the tree it is observing, forwarding
// the context it was handed, which is what a listener written without reading
// the contract does.
type reentrantEventListener struct {
	mu       sync.Mutex
	engine   *Engine
	process  *Process
	rootID   ProcessID
	attempts int
	errs     []error
	done     chan struct{}
	closed   bool
}

func (r *reentrantEventListener) OnEvent(ctx context.Context, _ Event) {
	r.mu.Lock()
	engine, process, rootID := r.engine, r.process, r.rootID
	r.mu.Unlock()
	if engine == nil || process == nil {
		return
	}

	_, inspectErr := engine.InspectTree(ctx, rootID)
	_, captureErr := engine.CaptureTree(ctx, rootID)
	releaseErr := engine.ReleaseTree(ctx, rootID)
	controlErr := process.Pause(ctx, "listener")

	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	r.errs = append(r.errs, inspectErr, captureErr, releaseErr, controlErr)
	if !r.closed {
		r.closed = true
		close(r.done)
	}
}

func (r *reentrantEventListener) bind(engine *Engine, process *Process) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.engine, r.process, r.rootID = engine, process, process.ID()
}

func (r *reentrantEventListener) failures() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

// A synchronous listener runs on its tree's own owner, and every query and
// control path waits for a turn from that same owner. Publication hands the
// listener a context stripped of cancellation so publication order survives a
// canceled caller, which is also the context those paths would otherwise fall
// back on to break the wait -- so a listener that queried its own tree used to
// hang for good with nothing to say why. Each entry point now refuses that call
// instead.
func TestListenerCallingItsOwnTreeIsRefusedRatherThanBlocked(t *testing.T) {
	listener := &reentrantEventListener{done: make(chan struct{})}
	engine, err := NewEngine(EngineConfig{EventListeners: []EventListener{listener}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	input, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}
	listener.bind(engine, root)

	// Start binds after the first events, so drive one more publication and
	// wait for the callback rather than assuming it already ran.
	if _, err := root.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-listener.done:
	case <-t.Context().Done():
		t.Fatal("listener never observed an event with the engine bound")
	}

	failures := listener.failures()
	if len(failures) == 0 {
		t.Fatal("listener recorded no attempts")
	}
	for index, failure := range failures {
		if !errors.Is(failure, ErrListenerReentrancy) {
			t.Fatalf("attempt %d = %v, want ErrListenerReentrancy", index, failure)
		}
	}
}

// The refusal is scoped to the observed tree. Another tree has its own owner
// and is not blocked by this callback, so inspecting it stays legal.
func TestListenerMayInspectAnotherTree(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	input, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}
	other, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}

	ctx := withObservedTree(t.Context(), observed.ID())
	if err := checkListenerReentrancy(ctx, other.ID(), "InspectTree"); err != nil {
		t.Fatalf("checkListenerReentrancy on another tree = %v, want nil", err)
	}
	if err := checkListenerReentrancy(ctx, observed.ID(), "InspectTree"); !errors.Is(err, ErrListenerReentrancy) {
		t.Fatalf("checkListenerReentrancy on the observed tree = %v, want ErrListenerReentrancy", err)
	}
	// An unmarked context is an ordinary caller.
	if err := checkListenerReentrancy(t.Context(), observed.ID(), "InspectTree"); err != nil {
		t.Fatalf("checkListenerReentrancy without a marker = %v, want nil", err)
	}
}
