package interaction

import (
	"context"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestExecutionObserverFailuresAreCountedAndIsolated(t *testing.T) {
	dispatcher := &Dispatcher{observer: panickingExecutionObserver{}}
	dispatcher.observeModel(t.Context(), ModelInvocation{}, &chat.Response{})
	tools := &toolDispatcher{observer: panickingExecutionObserver{}}
	tools.observeToolStarted(t.Context(), ToolInvocation{})
	tools.observeToolSettled(t.Context(), ToolInvocation{}, ToolSettlement{})

	counts := tools.observationFailures.snapshot()
	if dispatcher.ObservationFailures().ModelResponsePanics() != 1 ||
		counts.ToolStartedPanics() != 1 ||
		counts.ToolSettledPanics() != 1 {
		t.Fatalf(
			"observer failures = model %d, tool started %d, tool settled %d, want 1 each",
			dispatcher.ObservationFailures().ModelResponsePanics(),
			counts.ToolStartedPanics(),
			counts.ToolSettledPanics(),
		)
	}

	dispatcher.observationFailures.modelResponsePanics.Store(math.MaxUint64)
	dispatcher.observeModel(t.Context(), ModelInvocation{}, &chat.Response{})
	if got := dispatcher.ObservationFailures().ModelResponsePanics(); got != math.MaxUint64 {
		t.Fatalf("saturated model response panic count = %d", got)
	}
}

type panickingExecutionObserver struct{}

func (panickingExecutionObserver) OnModelResponse(context.Context, ModelInvocation, *chat.Response) {
	panic("model observer failed")
}

func (panickingExecutionObserver) OnToolStarted(context.Context, ToolInvocation) {
	panic("tool started observer failed")
}

func (panickingExecutionObserver) OnToolSettled(context.Context, ToolInvocation, ToolSettlement) {
	panic("tool settled observer failed")
}
