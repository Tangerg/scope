package interaction

import (
	"context"
	"math"
	"sync/atomic"

	"github.com/Tangerg/scope/core/chat"
)

// ModelObserver receives provider-neutral model responses. Callbacks are
// observational, must return in bounded time, and have their panics isolated.
type ModelObserver interface {
	// OnModelResponse receives the complete provider-neutral response after the
	// model boundary settles and before later Interaction work is observed. The
	// response is detached and may be mutated by the observer. Panics are
	// isolated and the callback has no control authority.
	OnModelResponse(ctx context.Context, invocation ModelInvocation, response *chat.Response)
}

// ToolObserver receives exact Tool-call facts. Callbacks are observational,
// must return in bounded time, and have their panics isolated. Tool children
// may invoke them concurrently when their calls may overlap.
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

// ObservationFailureCounts is an immutable snapshot of observer panics
// isolated by one model Dispatcher or ToolSet. Counts are monotonic and
// saturate at math.MaxUint64.
type ObservationFailureCounts struct {
	modelResponsePanics uint64
	toolStartedPanics   uint64
	toolSettledPanics   uint64
}

func (o ObservationFailureCounts) ModelResponsePanics() uint64 {
	return o.modelResponsePanics
}

func (o ObservationFailureCounts) ToolStartedPanics() uint64 {
	return o.toolStartedPanics
}

func (o ObservationFailureCounts) ToolSettledPanics() uint64 {
	return o.toolSettledPanics
}

type observationFailureCounters struct {
	modelResponsePanics atomic.Uint64
	toolStartedPanics   atomic.Uint64
	toolSettledPanics   atomic.Uint64
}

func (o *observationFailureCounters) snapshot() ObservationFailureCounts {
	return ObservationFailureCounts{
		modelResponsePanics: o.modelResponsePanics.Load(),
		toolStartedPanics:   o.toolStartedPanics.Load(),
		toolSettledPanics:   o.toolSettledPanics.Load(),
	}
}

func recordObserverPanic(counter *atomic.Uint64) {
	if recover() == nil {
		return
	}
	for {
		current := counter.Load()
		if current == math.MaxUint64 || counter.CompareAndSwap(current, current+1) {
			return
		}
	}
}

func (d *Dispatcher) observeModel(ctx context.Context, invocation ModelInvocation, response *chat.Response) {
	if d.observer == nil {
		return
	}
	defer recordObserverPanic(&d.observationFailures.modelResponsePanics)
	d.observer.OnModelResponse(ctx, invocation, response.Clone())
}

func (t *toolDispatcher) observeToolStarted(ctx context.Context, invocation ToolInvocation) {
	if t.observer == nil {
		return
	}
	defer recordObserverPanic(&t.observationFailures.toolStartedPanics)
	t.observer.OnToolStarted(ctx, invocation)
}

func (t *toolDispatcher) observeToolSettled(ctx context.Context, invocation ToolInvocation, settlement ToolSettlement) {
	if t.observer == nil {
		return
	}
	if settlement.Result != nil {
		settlement.Result = new(settlement.Result.Clone())
	}
	defer recordObserverPanic(&t.observationFailures.toolSettledPanics)
	t.observer.OnToolSettled(ctx, invocation, settlement)
}
