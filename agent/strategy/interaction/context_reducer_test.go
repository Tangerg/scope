package interaction_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestModelContextReductionReplacesLiveAndRecoverableWorkingContext(t *testing.T) {
	echo, err := tool.NewFunc(tool.FuncConfig{
		Name: "echo", Description: "Return the supplied value.",
	}, func(_ context.Context, input struct {
		Value string `json:"value"`
	}) (string, error) {
		return input.Value, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	reducer := &secondCallContextReducer{}
	model := &contextReductionModel{}
	result := runContextReductionInteraction(t, model, reducer, []tool.Tool{echo})
	if result.Status() != agent.StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	if model.Calls() != 3 || reducer.Calls() != 3 {
		t.Fatalf("calls = model:%d reducer:%d, want 3 each", model.Calls(), reducer.Calls())
	}
}

func TestModelContextReductionFailureDoesNotCallMainModel(t *testing.T) {
	var modelCalls atomic.Int32
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls.Add(1)
		return textResponse("must not run"), nil
	})
	result := runContextReductionInteraction(
		t,
		model,
		failingContextReducer{},
		nil,
	)
	assertInteractionHostFailure(t, result)
	if calls := modelCalls.Load(); calls != 0 {
		t.Fatalf("main model calls = %d, want zero", calls)
	}
}

func TestDispatcherRejectsTypedNilModelContextReducer(t *testing.T) {
	client, err := chatclient.New(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("done"), nil
	}), chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "interaction.context-reducer.typed-nil", Description: "Reject a typed nil reducer.",
		MaxModelCalls: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var reducer *secondCallContextReducer
	if _, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Model: client, ModelContextReducer: reducer,
	}); !errors.Is(err, interaction.ErrInvalidDispatcherConfig) {
		t.Fatalf("error = %v, want ErrInvalidDispatcherConfig", err)
	}
}

type secondCallContextReducer struct {
	mu    sync.Mutex
	calls int
}

func (s *secondCallContextReducer) ReduceModelContext(
	_ context.Context,
	invocation interaction.ModelInvocation,
	request *chat.Request,
) ([]chat.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if invocation.ModelCallSequence() != uint32(s.calls) {
		return nil, errors.New("reducer received the wrong model-call attribution")
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "echo" {
		return nil, errors.New("reducer did not receive the effective Tool manifest")
	}
	switch s.calls {
	case 1:
		if len(request.Messages) != 1 || request.Messages[0].Text() != "original context" {
			return nil, errors.New("first reduction input is not the initial WorkingContext")
		}
		return request.Messages, nil
	case 2:
		if len(request.Messages) != 3 || request.Messages[0].Text() != "original context" {
			return nil, errors.New("second reduction input does not include the first Tool round")
		}
		return []chat.Message{chat.NewSystemMessage("compacted context")}, nil
	case 3:
		if len(request.Messages) != 3 || request.Messages[0].Text() != "compacted context" {
			return nil, errors.New("third reduction regrew the pre-compaction WorkingContext")
		}
		for index := range request.Messages {
			if request.Messages[index].Text() == "original context" {
				return nil, errors.New("third reduction retained the original context")
			}
		}
		return request.Messages, nil
	default:
		return nil, errors.New("unexpected reducer call")
	}
}

func (s *secondCallContextReducer) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type failingContextReducer struct{}

func (failingContextReducer) ReduceModelContext(
	context.Context,
	interaction.ModelInvocation,
	*chat.Request,
) ([]chat.Message, error) {
	return nil, errors.New("compaction journal unavailable")
}

type contextReductionModel struct {
	mu    sync.Mutex
	calls int
}

func (c *contextReductionModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	switch c.calls {
	case 1:
		if len(request.Messages) != 1 || request.Messages[0].Text() != "original context" {
			return nil, errors.New("first model call received the wrong context")
		}
		return toolCallResponse(chat.ToolCall{
			ID: "call_before_compaction", Name: "echo", Arguments: `{"value":"first"}`,
		}), nil
	case 2:
		if len(request.Messages) != 1 || request.Messages[0].Role != chat.RoleSystem ||
			request.Messages[0].Text() != "compacted context" {
			return nil, errors.New("second model call did not receive the compacted context")
		}
		return toolCallResponse(chat.ToolCall{
			ID: "call_after_compaction", Name: "echo", Arguments: `{"value":"second"}`,
		}), nil
	case 3:
		if len(request.Messages) != 3 || request.Messages[0].Text() != "compacted context" ||
			request.Messages[1].Role != chat.RoleAssistant || request.Messages[2].Role != chat.RoleTool {
			return nil, errors.New("third model call did not continue from the compacted context")
		}
		return textResponse("done"), nil
	default:
		return nil, errors.New("unexpected model call")
	}
}

func (c *contextReductionModel) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func runContextReductionInteraction(
	t *testing.T,
	model chat.Model,
	reducer interaction.ModelContextReducer,
	tools []tool.Tool,
) agent.Result {
	t.Helper()
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	deployment := configuredInteraction(t, interaction.DefinitionConfig{
		Name: "interaction.context-reducer", Description: "Exercise model-context reduction.", MaxModelCalls: 3,
	}, interaction.DispatcherConfig{Model: client, ModelContextReducer: reducer}, interaction.ToolSetConfig{Tools: tools})
	return runInteraction(t, deployment, "original context")
}
