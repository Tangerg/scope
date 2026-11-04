package chat_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func TestPreparerPreservesOriginalPromptParts(t *testing.T) {
	image, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	emptyContext, err := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{AllowEmptyContext: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []struct {
		name      string
		augmenter rag.Augmenter
	}{
		{"identity", rag.IdentityAugmenter()},
		{"empty context", emptyContext},
	} {
		for _, input := range []struct {
			name  string
			parts []chat.Part
		}{
			{"padded text", []chat.Part{chat.NewTextPart("\n  indented question\n")}},
			{"multiple text parts", []chat.Part{chat.NewTextPart(" before"), chat.NewMediaPart(image), chat.NewTextPart(" after ")}},
		} {
			t.Run(stage.name+"/"+input.name, func(t *testing.T) {
				preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: &stubRetriever{}, Augmenter: stage.augmenter})
				if err != nil {
					t.Fatal(err)
				}
				request, err := chat.NewRequest(chat.NewUserMessage(input.parts...))
				if err != nil {
					t.Fatal(err)
				}
				prepared := mustPrepare(t, preparer, request)
				_, err = prepared.Call(t.Context(), chat.ModelFunc(func(_ context.Context, got *chat.Request) (*chat.Response, error) {
					if !reflect.DeepEqual(got.Messages, request.Messages) {
						t.Fatalf("preserved messages = %#v, want %#v", got.Messages, request.Messages)
					}
					return textResponse("answer"), nil
				}))
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestPreparerHonorsExplicitReplacementOfQueryText(t *testing.T) {
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: &stubRetriever{},
		Augmenter: rag.AugmenterFunc(func(_ context.Context, query rag.Query, _ rag.Candidates) (rag.Augmentation, error) {
			return rag.NewAugmentation(query.Text())
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("\n  question\n")))
	if err != nil {
		t.Fatal(err)
	}
	prepared := mustPrepare(t, preparer, request)
	_, err = prepared.Call(t.Context(), chat.ModelFunc(func(_ context.Context, got *chat.Request) (*chat.Response, error) {
		if text := got.Messages[0].Text(); text != "question" {
			t.Fatalf("explicit replacement = %q, want question", text)
		}
		return textResponse("answer"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ambiguous, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question"), chat.NewTextPart(" tail")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparer.Prepare(t.Context(), ambiguous); !errors.Is(err, rag.ErrInvalidAugmentation) {
		t.Fatalf("replacement of multiple parts = %v, want invalid augmentation", err)
	}
}
