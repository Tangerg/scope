package chatclient

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/inmemory"
	"github.com/Tangerg/scope/core/tool"
)

func TestSingleBatchHistoryOrder(t *testing.T) {
	for _, fullExchange := range []bool{false, true} {
		store := new(inmemory.Store)
		ctx := t.Context()
		old := []chat.Message{chat.NewUserMessage(chat.NewTextPart("old")), chat.NewAssistantMessage(chat.NewTextPart("answer"))}
		if _, err := store.Write(ctx, "conversation", old...); err != nil {
			t.Fatal(err)
		}
		recorder, err := history.NewMiddleware(history.MiddlewareConfig{Store: store, ConversationID: "conversation"})
		if err != nil {
			t.Fatal(err)
		}
		batch, err := NewSingleBatchToolMiddleware(middlewareTool{name: "write", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
			return chat.NewTextToolOutput("acknowledged"), nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		proposal := toolCallResponse(chat.ToolCall{ID: "call", Name: "write", Arguments: `{"value":"new"}`})
		final := &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("done"))), FinishReason: chat.FinishReasonStop}}
		calls := 0
		model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
			calls++
			if calls == 1 {
				return proposal.Clone(), nil
			}
			return final.Clone(), nil
		})
		combined := recorder.Call(batch(model))
		if fullExchange {
			combined = batch(recorder.Call(model))
		}
		request := textRequest("new")
		if _, callErr := combined.Call(ctx, request); callErr != nil {
			t.Fatal(callErr)
		}
		stored, err := store.Read(ctx, "conversation")
		if err != nil {
			t.Fatal(err)
		}
		want := append(old, request.Messages...)
		if fullExchange {
			want = append(want, *proposal.Output.Message, chat.NewToolMessage(chat.ToolResult{ID: "call", Name: "write", Output: chat.NewTextToolOutput("acknowledged")}))
		}
		want = append(want, *final.Output.Message)
		if !reflect.DeepEqual(stored, want) || calls != 2 {
			t.Fatalf("full exchange=%v: stored=%#v want=%#v calls=%d", fullExchange, stored, want, calls)
		}
	}
}

func TestSingleBatchMiddlewareCannotBeStacked(t *testing.T) {
	batch, err := NewSingleBatchToolMiddleware(middlewareTool{name: "write", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
		t.Fatal("tool executed")
		return chat.ToolOutput{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	model := batch(batch(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { t.Fatal("model called"); return nil, nil })))
	if _, err := model.Call(t.Context(), textRequest("request")); !errors.Is(err, ErrInvalidToolBatch) {
		t.Fatalf("error = %v", err)
	}
}

type failingHistory struct{ cause error }

func (f failingHistory) Read(context.Context, history.ConversationID) ([]chat.Message, error) {
	return nil, nil
}

func (f failingHistory) Write(context.Context, history.ConversationID, ...chat.Message) (history.WriteOutcome, error) {
	return history.WriteOutcome{}, f.cause
}

func TestToolMiddlewareRetainsResponseOnHistoryCommitFailure(t *testing.T) {
	cause := errors.New("history unavailable")
	recorder, err := history.NewMiddleware(history.MiddlewareConfig{Store: failingHistory{cause: cause}, ConversationID: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := NewSingleBatchToolMiddleware(middlewareTool{name: "write", call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) {
		t.Fatal("tool ran after commit failure")
		return chat.ToolOutput{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls++
		return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("completed"))), FinishReason: chat.FinishReasonStop}, Metadata: &chat.ResponseMetadata{ID: "response-1"}}, nil
	})
	response, err := batch(recorder.Call(model)).Call(t.Context(), textRequest("request"))
	var commit *history.CommitError
	if !errors.As(err, &commit) || !errors.Is(err, cause) || response == nil || response.Text() != "completed" || response.Metadata.ID != "response-1" || calls != 1 {
		t.Fatalf("response=%#v error=%v calls=%d", response, err, calls)
	}
}
