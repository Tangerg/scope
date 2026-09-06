package chatclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

var (
	// ErrInvalidToolMiddleware identifies an executable set or request that
	// cannot satisfy the middleware's single-batch ownership contract.
	ErrInvalidToolMiddleware = errors.New("chatclient: invalid tool middleware")
)

type toolMiddleware struct {
	bindings    map[string]tool.Binding
	definitions []chat.ToolDefinition
}

// NewToolMiddleware keeps direct Client usage useful for one model-requested
// Tool batch. It advertises a frozen Tool set and validates every invocation
// before executing any Tool. The accepted batch executes serially, followed by
// one model call. Runtime failures do not roll back completed Tools. Further
// rounds and execution policy remain outside this boundary.
func NewToolMiddleware(executables ...tool.Tool) (chat.CallMiddleware, error) {
	if len(executables) == 0 {
		return nil, fmt.Errorf("%w: at least one Tool is required", ErrInvalidToolMiddleware)
	}
	middleware := &toolMiddleware{
		bindings:    make(map[string]tool.Binding, len(executables)),
		definitions: make([]chat.ToolDefinition, 0, len(executables)),
	}
	for index, executable := range executables {
		binding, err := tool.Bind(executable)
		if err != nil {
			return nil, fmt.Errorf("%w: tools[%d]: %w", ErrInvalidToolMiddleware, index, err)
		}
		definition := binding.Definition()
		if _, duplicate := middleware.bindings[definition.Name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Tool name %q", ErrInvalidToolMiddleware, definition.Name)
		}
		middleware.bindings[definition.Name] = binding
		middleware.definitions = append(middleware.definitions, definition)
	}
	return middleware.wrap, nil
}

func (t *toolMiddleware) wrap(next chat.Model) chat.Model {
	return chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		return t.call(ctx, next, request)
	})
}

func (t *toolMiddleware) call(
	ctx context.Context,
	next chat.Model,
	request *chat.Request,
) (*chat.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: nil Request", ErrInvalidToolMiddleware)
	}
	if len(request.Tools) != 0 || request.ToolChoice != nil {
		return nil, fmt.Errorf(
			"%w: request tools and tool choice must be empty because the middleware owns the tool contract",
			ErrInvalidToolMiddleware,
		)
	}
	current := request.Clone()
	current.Tools = t.cloneDefinitions()
	response, err := next.Call(ctx, current)
	if err != nil {
		return nil, err
	}
	calls, err := toolCalls(response)
	if err != nil || len(calls) == 0 {
		return response, err
	}
	batch, err := t.prepare(calls)
	if err != nil {
		return nil, err
	}
	results, err := batch.execute(ctx)
	if err != nil {
		return nil, err
	}
	current.Messages = append(
		current.Messages,
		response.Output.Message.Clone(),
		chat.NewToolMessage(results...),
	)
	return next.Call(ctx, current)
}

type preparedToolCall struct {
	id         string
	name       string
	binding    tool.Binding
	invocation tool.Invocation
}

// A batch becomes executable only after every proposal satisfies its frozen
// contract, so a later invalid proposal cannot strand earlier side effects.
type preparedToolBatch []preparedToolCall

func (t *toolMiddleware) prepare(calls []chat.ToolCall) (preparedToolBatch, error) {
	batch := make(preparedToolBatch, len(calls))
	for index, call := range calls {
		binding, exists := t.bindings[call.Name]
		if !exists {
			return nil, fmt.Errorf("chatclient: execute tool call[%d]: tool %q is not bound", index, call.Name)
		}
		invocation, err := binding.Prepare(call)
		if err != nil {
			return nil, fmt.Errorf("chatclient: prepare tool call[%d]: %w", index, err)
		}
		batch[index] = preparedToolCall{
			id: call.ID, name: call.Name, binding: binding, invocation: invocation,
		}
	}
	return batch, nil
}

func (p preparedToolBatch) execute(ctx context.Context) ([]chat.ToolResult, error) {
	results := make([]chat.ToolResult, 0, len(p))
	for index, call := range p {
		output, err := call.binding.Call(ctx, call.invocation)
		if err != nil {
			return nil, fmt.Errorf("chatclient: execute tool call[%d] %q: %w", index, call.name, err)
		}
		if err := output.Validate(); err != nil {
			return nil, fmt.Errorf("chatclient: validate tool call[%d] %q output: %w", index, call.name, err)
		}
		results = append(results, chat.ToolResult{ID: call.id, Name: call.name, Output: output.Clone()})
	}
	return results, nil
}

func (t *toolMiddleware) cloneDefinitions() []chat.ToolDefinition {
	definitions := make([]chat.ToolDefinition, len(t.definitions))
	for index := range t.definitions {
		definitions[index] = t.definitions[index].Clone()
	}
	return definitions
}

func toolCalls(response *chat.Response) ([]chat.ToolCall, error) {
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("chatclient: tool middleware received an invalid response: %w", err)
	}
	if response.Output == nil || response.Output.Message == nil {
		return nil, nil
	}
	var calls []chat.ToolCall
	for _, part := range response.Output.Message.Parts {
		if part.Kind == chat.PartToolCall {
			calls = append(calls, *part.ToolCall)
		}
	}
	return calls, nil
}
