package interaction

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/panicinfo"
	"github.com/Tangerg/scope/core/chat"
)

// ModelObserver receives provider-neutral model responses. Callbacks are
// observational, must return in bounded time, and have their panics isolated.
// This Strategy hook exposes model-call details; agent.EventListener observes
// kernel lifecycle facts and agent.DeltaListener receives ephemeral stream data.
type ModelObserver interface {
	// OnModelResponse receives the complete provider-neutral response after the
	// response validates, before settlement encoding and capacity admission. A
	// later admission failure does not retract this response fact; kernel Effect
	// observations report the settlement outcome. The
	// response is detached and may be mutated by the observer. Panics are
	// isolated and the callback has no control authority.
	OnModelResponse(ctx context.Context, invocation ModelInvocation, response *chat.Response)
}

// ToolObserver receives exact Tool-call facts. Callbacks are observational,
// must return in bounded time, and have their panics isolated. Tool children
// may invoke them concurrently when their calls may overlap.
// This Strategy hook exposes Tool attempts; agent.EventListener observes kernel
// lifecycle facts and agent.DeltaListener receives ephemeral stream data.
// Strategy observation callbacks do not acknowledge durable Effect settlement.
type ToolObserver interface {
	// OnToolStarted marks the actual external Tool-call boundary; it is not
	// emitted for calls rejected before execution. Concurrently authorized Tool
	// calls may invoke this method in parallel.
	OnToolStarted(ctx context.Context, invocation ToolInvocation)
	// OnToolSettled receives exactly one conclusive or unknown host-boundary
	// outcome for a started Tool call. The ToolResult, when present, is detached;
	// the callback cannot alter the candidate value used for settlement.
	OnToolSettled(ctx context.Context, invocation ToolInvocation, settlement ToolSettlement)
}

// ToolSettlement is the observed outcome of one Tool call attempt. Result is
// the value produced for the model; it enters the Tool child state only after
// that Effect settles. InputRequired instead means the Tool
// returned a continuation request; the Engine has not yet committed its wait.
// Failure diagnoses an attempt that produced no ordinary ToolResult. Unknown
// means its external outcome remains unestablished, including host failures,
// cancellation, deadlines, and panics. Observation never settles the Effect.
type ToolSettlement struct {
	// Result is the exact ordinary Tool result produced by this call.
	Result *chat.ToolResult
	// InputRequired reports that the Tool paused before producing Result.
	InputRequired bool
	// Failure diagnoses an attempt that produced no Result.
	Failure string
	// Unknown reports that the external Tool settlement could not be determined.
	Unknown bool
}

// ObserverPanic is a detached diagnostic for one isolated callback failure.
// Message retains at most 4 KiB of the formatted panic value and Stack retains
// at most 64 KiB of the failing goroutine's stack. ProcessID and EffectID bind
// the failure to the exact invocation without retaining its request or response.
type ObserverPanic struct {
	ObserverType string
	ProcessID    agent.ProcessID
	EffectID     agent.EffectID
	Message      string
	Stack        string
}

// ObservationFailures snapshots panics isolated by one Dispatcher or ToolSet.
// Counts saturate at math.MaxUint64. Only the latest panic per callback is kept;
// editing a returned diagnostic cannot change the retained evidence.
type ObservationFailures struct {
	modelResponsePanics    uint64
	toolStartedPanics      uint64
	toolSettledPanics      uint64
	lastModelResponsePanic *ObserverPanic
	lastToolStartedPanic   *ObserverPanic
	lastToolSettledPanic   *ObserverPanic
}

func (o ObservationFailures) ModelResponsePanics() uint64 { return o.modelResponsePanics }
func (o ObservationFailures) ToolStartedPanics() uint64   { return o.toolStartedPanics }
func (o ObservationFailures) ToolSettledPanics() uint64   { return o.toolSettledPanics }

func (o ObservationFailures) LastModelResponsePanic() (ObserverPanic, bool) {
	if o.lastModelResponsePanic == nil {
		return ObserverPanic{}, false
	}
	return *o.lastModelResponsePanic, true
}

func (o ObservationFailures) LastToolStartedPanic() (ObserverPanic, bool) {
	if o.lastToolStartedPanic == nil {
		return ObserverPanic{}, false
	}
	return *o.lastToolStartedPanic, true
}

func (o ObservationFailures) LastToolSettledPanic() (ObserverPanic, bool) {
	if o.lastToolSettledPanic == nil {
		return ObserverPanic{}, false
	}
	return *o.lastToolSettledPanic, true
}

// observationCallback selects the counter and latest-diagnostic slot one
// callback owns. Binding the slot to the callback keeps that mapping in a single
// declaration: a new callback cannot exist without its slot, and no dispatch
// table can disagree with it. A table would also have to fail somewhere, and the
// only place to fail here is inside the recover that isolates observer panics.
type observationCallback func(*ObservationFailures) (panics *uint64, latest **ObserverPanic)

func modelResponseCallback(failures *ObservationFailures) (*uint64, **ObserverPanic) {
	return &failures.modelResponsePanics, &failures.lastModelResponsePanic
}

func toolStartedCallback(failures *ObservationFailures) (*uint64, **ObserverPanic) {
	return &failures.toolStartedPanics, &failures.lastToolStartedPanic
}

func toolSettledCallback(failures *ObservationFailures) (*uint64, **ObserverPanic) {
	return &failures.toolSettledPanics, &failures.lastToolSettledPanic
}

type observationFailureCounters struct {
	mu       sync.Mutex
	failures ObservationFailures
}

func (o *observationFailureCounters) snapshot() ObservationFailures {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.failures
}

// recordPanic must be deferred directly: recover cannot intercept a panic through a wrapper.
func (o *observationFailureCounters) recordPanic(callback observationCallback, observer any, processID agent.ProcessID, effectID agent.EffectID) {
	value := recover()
	if value == nil {
		return
	}
	message, stack := panicinfo.Capture(value)
	diagnostic := &ObserverPanic{
		ObserverType: fmt.Sprintf("%T", observer), ProcessID: processID, EffectID: effectID,
		Message: message, Stack: stack,
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	count, latest := callback(&o.failures)
	*latest = diagnostic
	if *count < math.MaxUint64 {
		*count++
	}
}
