package markdown_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	markdownsplitter "github.com/Tangerg/scope/etl/markdown"
)

type faultTokenizer struct {
	failAt int
	cause  error
	calls  int
}

func (f *faultTokenizer) Encode(ctx context.Context, text string) ([]int, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, f.cause
	}
	return (runeTokenizer{}).Encode(ctx, text)
}

func (f *faultTokenizer) Decode(ctx context.Context, tokens []int) (string, error) {
	f.calls++
	if f.calls == f.failAt {
		return "", f.cause
	}
	return (runeTokenizer{}).Decode(ctx, tokens)
}

func TestTokenizerFailureNeverPublishesAPartialMarkdownResult(t *testing.T) {
	cause := errors.New("tokenizer unavailable")
	for _, source := range []string{
		"# Pending",
		"# Topic\n\nabcdefghijklmnopqrstuvwxyz\n\nsecond paragraph",
		"- first item\n- second item\n- third item",
		"```go\nfirst()\nsecond()\nthird()\n```",
		"| Key |\n| --- |\n| one |\n| two |\n| three |",
		"    first()\n    second()\n    third()",
	} {
		t.Run(source, func(t *testing.T) {
			baseline := &faultTokenizer{}
			build := func(codec *faultTokenizer) *markdownsplitter.Splitter {
				t.Helper()
				splitter, err := markdownsplitter.NewSplitter(markdownsplitter.SplitterConfig{Tokenizer: codec, MaxTokensPerChunk: 28})
				if err != nil {
					t.Fatal(err)
				}
				return splitter
			}
			if _, err := build(baseline).SplitText(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			for failure := 1; failure <= baseline.calls; failure++ {
				t.Run(fmt.Sprint(failure), func(t *testing.T) {
					codec := &faultTokenizer{failAt: failure, cause: cause}
					chunks, err := build(codec).SplitText(t.Context(), source)
					if chunks != nil || !errors.Is(err, cause) {
						t.Fatalf("failure at tokenizer call %d: chunks=%q error=%v", failure, chunks, err)
					}
				})
			}
		})
	}
}
