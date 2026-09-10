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

func TestMiddlewareRejectsAmbiguousTextRewrite(t *testing.T) {
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
	middleware, err := ragchat.NewMiddleware(ragchat.MiddlewareConfig{
		Retriever: &stubRetriever{},
		Augmenter: rag.AugmenterFunc(func(context.Context, rag.Query, rag.Candidates) (rag.Augmentation, error) {
			return rag.NewAugmentation("rewritten text")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		called = true
		return textResponse("answer"), nil
	})
	if _, callErr := middleware.Call(model).Call(t.Context(), request); !errors.Is(callErr, rag.ErrInvalidAugmentation) || called {
		t.Fatalf("Call = %v, model called = %t", callErr, called)
	}
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		called = true
		return nil
	})
	for delta, streamErr := range middleware.Stream(streamer).Stream(t.Context(), request) {
		if delta != nil || !errors.Is(streamErr, rag.ErrInvalidAugmentation) || called {
			t.Fatalf("Stream = %v, %v, model called = %t", delta, streamErr, called)
		}
	}
}

func TestMiddlewareRejectsRetrievalBeforeAugmentation(t *testing.T) {
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
			middleware, err := ragchat.NewMiddleware(ragchat.MiddlewareConfig{
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
			model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				t.Fatal("invalid retrieval reached model")
				return nil, nil
			})
			if _, callErr := middleware.Call(model).Call(ctx, request); !errors.Is(callErr, test.want) {
				t.Fatalf("Call error = %v, want %v", callErr, test.want)
			}
		})
	}
}

func TestMiddlewareStreamPublishesRetrievalMetadataOnce(t *testing.T) {
	doc, err := document.NewDocument("evidence", nil)
	if err != nil {
		t.Fatal(err)
	}
	augmenter, err := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := ragchat.NewMiddleware(ragchat.MiddlewareConfig{Retriever: &stubRetriever{docs: rag.Candidates{candidate(doc)}}, Augmenter: augmenter})
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	if err != nil {
		t.Fatal(err)
	}
	complete, err := middleware.Call(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("abc"), nil
	})).Call(t.Context(), request)
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
	var candidatePayloads, citationPayloads int
	for delta, streamErr := range middleware.Stream(streamer).Stream(t.Context(), request) {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		if _, found, decodeErr := ragchat.CandidatesFromMetadata(delta.Metadata); decodeErr != nil {
			t.Fatal(decodeErr)
		} else if found {
			candidatePayloads++
		}
		if _, found, decodeErr := ragchat.CitationsFromMetadata(delta.Metadata); decodeErr != nil {
			t.Fatal(decodeErr)
		} else if found {
			citationPayloads++
		}
		if addErr := accumulator.Add(delta); addErr != nil {
			t.Fatal(addErr)
		}
	}
	streamed, err := accumulator.Response()
	if err != nil {
		t.Fatal(err)
	}
	if candidatePayloads != 1 || citationPayloads != 1 || !reflect.DeepEqual(streamed, complete) {
		t.Fatalf("payload counts = %d, %d; streamed = %#v, complete = %#v", candidatePayloads, citationPayloads, streamed, complete)
	}
}
