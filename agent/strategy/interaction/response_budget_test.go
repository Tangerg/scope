package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestModelStreamAdmitsEncodedBytesBeforeAccumulation(t *testing.T) {
	inline, err := media.NewBytes("image/png", []byte(strings.Repeat("x", 400)))
	if err != nil {
		t.Fatal(err)
	}
	for name, delta := range map[string]*chat.ResponseDelta{
		"many small fragments": {Parts: []chat.PartDelta{chat.NewTextDelta("x")}},
		"escaped text":         {Parts: []chat.PartDelta{chat.NewTextDelta(strings.Repeat("<", 90))}},
		"tool arguments":       {Parts: []chat.PartDelta{chat.NewToolCallDelta(chat.ToolCallDelta{ID: "call", Name: "write", Arguments: strings.Repeat("x", 400)})}},
		"metadata":             {Metadata: &chat.ResponseMetadata{Extra: metadata.Map{"test/large": json.RawMessage(`"` + strings.Repeat("x", 400) + `"`)}}},
		"media":                {Parts: []chat.PartDelta{chat.NewMediaDelta(inline)}},
		"reasoning":            {Parts: []chat.PartDelta{chat.NewReasoningDelta("", []byte(strings.Repeat("x", 400)))}},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := encodeModelResponseDelta(delta)
			if err != nil {
				t.Fatal(err)
			}
			const limit = 512
			produced, observed := 0, 0
			closed := false
			dispatcher := &Dispatcher{streamer: chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
				return func(yield func(*chat.ResponseDelta, error) bool) {
					defer func() { closed = true }()
					for range 1000 {
						produced++
						if !yield(delta, nil) {
							return
						}
					}
				}
			})}
			response, err := dispatcher.callModel(t.Context(), nil, func(json.RawMessage) { observed++ }, limit)
			if response != nil || !errors.Is(err, ErrModelResponseTooLarge) || !closed {
				t.Fatalf("response=%+v error=%v producer closed=%v", response, err, closed)
			}
			want := limit / len(encoded)
			if produced != want+1 || observed != want {
				t.Fatalf("produced=%d observed=%d, want %d and %d", produced, observed, want+1, want)
			}
		})
	}
}

func TestModelStreamWithinBudgetMatchesAggregateResponse(t *testing.T) {
	want := &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("hello"))), FinishReason: chat.FinishReasonStop}}
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if !yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("hel")}}, nil) {
				return
			}
			yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("lo")}, FinishReason: chat.FinishReasonStop}, nil)
		}
	})
	for _, dispatcher := range []*Dispatcher{
		{streamer: streamer},
		{model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return want.Clone(), nil })},
	} {
		got, err := dispatcher.callModel(t.Context(), nil, nil, 512)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("response=%+v error=%v", got, err)
		}
	}
}
