package agent

import (
	"fmt"
	"runtime"
	"strings"
)

const (
	maxListenerPanicMessageBytes = 4 << 10
	maxListenerPanicStackBytes   = 64 << 10
)

// ListenerPanic identifies one isolated listener failure. ListenerIndex is the
// zero-based position in EngineConfig.EventListeners or DeltaListeners, and
// ListenerType is its Go type. Message retains at most 4 KiB of the formatted
// panic value; Stack retains at most 64 KiB of the failing goroutine's stack.
type ListenerPanic struct {
	ListenerIndex int
	ListenerType  string
	ProcessID     ProcessID
	Message       string
	Stack         string
}

// ObservationFailures is an immutable snapshot of listener panics isolated by
// one Engine. Counts are monotonic and saturate at math.MaxUint64. Only the
// latest event-listener panic and delta-listener panic are retained.
type ObservationFailures struct {
	eventListenerPanics uint64
	deltaListenerPanics uint64
	lastEventPanic      *ListenerPanic
	lastDeltaPanic      *ListenerPanic
}

func (o ObservationFailures) EventListenerPanics() uint64 { return o.eventListenerPanics }

func (o ObservationFailures) DeltaListenerPanics() uint64 { return o.deltaListenerPanics }

// LastEventPanic reports the latest failure in the event-listener list.
func (o ObservationFailures) LastEventPanic() (ListenerPanic, bool) {
	if o.lastEventPanic == nil {
		return ListenerPanic{}, false
	}
	return *o.lastEventPanic, true
}

// LastDeltaPanic reports the latest failure in the delta-listener list.
func (o ObservationFailures) LastDeltaPanic() (ListenerPanic, bool) {
	if o.lastDeltaPanic == nil {
		return ListenerPanic{}, false
	}
	return *o.lastDeltaPanic, true
}

func captureListenerPanic(index int, listener any, processID ProcessID, value any) *ListenerPanic {
	message := fmt.Sprint(value)
	if len(message) > maxListenerPanicMessageBytes {
		// Clone the retained prefix so a large panic value cannot keep its full
		// backing allocation alive through the diagnostic snapshot.
		message = strings.Clone(message[:maxListenerPanicMessageBytes])
	}
	stack := make([]byte, maxListenerPanicStackBytes)
	size := runtime.Stack(stack, false)
	return &ListenerPanic{
		ListenerIndex: index, ListenerType: fmt.Sprintf("%T", listener),
		ProcessID: processID, Message: message, Stack: string(stack[:size]),
	}
}
