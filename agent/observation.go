package agent

import (
	"context"
	"fmt"
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
	// ProcessSequence for its Process within one tree runtime activation; a
	// restored nonterminal Process starts with EventProcessRestored. Different
	// tree runtimes may call the listener concurrently. It runs synchronously on
	// the observed tree's owner, so a blocked callback prevents that tree's
	// control and shutdown. Querying or controlling that tree with this context,
	// or a derived one, returns [ErrListenerReentrancy] until the invocation
	// returns. Calls to other trees must still be bounded; distinct owners do not
	// prevent cyclic waits between callbacks. Network and disk exporters belong
	// behind a Host-owned bounded queue.
	OnEvent(ctx context.Context, event Event)
}

// EventListenerFunc adapts a plain function to EventListener.
type EventListenerFunc func(ctx context.Context, event Event)

func (e EventListenerFunc) OnEvent(ctx context.Context, event Event) {
	e(ctx, event)
}

// DeltaListener observes best-effort Strategy streaming increments. Panics are
// isolated; all listeners and trees share one Engine queue and delivery worker,
// so a slow callback delays the others and can cause bounded queue drops.
// Implementations must return in bounded time. Hosts put network or disk
// exporters behind their own bounded queue and expose its drops.
type DeltaListener interface {
	// OnDelta receives an accepted increment in queue order, sequentially per
	// listener and possibly lagging execution. Engine.Close and FlushDeltas wait
	// for accepted delivery, so calling either with this context, or a derived
	// one, returns [ErrListenerReentrancy] while the invocation is active.
	OnDelta(ctx context.Context, delta Delta)
}

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

func (o *observationBus) recordDroppedEvent() {
	o.recordFailure((*ObservationFailures).recordDroppedEvent)
}

func (o *observationBus) recordFailure(record func(*ObservationFailures)) {
	o.failureMu.Lock()
	defer o.failureMu.Unlock()
	record(&o.failures)
}

func (o *observationBus) publishEvent(ctx context.Context, event Event) {
	for index, listener := range o.events {
		if failure := o.callEventListener(ctx, index, listener, event); failure != nil {
			o.recordFailure(func(failures *ObservationFailures) { failures.recordEventPanic(failure) })
		}
	}
}

func (o *observationBus) callEventListener(ctx context.Context, index int, listener EventListener, event Event) *ListenerPanic {
	key := activeEventListenerKey{bus: o, rootID: event.relation.RootID()}
	return callListener(ctx, key, index, listener, event.ProcessID(), func(ctx context.Context) { listener.OnEvent(ctx, event) })
}

// callListener marks ctx as inside this listener for reentrancy checks and
// contains a panic, because observation failures cannot veto execution.
func callListener(ctx context.Context, key any, index int, listener any, processID ProcessID, call func(context.Context)) (failure *ListenerPanic) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = captureListenerPanic(index, listener, processID, recovered)
		}
	}()
	active := new(atomic.Bool)
	active.Store(true)
	defer active.Store(false)
	call(context.WithValue(ctx, key, active))
	return nil
}

func (o *observationBus) offerDelta(ctx context.Context, delta Delta) bool {
	if len(o.deltas) == 0 {
		return true
	}
	ctx = context.WithoutCancel(RequireContext(ctx))
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
				o.recordFailure(func(failures *ObservationFailures) { failures.recordDeltaPanic(failure) })
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

func (o *observationBus) callDeltaListener(ctx context.Context, index int, listener DeltaListener, delta Delta) *ListenerPanic {
	return callListener(ctx, activeDeltaListenerKey{bus: o}, index, listener, delta.ProcessID(), func(ctx context.Context) { listener.OnDelta(ctx, delta) })
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

func (o *observationBus) checkDeltaListenerReentrancy(ctx context.Context, operation string) error {
	active, ok := ctx.Value(activeDeltaListenerKey{bus: o}).(*atomic.Bool)
	if !ok || !active.Load() {
		return nil
	}
	return fmt.Errorf("%w: %s would wait for its active Delta listener", ErrListenerReentrancy, operation)
}

func (o *observationBus) checkEventListenerReentrancy(ctx context.Context, rootID ProcessID, operation string) error {
	active, ok := ctx.Value(activeEventListenerKey{bus: o, rootID: rootID}).(*atomic.Bool)
	if !ok || !active.Load() {
		return nil
	}
	return fmt.Errorf("%w: %s on tree %s is unavailable while its listener is active", ErrListenerReentrancy, operation, rootID)
}
