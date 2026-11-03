package safeguard_test

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/safeguard"
)

func BenchmarkInputOnlyStream(b *testing.B) {
	for _, chunks := range []int{1024, 2048, 4096} {
		text := strings.Repeat("x", 32)
		source := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
			return func(yield func(*chat.ResponseDelta, error) bool) {
				for i := 0; i < chunks; i++ {
					if !yield(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta(text)}}, nil) {
						return
					}
				}
				yield(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop}, nil)
			}
		})
		middleware, err := safeguard.NewMiddleware(safeguard.MiddlewareConfig{Scope: safeguard.ScopeInput, Matcher: safeguard.MatcherFunc(func(context.Context, string) (safeguard.Match, error) {
			return safeguard.Match{}, nil
		})})
		if err != nil {
			b.Fatal(err)
		}
		for _, item := range []struct {
			name   string
			stream chat.Streamer
		}{
			{"plain", source}, {"input_only", middleware.Stream(source)},
		} {
			b.Run(fmt.Sprintf("%s/chunks_%d", item.name, chunks), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(chunks * 32))
				for i := 0; i < b.N; i++ {
					received := 0
					for delta, err := range item.stream.Stream(b.Context(), nil) {
						if err != nil {
							b.Fatal(err)
						}
						if delta == nil {
							b.Fatal("nil delta")
						}
						received++
					}
					if received != chunks+1 {
						b.Fatalf("received %d increments, want %d", received, chunks+1)
					}
				}
			})
		}
	}
}
