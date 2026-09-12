package chat_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

func BenchmarkResponseAccumulator(b *testing.B) {
	for _, size := range []int{1024, 2048, 4096, 8192} {
		for _, extraBytes := range []int{0, 65536} {
			b.Run(fmt.Sprintf("deltas=%d/metadata=%d", size, extraBytes), func(b *testing.B) {
				extra := metadata.Map{}
				if extraBytes > 0 {
					if err := extra.Set("payload", strings.Repeat("x", extraBytes)); err != nil {
						b.Fatal(err)
					}
				}
				first := &chat.ResponseDelta{Metadata: &chat.ResponseMetadata{Extra: extra}}
				delta := &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("0123456789abcdef")}}
				finish := &chat.ResponseDelta{FinishReason: chat.FinishReasonStop}
				b.ReportAllocs()
				for b.Loop() {
					var accumulator chat.ResponseAccumulator
					if err := accumulator.Add(first); err != nil {
						b.Fatal(err)
					}
					for range size {
						if err := accumulator.Add(delta); err != nil {
							b.Fatal(err)
						}
					}
					if err := accumulator.Add(finish); err != nil {
						b.Fatal(err)
					}
					response, err := accumulator.Response()
					if err != nil || len(response.Text()) != size*16 {
						b.Fatalf("response: %v", err)
					}
				}
			})
		}
	}
}
