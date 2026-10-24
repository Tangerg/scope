package interaction_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

type countingToolDispatcher struct {
	next       agent.Dispatcher
	dispatches *atomic.Int32
}

func (c countingToolDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	c.dispatches.Add(1)
	return c.next.Dispatch(ctx, request, emit)
}

func (c countingToolDispatcher) Policy(effect agent.Effect) agent.EffectPolicy {
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
	decorate := func(next agent.Dispatcher) (agent.Dispatcher, error) {
		constructions.Add(1)
		return countingToolDispatcher{next: next, dispatches: &dispatches}, nil
	}
	client, err := chatclient.New(&probeThenStopModel{}, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	deployment := configuredInteraction(t,
		interaction.DefinitionConfig{Name: "interaction.decorate", Description: "Decorate the Tool dispatcher.", MaxModelCalls: agent.NewQuota(2)},
		interaction.DispatcherConfig{Model: client},
		interaction.ToolSetConfig{Tools: []tool.Tool{executable}, DecorateDispatcher: decorate},
	)
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(context.Background(), deployment.Deployment, interactionInput(t, "probe"))
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
	if result.Termination().Status() != agent.StatusCompleted {
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
	base := interaction.ToolSetConfig{
		Name: "interaction.decorate.guard", Description: "Guard decorator construction.", Tools: []tool.Tool{executable},
		ImplementationDigest: agent.ComputeDigest([]byte("guard")), ConfigurationDigest: agent.ComputeDigest([]byte("guard")),
	}

	failing := base
	failing.DecorateDispatcher = func(agent.Dispatcher) (agent.Dispatcher, error) { return nil, errors.New("boom") }
	if _, err := interaction.NewToolSet(failing); !errors.Is(err, interaction.ErrInvalidToolSet) {
		t.Fatalf("decorator error = %v, want ErrInvalidToolSet", err)
	}

	returnsNil := base
	returnsNil.DecorateDispatcher = func(agent.Dispatcher) (agent.Dispatcher, error) { return nil, nil }
	if _, err := interaction.NewToolSet(returnsNil); !errors.Is(err, interaction.ErrInvalidToolSet) {
		t.Fatalf("nil dispatcher = %v, want ErrInvalidToolSet", err)
	}
}
