package agent

import (
	"context"
	"math"
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
	// controlling that tree (including Engine.InspectTree). A call that forwards
	// this context to that tree is refused with [ErrListenerReentrancy] rather
	// than left waiting for a turn this callback is holding. It has no veto or
	// acknowledgment authority.
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
// isolated; slow listeners may cause bounded queue drops.
type DeltaListener interface {
	// OnDelta receives an accepted best-effort increment in queue order. Delivery
	// is sequential per listener but may lag Process execution; slow callbacks can
	// cause later increments to be dropped. The callback cannot affect execution.
	OnDelta(ctx context.Context, delta Delta)
}

// DeltaListenerFunc adapts a plain function to the delta listener interface.
// It returns nothing for the same reason as [EventListenerFunc], and because a
// dropped delta must never change execution.
type DeltaListenerFunc func(ctx context.Context, delta Delta)

func (d DeltaListenerFunc) OnDelta(ctx context.Context, delta Delta) {
	d(ctx, delta)
}

// ObservationFailureCounts is an immutable snapshot of listener panics
// isolated by one Engine. Counts are monotonic and saturate at math.MaxUint64.
type ObservationFailureCounts struct {
	eventListenerPanics uint64
	deltaListenerPanics uint64
}

func (o ObservationFailureCounts) EventListenerPanics() uint64 {
	return o.eventListenerPanics
}

func (o ObservationFailureCounts) DeltaListenerPanics() uint64 {
	return o.deltaListenerPanics
}

type observationBus struct {
	events []EventListener
	deltas []DeltaListener

	eventListenerPanics atomic.Uint64
	deltaListenerPanics atomic.Uint64

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
	// Mark the tree whose owner is delivering this event, so a listener that
	// forwards this context into a query or control call on that tree gets
	// [ErrListenerReentrancy] rather than the permanent wait it would
	// otherwise sit in.
	ctx = withObservedTree(ctx, event.relation.RootID())
	for _, listener := range o.events {
		if callEventListener(ctx, listener, event) {
			incrementObservationFailure(&o.eventListenerPanics)
		}
	}
}

func callEventListener(ctx context.Context, listener EventListener, event Event) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	listener.OnEvent(ctx, event)
	return false
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
		for _, listener := range o.deltas {
			if callDeltaListener(observation.ctx, listener, observation.delta) {
				incrementObservationFailure(&o.deltaListenerPanics)
			}
		}
	}
}

func (o *observationBus) flushDeltas(ctx context.Context) error {
	if o.deltaQueue == nil {
		return nil
	}
	ctx = requireContext(ctx)
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

func callDeltaListener(ctx context.Context, listener DeltaListener, delta Delta) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	listener.OnDelta(ctx, delta)
	return false
}

func (o *observationBus) failureCounts() ObservationFailureCounts {
	return ObservationFailureCounts{
		eventListenerPanics: o.eventListenerPanics.Load(),
		deltaListenerPanics: o.deltaListenerPanics.Load(),
	}
}

func incrementObservationFailure(counter *atomic.Uint64) {
	for {
		current := counter.Load()
		if current == math.MaxUint64 || counter.CompareAndSwap(current, current+1) {
			return
		}
	}
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
