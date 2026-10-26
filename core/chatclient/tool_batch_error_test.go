package chatclient

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolBatchDoesNotClaimNestedModelNonExecution(t *testing.T) {
	cause := &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: errors.New("nested provider rejected the call")}
	toolCalls := 0
	middleware, err := NewSingleBatchToolMiddleware(middlewareTool{name: "write", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
		toolCalls++
		return chat.ToolOutput{}, cause
	}})
	if err != nil {
		t.Fatal(err)
	}
	model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return toolCallResponse(chat.ToolCall{ID: "write", Name: "write", Arguments: `{"value":"one"}`}), nil
	}))
	response, err := model.Call(t.Context(), textRequest("write"))
	if response != nil || toolCalls != 1 || chat.ClassifyFailure(err) != chat.CallFailureUnknown || !errors.Is(err, cause) {
		t.Fatalf("nested failure escaped the Tool boundary: response=%v calls=%d error=%v", response, toolCalls, err)
	}
}

func TestToolBatchFailureClassification(t *testing.T) {
	cause := errors.New("write outcome unavailable")
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: cause, Output: chat.NewTextToolOutput("write acknowledged; later work failed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := tool.NewCallError(tool.CallErrorConfig{Cause: cause, Evidence: chat.NewTextToolOutput("write started")})
	if err != nil {
		t.Fatal(err)
	}
	wrappedFailure, err := tool.NewCallError(tool.CallErrorConfig{Cause: failure, Evidence: chat.NewTextToolOutput("write started")})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		cause error
		kind  chat.CallFailureKind
	}{
		{name: "definite failure", cause: failure, kind: chat.CallFailureFailed},
		{name: "wrapped definite failure", cause: fmt.Errorf("write: %w", failure), kind: chat.CallFailureFailed},
		{name: "joined diagnostic", cause: errors.Join(errors.New("host diagnostic"), failure), kind: chat.CallFailureFailed},
		{name: "evidence around definite failure", cause: wrappedFailure, kind: chat.CallFailureFailed},
		{name: "ordinary error", cause: cause, kind: chat.CallFailureUnknown},
		{name: "non-final evidence", cause: evidence, kind: chat.CallFailureUnknown},
		{name: "invalid failure", cause: new(tool.Failure), kind: chat.CallFailureUnknown},
		{name: "typed nil failure", cause: (*tool.Failure)(nil), kind: chat.CallFailureUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			var executions, modelCalls int
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "write"}, func(context.Context, struct{}) (string, error) {
				executions++
				return "", test.cause
			})
			if err != nil {
				t.Fatal(err)
			}
			middleware, err := NewSingleBatchToolMiddleware(executable)
			if err != nil {
				t.Fatal(err)
			}
			call := chat.ToolCall{ID: "write", Name: "write", Arguments: `{}`}
			model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				modelCalls++
				return toolCallResponse(call), nil
			}))
			response, err := model.Call(t.Context(), textRequest("write"))
			if response != nil || executions != 1 || modelCalls != 1 || chat.ClassifyFailure(err) != test.kind || !errors.Is(err, test.cause) {
				t.Fatalf("response=%v executions=%d model calls=%d kind=%s error=%v", response, executions, modelCalls, chat.ClassifyFailure(err), err)
			}
			batch, ok := errors.AsType[*ToolBatchError](err)
			if !ok || len(batch.Completed()) != 0 || batch.FailedCall() != call {
				t.Fatalf("batch evidence lost: %v", err)
			}
		})
	}
}

func TestToolBatchErrorPreservesCompletedEffectsAndFailedCall(t *testing.T) {
	for _, test := range []struct {
		name          string
		invalidOutput bool
		kind          chat.CallFailureKind
	}{
		{name: "execution failure", kind: chat.CallFailureFailed},
		{name: "invalid output", invalidOutput: true, kind: chat.CallFailureUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("write failed after partial progress")
			failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: cause, Output: chat.NewTextToolOutput("partial write acknowledged")})
			if err != nil {
				t.Fatal(err)
			}
			var executed []string
			output := chat.NewTextToolOutput("first write acknowledged")
			middleware, err := NewSingleBatchToolMiddleware(middlewareTool{
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
			proposal := toolCallResponse(
				chat.ToolCall{ID: "first", Name: "write", Arguments: `{"value":"first"}`}, failedCall,
				chat.ToolCall{ID: "third", Name: "write", Arguments: `{"value":"third"}`},
			)
			proposal.Output.Message.Parts = append([]chat.Part{chat.NewTextPart("proposed operations")}, proposal.Output.Message.Parts...)
			proposal.Metadata = &chat.ResponseMetadata{ID: "proposal-id"}
			wantProposal := proposal.Clone()
			modelCalls := 0
			model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				modelCalls++
				return proposal, nil
			}))
			response, err := model.Call(t.Context(), textRequest("write"))
			batchError, ok := errors.AsType[*ToolBatchError](err)
			if !ok || response != nil || modelCalls != 1 || len(executed) != 2 {
				t.Fatalf("response = %v, error = %v, model calls = %d, executed = %v", response, err, modelCalls, executed)
			}
			if kind := chat.ClassifyFailure(err); kind != test.kind {
				t.Fatalf("failure kind = %s, want %s", kind, test.kind)
			}
			if !test.invalidOutput && (!errors.Is(failure.Cause(), cause) || !errors.Is(err, failure) || errors.Is(err, cause)) {
				t.Fatalf("error lost failure cause: %v", err)
			}
			if test.invalidOutput && !errors.Is(err, chat.ErrInvalidToolOutput) {
				t.Fatalf("error lost invalid output cause: %v", err)
			}
			want := []chat.ToolResult{{ID: "first", Name: "write", Output: output.Clone()}}
			if !reflect.DeepEqual(batchError.Completed(), want) || batchError.FailedCall() != failedCall {
				t.Fatalf("completed = %#v, failed = %#v", batchError.Completed(), batchError.FailedCall())
			}
			if !reflect.DeepEqual(batchError.Proposal(), wantProposal) {
				t.Fatal("batch failure lost full proposal")
			}
			proposal.Output.Message.Parts[3].ToolCall.Arguments = "mutated"
			snapshot := batchError.Proposal()
			snapshot.Output.Message.Parts[0].Text = "mutated"
			if !reflect.DeepEqual(batchError.Proposal(), wantProposal) {
				t.Fatal("proposal snapshot is not independently owned")
			}
			input := batchError.Request()
			if input.Messages[0].Text() != "write" || len(input.Tools) != 1 {
				t.Fatalf("original model input = %#v", input)
			}
			input.Messages[0].Parts[0].Text = "mutated"
			if batchError.Request().Messages[0].Text() != "write" {
				t.Fatal("request snapshot is not independently owned")
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

type suspendingBatchTool struct{ calls int }

func (*suspendingBatchTool) Definition() chat.ToolDefinition {
	return middlewareTool{name: "confirm"}.Definition()
}

func (s *suspendingBatchTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	s.calls++
	if s.calls == 1 {
		return tool.Result{Output: chat.NewTextToolOutput("first completed")}, nil
	}
	schema, err := jsonschema.Parse([]byte(`{"type":"boolean"}`))
	if err != nil {
		return tool.Result{}, err
	}
	request, err := tool.NewInputRequest(tool.InputRequestConfig{Prompt: []byte(`"confirm?"`), ResponseSchema: schema, ContinuationState: []byte(`{"step":2}`)})
	return tool.Result{InputRequest: &request}, err
}

func TestSynchronousBatchRetainsInputCheckpointAndCompletedPrefix(t *testing.T) {
	executable := new(suspendingBatchTool)
	middleware, err := NewSingleBatchToolMiddleware(executable)
	if err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	model := middleware(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls++
		return toolCallResponse(chat.ToolCall{ID: "first", Name: "confirm", Arguments: `{"value":"one"}`}, chat.ToolCall{ID: "second", Name: "confirm", Arguments: `{"value":"two"}`}, chat.ToolCall{ID: "third", Name: "confirm", Arguments: `{"value":"three"}`}), nil
	}))
	response, err := model.Call(t.Context(), textRequest("write"))
	var failure *ToolBatchError
	if response != nil || !errors.As(err, &failure) || len(failure.Completed()) != 1 || failure.FailedCall().ID != "second" || executable.calls != 2 || modelCalls != 1 {
		t.Fatalf("response=%v error=%v tool calls=%d model calls=%d", response, err, executable.calls, modelCalls)
	}
	if kind := chat.ClassifyFailure(err); kind != chat.CallFailureFailed {
		t.Fatalf("unsupported continuation kind = %s, want %s", kind, chat.CallFailureFailed)
	}
	request, present := failure.InputRequest()
	if !present || string(request.Prompt()) != `"confirm?"` {
		t.Fatalf("checkpoint=%+v present=%t", request, present)
	}
	continuation, err := request.Continuation([]byte(`true`))
	if err != nil {
		t.Fatal(err)
	}
	if string(continuation.State()) != `{"step":2}` {
		t.Fatal("Tool checkpoint changed")
	}
}
