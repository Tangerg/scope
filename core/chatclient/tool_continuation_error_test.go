package chatclient

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolContinuationOwnsTheCompleteCallFailure(t *testing.T) {
	for _, kind := range []chat.CallFailureKind{chat.CallFailureNotExecuted, chat.CallFailureFailed, chat.CallFailureUnknown} {
		t.Run(string(kind), func(t *testing.T) {
			cause := &chat.CallFailure{Kind: kind, Cause: errors.New("continuation failed")}
			writes, calls := 0, 0
			middleware, err := NewSingleBatchToolMiddleware(middlewareTool{name: "write", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
				writes++
				return chat.NewTextToolOutput("committed"), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				calls++
				if calls == 1 {
					return toolCallResponse(chat.ToolCall{ID: "write", Name: "write", Arguments: `{"value":"one"}`}), nil
				}
				return nil, fmt.Errorf("inner model: %w", cause)
			}))
			response, err := model.Call(t.Context(), textRequest("write"))
			want := kind
			if want == chat.CallFailureNotExecuted {
				want = chat.CallFailureFailed
			}
			failure, found := errors.AsType[*ToolContinuationError](err)
			if response != nil || chat.ClassifyFailure(err) != want || !errors.Is(err, cause) || !found || len(failure.Completed()) != 1 || writes != 1 || calls != 2 {
				t.Fatalf("kind=%s error=%v writes=%d calls=%d", chat.ClassifyFailure(err), err, writes, calls)
			}
		})
	}
}

func TestToolContinuationRetainsEffectsAndResumesOnlyModel(t *testing.T) {
	cause := errors.New("model unavailable")
	executions := 0
	middleware, err := NewSingleBatchToolMiddleware(middlewareTool{
		name: "write",
		call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
			executions++
			return chat.NewTextToolOutput("write acknowledged"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	var continuation *chat.Request
	partial := textResponse("partial answer")
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		modelCalls++
		switch modelCalls {
		case 1:
			return toolCallResponse(chat.ToolCall{ID: "write-1", Name: "write", Arguments: `{"value":"first"}`}), nil
		case 2:
			continuation = request.Clone()
			return partial, cause
		default:
			if !reflect.DeepEqual(request, continuation) {
				t.Fatalf("resumed request = %#v, want %#v", request, continuation)
			}
			return textResponse("done"), nil
		}
	})
	request := textRequest("write")
	response, err := middleware(model).Call(t.Context(), request)
	failure, ok := errors.AsType[*ToolContinuationError](err)
	if !ok || !errors.Is(err, cause) || response != partial || executions != 1 || modelCalls != 2 {
		t.Fatalf("response = %v, error = %v, executions = %d, model calls = %d", response, err, executions, modelCalls)
	}
	want := []chat.ToolResult{{ID: "write-1", Name: "write", Output: chat.NewTextToolOutput("write acknowledged")}}
	if !reflect.DeepEqual(failure.Completed(), want) || !reflect.DeepEqual(failure.Request(), continuation) {
		t.Fatalf("completed = %#v, continuation = %#v", failure.Completed(), failure.Request())
	}
	request.Messages[0].Parts[0].Text = "caller reused request"
	failure.Completed()[0].Output.Content[0].Text = "caller reused results"
	failure.Request().Messages[2].Parts[0].ToolResult.Output.Content[0].Text = "caller reused continuation"
	if !reflect.DeepEqual(failure.Completed(), want) {
		t.Fatal("completed effects share caller storage")
	}
	response, err = model.Call(t.Context(), failure.Request())
	if err != nil || response.Text() != "done" || executions != 1 || modelCalls != 3 {
		t.Fatalf("resume response = %v, error = %v, executions = %d, model calls = %d", response, err, executions, modelCalls)
	}
}
