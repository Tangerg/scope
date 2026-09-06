package tokenwindow_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/markdown"
)

// This vocabulary models the leading-space merges used by BPE tokenizers:
// " indentation" is one token, while "indentation" needs two.
type mergedTokenizer struct{}

func (m mergedTokenizer) vocabulary() []string {
	return []string{" indentation", "indent", "ation", "hello", " H", " ", "#", "\n"}
}

func (m mergedTokenizer) Encode(ctx context.Context, source string) ([]int, error) {
	var tokens []int
	for source != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		matched := false
		for index, word := range m.vocabulary() {
			if strings.HasPrefix(source, word) {
				tokens = append(tokens, index)
				source = source[len(word):]
				matched = true
				break
			}
		}
		if !matched {
			tokens = append(tokens, 256+int(source[0]))
			source = source[1:]
		}
	}
	return tokens, nil
}

func (m mergedTokenizer) Decode(ctx context.Context, tokens []int) (string, error) {
	var text strings.Builder
	for _, token := range tokens {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		switch {
		case token >= 0 && token < len(m.vocabulary()):
			text.WriteString(m.vocabulary()[token])
		case token >= 256 && token < 512:
			text.WriteByte(byte(token - 256))
		default:
			return "", fmt.Errorf("invalid token %d", token)
		}
	}
	return text.String(), nil
}

func TestSplittersMeasureFinalTextAfterTrimmingAndRendering(t *testing.T) {
	codec := mergedTokenizer{}
	const source = "hello indentation hello indentation"
	want := []string{"hello", "indent", "ation", "hello", "indent", "ation"}
	for _, name := range []string{"plain", "markdown", "markdown with headings"} {
		t.Run(name, func(t *testing.T) {
			limit := 1
			input := source
			prefix := ""
			if name == "markdown with headings" {
				limit = 5
				prefix = "# H\n\n"
				input = prefix + source
			}
			var splitter interface {
				SplitText(context.Context, string) ([]string, error)
			}
			var err error
			if name == "plain" {
				splitter, err = etl.NewTokenSplitter(etl.TokenSplitterConfig{Tokenizer: codec, MaxTokensPerChunk: limit})
			} else {
				splitter, err = markdown.NewSplitter(markdown.SplitterConfig{Tokenizer: codec, MaxTokensPerChunk: limit})
			}
			if err != nil {
				t.Fatal(err)
			}
			chunks, err := splitter.SplitText(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			expected := make([]string, len(want))
			for index, body := range want {
				expected[index] = prefix + body
			}
			if !slices.Equal(chunks, expected) {
				t.Fatalf("chunks = %q, want %q", chunks, expected)
			}
			for _, chunk := range chunks {
				tokens, err := codec.Encode(t.Context(), chunk)
				if err != nil || len(tokens) > limit {
					t.Fatalf("chunk %q has %d tokens, budget %d, error %v", chunk, len(tokens), limit, err)
				}
			}
		})
	}
}
