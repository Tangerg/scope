package chat_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestStreamingModelPreservesKnownGenerationOnLateFailure(t *testing.T) {
	failure := errors.New("postprocessing failed")
	model, err := chat.NewStreamModel(chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("known")}, FinishReason: chat.FinishReasonStop, Metadata: &chat.ResponseMetadata{Usage: &chat.Usage{OutputTokens: 5}}}, nil) {
				yield(nil, failure)
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("input")))
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), request)
	if !errors.Is(err, failure) || response == nil || response.Text() != "known" || response.Metadata.Usage.OutputTokens != 5 {
		t.Fatalf("generation and failure: %#v, %v", response, err)
	}
	if _, claimsDiscovery := any(model).(interface {
		ListModels(context.Context, int, int64) ([]string, error)
	}); claimsDiscovery {
		t.Fatal("wire compatibility advertised model discovery")
	}
	if _, claimsCounting := any(model).(interface {
		CountInputTokens(context.Context, *chat.Request) (int64, error)
	}); claimsCounting {
		t.Fatal("wire compatibility advertised input token counting")
	}
}
