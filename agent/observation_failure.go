package agent

import (
	"fmt"

	"github.com/Tangerg/scope/agent/internal/panicinfo"
)

// ListenerPanic is a detached report of one isolated listener failure.
// ListenerIndex is the zero-based position in EngineConfig.EventListeners or
// DeltaListeners, and ListenerType is its Go type. Message retains at most
// 4 KiB of the formatted panic value; Stack retains at most 64 KiB.
type ListenerPanic struct {
	ListenerIndex int
	ListenerType  string
	ProcessID     ProcessID
	Message       string
	Stack         string
}

// ObservationFailures is an immutable snapshot of event loss and listener
// panics isolated by one Engine. Counts are monotonic and saturate at
// math.MaxUint64; only the latest panic of each listener kind is retained.
type ObservationFailures struct {
	droppedEvents       uint64
	eventListenerPanics uint64
	deltaListenerPanics uint64
	lastEventPanic      *ListenerPanic
	lastDeltaPanic      *ListenerPanic
}

// DroppedEvents counts events omitted because their Process publication sequence
// exhausted uint64. It never wraps and does not change Process execution.
func (o ObservationFailures) DroppedEvents() uint64 { return o.droppedEvents }

func (o ObservationFailures) EventListenerPanics() uint64 { return o.eventListenerPanics }

func (o ObservationFailures) DeltaListenerPanics() uint64 { return o.deltaListenerPanics }

func (o ObservationFailures) LastEventPanic() (ListenerPanic, bool) {
	if o.lastEventPanic == nil {
		return ListenerPanic{}, false
	}
	return *o.lastEventPanic, true
}

func (o ObservationFailures) LastDeltaPanic() (ListenerPanic, bool) {
	if o.lastDeltaPanic == nil {
		return ListenerPanic{}, false
	}
	return *o.lastDeltaPanic, true
}

func captureListenerPanic(index int, listener any, processID ProcessID, value any) *ListenerPanic {
	message, stack := panicinfo.Capture(value)
	return &ListenerPanic{
		ListenerIndex: index, ListenerType: fmt.Sprintf("%T", listener),
		ProcessID: processID, Message: message, Stack: stack,
	}
}
