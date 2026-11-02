package runtest

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/Tangerg/scope/agent/run"
)

var ErrInvalidEventPredicate = errors.New("runtest: invalid event predicate")

// ObservationRecorder is a concurrency-safe EventListener and DeltaListener.
// Its zero value is ready for use.
type ObservationRecorder struct {
	mu            sync.Mutex
	events        []run.Event
	deltas        []run.Delta
	eventRecorded chan struct{}
}

func (o *ObservationRecorder) OnEvent(_ context.Context, event run.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
	o.notifyEventLocked()
}

func (o *ObservationRecorder) OnDelta(_ context.Context, delta run.Delta) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.deltas = append(o.deltas, delta)
}

func (o *ObservationRecorder) notifyEventLocked() {
	if o.eventRecorded != nil {
		close(o.eventRecorded)
	}
	o.eventRecorded = make(chan struct{})
}

// Events returns recorded events in publication order.
func (o *ObservationRecorder) Events() []run.Event {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.events)
}

// Deltas returns recorded increments in delivery order.
func (o *ObservationRecorder) Deltas() []run.Delta {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.deltas)
}

// AwaitEvent returns the first recorded Event accepted by predicate. It waits
// for later events until ctx ends and never polls Process state.
func (o *ObservationRecorder) AwaitEvent(
	ctx context.Context,
	predicate func(run.Event) bool,
) (run.Event, error) {
	if o == nil || predicate == nil {
		return run.Event{}, ErrInvalidEventPredicate
	}
	next := 0
	for {
		o.mu.Lock()
		batch := slices.Clone(o.events[next:])
		next = len(o.events)
		if o.eventRecorded == nil {
			o.eventRecorded = make(chan struct{})
		}
		eventRecorded := o.eventRecorded
		o.mu.Unlock()

		for _, event := range batch {
			if predicate(event) {
				return event, nil
			}
		}

		select {
		case <-ctx.Done():
			return run.Event{}, ctx.Err()
		case <-eventRecorded:
		}
	}
}
