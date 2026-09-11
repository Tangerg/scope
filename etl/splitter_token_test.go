package etl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

type runeTokenizer struct{}

type chunkLimitTokenizer struct{ runeTokenizer }

func (c chunkLimitTokenizer) Encode(ctx context.Context, text string) ([]int, error) {
	if text == "defgh" {
		return nil, errors.New("tokenizer called after the chunk limit")
	}
	return c.runeTokenizer.Encode(ctx, text)
}

func (runeTokenizer) Encode(ctx context.Context, text string) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tokens := make([]int, 0, len(text))
	for _, value := range text {
		tokens = append(tokens, int(value))
	}
	return tokens, nil
}

func (runeTokenizer) Decode(ctx context.Context, tokens []int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	values := make([]rune, len(tokens))
	for index, token := range tokens {
		values[index] = rune(token)
	}
	return string(values), nil
}

func TestTokenSplitterHonorsTokenAndSentenceBounds(t *testing.T) {
	splitter, err := etl.NewTokenSplitter(etl.TokenSplitterConfig{
		Tokenizer:         runeTokenizer{},
		MaxTokensPerChunk: 10,
		MinTokensPerChunk: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := document.NewDocument("abcdef.ghijklmnop", nil)
	chunks, err := splitter.Split(t.Context(), []*document.Document{doc})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(chunks))
	}
	if chunks[0].Text != "abcdef." {
		t.Fatalf("first chunk = %q, want sentence boundary", chunks[0].Text)
	}
	for index, chunk := range chunks {
		if len([]rune(chunk.Text)) > 10 {
			t.Fatalf("chunk %d has %d tokens, want at most 10", index, len([]rune(chunk.Text)))
		}
	}
}

func TestTokenSplitterFailsInsteadOfEmittingOversizedTail(t *testing.T) {
	splitter, err := etl.NewTokenSplitter(etl.TokenSplitterConfig{
		Tokenizer:         chunkLimitTokenizer{},
		MaxTokensPerChunk: 3,
		MinTokensPerChunk: 1,
		MaxChunks:         1,
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := document.NewDocument("abcdefgh", nil)
	if _, err := splitter.Split(t.Context(), []*document.Document{doc}); !errors.Is(err, etl.ErrChunkLimitExceeded) {
		t.Fatalf("Split() error = %v, want ErrChunkLimitExceeded", err)
	}
}

func TestTokenSplitterPreservesShortTail(t *testing.T) {
	splitter, err := etl.NewTokenSplitter(etl.TokenSplitterConfig{
		Tokenizer: runeTokenizer{}, MaxTokensPerChunk: 3, MinTokensPerChunk: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := splitter.SplitText(t.Context(), "abcd")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0] != "abc" || chunks[1] != "d" {
		t.Fatalf("chunks = %#v, want all source content", chunks)
	}
}

func TestTokenSplitterValidatesLimits(t *testing.T) {
	for _, config := range []etl.TokenSplitterConfig{
		{},
		{Tokenizer: runeTokenizer{}, MaxTokensPerChunk: -1},
		{Tokenizer: runeTokenizer{}, MaxTokensPerChunk: 2, MinTokensPerChunk: 3},
	} {
		if _, err := etl.NewTokenSplitter(config); err == nil {
			t.Fatalf("NewTokenSplitter(%+v) error = nil", config)
		}
	}
}
