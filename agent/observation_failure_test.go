package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestObservationFailureSnapshotsOwnBoundedDiagnostics(t *testing.T) {
	message := "first panic"
	bus := newObservationBus([]EventListener{
		EventListenerFunc(func(context.Context, Event) { panic(message) }),
	}, nil, 1)
	t.Cleanup(bus.close)
	if _, found := bus.failureSnapshot().LastEventPanic(); found {
		t.Fatal("new bus reports a listener panic")
	}
	bus.publishEvent(t.Context(), Event{})
	first := bus.failureSnapshot()
	message = strings.Repeat("x", maxListenerPanicMessageBytes*2)
	bus.publishEvent(t.Context(), Event{})
	latest := bus.failureSnapshot()
	last, found := latest.LastEventPanic()
	if !found || latest.EventListenerPanics() != 2 || last.ListenerIndex != 0 ||
		last.Message != message[:maxListenerPanicMessageBytes] || len(last.Stack) == 0 || len(last.Stack) > maxListenerPanicStackBytes {
		t.Fatalf("latest diagnostic = %#v, count = %d", last, latest.EventListenerPanics())
	}
	last.Message = "caller changed its copy"
	firstPanic, found := first.LastEventPanic()
	if !found || first.EventListenerPanics() != 1 || firstPanic.Message != "first panic" {
		t.Fatalf("older snapshot changed: %#v", first)
	}
	if retained, _ := latest.LastEventPanic(); retained.Message != message[:maxListenerPanicMessageBytes] {
		t.Fatal("diagnostic accessor exposed mutable state")
	}
	if _, found := latest.LastDeltaPanic(); found {
		t.Fatal("event failure created a delta diagnostic")
	}
}

func TestObservationFailureSnapshotsAreConsistentAcrossConcurrentEvents(t *testing.T) {
	bus := newObservationBus([]EventListener{
		EventListenerFunc(func(context.Context, Event) { panic("listener failed") }),
	}, nil, 1)
	t.Cleanup(bus.close)
	const count = 32
	var group sync.WaitGroup
	for range count {
		group.Go(func() {
			bus.publishEvent(t.Context(), Event{})
			snapshot := bus.failureSnapshot()
			failure, found := snapshot.LastEventPanic()
			if snapshot.EventListenerPanics() == 0 || !found || failure.Message != "listener failed" {
				t.Errorf("inconsistent snapshot = %#v", snapshot)
			}
		})
	}
	group.Wait()
	if got := bus.failureSnapshot().EventListenerPanics(); got != count {
		t.Fatalf("panic count = %d, want %d", got, count)
	}
}
