package tokenwindow_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tangerg/scope/etl"
	"github.com/Tangerg/scope/etl/internal/tokenwindow"
	"github.com/Tangerg/scope/etl/markdown"
)

// The vocabulary is reversible but token prefixes do not preserve rune boundaries.
type boundaryTokenizer struct{ byteTokenizer }

func (b boundaryTokenizer) Encode(ctx context.Context, text string) ([]int, error) {
	switch text {
	case "éx":
		return []int{256, 257}, nil
	case "é":
		return []int{260}, nil
	case "ab":
		return []int{261}, nil
	case "[é]":
		return []int{262}, nil
	default:
		return b.byteTokenizer.Encode(ctx, text)
	}
}

func (b boundaryTokenizer) Decode(ctx context.Context, tokens []int) (string, error) {
	switch {
	case slices.Equal(tokens, []int{256}):
		return "\xc3", nil
	case slices.Equal(tokens, []int{256, 257}):
		return "éx", nil
	case slices.Equal(tokens, []int{260}):
		return "é", nil
	case slices.Equal(tokens, []int{261}):
		return "ab", nil
	case slices.Equal(tokens, []int{262}):
		return "[é]", nil
	default:
		return b.byteTokenizer.Decode(ctx, tokens)
	}
}

func TestBoundaryTokenizerRoundTrip(t *testing.T) {
	codec := boundaryTokenizer{}
	for _, source := range []string{"éx", "é", "ab", "[é]", "abc", " a"} {
		tokens, err := codec.Encode(t.Context(), source)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codec.Decode(t.Context(), tokens)
		if err != nil || decoded != source {
			t.Fatalf("round trip %q = %q, %v", source, decoded, err)
		}
	}
}

type measuredTokenizer struct {
	byteTokenizer
	encodedBytes int
}

func (m *measuredTokenizer) Encode(ctx context.Context, text string) ([]int, error) {
	m.encodedBytes += len(text)
	return m.byteTokenizer.Encode(ctx, text)
}

func TestPrefixKeepsSuccessfulProbeWorkBounded(t *testing.T) {
	codec := &measuredTokenizer{}
	source := strings.Repeat("a", 1<<20)
	prefix, err := tokenwindow.Prefix(t.Context(), codec, source, 32, strings.TrimSpace)
	if err != nil || len(prefix) != 32 {
		t.Fatalf("Prefix() length = %d, error = %v", len(prefix), err)
	}
	if codec.encodedBytes > 8192 {
		t.Fatalf("one small chunk encoded %d bytes of a long document", codec.encodedBytes)
	}
}

func TestPrefixCancelsSourceBoundarySearch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := tokenwindow.Prefix(ctx, boundaryTokenizer{}, "éx", 1, func(string) string {
		cancel()
		return "over budget"
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prefix() error = %v, want cancellation", err)
	}
}

func TestPrefixSearchesSourceBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
		limit              int
		render             func(string) string
	}{
		{name: "independent rune", source: "éx", want: "é", limit: 1, render: strings.TrimSpace},
		{name: "structural rendering", source: "éx", want: "é", limit: 1, render: func(text string) string { return "[" + text + "]" }},
		{name: "nonmonotonic rendering", source: "abc", want: "ab", limit: 1, render: func(text string) string {
			if text == "a" {
				return "too large"
			}
			return text
		}},
		{name: "empty rendered prefix", source: " a", want: " a", limit: 1, render: strings.TrimSpace},
		{name: "empty source", source: "", render: strings.TrimSpace},
		{name: "all rendering empty", source: "abc", render: func(string) string { return "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := tokenwindow.Prefix(t.Context(), boundaryTokenizer{}, test.source, test.limit, test.render)
			if err != nil || got != test.want {
				t.Fatalf("Prefix() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

// Byte tokens reproduce vocabularies whose individual tokens split a rune.
type byteTokenizer struct{ replaceInvalid bool }

type utf8Tokenizer struct{ byteTokenizer }

func (u utf8Tokenizer) Encode(ctx context.Context, text string) ([]int, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("invalid UTF-8 tokenizer input")
	}
	return u.byteTokenizer.Encode(ctx, text)
}

func TestTokenWindowsGrowWithoutSplittingSourceCharacters(t *testing.T) {
	source := strings.Repeat("a", 4095) + "世" + strings.Repeat("b", 16<<10)
	codec := utf8Tokenizer{}
	for _, limit := range []int{256, 8192} {
		remaining := source
		var chunks []string
		for remaining != "" {
			prefix, err := tokenwindow.Prefix(t.Context(), codec, remaining, limit, strings.TrimSpace)
			if err != nil || prefix == "" || !strings.HasPrefix(remaining, prefix) {
				t.Fatalf("lossless prefix = %q, err=%v", prefix, err)
			}
			tokens, err := codec.Encode(t.Context(), prefix)
			if err != nil || len(tokens) > limit {
				t.Fatalf("prefix exceeds budget: tokens=%d err=%v", len(tokens), err)
			}
			chunks = append(chunks, prefix)
			remaining = remaining[len(prefix):]
		}
		if strings.Join(chunks, "") != source {
			t.Fatal("window selection changed source content")
		}
	}
}

func (b byteTokenizer) Encode(_ context.Context, text string) ([]int, error) {
	tokens := make([]int, len(text))
	for index, value := range []byte(text) {
		tokens[index] = int(value)
	}
	return tokens, nil
}

func (b byteTokenizer) Decode(_ context.Context, tokens []int) (string, error) {
	data := make([]byte, len(tokens))
	for index, token := range tokens {
		data[index] = byte(token)
	}
	text := string(data)
	if b.replaceInvalid {
		text = strings.ToValidUTF8(text, "�")
	}
	return text, nil
}

func TestSplittersPreserveCharactersAcrossVocabularyTokens(t *testing.T) {
	for _, replaceInvalid := range []bool{false, true} {
		codec := byteTokenizer{replaceInvalid: replaceInvalid}
		for _, limit := range []int{1, 2, 3, 4} {
			plain, err := etl.NewTokenSplitter(etl.TokenSplitterConfig{
				Tokenizer: codec, MaxTokensPerChunk: limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			structured, err := markdown.NewSplitter(markdown.SplitterConfig{
				Tokenizer: codec, MaxTokensPerChunk: limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, splitter := range []interface {
				SplitText(context.Context, string) ([]string, error)
			}{plain, structured} {
				chunks, splitErr := splitter.SplitText(t.Context(), "A世B")
				if limit < 3 {
					wantErr := etl.ErrChunkBudgetTooSmall
					if _, structured := splitter.(*markdown.Splitter); structured {
						wantErr = markdown.ErrSemanticUnitTooLarge
					}
					if !errors.Is(splitErr, wantErr) || len(chunks) != 0 {
						t.Fatalf("%T limit %d: chunks = %q, error = %v; want explicit budget failure", splitter, limit, chunks, splitErr)
					}
					continue
				}
				want := []string{"A", "世", "B"}
				if limit == 4 {
					want = []string{"A世", "B"}
				}
				if splitErr != nil || !slices.Equal(chunks, want) {
					t.Fatalf("%T limit %d: chunks = %q, error = %v; want %q", splitter, limit, chunks, splitErr, want)
				}
			}
		}
	}
}
