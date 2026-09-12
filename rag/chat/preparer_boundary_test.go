package chat_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func TestPreparerRejectsAmbiguousTextRewrite(t *testing.T) {
	image, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(
		chat.NewTextPart("before"), chat.NewMediaPart(image), chat.NewTextPart("after"),
	))
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: &stubRetriever{},
		Augmenter: rag.AugmenterFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Augmentation, error) {
			return rag.NewAugmentation("rewritten text")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, prepareErr := preparer.Prepare(t.Context(), request); !errors.Is(prepareErr, rag.ErrInvalidAugmentation) {
		t.Fatalf("Prepare = %v", prepareErr)
	}
}

func TestPreparerRejectsRetrievalBeforeAugmentation(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
		want   error
	}{
		{name: "invalid candidate", want: rag.ErrInvalidCandidate},
		{name: "canceled retrieval", cancel: true, want: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
				Retriever: rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) {
					if test.cancel {
						cancel()
						return nil, nil
					}
					return rag.Candidates{{}}, nil
				}),
				Augmenter: rag.AugmenterFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Augmentation, error) {
					t.Fatal("invalid retrieval reached augmentation")
					return rag.Augmentation{}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
			if err != nil {
				t.Fatal(err)
			}
			if _, prepareErr := preparer.Prepare(ctx, request); !errors.Is(prepareErr, test.want) {
				t.Fatalf("Prepare error = %v, want %v", prepareErr, test.want)
			}
		})
	}
}

func TestPreparerStreamPublishesRetrievalMetadataOnce(t *testing.T) {
	doc, err := document.NewDocument("evidence", nil)
	if err != nil {
		t.Fatal(err)
	}
	augmenter, err := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: &stubRetriever{docs: rag.Candidates{candidate(doc)}}, Augmenter: augmenter})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	if err != nil {
		t.Fatal(err)
	}
	prepared := mustPrepare(t, preparer, request)
	complete, err := prepared.Call(t.Context(), chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("abc"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			for index, text := range []string{"a", "b", "c"} {
				delta := &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(text)}}
				if index == 2 {
					delta.FinishReason = chat.FinishReasonStop
				}
				if !yield(delta, nil) {
					return
				}
			}
		}
	})
	var accumulator chat.ResponseAccumulator

	for delta, streamErr := range prepared.Stream(t.Context(), streamer) {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		if delta.Metadata != nil {
			t.Fatal("retrieval changed delta metadata")
		}
		if addErr := accumulator.Add(delta); addErr != nil {
			t.Fatal(addErr)
		}
	}
	streamed, err := accumulator.Response()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(streamed, complete) {
		t.Fatalf("streamed = %#v, complete = %#v", streamed, complete)
	}
	evidence := prepared.Evidence()
	if len(evidence.Candidates) != 1 || len(evidence.Citations) != 1 {
		t.Fatalf("evidence = %#v", evidence)
	}
}
