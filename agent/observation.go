package agent

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
)

const defaultDeltaBuffer = 256

// EventListener observes ordered Framework facts. Panics are isolated from
// Process execution and never alter committed state. Implementations must
// return in bounded time and must not query or control the observed tree.
type EventListener interface {
	// OnEvent receives one committed or attempted Framework fact in increasing
	// ProcessSequence for its Process within one tree runtime activation. A
	// restored nonterminal Process starts with EventProcessRestored; durable
	// activations also carry distinct TreeIncarnationIDs. Different tree runtimes
	// may call the listener concurrently. It runs synchronously on the
	// observed tree's owner, so it must return in bounded time without querying or
	// controlling that tree, or calling Process.Await on it. Calls using this
	// context, or a derived context, return [ErrListenerReentrancy] while the
	// callback is active. The restriction ends when this invocation returns,
	// including after a panic. Calls to other trees must still return in bounded
	// time; distinct owners do not prevent cyclic waits between callbacks.
	// The listener has no veto or acknowledgment authority.
	OnEvent(ctx context.Context, event Event)
}

// EventListenerFunc adapts a plain function to the event listener interface.
// A listener observes and must not steer execution, so the signature returns
// nothing to make that boundary hard to violate by accident.
type EventListenerFunc func(ctx context.Context, event Event)

func (e EventListenerFunc) OnEvent(ctx context.Context, event Event) {
	e(ctx, event)
}

// DeltaListener observes best-effort Strategy streaming increments. Panics are
// isolated; all listeners and trees share one Engine queue and delivery worker.
// A slow callback delays the other listeners and trees and can cause bounded
// queue drops. Implementations must return in bounded time without closing or
// flushing their Engine.
type DeltaListener interface {
	// OnDelta receives an accepted best-effort increment in queue order. Delivery
	// is sequential per listener but may lag Process execution; slow callbacks can
	// cause later increments to be dropped. It has no acknowledgment authority;
	// Engine.Close and FlushDeltas still wait for accepted callback delivery.
	// Close and FlushDeltas using this context, or a derived context, return
	// [ErrListenerReentrancy] while this invocation is active because both would
	// wait for the worker delivering this callback.
	OnDelta(ctx context.Context, delta Delta)
}

// DeltaListenerFunc adapts a plain function to the delta listener interface.
// It returns nothing for the same reason as [EventListenerFunc], and because a
// dropped delta must never change execution.
type DeltaListenerFunc func(ctx context.Context, delta Delta)

func (d DeltaListenerFunc) OnDelta(ctx context.Context, delta Delta) {
	d(ctx, delta)
}

type observationBus struct {
	events []EventListener
	deltas []DeltaListener

	failureMu sync.RWMutex
	failures  ObservationFailures

	deltaMu     sync.RWMutex
	deltaQueue  chan deltaObservation
	deltaClosed bool
	deltaDone   chan struct{}
}

// deltaObservation is either one best-effort Delta or an ordering barrier. A
// barrier does not make dropped increments reliable; it only proves that every
// increment the bounded queue accepted before it has finished calling listeners.
type deltaObservation struct {
	ctx     context.Context
	delta   Delta
	barrier chan struct{}
}

func newObservationBus(events []EventListener, deltas []DeltaListener, capacity int) *observationBus {
	bus := &observationBus{
		events: slices.Clone(events),
		deltas: slices.Clone(deltas),
	}
	if len(bus.deltas) > 0 {
		bus.deltaQueue = make(chan deltaObservation, capacity)
		bus.deltaDone = make(chan struct{})
		go bus.deliverDeltas()
	}
	return bus
}

func (o *observationBus) publishEvent(ctx context.Context, event Event) {
	for index, listener := range o.events {
		if failure := o.callEventListener(ctx, index, listener, event); failure != nil {
			o.failureMu.Lock()
			o.failures.eventListenerPanics = saturatingCountAdd(o.failures.eventListenerPanics, 1)
			o.failures.lastEventPanic = failure
			o.failureMu.Unlock()
		}
	}
}

func (o *observationBus) callEventListener(ctx context.Context, index int, listener EventListener, event Event) (failure *ListenerPanic) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = captureListenerPanic(index, listener, event.ProcessID(), recovered)
		}
	}()
	active := new(atomic.Bool)
	active.Store(true)
	defer active.Store(false)
	ctx = context.WithValue(ctx, observedTreeKey{bus: o, rootID: event.relation.RootID()}, active)
	listener.OnEvent(ctx, event)
	return nil
}

func (o *observationBus) offerDelta(ctx context.Context, delta Delta) bool {
	if len(o.deltas) == 0 {
		return true
	}
	ctx = context.WithoutCancel(requireContext(ctx))
	o.deltaMu.RLock()
	defer o.deltaMu.RUnlock()
	if o.deltaClosed {
		return false
	}
	select {
	case o.deltaQueue <- deltaObservation{ctx: ctx, delta: delta}:
		return true
	default:
		return false
	}
}

func (o *observationBus) deliverDeltas() {
	defer close(o.deltaDone)
	for observation := range o.deltaQueue {
		if observation.barrier != nil {
			close(observation.barrier)
			continue
		}
		for index, listener := range o.deltas {
			if failure := o.callDeltaListener(observation.ctx, index, listener, observation.delta); failure != nil {
				o.failureMu.Lock()
				o.failures.deltaListenerPanics = saturatingCountAdd(o.failures.deltaListenerPanics, 1)
				o.failures.lastDeltaPanic = failure
				o.failureMu.Unlock()
			}
		}
	}
}

func (o *observationBus) flushDeltas(ctx context.Context) error {
	if o.deltaQueue == nil {
		return nil
	}
	barrier := make(chan struct{})
	o.deltaMu.RLock()
	if o.deltaClosed {
		o.deltaMu.RUnlock()
		return ErrEngineClosed
	}
	select {
	case o.deltaQueue <- deltaObservation{barrier: barrier}:
		o.deltaMu.RUnlock()
	case <-ctx.Done():
		o.deltaMu.RUnlock()
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *observationBus) callDeltaListener(ctx context.Context, index int, listener DeltaListener, delta Delta) (failure *ListenerPanic) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = captureListenerPanic(index, listener, delta.ProcessID(), recovered)
		}
	}()
	active := new(atomic.Bool)
	active.Store(true)
	defer active.Store(false)
	ctx = context.WithValue(ctx, observedDeltaKey{bus: o}, active)
	listener.OnDelta(ctx, delta)
	return nil
}

func (o *observationBus) failureSnapshot() ObservationFailures {
	o.failureMu.RLock()
	defer o.failureMu.RUnlock()
	return o.failures
}

func (o *observationBus) close() {
	if o.deltaQueue == nil {
		return
	}
	o.deltaMu.Lock()
	if !o.deltaClosed {
		o.deltaClosed = true
		close(o.deltaQueue)
	}
	o.deltaMu.Unlock()
	<-o.deltaDone
}
