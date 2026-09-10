package chatclient

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolBatchErrorPreservesCompletedEffectsAndFailedCall(t *testing.T) {
	for _, test := range []struct {
		name          string
		invalidOutput bool
	}{{name: "execution failure"}, {name: "invalid output", invalidOutput: true}} {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("write failed after partial progress")
			failure, err := tool.NewFailure(cause, chat.NewTextToolOutput("partial write acknowledged"))
			if err != nil {
				t.Fatal(err)
			}
			var executed []string
			output := chat.NewTextToolOutput("first write acknowledged")
			middleware, err := NewToolMiddleware(middlewareTool{
				name: "write",
				call: func(_ context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
					executed = append(executed, string(invocation.Arguments()))
					if len(executed) == 1 {
						return output, nil
					}
					if test.invalidOutput {
						return chat.ToolOutput{Details: []byte("{")}, nil
					}
					return chat.ToolOutput{}, failure
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			failedCall := chat.ToolCall{ID: "second", Name: "write", Arguments: `{"value":"second"}`}
			modelCalls := 0
			model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				modelCalls++
				return toolCallResponse(
					chat.ToolCall{ID: "first", Name: "write", Arguments: `{"value":"first"}`},
					failedCall,
					chat.ToolCall{ID: "third", Name: "write", Arguments: `{"value":"third"}`},
				), nil
			}))
			response, err := model.Call(t.Context(), textRequest("write"))
			batchError, ok := errors.AsType[*ToolBatchError](err)
			if !ok || response != nil || modelCalls != 1 || len(executed) != 2 {
				t.Fatalf("response = %v, error = %v, model calls = %d, executed = %v", response, err, modelCalls, executed)
			}
			if !test.invalidOutput && (!errors.Is(err, cause) || !errors.Is(err, failure)) {
				t.Fatalf("error lost failure cause: %v", err)
			}
			if test.invalidOutput && !errors.Is(err, chat.ErrInvalidToolOutput) {
				t.Fatalf("error lost invalid output cause: %v", err)
			}
			want := []chat.ToolResult{{ID: "first", Name: "write", Output: output.Clone()}}
			if !reflect.DeepEqual(batchError.Completed(), want) || batchError.FailedCall() != failedCall {
				t.Fatalf("completed = %#v, failed = %#v", batchError.Completed(), batchError.FailedCall())
			}
			output.Content[0].Text = "tool mutated its output"
			completed := batchError.Completed()
			completed[0].Output.Content[0].Text = "caller mutated its snapshot"
			if !reflect.DeepEqual(batchError.Completed(), want) {
				t.Fatal("batch error retained mutable output storage")
			}
		})
	}
}
