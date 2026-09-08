package chatclient

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type middlewareTool struct {
	name string
	call func(context.Context, tool.Invocation) (chat.ToolOutput, error)
}

func (m middlewareTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name:        m.name,
		Description: "test tool",
		InputSchema: []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
	}
}

func (m middlewareTool) Call(ctx context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
	return m.call(ctx, invocation)
}

func TestToolMiddlewareExecutesSeriallyUntilFinalResponse(t *testing.T) {
	var executed []string
	executable := middlewareTool{name: "lookup", call: func(_ context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
		executed = append(executed, string(invocation.Arguments()))
		return chat.NewTextToolOutput("found"), nil
	}}
	middleware, err := NewToolMiddleware(executable)
	if err != nil {
		t.Fatalf("NewToolMiddleware() error = %v", err)
	}

	var requests []*chat.Request
	model := middleware(chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		requests = append(requests, request.Clone())
		if len(requests) == 1 {
			return toolCallResponse(
				chat.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"value":"first"}`},
				chat.ToolCall{ID: "call-2", Name: "lookup", Arguments: `{"value":"second"}`},
			), nil
		}
		return textResponse("done"), nil
	}))
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("search"))}}
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if response.Text() != "done" {
		t.Fatalf("Call() text = %q, want done", response.Text())
	}
	if !reflect.DeepEqual(executed, []string{`{"value":"first"}`, `{"value":"second"}`}) {
		t.Fatalf("execution order = %v", executed)
	}
	if len(request.Tools) != 0 || len(request.Messages) != 1 {
		t.Fatalf("caller request mutated: %#v", request)
	}
	if len(requests) != 2 || len(requests[0].Tools) != 1 || len(requests[1].Messages) != 3 {
		t.Fatalf("model requests = %#v", requests)
	}
	results := requests[1].Messages[2]
	if results.Role != chat.RoleTool || len(results.Parts) != 2 || results.Parts[0].ToolResult.Output.Content[0].Text != "found" {
		t.Fatalf("tool results = %#v", results)
	}
}

func TestToolMiddlewareRejectsCompetingManifest(t *testing.T) {
	middleware, err := NewToolMiddleware(middlewareTool{name: "lookup", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
		return chat.ToolOutput{}, nil
	}})
	if err != nil {
		t.Fatalf("NewToolMiddleware() error = %v", err)
	}
	model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		t.Fatal("model must not be called")
		return nil, nil
	}))
	request := &chat.Request{
		Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("search"))},
		Tools:    []chat.ToolDefinition{middlewareTool{name: "other"}.Definition()},
	}
	if _, err := model.Call(t.Context(), request); !errors.Is(err, ErrInvalidToolMiddleware) {
		t.Fatalf("Call() error = %v, want ErrInvalidToolMiddleware", err)
	}
}

func TestToolMiddlewareRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewToolMiddleware(); !errors.Is(err, ErrInvalidToolMiddleware) {
		t.Fatalf("NewToolMiddleware() error = %v, want ErrInvalidToolMiddleware", err)
	}
	duplicate := middlewareTool{name: "duplicate", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
		return chat.ToolOutput{}, nil
	}}
	if _, err := NewToolMiddleware(duplicate, duplicate); !errors.Is(err, ErrInvalidToolMiddleware) {
		t.Fatalf("NewToolMiddleware(duplicate) error = %v, want ErrInvalidToolMiddleware", err)
	}
}

func TestToolMiddlewarePropagatesExecutionFailure(t *testing.T) {
	want := errors.New("unavailable")
	middleware, err := NewToolMiddleware(middlewareTool{name: "lookup", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
		return chat.ToolOutput{}, want
	}})
	if err != nil {
		t.Fatalf("NewToolMiddleware() error = %v", err)
	}
	model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return toolCallResponse(chat.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"value":"first"}`}), nil
	}))
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("search"))}}
	if _, err := model.Call(t.Context(), request); !errors.Is(err, want) {
		t.Fatalf("Call() error = %v, want execution error", err)
	}
}

func TestToolMiddlewareRejectsEntireInvalidBatchBeforeExecution(t *testing.T) {
	for _, second := range []chat.ToolCall{
		{ID: "second", Name: "missing", Arguments: `{"value":"second"}`},
		{ID: "second", Name: "write", Arguments: `{"value":123}`},
		{ID: "first", Name: "write", Arguments: `{"value":"second"}`},
	} {
		t.Run(second.Name, func(t *testing.T) {
			executions := 0
			middleware, err := NewToolMiddleware(middlewareTool{
				name: "write",
				call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
					executions++
					return chat.NewTextToolOutput("changed"), nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			modelCalls := 0
			model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				modelCalls++
				return toolCallResponse(
					chat.ToolCall{ID: "first", Name: "write", Arguments: `{"value":"first"}`}, second,
				), nil
			}))
			response, err := model.Call(t.Context(), textRequest("write"))
			if err == nil || response != nil || executions != 0 || modelCalls != 1 {
				t.Fatalf("response=%v error=%v executions=%d model calls=%d", response, err, executions, modelCalls)
			}
		})
	}
}

func TestToolMiddlewareExecutesOnlyOneBatch(t *testing.T) {
	var executions int
	executable := middlewareTool{name: "lookup", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
		executions++
		return chat.NewTextToolOutput("found"), nil
	}}
	middleware, err := NewToolMiddleware(executable)
	if err != nil {
		t.Fatalf("NewToolMiddleware() error = %v", err)
	}
	var calls int
	model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls++
		return toolCallResponse(chat.ToolCall{ID: "call", Name: "lookup", Arguments: `{"value":"again"}`}), nil
	}))
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("search"))}}
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || executions != 1 || response.Output.FinishReason != chat.FinishReasonToolCalls {
		t.Fatalf("model calls = %d, tool executions = %d, response = %#v", calls, executions, response)
	}
}

func TestToolMiddlewarePreservesNonToolCompletionWithoutExecution(t *testing.T) {
	for _, reason := range []chat.FinishReason{chat.FinishReasonStop, chat.FinishReasonLength, chat.FinishReasonContentFilter, chat.FinishReasonRefusal, chat.FinishReasonOther} {
		t.Run(reason.String(), func(t *testing.T) {
			executions := 0
			middleware, err := NewToolMiddleware(middlewareTool{name: "write", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
				executions++
				return chat.NewTextToolOutput("changed"), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			original := toolCallResponse(chat.ToolCall{ID: "call-1", Name: "write", Arguments: `{"value":"first"}`})
			original.Output.FinishReason = reason
			modelCalls := 0
			model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				modelCalls++
				return original, nil
			}))
			response, err := model.Call(t.Context(), textRequest("write"))
			if err != nil || response != original || executions != 0 || modelCalls != 1 {
				t.Fatalf("response preserved=%v error=%v executions=%d model calls=%d", response == original, err, executions, modelCalls)
			}
		})
	}
}

func toolCallResponse(calls ...chat.ToolCall) *chat.Response {
	parts := make([]chat.Part, len(calls))
	for index := range calls {
		parts[index] = chat.NewToolCallPart(calls[index])
	}
	return &chat.Response{Output: &chat.Output{
		Message:      new(chat.NewAssistantMessage(parts...)),
		FinishReason: chat.FinishReasonToolCalls,
	}}
}

func textResponse(text string) *chat.Response {
	return &chat.Response{Output: &chat.Output{
		Message:      new(chat.NewAssistantMessage(chat.NewTextPart(text))),
		FinishReason: chat.FinishReasonStop,
	}}
}
