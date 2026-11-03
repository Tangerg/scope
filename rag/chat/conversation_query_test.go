package chat_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func mustConversationQuery(t *testing.T, query rag.Query, history []chat.Message) ragchat.ConversationQuery {
	t.Helper()
	input, err := ragchat.NewConversationQuery(query, history)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestConversationQueryOwnsHistorySnapshot(t *testing.T) {
	history := []chat.Message{chat.NewUserMessage(chat.NewTextPart("original"))}
	input := mustConversationQuery(t, mustQuery(t, "follow up"), history)
	history[0].Parts[0].Text = "caller changed"
	returned := input.History()
	returned[0].Parts[0].Text = "consumer changed"
	if input.History()[0].Text() != "original" || input.Query().Text() != "follow up" {
		t.Fatal("conversation input was mutated through a borrowed message")
	}
	if _, err := ragchat.NewConversationQuery(rag.Query{}, nil); !errors.Is(err, rag.ErrInvalidQuery) {
		t.Fatalf("invalid query error = %v", err)
	}
	if _, err := ragchat.NewConversationQuery(mustQuery(t, "question"), []chat.Message{{}}); err == nil {
		t.Fatal("invalid history was accepted")
	}
}

func TestPreparerTransformsRetrievalWithoutRewritingActiveQuestion(t *testing.T) {
	var retrieved, augmented string
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: rag.RetrieverFunc(func(_ context.Context, query rag.Query) (rag.Candidates, error) {
			retrieved = query.Text()
			return nil, nil
		}),
		Augmenter: rag.AugmenterFunc(func(ctx context.Context, query rag.Query, candidates rag.Candidates) (rag.Augmentation, error) {
			augmented = query.Text()
			return rag.IdentityAugmenter().Augment(ctx, query, candidates)
		}),
		QueryTransformer: ragchat.ConversationTransformerFunc(func(_ context.Context, input ragchat.ConversationQuery) (rag.Query, error) {
			history := input.History()
			if len(history) != 1 || history[0].Text() != "topic" {
				t.Fatalf("history = %v", history)
			}
			history[0].Parts[0].Text = "attempted mutation"
			return input.Query().WithText("standalone retrieval query")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("topic")), chat.NewUserMessage(chat.NewTextPart("follow up")))
	if err != nil {
		t.Fatal(err)
	}
	prepared := mustPrepare(t, preparer, request)
	_, err = prepared.Call(t.Context(), chat.ModelFunc(func(_ context.Context, snapshot *chat.Request) (*chat.Response, error) {
		if snapshot.Messages[0].Text() != "topic" || snapshot.Messages[1].Text() != "follow up" {
			t.Fatalf("mutated request: %v", snapshot.Messages)
		}
		return textResponse("answer"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if retrieved != "standalone retrieval query" || augmented != "follow up" {
		t.Fatalf("retrieval=%q augmentation=%q", retrieved, augmented)
	}
}

func TestConversationTransformFailureDoesNotReachRetrieval(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		called := false
		preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
			Retriever: rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) { called = true; return nil, nil }),
			Augmenter: rag.IdentityAugmenter(),
			QueryTransformer: ragchat.ConversationTransformerFunc(func(_ context.Context, input ragchat.ConversationQuery) (rag.Query, error) {
				if canceled {
					cancel()
					return input.Query(), nil
				}
				return rag.Query{}, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
		_, err = preparer.Prepare(ctx, request)
		cancel()
		want := rag.ErrInvalidQuery
		if canceled {
			want = context.Canceled
		}
		if called || !errors.Is(err, want) {
			t.Fatalf("retrieved=%t error=%v want=%v", called, err, want)
		}
	}
}
