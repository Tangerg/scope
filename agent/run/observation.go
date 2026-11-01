package run

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

const defaultObservationBuffer = 256

// EventListener receives ordered, best-effort facts in its own bounded lane.
// A callback never runs on a tree writer. Slow listeners can miss events,
// independently of other listeners; see Runtime.ObservationFailures. Use
// Handle.Observe for explicit cursor gaps and atomic snapshot resynchronization.
// FlushObservations or Close joins accepted work. A listener must not wait for
// its own delivery; callback contexts preserve that restriction.
type EventListener interface {
	// OnEvent observes one accepted event without holding runtime state.
	OnEvent(ctx context.Context, event Event)
}

type EventListenerFunc func(ctx context.Context, event Event)

func (e EventListenerFunc) OnEvent(ctx context.Context, event Event) {
	e(ctx, event)
}

// DeltaListener observes best-effort streaming increments in its own bounded
// lane. Callbacks are sequential per registration and may lag execution.
// FlushObservations or Close joins accepted deliveries.
type DeltaListener interface {
	// OnDelta observes one accepted increment.
	OnDelta(ctx context.Context, delta Delta)
}

type DeltaListenerFunc func(ctx context.Context, delta Delta)

func (d DeltaListenerFunc) OnDelta(ctx context.Context, delta Delta) {
	d(ctx, delta)
}

// A lane has at most one running callback and capacity waiting callbacks. The
// idle worker exits. No lock is held across host code. A flush snapshots the
// tail of each lane without consuming queue capacity or delaying a publisher.
type delivery struct {
	call func()
	done chan struct{}
	next *delivery
}
type deliveryLane struct {
	mu                sync.Mutex
	head, tail        *delivery
	waiting, capacity int
	closed            bool
	drops             uint64
}

func (d *deliveryLane) offer(call func()) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.head != nil && d.waiting >= d.capacity {
		d.drops = saturatingCountAdd(d.drops, 1)
		return false
	}
	item := &delivery{call: call}
	if d.head == nil {
		d.head, d.tail = item, item
		go d.drain()
		return true
	}
	d.tail.next = item
	d.tail = item
	d.waiting++
	return true
}
func (d *deliveryLane) drain() {
	d.mu.Lock()
	for d.head != nil {
		item := d.head
		d.mu.Unlock()
		item.call()
		d.mu.Lock()
		if item.done != nil {
			close(item.done)
		}
		d.head = item.next
		if d.head != nil {
			d.waiting--
		} else {
			d.tail = nil
		}
	}
	d.mu.Unlock()
}
func (d *deliveryLane) fence(closeLane bool) <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if closeLane {
		d.closed = true
	}
	if d.tail == nil {
		return nil
	}
	if d.tail.done == nil {
		d.tail.done = make(chan struct{})
	}
	return d.tail.done
}
func (d *deliveryLane) lost() uint64 { d.mu.Lock(); defer d.mu.Unlock(); return d.drops }

type observationBus struct {
	events    []eventDelivery
	deltas    []deltaDelivery
	failureMu sync.RWMutex
	failures  ObservationFailures
}
type eventDelivery struct {
	listener EventListener
	lane     deliveryLane
}
type deltaDelivery struct {
	listener DeltaListener
	lane     deliveryLane
}

func newObservationBus(events []EventListener, deltas []DeltaListener, eventCapacity, deltaCapacity int) *observationBus {
	o := &observationBus{events: make([]eventDelivery, len(events)), deltas: make([]deltaDelivery, len(deltas))}
	for i, l := range events {
		o.events[i].listener = l
		o.events[i].lane.capacity = eventCapacity
	}
	for i, l := range deltas {
		o.deltas[i].listener = l
		o.deltas[i].lane.capacity = deltaCapacity
	}
	return o
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
	for i := range o.events {
		dst := &o.events[i]
		dst.lane.offer(func() {
			key := activeEventListenerKey{bus: o, rootID: event.Relation().RootID()}
			if failure := o.callListener(ctx, &key, i, dst.listener, event.Relation().ProcessID(), func(ctx context.Context) { dst.listener.OnEvent(ctx, event) }); failure != nil {
				o.recordFailure(func(f *ObservationFailures) { f.recordEventPanic(failure) })
			}
		})
	}
}

// callListener marks ctx as inside this listener for reentrancy checks and
// contains a panic, because observation failures cannot veto execution.
func (o *observationBus) callListener(ctx context.Context, key *activeEventListenerKey, index int, listener any, processID ProcessID, call func(context.Context)) (failure *ListenerPanic) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = captureListenerPanic(index, listener, processID, recovered)
		}
	}()
	active := new(atomic.Bool)
	active.Store(true)
	defer active.Store(false)
	ctx = context.WithValue(ctx, activeJoinKey{bus: o}, active)
	if key != nil {
		ctx = context.WithValue(ctx, *key, active)
	}
	call(ctx)
	return nil
}

func (o *observationBus) offerDelta(ctx context.Context, delta Delta) bool {
	ctx = context.WithoutCancel(RequireContext(ctx))
	all := true
	for i := range o.deltas {
		dst := &o.deltas[i]
		if !dst.lane.offer(func() {
			if failure := o.callListener(ctx, nil, i, dst.listener, delta.ProcessID(), func(ctx context.Context) { dst.listener.OnDelta(ctx, delta) }); failure != nil {
				o.recordFailure(func(f *ObservationFailures) { f.recordDeltaPanic(failure) })
			}
		}) {
			all = false
		}
	}
	return all
}

// Snapshot all tails before waiting so future observations cannot prolong a
// flush indefinitely. Closing first seals every lane, then joins the same tails.
func (o *observationBus) join(ctx context.Context, closing bool) error {
	var fences []<-chan struct{}
	for i := range o.events {
		if ch := o.events[i].lane.fence(closing); ch != nil {
			fences = append(fences, ch)
		}
	}
	for i := range o.deltas {
		if ch := o.deltas[i].lane.fence(closing); ch != nil {
			fences = append(fences, ch)
		}
	}
	for _, ch := range fences {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (o *observationBus) flush(ctx context.Context) error { return o.join(ctx, false) }
func (o *observationBus) close()                          { _ = o.join(context.Background(), true) }
func (o *observationBus) failureSnapshot() ObservationFailures {
	o.failureMu.RLock()
	f := o.failures
	o.failureMu.RUnlock()
	for i := range o.events {
		f.eventDrops = append(f.eventDrops, o.events[i].lane.lost())
	}
	for i := range o.deltas {
		f.deltaDrops = append(f.deltaDrops, o.deltas[i].lane.lost())
	}
	return f
}

func (o *observationBus) checkListenerJoin(ctx context.Context, operation string) error {
	active, ok := ctx.Value(activeJoinKey{bus: o}).(*atomic.Bool)
	if !ok || !active.Load() {
		return nil
	}
	return fmt.Errorf("%w: %s would wait for its active listener", ErrListenerReentrancy, operation)
}

func (o *observationBus) checkEventListenerReentrancy(ctx context.Context, rootID ProcessID, operation string) error {
	active, ok := ctx.Value(activeEventListenerKey{bus: o, rootID: rootID}).(*atomic.Bool)
	if !ok || !active.Load() {
		return nil
	}
	return fmt.Errorf("%w: %s on tree %s is unavailable while its listener is active", ErrListenerReentrancy, operation, rootID)
}
