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

// ModelObserver receives provider-neutral model attempts. Callbacks are
// observational, must return in bounded time, and have their panics isolated.
type ModelObserver interface {
	// OnModelStarted receives the effective request after context reduction and
	// local admission, immediately before calling the model. It is detached.
	OnModelStarted(ctx context.Context, invocation ModelInvocation, request *chat.Request)
	// OnModelSettled receives exactly one host-boundary outcome for a started
	// call, including cancellation, invalid responses and panics. A valid
	// response is observed before settlement encoding and capacity admission;
	// a later admission failure does not retract this response fact.
	OnModelSettled(ctx context.Context, invocation ModelInvocation, settlement ModelSettlement)
}

// ModelSettlement describes a physical model call, not durable Effect
// settlement. Response is a detached validated response; without one, no
// complete response was established and Failure is a bounded diagnostic,
// never proof that the provider did no work or consumed no tokens.
type ModelSettlement struct {
	Response *chat.Response
	Failure  string
}

// Unknown reports a call that established no complete response.
func (m ModelSettlement) Unknown() bool { return m.Response == nil }

// ToolObserver receives exact Tool-call facts. Callbacks are observational,
// must return in bounded time, and have their panics isolated. Tool children
// may invoke them concurrently when their calls may overlap. Callbacks do not
// acknowledge durable Effect settlement.
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
// With neither, the call's external outcome remains unestablished, including
// host failures, cancellation, deadlines, and panics, and Failure diagnoses
// it. Observation never settles the Effect.
type ToolSettlement struct {
	Result        *chat.ToolResult
	InputRequired bool
	Failure       string
	// Evidence is non-final output from an unknown call. It is never promoted
	// to Result and cannot establish whether the external operation succeeded.
	Evidence *chat.ToolOutput
}

// Unknown reports a call whose external outcome remains unestablished.
func (t ToolSettlement) Unknown() bool { return t.Result == nil && !t.InputRequired }

// ObserverPanic is a detached diagnostic for one isolated callback failure.
// Message retains at most 4 KiB of the formatted panic value and Stack retains
// at most 64 KiB of the failing goroutine's stack. ProcessID and EffectID bind
// the failure to the exact invocation without retaining its request or response.
type ObserverPanic struct {
	ObserverType string
	ProcessID    agent.ProcessID
	EffectID     agent.EffectID
	AttemptID    agent.EffectAttemptID
	Message      string
	Stack        string
}

// ObservationFailures snapshots panics isolated by one Dispatcher or ToolSet.
// Counts saturate at math.MaxUint64. Only the latest panic per callback is kept;
// editing a returned diagnostic cannot change the retained evidence.
type ObservationFailures struct {
	modelStartedPanics    uint64
	modelSettledPanics    uint64
	toolStartedPanics     uint64
	toolSettledPanics     uint64
	lastModelStartedPanic *ObserverPanic
	lastModelSettledPanic *ObserverPanic
	lastToolStartedPanic  *ObserverPanic
	lastToolSettledPanic  *ObserverPanic
}

func (o ObservationFailures) ModelStartedPanics() uint64 { return o.modelStartedPanics }
func (o ObservationFailures) ModelSettledPanics() uint64 { return o.modelSettledPanics }
func (o ObservationFailures) ToolStartedPanics() uint64  { return o.toolStartedPanics }
func (o ObservationFailures) ToolSettledPanics() uint64  { return o.toolSettledPanics }

func (o ObservationFailures) LastModelStartedPanic() (ObserverPanic, bool) {
	if o.lastModelStartedPanic == nil {
		return ObserverPanic{}, false
	}
	return *o.lastModelStartedPanic, true
}

func (o ObservationFailures) LastModelSettledPanic() (ObserverPanic, bool) {
	if o.lastModelSettledPanic == nil {
		return ObserverPanic{}, false
	}
	return *o.lastModelSettledPanic, true
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

// Each callback owns its counter slot, so panic recovery needs no fallible lookup.
type observationCallback func(*ObservationFailures) (panics *uint64, latest **ObserverPanic)

func modelStartedCallback(failures *ObservationFailures) (*uint64, **ObserverPanic) {
	return &failures.modelStartedPanics, &failures.lastModelStartedPanic
}

func modelSettledCallback(failures *ObservationFailures) (*uint64, **ObserverPanic) {
	return &failures.modelSettledPanics, &failures.lastModelSettledPanic
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
func (o *observationFailureCounters) recordPanic(callback observationCallback, observer any, processID agent.ProcessID, effectID agent.EffectID, attemptID agent.EffectAttemptID) {
	value := recover()
	if value == nil {
		return
	}
	message, stack := panicinfo.Capture(value)
	diagnostic := &ObserverPanic{
		ObserverType: fmt.Sprintf("%T", observer), ProcessID: processID, EffectID: effectID, AttemptID: attemptID,
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
