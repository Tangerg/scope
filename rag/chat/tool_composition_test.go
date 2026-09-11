package chat_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func TestPreparedRequestPreservesEvidenceThroughToolContinuation(t *testing.T) {
	evidence := rag.Candidates{{Document: &document.Document{ID: "fact", Text: "retrieved evidence"}}}
	retrievals := 0
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: rag.RetrieverFunc(func(_ context.Context, query rag.Query) (rag.Candidates, error) {
			retrievals++
			if query.Text() != "question" {
				t.Fatalf("query = %q", query.Text())
			}
			return evidence.Clone(), nil
		}),
		Augmenter: rag.AugmenterFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Augmentation, error) {
			return rag.NewAugmentation("question with retrieved evidence")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	executions := 0
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "write", Description: "write a value"},
		func(context.Context, struct{}) (string, error) {
			executions++
			return "acknowledged", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := chatclient.NewToolMiddleware(executable)
	if err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		modelCalls++
		if request.Messages[0].Text() != "question with retrieved evidence" {
			t.Fatalf("model call %d lost evidence: %v", modelCalls, request.Messages)
		}
		if modelCalls == 1 {
			return &chat.Response{Output: &chat.Output{
				Message: new(chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
					ID: "write-1", Name: "write", Arguments: `{}`,
				}))),
				FinishReason: chat.FinishReasonToolCalls,
			}}, nil
		}
		if len(request.Messages) != 3 || request.Messages[2].Role != chat.RoleTool {
			t.Fatalf("continuation = %#v", request.Messages)
		}
		return textResponse("done"), nil
	})
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	if err != nil {
		t.Fatal(err)
	}
	prepared := mustPrepare(t, preparer, request)
	request.Messages[0].Parts[0].Text = "caller reused input"
	response, err := prepared.Call(t.Context(), middleware(model))
	if err != nil || response.Text() != "done" || retrievals != 1 || executions != 1 || modelCalls != 2 {
		t.Fatalf("response = %v, error = %v, retrievals = %d, tools = %d, model calls = %d", response, err, retrievals, executions, modelCalls)
	}
	candidates, found, err := ragchat.CandidatesFromMetadata(response.Metadata)
	if err != nil || !found || !reflect.DeepEqual(candidates, evidence) {
		t.Fatalf("evidence = %#v, found = %v, error = %v", candidates, found, err)
	}
}
