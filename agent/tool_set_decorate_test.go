package agent_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

type countingToolDispatcher struct {
	next       run.Dispatcher
	dispatches *atomic.Int32
}

func (c countingToolDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	c.dispatches.Add(1)
	return c.next.Dispatch(ctx, request, emit)
}

func (c countingToolDispatcher) Policy(effect run.Effect) run.EffectPolicy {
	return c.next.Policy(effect)
}

type probeThenStopModel struct{ calls atomic.Int32 }

func (p *probeThenStopModel) Call(context.Context, *chat.Request) (*chat.Response, error) {
	if p.calls.Add(1) == 1 {
		return toolBatchResponse(chat.ToolCall{ID: "call_probe", Name: "probe", Arguments: "{}"}), nil
	}
	return textResponse("done"), nil
}

func TestToolSetDecorateDispatcherWrapsLiveDispatch(t *testing.T) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "probe", Description: "A decorated-dispatch probe Tool."},
		func(context.Context, struct{}) (string, error) { return "probed", nil })
	if err != nil {
		t.Fatal(err)
	}
	var constructions, dispatches atomic.Int32
	decorate := func(next run.Dispatcher) (run.Dispatcher, error) {
		constructions.Add(1)
		return countingToolDispatcher{next: next, dispatches: &dispatches}, nil
	}
	client, err := chatclient.New(&probeThenStopModel{}, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	deployment := configuredInteraction(t,
		agent.DefinitionConfig{Name: "interaction.decorate", Description: "Decorate the Tool dispatcher.", MaxModelCalls: run.NewQuota(2)},
		agent.DispatcherConfig{Model: client},
		agent.ToolSetConfig{Tools: []tool.Tool{executable}, DecorateDispatcher: decorate},
	)
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "probe")})
	if err != nil {
		t.Fatal(err)
	}
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("status = %s", result.Termination().Status())
	}
	if constructions.Load() != 1 || dispatches.Load() < 1 {
		t.Fatalf("decorator constructions=%d dispatches=%d, want one construction and a wrapped dispatch", constructions.Load(), dispatches.Load())
	}
}

func TestToolSetDecorateDispatcherGuardsConstruction(t *testing.T) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "probe", Description: "A decorated-dispatch probe Tool."},
		func(context.Context, struct{}) (string, error) { return "probed", nil })
	if err != nil {
		t.Fatal(err)
	}
	base := agent.ToolSetConfig{
		Name: "interaction.decorate.guard", Description: "Guard decorator construction.", Tools: []tool.Tool{executable},
		ImplementationDigest: run.ComputeDigest([]byte("guard")), ConfigurationDigest: run.ComputeDigest([]byte("guard")),
	}

	invalid := base
	invalid.ConfigurationDigest = run.Digest{}
	invalid.DecorateDispatcher = func(run.Dispatcher) (run.Dispatcher, error) {
		t.Fatal("decorated a tool set without an opaque configuration identity")
		return nil, nil
	}
	if _, err := agent.NewToolSet(invalid); !errors.Is(err, agent.ErrInvalidToolSet) || !errors.Is(err, run.ErrInvalidDigest) {
		t.Fatal(err)
	}

	failing := base
	failing.DecorateDispatcher = func(run.Dispatcher) (run.Dispatcher, error) { return nil, errors.New("boom") }
	if _, err := agent.NewToolSet(failing); !errors.Is(err, agent.ErrInvalidToolSet) {
		t.Fatalf("decorator error = %v, want ErrInvalidToolSet", err)
	}

	returnsNil := base
	returnsNil.DecorateDispatcher = func(run.Dispatcher) (run.Dispatcher, error) { return nil, nil }
	if _, err := agent.NewToolSet(returnsNil); !errors.Is(err, agent.ErrInvalidToolSet) {
		t.Fatalf("nil dispatcher = %v, want ErrInvalidToolSet", err)
	}
}
