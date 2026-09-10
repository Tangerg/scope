package interaction

import (
	"context"
	"errors"
	"math"
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
		config := DispatcherConfig{Client: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
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
