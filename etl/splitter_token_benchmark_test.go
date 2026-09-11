package etl_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl"
)

type countingTokenizer struct {
	runeTokenizer
	encodedBytes int64
}

func (c *countingTokenizer) Encode(ctx context.Context, text string) ([]int, error) {
	c.encodedBytes += int64(len(text))
	return c.runeTokenizer.Encode(ctx, text)
}

func BenchmarkTokenSplitterLongText(b *testing.B) {
	for _, size := range []int{16 << 10, 64 << 10, 256 << 10} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			codec := &countingTokenizer{}
			splitter, err := etl.NewTokenSplitter(etl.TokenSplitterConfig{
				Tokenizer: codec, MaxTokensPerChunk: 256,
			})
			if err != nil {
				b.Fatal(err)
			}
			source := strings.Repeat("x", size)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				chunks, splitErr := splitter.SplitText(b.Context(), source)
				if splitErr != nil || len(chunks) != size/256 || strings.Join(chunks, "") != source {
					b.Fatalf("invalid split: chunks=%d err=%v", len(chunks), splitErr)
				}
			}
			b.ReportMetric(float64(codec.encodedBytes)/float64(b.N), "encoded_bytes/op")
		})
	}
}
