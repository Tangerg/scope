package interaction

import (
	"context"
	"math"
	"sync/atomic"

	"github.com/Tangerg/scope/core/chat"
)

// ExecutionObserver receives exact, provider-neutral Interaction facts after
// model and Tool boundaries settle. It is observational: callbacks cannot
// alter execution, panics are isolated, and implementations must return in
// bounded time. A Dispatcher may invoke it concurrently for explicitly
// concurrent Tool calls.
type ExecutionObserver interface {
	// OnModelResponse receives the complete provider-neutral response after the
	// model boundary settles and before later Interaction work is observed. The
	// response is detached and may be mutated by the observer. Panics are
	// isolated and the callback has no control authority.
	OnModelResponse(ctx context.Context, invocation ModelInvocation, response *chat.Response)
	// OnToolStarted marks the actual external Tool-call boundary; it is not
	// emitted for calls rejected before execution. Concurrently authorized Tool
	// calls may invoke this method in parallel.
	OnToolStarted(ctx context.Context, invocation ToolInvocation)
	// OnToolSettled receives exactly one conclusive or unknown host-boundary
	// outcome for a started Tool call. The ToolResult, when present, is detached;
	// the callback cannot alter the value committed to Interaction state.
	OnToolSettled(ctx context.Context, invocation ToolInvocation, settlement ToolSettlement)
}

// ToolSettlement is the conclusive outcome visible at the Tool host boundary.
// Result is the exact value fed back to the model. InputRequired instead means
// the Tool paused durably before producing a result. Failure is reserved for a
// host/cancellation failure for which no ordinary ToolResult was produced.
type ToolSettlement struct {
	// Result is the exact ordinary Tool result fed back to the model.
	Result *chat.ToolResult
	// InputRequired reports that the Tool paused before producing Result.
	InputRequired bool
	// Failure describes a host or cancellation failure that produced no Result.
	Failure string
	// Unknown reports that the external Tool settlement could not be determined.
	Unknown bool
}

// ObservationFailureCounts is an immutable snapshot of ExecutionObserver
// panics isolated by one Dispatcher. Counts are monotonic and saturate at
// math.MaxUint64.
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

func (d *Dispatcher) observeToolStarted(ctx context.Context, invocation ToolInvocation) {
	if d.observer == nil {
		return
	}
	defer recordObserverPanic(&d.observationFailures.toolStartedPanics)
	d.observer.OnToolStarted(ctx, invocation)
}

func (d *Dispatcher) observeToolSettled(ctx context.Context, invocation ToolInvocation, settlement ToolSettlement) {
	if d.observer == nil {
		return
	}
	if settlement.Result != nil {
		settlement.Result = new(settlement.Result.Clone())
	}
	defer recordObserverPanic(&d.observationFailures.toolSettledPanics)
	d.observer.OnToolSettled(ctx, invocation, settlement)
}
