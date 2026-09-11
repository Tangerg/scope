package chat_test

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func BenchmarkPreparerRetrievalMetadata(b *testing.B) {
	candidates := make(rag.Candidates, 16)
	for index := range candidates {
		candidates[index] = rag.Candidate{Document: &document.Document{Text: strings.Repeat("text", 1024)}}
	}
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) {
			return candidates.Clone(), nil
		}),
		Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		b.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("question")))
	if err != nil {
		b.Fatal(err)
	}
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			for index := range 64 {
				delta := &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("word")}}
				if index == 63 {
					delta.FinishReason = chat.FinishReasonStop
				}
				if !yield(delta, nil) {
					return
				}
			}
		}
	})
	b.ReportAllocs()
	for b.Loop() {
		prepared, prepareErr := preparer.Prepare(b.Context(), request)
		if prepareErr != nil {
			b.Fatal(prepareErr)
		}
		for _, streamErr := range prepared.Stream(b.Context(), streamer) {
			if streamErr != nil {
				b.Fatal(streamErr)
			}
		}
	}
}
