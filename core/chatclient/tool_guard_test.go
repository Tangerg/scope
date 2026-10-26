package chatclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestGuardedFuncToolMiddleware(t *testing.T) {
	var authorizations, executions, modelCalls int
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "lookup"}, func(_ context.Context, input struct {
		Query string `json:"query"`
	}) (string, error) {
		executions++
		return input.Query, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(_ context.Context, authorization tool.Authorization) (bool, error) {
			authorizations++
			if authorization.Definition().Name != "lookup" || string(authorization.Arguments()) != `{"query":"scope"}` {
				t.Fatalf("authorization = %v, %s", authorization.Definition(), authorization.Arguments())
			}
			return true, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := chatclient.NewSingleBatchToolMiddleware(guard)
	if err != nil {
		t.Fatal(err)
	}
	model := middleware(chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		modelCalls++
		if modelCalls == 1 {
			return &chat.Response{Output: &chat.Output{
				FinishReason: chat.FinishReasonToolCalls,
				Message: new(chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
					ID: "call", Name: "lookup", Arguments: `{"query":"scope"}`,
				}))),
			}}, nil
		}
		result := request.Messages[len(request.Messages)-1].Parts[0].ToolResult
		if result.ID != "call" || result.Output.Content[0].Text != "scope" {
			t.Fatalf("result = %#v", result)
		}
		return &chat.Response{Output: &chat.Output{
			FinishReason: chat.FinishReasonStop,
			Message:      new(chat.NewAssistantMessage(chat.NewTextPart("done"))),
		}}, nil
	}))
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("find scope")))
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), request)
	if err != nil || response.Text() != "done" || authorizations != 1 || executions != 1 || modelCalls != 2 {
		t.Fatalf("response = %v, error = %v, authorizations = %d, executions = %d, model calls = %d",
			response, err, authorizations, executions, modelCalls)
	}
}

func TestToolMiddlewarePreservesGuardRejection(t *testing.T) {
	var executions, modelCalls int
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "write"}, func(context.Context, struct{}) (string, error) {
		executions++
		return "written", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
			return false, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := chatclient.NewSingleBatchToolMiddleware(guard)
	if err != nil {
		t.Fatal(err)
	}
	call := chat.ToolCall{ID: "write", Name: "write", Arguments: `{}`}
	model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls++
		return &chat.Response{Output: &chat.Output{
			FinishReason: chat.FinishReasonToolCalls,
			Message:      new(chat.NewAssistantMessage(chat.NewToolCallPart(call))),
		}}, nil
	}))
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("write")))
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), request)
	if response != nil || executions != 0 || modelCalls != 1 || chat.ClassifyFailure(err) != chat.CallFailureFailed {
		t.Fatalf("response=%v executions=%d model calls=%d kind=%s error=%v", response, executions, modelCalls, chat.ClassifyFailure(err), err)
	}
	failure, ok := errors.AsType[*tool.Failure](err)
	if !ok || failure.Kind() != tool.FailureKindRejected {
		t.Fatalf("rejection lost: %v", err)
	}
	batch, ok := errors.AsType[*chatclient.ToolBatchError](err)
	if !ok || len(batch.Completed()) != 0 || batch.FailedCall() != call {
		t.Fatalf("batch evidence lost: %v", err)
	}
}
