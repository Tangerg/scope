package chat_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func TestPreparedStreamPreservesFailureEvidenceAndStopsAtItsBoundary(t *testing.T) {
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: &stubRetriever{}, Augmenter: rag.IdentityAugmenter()})
	if err != nil {
		t.Fatal(err)
	}
	prepared := mustPrepare(t, preparer, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}})
	cause := errors.New("provider failed")
	for _, test := range []struct {
		name  string
		delta *chat.ResponseDelta
		err   error
		want  error
	}{
		{name: "empty event", want: ragchat.ErrNilResponse},
		{name: "failure", err: cause, want: cause},
		{name: "evidence with failure", delta: &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("partial")}}, err: cause, want: cause},
	} {
		t.Run(test.name, func(t *testing.T) {
			closed := false
			streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
				return func(yield func(*chat.ResponseDelta, error) bool) {
					defer func() { closed = true }()
					if yield(test.delta, test.err) {
						t.Fatal("failed stream asked for another event")
					}
				}
			})
			count := 0
			for delta, err := range prepared.Stream(t.Context(), streamer) {
				count++
				if delta != test.delta || !errors.Is(err, test.want) {
					t.Fatalf("event = %v, %v", delta, err)
				}
			}
			if count != 1 || !closed {
				t.Fatalf("count=%d closed=%t", count, closed)
			}
		})
	}
	for delta, err := range prepared.Stream(t.Context(), chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] { return nil })) {
		if delta != nil || !errors.Is(err, ragchat.ErrNilStreamSequence) {
			t.Fatalf("nil sequence = %v, %v", delta, err)
		}
	}
	for _, responseErr := range []error{nil, cause} {
		response, err := prepared.Call(t.Context(), chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return nil, responseErr }))
		want := responseErr
		if want == nil {
			want = ragchat.ErrNilResponse
		}
		if response != nil || !errors.Is(err, want) {
			t.Fatalf("nil response = %v, %v", response, err)
		}
	}
}

func TestPreparedStreamReleasesProducerWhenConsumerStops(t *testing.T) {
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{Retriever: &stubRetriever{}, Augmenter: rag.IdentityAugmenter()})
	if err != nil {
		t.Fatal(err)
	}
	prepared := mustPrepare(t, preparer, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}})
	started, closed := false, false
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		started = true
		return func(yield func(*chat.ResponseDelta, error) bool) {
			defer func() { closed = true }()
			if yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("first")}}, nil) {
				t.Fatal("stopped consumer requested a second event")
			}
		}
	})
	sequence := prepared.Stream(t.Context(), streamer)
	if started {
		t.Fatal("stream started before iteration")
	}
	for range sequence {
		break
	}
	if !started || !closed {
		t.Fatalf("started=%t closed=%t", started, closed)
	}
}
