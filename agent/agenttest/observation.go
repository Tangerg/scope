package agenttest

import (
	"context"
	"errors"
	"slices"
	"sync"

	agent "github.com/Tangerg/scope/agent"
)

var ErrInvalidEventPredicate = errors.New("agenttest: invalid event predicate")

// ObservationRecorder is a concurrency-safe EventListener and DeltaListener.
// Its zero value is ready for use.
type ObservationRecorder struct {
	mu            sync.Mutex
	events        []agent.Event
	deltas        []agent.Delta
	eventRecorded chan struct{}
}

func (o *ObservationRecorder) OnEvent(_ context.Context, event agent.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
	o.notifyEventLocked()
}

func (o *ObservationRecorder) OnDelta(_ context.Context, delta agent.Delta) {
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
func (o *ObservationRecorder) Events() []agent.Event {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.events)
}

// Deltas returns recorded increments in delivery order.
func (o *ObservationRecorder) Deltas() []agent.Delta {
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
	predicate func(agent.Event) bool,
) (agent.Event, error) {
	if o == nil || predicate == nil {
		return agent.Event{}, ErrInvalidEventPredicate
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
			return agent.Event{}, ctx.Err()
		case <-eventRecorded:
		}
	}
}
