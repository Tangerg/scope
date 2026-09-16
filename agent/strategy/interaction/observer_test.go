package interaction

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestExecutionConstructorsRejectTypedNilObservers(t *testing.T) {
	var observer *panickingExecutionObserver
	t.Run("model", func(t *testing.T) {
		definition, err := NewDefinition(DefinitionConfig{
			Name: "interaction.observer", Description: "Validate optional model observation.", MaxModelCalls: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		config := DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			return nil, errors.New("model must not run during construction")
		})}
		if _, err := NewDispatcher(definition, config); err != nil {
			t.Fatalf("absent observer is invalid: %v", err)
		}
		config.Observer = observer
		if _, err := NewDispatcher(definition, config); !errors.Is(err, ErrInvalidDispatcherConfig) {
			t.Fatalf("typed nil model observer was admitted: %v", err)
		}
	})
	t.Run("tool", func(t *testing.T) {
		executable, err := tool.NewFunc(tool.FuncConfig{
			Name: "noop", Description: "Validate optional Tool observation.",
		}, func(context.Context, struct{}) (string, error) { return "done", nil })
		if err != nil {
			t.Fatal(err)
		}
		config := ToolSetConfig{
			Name: "interaction.observer.tools", Description: "Validate optional Tool observation.", Tools: []tool.Tool{executable},
			ImplementationDigest: agent.ComputeDigest([]byte("observer-test")),
			ConfigurationDigest:  agent.ComputeDigest([]byte("observer-config")),
		}
		if _, err := NewToolSet(config); err != nil {
			t.Fatalf("absent observer is invalid: %v", err)
		}
		config.Observer = observer
		if _, err := NewToolSet(config); !errors.Is(err, ErrInvalidToolSet) {
			t.Fatalf("typed nil Tool observer was admitted: %v", err)
		}
	})
}

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

	modelPanic, hasModel := dispatcher.ObservationFailures().LastModelResponsePanic()
	startedPanic, hasStarted := counts.LastToolStartedPanic()
	settledPanic, hasSettled := counts.LastToolSettledPanic()
	if !hasModel || !hasStarted || !hasSettled || modelPanic.Message != "model observer failed" || startedPanic.Message != "tool started observer failed" || settledPanic.Message != "tool settled observer failed" {
		t.Fatal("callback-specific diagnostics were lost")
	}

	dispatcher.observationFailures.failures.modelResponsePanics = math.MaxUint64
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

func TestObserverPanicDiagnosticsAreBoundedDetachedAndConcurrent(t *testing.T) {
	processID, _ := agent.ParseProcessID("process:observer")
	effectID, _ := agent.ParseEffectID("effect:observer")
	var failures observationFailureCounters
	if _, present := failures.snapshot().LastModelResponsePanic(); present {
		t.Fatal("zero report has a diagnostic")
	}
	if _, present := failures.snapshot().LastToolStartedPanic(); present {
		t.Fatal("zero report has a diagnostic")
	}
	if _, present := failures.snapshot().LastToolSettledPanic(); present {
		t.Fatal("zero report has a diagnostic")
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			defer failures.recordPanic(toolSettledCallback, panickingExecutionObserver{}, processID, effectID)
			panic(strings.Repeat("x", 5000))
		})
	}
	group.Wait()
	snapshot := failures.snapshot()
	diagnostic, present := snapshot.LastToolSettledPanic()
	if !present || snapshot.ToolSettledPanics() != 16 || diagnostic.ObserverType != "interaction.panickingExecutionObserver" || diagnostic.ProcessID != processID || diagnostic.EffectID != effectID || diagnostic.Message != strings.Repeat("x", 4096) || len(diagnostic.Stack) > 64<<10 || !strings.Contains(diagnostic.Stack, "TestObserverPanicDiagnosticsAreBoundedDetachedAndConcurrent") {
		t.Fatalf("unexpected diagnostic: %+v, count %d", diagnostic, snapshot.ToolSettledPanics())
	}
	diagnostic.Message = "edited"
	retained, _ := failures.snapshot().LastToolSettledPanic()
	if retained.Message == "edited" {
		t.Fatal("returned diagnostic aliases retained evidence")
	}
	failures.failures.toolSettledPanics = math.MaxUint64
	func() {
		defer failures.recordPanic(toolSettledCallback, panickingExecutionObserver{}, processID, effectID)
		panic("latest")
	}()
	latest, _ := failures.snapshot().LastToolSettledPanic()
	prior, _ := snapshot.LastToolSettledPanic()
	if failures.snapshot().ToolSettledPanics() != math.MaxUint64 || latest.Message != "latest" || prior.Message != strings.Repeat("x", 4096) {
		t.Fatal("saturation lost fresh evidence or rewrote an earlier snapshot")
	}
}
