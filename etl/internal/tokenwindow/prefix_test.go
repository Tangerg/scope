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
