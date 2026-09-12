package interaction

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func BenchmarkModelStreamObservation(b *testing.B) {
	for _, observed := range []bool{false, true} {
		name := "disabled"
		if observed {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			delta := &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("increment")}}
			finish := &chat.ResponseDelta{FinishReason: chat.FinishReasonStop}
			dispatcher := &Dispatcher{streamer: chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
				return func(yield func(*chat.ResponseDelta, error) bool) {
					for range 1024 {
						if !yield(delta, nil) {
							return
						}
					}
					yield(finish, nil)
				}
			})}
			var emit agent.DeltaEmitter
			if observed {
				emit = func(_ json.RawMessage) {}
			}
			b.ReportAllocs()
			for b.Loop() {
				response, err := dispatcher.callModel(b.Context(), nil, emit)
				if err != nil || len(response.Text()) != 1024*len("increment") {
					b.Fatalf("response: %v", err)
				}
			}
		})
	}
}
